package core

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// bofengPlaylistFixture 造一份 #EXTINF 均匀的点播播放列表。
func bofengPlaylistFixture(segments int, seconds float64) string {
	var builder strings.Builder
	builder.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:0\n")
	span := strconv.FormatFloat(seconds, 'f', 3, 64)
	for index := 0; index < segments; index++ {
		builder.WriteString("#EXTINF:" + span + ",\n")
		builder.WriteString("seg" + strconv.Itoa(index) + ".ts\n")
	}
	builder.WriteString("#EXT-X-ENDLIST\n")
	return builder.String()
}

func TestBofengPlayItemsSplitsEpisodesAndResolvesRelativeURLs(t *testing.T) {
	items := bofengPlayItems("bfzym3u8", "第01集$https://bfeng10.com/video/a/index.m3u8#第02集$/video/a/2/index.m3u8")
	if len(items) != 2 {
		t.Fatalf("unexpected episode count: %d", len(items))
	}
	if items[0].title != "第01集" || items[0].url != "https://bfeng10.com/video/a/index.m3u8" {
		t.Fatalf("first episode parsed incorrectly: %+v", items[0])
	}
	// 相对路径按同线路里第一个绝对地址的源站补齐。
	if items[1].url != "https://bfeng10.com/video/a/2/index.m3u8" {
		t.Fatalf("relative episode url was not resolved: %s", items[1].url)
	}
	// 多线路时优先选 m3u8 那条线（另一条可能是网盘/专用协议）。
	multi := bofengPlayItems("bfzym3u8$$$baidu", "第01集$https://cdn.example/vod/1/index.m3u8$$$第01集$https://pan.example/share/1")
	if len(multi) != 1 || multi[0].url != "https://cdn.example/vod/1/index.m3u8" {
		t.Fatalf("play line selection ignored m3u8: %+v", multi)
	}
	if empty := bofengPlayItems("", ""); empty != nil {
		t.Fatalf("empty play url should yield no episode: %+v", empty)
	}
}

func TestBofengPlayEntriesSeparateRoutesFromEpisodes(t *testing.T) {
	// 实测：《闪婚》(2022 老片) 的两条录目是线路名，不是第 1/2 集。
	movie := bofengPlayItems("bfzym3u8", "百度云$https://pan.example/s/1#HD国语$https://c1.rrcdnbf2.com/video/shanhun/HD%E5%9B%BD%E8%AF%AD/index.m3u8")
	if len(movie) != 2 {
		t.Fatalf("unexpected entry count: %+v", movie)
	}
	if episodes := bofengEpisodeItems(movie); len(episodes) != 0 {
		t.Fatalf("route names must not be treated as episodes: %+v", episodes)
	}
	route, ok := bofengPrimaryRoute(movie)
	if !ok || !strings.HasSuffix(route.url, ".m3u8") || !strings.Contains(route.url, "rrcdnbf2.com") {
		t.Fatalf("primary route should be the m3u8 one: %+v", route)
	}
	// 集号与线路名混在一起时只取真集，避免选集页凭空多出「HD」这一集。
	mixed := bofengPlayItems("bfzym3u8", "第01集$https://cdn.example/1.m3u8#第02集$https://cdn.example/2.m3u8#HD$https://cdn.example/hd.m3u8")
	if episodes := bofengEpisodeItems(mixed); len(episodes) != 2 {
		t.Fatalf("mixed entries should keep only episodes: %+v", episodes)
	}
	for _, title := range []string{"第01集", "更新至12集", "EP03", "01"} {
		if !bofengIsEpisodeTitle(title) {
			t.Fatalf("%q should be recognised as an episode", title)
		}
	}
	for _, title := range []string{"HD国语", "百度云", "正片", "蓝光1080", "完整版", ""} {
		if bofengIsEpisodeTitle(title) {
			t.Fatalf("%q must not be treated as an episode", title)
		}
	}
}

func TestBofengSingleChapterTitleIsHonest(t *testing.T) {
	movie := bofengPlayItem{title: "HD国语", url: "https://c1.rrcdnbf2.com/video/shanhun/index.m3u8"}
	// 91.5 分钟的正片：写清时长，不伪装成「第 1 集」。
	if title := bofengSingleChapterTitle(movie, 91*time.Minute+30*time.Second, true); title != "完整版（约 92 分钟）" {
		t.Fatalf("long single stream must be labelled by duration: %q", title)
	}
	if title := bofengSingleChapterTitle(bofengPlayItem{title: "第01集"}, 3*time.Minute, true); title != "第01集" {
		t.Fatalf("short single episode keeps its own title: %q", title)
	}
	if title := bofengSingleChapterTitle(bofengPlayItem{}, 0, false); title != "正片" {
		t.Fatalf("empty title should fall back to 正片: %q", title)
	}
}

func TestBofengWholeDramaSingleStreamIsSlicedIntoLogicalEpisodes(t *testing.T) {
	manifest := bofengParseManifest(bofengPlaylistFixture(300, 3))
	if len(manifest.segments) != 300 || !manifest.vod || manifest.rotated {
		t.Fatalf("manifest parsed incorrectly: segments=%d vod=%t rotated=%t", len(manifest.segments), manifest.vod, manifest.rotated)
	}
	total := time.Duration(0)
	for _, segment := range manifest.segments {
		total += segment.duration
	}
	if total != 15*time.Minute {
		t.Fatalf("unexpected total duration: %s", total)
	}
	// 只有「短剧大全」父类（type_id_1=58）下的长单流才算整剧单流。
	short := map[string]any{"type_id_1": "58", "type_name": "闪婚离婚"}
	if !bofengWholeDrama(short, total) {
		t.Fatal("long single stream under the short-drama parent should be sliced")
	}
	// 真实接口的 type_id_1 是 JSON 数字，经 json.Number 解码，不是字符串。
	if !bofengWholeDrama(map[string]any{"type_id_1": json.Number("58")}, total) {
		t.Fatal("numeric type_id_1 must be recognised as the short-drama parent")
	}
	// 电影分类下 90 分钟是正常的正片，绝不能切。
	movie := map[string]any{"type_id_1": "1", "type_name": "科幻电影"}
	if bofengWholeDrama(movie, total) || bofengWholeDrama(movie, 91*time.Minute+30*time.Second) {
		t.Fatal("movie-like single streams must not be sliced")
	}
	if bofengWholeDrama(short, 5*time.Minute) {
		t.Fatal("a stream shorter than one episode must stay a single episode")
	}
	// 上游漏字段时只认题材里明说「短剧」的，识别不出就不切。
	if bofengWholeDrama(map[string]any{"type_name": "反转爽文"}, total) {
		t.Fatal("unknown category must not be sliced")
	}
	if !bofengWholeDrama(map[string]any{"type_name": "短剧大全"}, total) {
		t.Fatal("explicit short-drama category should still be sliced without type_id_1")
	}
	chapters := bofengSliceChapters("1", "https://bfeng10.com/video/a/index.m3u8", manifest)
	// 15 分钟按每集 2 分钟切 => 7 个整集 + 1 个残集。
	if len(chapters) != 8 {
		t.Fatalf("unexpected logical episode count: %d", len(chapters))
	}
	if chapters[0].ID != providerChapterID(sourceBofeng, "1", "s0-40") || chapters[0].Title != "第 1 集" {
		t.Fatalf("first logical episode is wrong: %+v", chapters[0])
	}
	if _, _, valid := bofengSliceRange(strings.TrimPrefix(chapters[0].ID, providerChapterID(sourceBofeng, "1", ""))); !valid {
		t.Fatal("slice range could not be recovered from the chapter id")
	}
	sliced := bofengRenderManifest(manifest, 0, 40)
	lines := strings.Split(strings.TrimSpace(sliced), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "#EXTM3U") || lines[len(lines)-1] != "#EXT-X-ENDLIST" {
		t.Fatalf("sliced playlist lost its envelope: %q", lines)
	}
	media := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "#") {
			media++
		}
	}
	if media != 40 {
		t.Fatalf("sliced playlist has %d segments, want 40", media)
	}
}

func TestBofengShortAndLiveStreamsStayOneEpisode(t *testing.T) {
	short := bofengParseManifest(bofengPlaylistFixture(60, 3))
	total := time.Duration(0)
	for _, segment := range short.segments {
		total += segment.duration
	}
	if total != 3*time.Minute || bofengWholeDrama(map[string]any{"type_id_1": "58"}, total) {
		t.Fatalf("a 3 minute stream is not a whole drama: total=%s", total)
	}
	if chapters := bofengSliceChapters("1", "https://bfeng10.com/video/a/index.m3u8", short); chapters != nil {
		t.Fatalf("short playlist must not be sliced: %d", len(chapters))
	}
	// 直播列表（没有 #EXT-X-ENDLIST）切片没有意义。
	live := strings.ReplaceAll(bofengPlaylistFixture(300, 3), "#EXT-X-ENDLIST\n", "")
	if chapters := bofengSliceChapters("1", "https://bfeng10.com/video/a/index.m3u8", bofengParseManifest(live)); chapters != nil {
		t.Fatalf("live playlist must not be sliced: %d", len(chapters))
	}
}

func TestBofengDramaRowMapping(t *testing.T) {
	row := map[string]any{
		"vod_id":      json.Number("148397"),
		"vod_name":    "闪婚遇上裴先生2",
		"vod_pic":     "https://p.bfvp26.com/upload/vod/a.jpg",
		"vod_blurb":   "暂无剧情介绍",
		"vod_content": "简介 <b>正文</b>",
		"vod_year":    "2026",
		"vod_remarks": "更新至30集",
		"vod_class":   "闪婚离婚,甜宠",
		"type_name":   "闪婚离婚",
		"vod_score":   "4.0",
	}
	drama, ok := bofengDramaFromRow(row)
	if !ok || drama.ID != "bofeng:148397" || drama.SourceID != "148397" {
		t.Fatalf("drama row mapped incorrectly: %+v", drama)
	}
	if drama.Title != "闪婚遇上裴先生2" || drama.ChannelName != "暴风资源" {
		t.Fatalf("drama identity is wrong: %+v", drama)
	}
	if drama.Cover != "https://p.bfvp26.com/upload/vod/a.jpg" {
		t.Fatalf("cover was not kept: %v", drama.Cover)
	}
	// 「暂无剧情介绍」不是简介，要跳到 vod_content 取正文。
	if drama.Desc != "简介 正文" {
		t.Fatalf("description was not cleaned: %q", drama.Desc)
	}
	if drama.EpisodeCount != 30 || drama.ReleaseStatus != "ongoing" {
		t.Fatalf("remark derived fields are wrong: %+v", drama)
	}
	if drama.Score != "4.0分" || len(drama.Tags) != 3 {
		t.Fatalf("score/tags are wrong: %+v", drama)
	}
	if drama, ok := bofengDramaFromRow(map[string]any{"vod_id": "0", "vod_name": "空 ID"}); ok || drama.ID != "" {
		t.Fatal("invalid vod_id must be rejected")
	}
}

func TestBofengCatalogParsesRowsAndPagination(t *testing.T) {
	body := `{"code":1,"msg":"数据列表","page":1,"pagecount":26,"limit":20,"total":508,"list":[` +
		`{"vod_id":148397,"vod_name":"闪婚遇上裴先生2","vod_pic":"https://p.bfvp26.com/a.jpg","vod_remarks":"完结","vod_class":"闪婚离婚","type_name":"闪婚离婚","vod_score":4.0},` +
		`{"vod_id":148398,"vod_name":"  ","vod_pic":"","vod_remarks":"更新至3集"}]}`
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "bfzyapi.com" || !strings.HasPrefix(request.URL.Path, "/api.php/provide/vod") {
			t.Fatalf("unexpected bofeng request: %s", request.URL.String())
		}
		if values := request.URL.Query(); values.Get("ac") != "detail" || values.Get("pg") != "2" || values.Get("t") != "70" {
			t.Fatalf("catalog request lost its parameters: %s", request.URL.RawQuery)
		}
		return sourceFixtureResponse(request, http.StatusOK, body), nil
	})
	items, more, err := d.fetchBofengCatalogPage(context.Background(), 2, "70", "")
	// 第二条没有剧名，应当被丢掉，而不是带着空标题进列表。
	if err != nil || len(items) != 1 || !more {
		t.Fatalf("catalog page parsed incorrectly: items=%d more=%t err=%v", len(items), more, err)
	}
	if items[0].ID != "bofeng:148397" || items[0].Title != "闪婚遇上裴先生2" || items[0].ReleaseStatus != "finished" {
		t.Fatalf("catalog row mapped incorrectly: %+v", items[0])
	}
	if items[0].Cover != "https://p.bfvp26.com/a.jpg" || items[0].Score != "4.0分" {
		t.Fatalf("catalog row cover/score is wrong: %+v", items[0])
	}
}

// 空结果不能当故障：code != 1 或 list 为 null 时列表页要显示「没有找到匹配的短剧」，
// 而不是「暂时无法加载」。
func TestBofengCatalogTreatsEmptyPayloadAsEmptyResult(t *testing.T) {
	for _, payload := range []string{
		`{"code":0,"msg":"参数错误","list":null}`,
		`{"code":1,"msg":"数据列表","page":1,"pagecount":0,"limit":20,"total":0,"list":null}`,
		`{"code":"1","msg":"数据列表","page":1,"total":0,"list":[]}`,
	} {
		d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
			return sourceFixtureResponse(request, http.StatusOK, payload), nil
		})
		items, more, err := d.fetchBofengCatalogPage(context.Background(), 1, "", "不存在的剧名")
		if err != nil || len(items) != 0 || more {
			t.Fatalf("empty payload must be an empty result: payload=%s items=%d more=%t err=%v", payload, len(items), more, err)
		}
	}
}

func TestBofengCategoriesKeepParentsBeforeChildren(t *testing.T) {
	body := `{"code":1,"class":[` +
		`{"type_id":58,"type_pid":0,"type_name":"短剧大全"},` +
		`{"type_id":68,"type_pid":58,"type_name":"反转爽文"},` +
		`{"type_id":70,"type_pid":58,"type_name":"闪婚离婚"},` +
		`{"type_id":1,"type_pid":0,"type_name":"电影"},` +
		`{"type_id":99,"type_pid":123,"type_name":"孤立子类"}]}`
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("ac") != "list" {
			t.Fatalf("categories must use ac=list: %s", request.URL.RawQuery)
		}
		return sourceFixtureResponse(request, http.StatusOK, body), nil
	})
	categories, err := d.fetchBofengCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 父类「短剧大全」自身就是一个可翻页的大分类（实测 t=58 有 12183 部），
	// 必须保留并排在它的子类前面；平铺的「电影」保留；父类缺失时子类补回来。
	want := []nativeCategory{
		{ID: "58", Name: "短剧大全"},
		{ID: "68", Name: "反转爽文"},
		{ID: "70", Name: "闪婚离婚"},
		{ID: "1", Name: "电影"},
		{ID: "99", Name: "孤立子类"},
	}
	if len(categories) != len(want) {
		t.Fatalf("unexpected categories: %+v", categories)
	}
	for index, entry := range want {
		if categories[index] != entry {
			t.Fatalf("category %d is %+v, want %+v", index, categories[index], entry)
		}
	}
	if !validBofengCategory("70") || validBofengCategory("短剧大全") || !validBofengCategory("") {
		t.Fatal("bofeng category validation is wrong")
	}
}

// 分类表必须一条不落地全收、「短剧大全(58)」排最前，并且重排不能搞坏父子结构。
//
// 断言刻意用**字面量 49**，不是拿 bofengMaxCategories 比自己：拿常量比会跟着常量
// 一起错——当初把上限写成 40（D1 事故）时，`len(categories) == bofengMaxCategories`
// 依然成立，测试照样绿，等于一点都没防住。夹具按上游真实形状造：
// 49 条 = 9 个父类 + 40 个子类，58 落在数组第 38 位，最后一条是 73「福利」。
func TestBofengCategoriesKeepEveryUpstreamEntry(t *testing.T) {
	parts := make([]string, 0, 49)
	parentOf := map[string]string{}
	addClass := func(id, parent, name string) {
		parts = append(parts, `{"type_id":`+id+`,"type_pid":`+parent+`,"type_name":"`+name+`"}`)
		if parent != "0" {
			parentOf[id] = parent
		}
	}
	// 父类 1..7，各带 4 个子类 → 35 条，占满 58 之前的大部分位置。
	for index := 1; index <= 7; index++ {
		parent := strconv.Itoa(index)
		addClass(parent, "0", "分类"+parent)
		for child := 0; child < 4; child++ {
			id := strconv.Itoa(100*index + child)
			addClass(id, parent, "子"+id)
		}
	}
	// 父类 8 和它的前两个子类 → 58 之前正好 38 条。
	addClass("8", "0", "分类8")
	addClass("800", "8", "子800")
	addClass("801", "8", "子801")
	// 58 落在数组第 39 个位置（index 38），和上游一致。
	shortChildren := []string{"68", "70", "61", "62", "63", "64", "65", "66", "67"}
	addClass("58", "0", "短剧大全")
	for _, id := range shortChildren {
		addClass(id, "58", "短剧"+id)
	}
	// 最后一条是 73「福利」——上限一旦被调小，先被砍掉的就是它。
	addClass("73", "8", "福利")
	body := `{"code":1,"class":[` + strings.Join(parts, ",") + `]}`

	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		return sourceFixtureResponse(request, http.StatusOK, body), nil
	})
	categories, err := d.fetchBofengCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// ① 一条不落：绝对条数，不是跟常量比。
	if len(categories) != 49 {
		t.Fatalf("上游实测 49 条，只拿到 %d 条：分类上限是不是被调回去了？", len(categories))
	}
	// ② 主力分类（12183 部）必须排第一——上游把它放在第 38 位，重排后应当是 0。
	shortPosition := -1
	for index, entry := range categories {
		if entry.ID == bofengShortDramaTypeID {
			shortPosition = index
			break
		}
	}
	if shortPosition != 0 {
		t.Fatalf("「短剧大全(58)」排在第 %d/%d 位，应当排第一", shortPosition, len(categories))
	}
	// ③ 58 的 9 个子类必须紧跟其后，中间不能被别的父类（20/30 那种）插进来。
	for index, id := range shortChildren {
		if got := categories[index+1].ID; got != id {
			t.Fatalf("第 %d 位应当是 58 的子类 %s，实际是 %s", index+1, id, got)
		}
	}
	// ④ 尾部没被挤掉：重排不能顺手截断，否则等于换个姿势复发 D1。
	if last := categories[len(categories)-1]; last.ID != "73" {
		t.Fatalf("最后一条应当是 73 福利，被截断后变成 %+v", last)
	}
	// ⑤ 没有重复、没有丢失。
	position := map[string]int{}
	for index, entry := range categories {
		if _, duplicate := position[entry.ID]; duplicate {
			t.Fatalf("分类 %s 重复出现", entry.ID)
		}
		position[entry.ID] = index
	}
	if len(position) != 49 {
		t.Fatalf("去重后只剩 %d 个分类", len(position))
	}
	// ⑥ 重排不能搞坏父子顺序：任何一个子类都必须排在它父类之后。
	for child, parent := range parentOf {
		childIndex, childOK := position[child]
		parentIndex, parentOK := position[parent]
		if !childOK || !parentOK {
			t.Fatalf("分类 %s（父 %s）丢了", child, parent)
		}
		if parentIndex > childIndex {
			t.Fatalf("父类 %s（第 %d 位）排到了子类 %s（第 %d 位）后面", parent, parentIndex, child, childIndex)
		}
	}
}

// 末集过短时并入前一集：601 个 3 秒分片按 120 秒切会得到 15 个整集 + 1 个 3 秒末集，
// 那个 3 秒末集必须并进前一集（实测最短的末集只有 2.1 秒）。
func TestBofengSliceMergesShortTailIntoPreviousEpisode(t *testing.T) {
	manifest := bofengParseManifest(bofengPlaylistFixture(601, 3))
	chapters := bofengSliceChapters("1", "https://bfeng10.com/video/a/index.m3u8", manifest)
	if len(chapters) != 15 {
		t.Fatalf("short tail must be merged into the previous episode, got %d episodes", len(chapters))
	}
	prefix := providerChapterID(sourceBofeng, "1", "")
	from, to, ok := bofengSliceRange(strings.TrimPrefix(chapters[len(chapters)-1].ID, prefix))
	if !ok || from != 560 || to != 601 {
		t.Fatalf("last episode must swallow the tail: from=%d to=%d ok=%t", from, to, ok)
	}
}

// 同一把 key 被镜像重复写多行不算轮换；只有出现两个不同 URI 才是真轮换。
func TestBofengRepeatedKeyIsNotRotation(t *testing.T) {
	key := `#EXT-X-KEY:METHOD=AES-128,URI="https://cdn.example/k1"`
	repeated := "#EXTM3U\n#EXT-X-VERSION:3\n" + key + "\n" + key + "\n" + key + "\n" +
		"#EXTINF:3.000,\nseg0.ts\n#EXT-X-ENDLIST\n"
	if bofengParseManifest(repeated).rotated {
		t.Fatal("the same key repeated on several lines is not key rotation")
	}
	rotated := "#EXTM3U\n#EXT-X-VERSION:3\n" + key + "\n" +
		`#EXT-X-KEY:METHOD=AES-128,URI="https://cdn.example/k2"` + "\n" +
		"#EXTINF:3.000,\nseg0.ts\n#EXT-X-ENDLIST\n"
	if !bofengParseManifest(rotated).rotated {
		t.Fatal("two distinct key URIs must be treated as rotation")
	}
	// METHOD=NONE 没有 URI（表示后续明文），不能算一把 key。
	clear := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:3.000,\nseg0.ts\n#EXT-X-ENDLIST\n"
	if bofengParseManifest(clear).rotated {
		t.Fatal("METHOD=NONE without URI must not be counted as a key")
	}
}

// 声明集数闸门：声明的总集数很小、却切出几十集时，说明这条流其实是「一条正常的长单集」。
func TestBofengDeclaredEpisodesGate(t *testing.T) {
	if value := bofengDeclaredEpisodes("更新至66集"); value != 66 {
		t.Fatalf("更新至66集 should parse as 66, got %d", value)
	}
	if value := bofengDeclaredEpisodes("全40集"); value != 40 {
		t.Fatalf("全40集 should parse as 40, got %d", value)
	}
	// 上游常见的写法是「更新至66集」「全40集」，数字后面都带单位。
	// `全集36`（无单位）和 `更新至第12集`（多个「第」）解析不出来，返回 0 = 不设限。
	// 这是**刻意的 fail-open**，不是 bug：少认一种写法只是不加限制，把正则放宽
	// 反而可能把别的东西认成集数。别为了多认这几种去改 bofengDeclaredTotal。
	if value := bofengDeclaredEpisodes("全集36"); value != 0 {
		t.Fatalf("全集36 没有单位，应当保持未知（fail-open），got %d", value)
	}
	if value := bofengDeclaredEpisodes("更新至第12集"); value != 0 {
		t.Fatalf("更新至第12集 带「第」，应当保持未知（fail-open），got %d", value)
	}
	// 「第 47 集」是单集编号不是总集数，必须解析不出（返回 0 表示不设限）。
	if value := bofengDeclaredEpisodes("第47集"); value != 0 {
		t.Fatalf("episode number must not be read as a total, got %d", value)
	}
	if value := bofengDeclaredEpisodes("完结"); value != 0 {
		t.Fatalf("remarks without a total should return 0, got %d", value)
	}
	// 实测语料里 remarks 几乎全是「完结」，这条闸门触发率是 0；
	// 「更新至1集」也被 value<2 归为未知（fail-open），这是刻意行为，别当成 bug 改掉。
	if value := bofengDeclaredEpisodes("更新至1集"); value != 0 {
		t.Fatalf("a 1-episode declaration is treated as unknown on purpose, got %d", value)
	}
	if !bofengSliceAcceptable(15, 0) || !bofengSliceAcceptable(15, 20) {
		t.Fatal("a slice count matching the declaration must be accepted")
	}
	if bofengSliceAcceptable(23, 4) {
		t.Fatal("23 fake episodes for a 4-episode drama must be rejected")
	}
}

// 详情全链路：1 条播放串 + 一段真实结构的 m3u8，应当切出 N 个逻辑集并打上来源标签。
func TestBofengDetailSlicesWholeDramaSingleStream(t *testing.T) {
	const stream = "https://bfeng10.com/video/shanhun/index.m3u8"
	// 600 个 3 秒分片 = 30 分钟，按 2 分钟切正好 15 集。
	playlist := bofengPlaylistFixture(600, 3)
	// type_id_1 用 JSON 数字（真实接口就是这样），解码后是 json.Number 而不是字符串。
	detail := `{"code":1,"msg":"数据列表","page":1,"pagecount":1,"limit":1,"total":1,"list":[` +
		`{"vod_id":148397,"vod_name":"闪婚遇上裴先生2","vod_pic":"https://p.bfvp26.com/a.jpg",` +
		`"vod_remarks":"更新至20集","type_id":70,"type_id_1":58,"type_name":"闪婚离婚",` +
		`"vod_play_from":"bfzym3u8","vod_play_url":"第01集$` + stream + `"}]}`
	probes := 0
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/index.m3u8") {
			probes++
			return sourceFixtureResponse(request, http.StatusOK, playlist), nil
		}
		return sourceFixtureResponse(request, http.StatusOK, detail), nil
	})
	drama, chapters, err := d.fetchBofengDetail(context.Background(), "148397")
	if err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("detail must probe the playlist exactly once, got %d", probes)
	}
	if len(chapters) != 15 || drama.TotalEpisode != 15 || drama.EpisodeCount != 15 {
		t.Fatalf("unexpected logical episode count: chapters=%d drama=%+v", len(chapters), drama)
	}
	if chapters[0].Title != "第 1 集" || chapters[14].Title != "第 15 集" {
		t.Fatalf("slice titles are wrong: %q .. %q", chapters[0].Title, chapters[14].Title)
	}
	if chapters[0].ID != providerChapterID(sourceBofeng, "148397", "s0-40") {
		t.Fatalf("first slice id is wrong: %s", chapters[0].ID)
	}
	if chapters[0].VideoURL != stream {
		t.Fatalf("logical episodes must point at the whole stream: %s", chapters[0].VideoURL)
	}
	tagged := false
	for _, tag := range drama.Tags {
		if tag == "整剧单流·按 2 分钟合成分集" {
			tagged = true
		}
	}
	if !tagged {
		t.Fatalf("missing the slice tag: %v", drama.Tags)
	}
}

// 声明集数很小时不能切：30 分钟的单流声明「更新至4集」，切 15 集就是 11 个假集。
func TestBofengDetailKeepsWholeVersionWhenDeclarationContradicts(t *testing.T) {
	const stream = "https://bfeng10.com/video/shanhun/index.m3u8"
	playlist := bofengPlaylistFixture(600, 3)
	detail := `{"code":1,"msg":"数据列表","page":1,"pagecount":1,"limit":1,"total":1,"list":[` +
		`{"vod_id":148397,"vod_name":"长单集短剧","vod_pic":"https://p.bfvp26.com/a.jpg",` +
		`"vod_remarks":"更新至4集","type_id":70,"type_id_1":58,"type_name":"短剧大全",` +
		`"vod_play_from":"bfzym3u8","vod_play_url":"第01集$` + stream + `"}]}`
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/index.m3u8") {
			return sourceFixtureResponse(request, http.StatusOK, playlist), nil
		}
		return sourceFixtureResponse(request, http.StatusOK, detail), nil
	})
	drama, chapters, err := d.fetchBofengDetail(context.Background(), "148397")
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 1 {
		t.Fatalf("a contradicting declaration must block slicing, got %d episodes", len(chapters))
	}
	if chapters[0].Title != "完整版（约 30 分钟）" {
		t.Fatalf("single chapter must be honest about the duration: %q", chapters[0].Title)
	}
	for _, tag := range drama.Tags {
		if tag == "整剧单流·按 2 分钟合成分集" {
			t.Fatalf("blocked slice must not be tagged: %v", drama.Tags)
		}
	}
}

// 播放全链路：子播放列表要保住外框（#EXTM3U / #EXT-X-ENDLIST）、#EXT-X-KEY 与分片数。
func TestBofengResolveKeepsKeyAndEnvelope(t *testing.T) {
	const stream = "https://bfeng10.com/video/shanhun/index.m3u8"
	var builder strings.Builder
	builder.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:3\n")
	builder.WriteString(`#EXT-X-KEY:METHOD=AES-128,URI="https://bfeng10.com/video/shanhun/key.key"` + "\n")
	for index := 0; index < 200; index++ {
		builder.WriteString("#EXTINF:3.000,\nseg" + strconv.Itoa(index) + ".ts\n")
	}
	builder.WriteString("#EXT-X-ENDLIST\n")
	playlist := builder.String()
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		return sourceFixtureResponse(request, http.StatusOK, playlist), nil
	})
	media, err := d.resolveBofengMedia(context.Background(), Task{
		DramaID: providerDramaID(sourceBofeng, "1"),
		Chapter: Chapter{
			ID:       providerChapterID(sourceBofeng, "1", bofengSliceKey(0, 40)),
			Source:   sourceBofeng,
			VideoURL: stream,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if media.Playlist == "" || media.URL != stream {
		t.Fatalf("slice was not applied: url=%s playlist=%d", media.URL, len(media.Playlist))
	}
	lines := strings.Split(strings.TrimSpace(media.Playlist), "\n")
	if !strings.HasPrefix(lines[0], "#EXTM3U") || lines[len(lines)-1] != "#EXT-X-ENDLIST" {
		t.Fatalf("sliced playlist lost its envelope: %v", lines)
	}
	if !strings.Contains(media.Playlist, `URI="https://bfeng10.com/video/shanhun/key.key"`) {
		t.Fatal("sliced playlist dropped the AES key line")
	}
	mediaLines := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "#") {
			mediaLines++
		}
	}
	if mediaLines != 40 {
		t.Fatalf("sliced playlist has %d segments, want 40", mediaLines)
	}
	if media.Duration != 2*time.Minute {
		t.Fatalf("sliced duration is wrong: %s", media.Duration)
	}
}

// R6 的唯一设计目的：补封面**不能**顺带跑一次整剧单流的 m3u8 探测。
// 列表页会批量补封面，每多一次 60KB 播放列表下载（实测 1.84 秒）就会把界面拖死。
// 这条测试守的就是「0 次 m3u8 请求」——谁把 fetchBofengCover 改回 fetchBofengDetail，
// 或者顺手在里面加一句「顺便探测一下时长」，这里必须红。
func TestBofengCoverDoesNotProbePlaylist(t *testing.T) {
	const stream = "https://bfeng10.com/video/shanhun/index.m3u8"
	detail := `{"code":1,"msg":"数据列表","page":1,"pagecount":1,"limit":1,"total":1,"list":[` +
		`{"vod_id":148397,"vod_name":"闪婚遇上裴先生2","vod_pic":"https://p.bfvp26.com/a.jpg",` +
		`"vod_remarks":"更新至66集","type_id_1":58,"type_name":"闪婚离婚",` +
		`"vod_play_from":"bfzym3u8","vod_play_url":"第01集$` + stream + `"}]}`
	detailHits, playlistHits := 0, 0
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/index.m3u8") {
			playlistHits++
			return sourceFixtureResponse(request, http.StatusOK, bofengPlaylistFixture(600, 3)), nil
		}
		if request.URL.Query().Get("ac") == "detail" {
			detailHits++
		}
		return sourceFixtureResponse(request, http.StatusOK, detail), nil
	})
	cover, err := d.fetchBofengCover(context.Background(), "148397")
	if err != nil {
		t.Fatal(err)
	}
	if cover != "https://p.bfvp26.com/a.jpg" {
		t.Fatalf("cover resolution is wrong: %q", cover)
	}
	if detailHits != 1 {
		t.Fatalf("cover fetch must issue exactly one ac=detail request, got %d", detailHits)
	}
	if playlistHits != 0 {
		t.Fatalf("cover fetch must never touch the playlist, got %d m3u8 requests", playlistHits)
	}
}

// bofengDetailProbeFailureFixture 造一个「1 条播放串 + m3u8 拉不下来」的下载器。
// 上游 bfeng10.com 有 4 个 IP、抖动是常态，这条路径会被反复触发。
func bofengDetailProbeFailureFixture(t *testing.T) *Downloader {
	t.Helper()
	const stream = "https://bfeng10.com/video/shanhun/index.m3u8"
	detail := `{"code":1,"msg":"数据列表","page":1,"pagecount":1,"limit":1,"total":1,"list":[` +
		`{"vod_id":148397,"vod_name":"闪婚遇上裴先生2","vod_pic":"https://p.bfvp26.com/a.jpg",` +
		`"vod_remarks":"更新至66集","type_id":70,"type_id_1":58,"type_name":"闪婚离婚",` +
		`"vod_play_from":"bfzym3u8","vod_play_url":"第01集$` + stream + `"}]}`
	return sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/index.m3u8") {
			return sourceFixtureResponse(request, http.StatusNotFound, ""), nil
		}
		return sourceFixtureResponse(request, http.StatusOK, detail), nil
	})
}

// R9：探测失败不能把「1 集」当成事实。
// ① 不报错；② 集名说明「时长未知」而不是伪装成「第01集」；
// ③ 集数保留 vod_remarks 声明的 66，绝不能被写成 1。
func TestBofengProbeFailureDoesNotCollapseDramaToOneEpisode(t *testing.T) {
	d := bofengDetailProbeFailureFixture(t)
	drama, chapters, err := d.fetchBofengDetail(context.Background(), "148397")
	if err != nil {
		t.Fatalf("probe failure must degrade instead of failing: %v", err)
	}
	if len(chapters) != 1 {
		t.Fatalf("probe failure yields a single fallback chapter, got %d", len(chapters))
	}
	if chapters[0].Title != bofengProbeFailedTitle {
		t.Fatalf("fallback chapter must be honest about the unknown duration: %q", chapters[0].Title)
	}
	if !drama.EpisodeCountUnknown {
		t.Fatal("probe failure must mark the episode count as unknown")
	}
	if count := nativeText(drama.EpisodeCount); count != "66" {
		t.Fatalf("declared episode count must survive a probe failure, got %q", count)
	}
}

// R9 的缓存侧：nativeDetail 不能拿 len(chapters)=1 覆盖已知的 66 集，
// 否则 saveDetailMetadata 把「1 集」落盘，用户之后一直是 1 集、刷新也救不回来。
func TestBofengProbeFailureKeepsCachedEpisodeCount(t *testing.T) {
	d := bofengDetailProbeFailureFixture(t)
	engine := sourceFixtureEngine(t, d.client.Transport.(sourceFixtureTransport))
	result, err := engine.nativeDetail(context.Background(), nativeDrama{
		MetadataSchema: 1,
		ID:             providerDramaID(sourceBofeng, "148397"),
		Source:         sourceBofeng,
		SourceID:       "148397",
		Title:          "闪婚遇上裴先生2",
		Episodes:       66,
	})
	if err != nil {
		t.Fatalf("nativeDetail must not fail on a probe failure: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected nativeDetail payload: %#v", result)
	}
	drama, ok := payload["drama"].(nativeDrama)
	if !ok {
		t.Fatalf("nativeDetail returned no drama: %#v", payload)
	}
	if drama.Episodes != 66 {
		t.Fatalf("a CDN hiccup must not shrink the cached episode count: %d", drama.Episodes)
	}
}

// R8：裸数字 720 / 1080 是清晰度线路，不是第 720 / 1080 集。
func TestBofengBareNumbersAbove500AreNotEpisodes(t *testing.T) {
	for _, title := range []string{"720", "1080", "2160"} {
		if bofengIsEpisodeTitle(title) {
			t.Fatalf("%q looks like a resolution, not an episode", title)
		}
	}
	// 带中文的集号走公共正则，不受 500 这个闸门影响。
	for _, title := range []string{"480", "第480集", "更新至480集", "全480集", "共480集", "01", "12"} {
		if !bofengIsEpisodeTitle(title) {
			t.Fatalf("%q must stay an episode", title)
		}
	}
	// `720$url#1080$url` 会被当成两条线路，最终只剩一条可用线路。
	items := bofengPlayItems("bfzym3u8", "720$https://cdn.example/720/index.m3u8#1080$https://cdn.example/1080/index.m3u8")
	if episodes := bofengEpisodeItems(items); len(episodes) != 0 {
		t.Fatalf("resolution routes must not become episodes: %+v", episodes)
	}
	route, ok := bofengPrimaryRoute(items)
	if !ok || !strings.Contains(route.url, "/720/") {
		t.Fatalf("primary route should be the first m3u8 one: %+v", route)
	}
}
