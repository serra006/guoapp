package core

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 麒麟短剧是第二个走苹果 CMS 通用层的站源。这组用例守三件**只有麒麟才会错**的事：
//  1. 短剧分类 type_id 是 3（不是暴风的 58），写错则整剧单流永不切片；
//  2. 采集接口域名只认 qlys.cc / www.qlys.cc，封面主机 shandianpic.vip 绝不能被认成接口域名；
//  3. 分类栏把「短剧(3)」排在第一位。

// 短剧判定的 type_id 必须来自 qilinSite.shortTypeID。58 是暴风的分类 id，
// 在麒麟上出现时**不能**当成短剧——两家上游的 type_id 编号体系互不相干。
func TestQilinShortDramaGateUsesTypeID3(t *testing.T) {
	if qilinSite.shortTypeID != "3" {
		t.Fatalf("麒麟的短剧 type_id 必须是 3，实际是 %q", qilinSite.shortTypeID)
	}
	for _, row := range []map[string]any{
		{"type_id": "3"},
		{"type_id_1": "3"},
		{"type_id_1": "3", "type_id": "31"},
		{"type_name": "短剧"}, // 上游漏 type_id 时按题材兜底
	} {
		if !maccmsShortDrama(qilinSite, row) {
			t.Fatalf("row %v should be a short drama on qilin", row)
		}
	}
	// 暴风的 58 在麒麟上是「别的分类」，不能被认成短剧，否则会把电影整片切碎。
	if maccmsShortDrama(qilinSite, map[string]any{"type_id_1": "58", "type_id": "58"}) {
		t.Fatal("type 58 belongs to bofeng, not qilin")
	}
	if maccmsShortDrama(qilinSite, map[string]any{"type_id": "1", "type_name": "电影"}) {
		t.Fatal("a movie category must not be treated as a short drama")
	}
	// 时长闸门与 bofeng 共用：不够一集的单流不切。
	if maccmsWholeDrama(qilinSite, map[string]any{"type_id": "3"}, 5*time.Minute) {
		t.Fatal("a stream shorter than one episode must not be sliced")
	}
	if !maccmsWholeDrama(qilinSite, map[string]any{"type_id": "3"}, 84*time.Minute) {
		t.Fatal("an 84-minute whole-drama stream under type 3 must be sliced")
	}
}

// 麒麟的每一部剧都是一条覆盖整剧的长单流（实测 2509~4827 个分片）。
// 详情必须把它切成逻辑集，章节 ID/Source 都要挂在 qilin 上——否则选集页只有 1 条。
func TestQilinDetailSlicesWholeDramaSingleStream(t *testing.T) {
	const stream = "https://sdm.example.test/video/qilin/index.m3u8"
	// 600 个 3 秒分片 = 30 分钟，按 2 分钟切正好 15 集。
	playlist := bofengPlaylistFixture(600, 3)
	detail := `{"code":1,"msg":"数据列表","page":1,"pagecount":1,"limit":1,"total":1,"list":[` +
		`{"vod_id":90210,"vod_name":"闪婚老公是首富","vod_pic":"https://shandianpic.vip/upload/a.jpg",` +
		`"vod_remarks":"完结","type_id":3,"type_id_1":0,"type_name":"短剧",` +
		`"vod_play_from":"sdm3u8$$$wsym3u8",` +
		`"vod_play_url":"全集$` + stream + `$$$全集$https://wsym.example.test/video/qilin/index.m3u8"}]}`
	probes := 0
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		// 接口请求只能走 qilin 的接口域名；播放列表探测必须原样打到媒体主机
		// （这正是「播放主机不能被接口域名改写」那条边界的实证）。
		if strings.HasPrefix(request.URL.Path, "/api.php/provide/vod") {
			if !qilinAPIHostAllowed(request.URL.Hostname()) {
				t.Fatalf("unexpected qilin api host: %s", request.URL.String())
			}
		} else if request.URL.Host != "sdm.example.test" {
			t.Fatalf("playlist probe must keep its media host: %s", request.URL.String())
		}
		if strings.HasSuffix(request.URL.Path, "/index.m3u8") {
			probes++
			return sourceFixtureResponse(request, http.StatusOK, playlist), nil
		}
		return sourceFixtureResponse(request, http.StatusOK, detail), nil
	})
	drama, chapters, err := d.fetchQilinDetail(context.Background(), "90210")
	if err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("detail must probe the playlist exactly once, got %d", probes)
	}
	if len(chapters) != 15 || drama.EpisodeCount != 15 || drama.TotalEpisode != 15 {
		t.Fatalf("qilin whole-drama stream was not sliced: chapters=%d drama=%+v", len(chapters), drama)
	}
	// 线路 0（sdm3u8）含 m3u8 优先命中，不能被 wsym3u8 抢走。
	if chapters[0].VideoURL != stream {
		t.Fatalf("the m3u8 route was not picked: %s", chapters[0].VideoURL)
	}
	if chapters[0].Source != sourceQilin || drama.Source != sourceQilin || drama.ChannelName != "麒麟短剧" {
		t.Fatalf("qilin identity leaked: chapter=%+v drama=%+v", chapters[0], drama)
	}
	if chapters[0].ID != providerChapterID(sourceQilin, "90210", bofengSliceKey(0, 40)) {
		t.Fatalf("chapter id must be qilin-scoped: %s", chapters[0].ID)
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
	// 封面走 shandianpic.vip，既不能被改写，也不能被当成接口域名。
	if cover := nativeText(drama.Cover); cover != "https://shandianpic.vip/upload/a.jpg" {
		t.Fatalf("cover was rewritten: %v", drama.Cover)
	}
}

// 分类栏：主力「短剧(3)」必须排第一（上游实测 t=3 有 36668 部，位置却排在后面）。
func TestQilinCategoriesKeepShortDramaFirst(t *testing.T) {
	body := `{"code":1,"class":[` +
		`{"type_id":1,"type_pid":0,"type_name":"电影"},` +
		`{"type_id":2,"type_pid":0,"type_name":"连续剧"},` +
		`{"type_id":3,"type_pid":0,"type_name":"短剧"},` +
		`{"type_id":31,"type_pid":3,"type_name":"古装"},` +
		`{"type_id":4,"type_pid":0,"type_name":"动漫"}]}`
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("ac") != "list" {
			t.Fatalf("categories must use ac=list: %s", request.URL.RawQuery)
		}
		return sourceFixtureResponse(request, http.StatusOK, body), nil
	})
	categories, err := d.fetchQilinCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(categories) != 5 {
		t.Fatalf("unexpected categories: %+v", categories)
	}
	if categories[0].ID != "3" || categories[0].Name != "短剧" {
		t.Fatalf("「短剧(3)」必须排第一，实际是 %+v", categories[0])
	}
	if categories[1].ID != "31" {
		t.Fatalf("短剧的子类要紧跟其后，实际是 %+v", categories[1])
	}
}

// 域名边界：接口域名认 qlys.cc，封面 CDN（shandianpic.vip）与播放主机一律不认；
// 镜像展开必须按 qilinAPIBaseMirrors 的顺序，且保留路径与查询串。
func TestQilinSourceForURLAndMirrors(t *testing.T) {
	for _, raw := range []string{
		"https://www.qlys.cc/api.php/provide/vod/?ac=list",
		"https://qlys.cc/api.php/provide/vod/?ac=detail&pg=1&t=3",
	} {
		if source := providerSourceForURL(raw); source != sourceQilin {
			t.Fatalf("%s should be %q, got %q", raw, sourceQilin, source)
		}
	}
	for _, raw := range []string{
		"https://shandianpic.vip/upload/a.jpg",
		"https://sdm.example.test/video/qilin/index.m3u8",
		"https://www.qlys.cc.evil.example/api.php/provide/vod/?ac=list",
	} {
		if source := providerSourceForURL(raw); source != "" {
			t.Fatalf("%s must not be recognised as a provider source, got %q", raw, source)
		}
	}
	candidates := providerURLCandidates("https://www.qlys.cc/api.php/provide/vod/?ac=detail&pg=2&t=3")
	if len(candidates) != len(qilinAPIBaseMirrors) {
		t.Fatalf("qilin should expand to %d mirrors, got %v", len(qilinAPIBaseMirrors), candidates)
	}
	if !strings.Contains(candidates[0], "www.qlys.cc") {
		t.Fatalf("the first candidate must be the working domain, got %s", candidates[0])
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate] {
			t.Fatalf("duplicate candidate: %v", candidates)
		}
		seen[candidate] = true
		if !strings.Contains(candidate, "/api.php/provide/vod/?ac=detail&pg=2&t=3") {
			t.Fatalf("candidate lost its parameters: %s", candidate)
		}
	}
	// 非本站源的地址保持原样，不能凭空造出镜像。
	if got := providerURLCandidates("https://shandianpic.vip/upload/a.jpg"); len(got) != 1 || got[0] != "https://shandianpic.vip/upload/a.jpg" {
		t.Fatalf("cover hosts must not be rewritten: %v", got)
	}
}

// 目录分页参数必须带上 t=<type_id>，否则「短剧」分类点进去会变成全站列表。
func TestQilinCatalogPageSendsTypeAndPagination(t *testing.T) {
	body := `{"code":1,"page":2,"pagecount":1834,"limit":20,"total":36668,"list":[` +
		`{"vod_id":90210,"vod_name":"闪婚老公是首富","vod_pic":"https://shandianpic.vip/a.jpg",` +
		`"vod_remarks":"完结","type_id":3,"type_name":"短剧","vod_score":8.5}]}`
	d := sourceFixtureDownloader(t, func(request *http.Request) (*http.Response, error) {
		values := request.URL.Query()
		if values.Get("ac") != "detail" || values.Get("pg") != "2" || values.Get("t") != "3" {
			t.Fatalf("catalog request lost its parameters: %s", request.URL.RawQuery)
		}
		return sourceFixtureResponse(request, http.StatusOK, body), nil
	})
	items, more, err := d.fetchQilinCatalogPage(context.Background(), 2, "3", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !more {
		t.Fatalf("catalog page parsed incorrectly: items=%d more=%t", len(items), more)
	}
	if items[0].ID != "qilin:90210" || items[0].Source != sourceQilin || items[0].ChannelName != "麒麟短剧" {
		t.Fatalf("catalog row mapped incorrectly: %+v", items[0])
	}
	if items[0].Score != "8.5分" || items[0].ReleaseStatus != "finished" {
		t.Fatalf("catalog row derived fields are wrong: %+v", items[0])
	}
	// 分类校验：空串（全部）与纯数字合法，别的字符串一律拒绝。
	for _, category := range []string{"", "3", "31"} {
		if !validQilinCategory(category) {
			t.Fatalf("category %q should be valid", category)
		}
	}
	for _, category := range []string{"短剧", "../3", "3;drop"} {
		if validQilinCategory(category) {
			t.Fatalf("category %q must be rejected", category)
		}
	}
}

// 镜像表退化守卫（变异测试发现的缺口）。
//
// 麒麟抗域名封禁的**唯一**手段就是镜像轮换：暴风就是 bfzyapi.com 被域名级阻断
// （DNS 污染成 31.13.81.4 + SNI RST）之后被迫迁到 bfzy3.tv，整个源一度读不到目录。
// 之前的 5 条用例只验证「当前这张表能用」，如果镜像被删到只剩一条、或者改了域名
// 却忘了同步 qilinAPIHosts，测试会全绿、用户端却已经无路可退——那次事故正是这么发生的。
// 这条用例就是让那两种手滑立刻变红：
//  1. 镜像数 < 2（单点故障）；
//  2. 首位不是主域名（「顺序即优先级」被破坏，首个候选可能变成慢域名/备用域名）；
//  3. 镜像 host 没登记进 qilinAPIHosts（改了表忘了改 hosts：providerSourceForURL 认不出，
//     providerURLCandidates 就不会展开，镜像表静默失效）；
//  4. 镜像丢了采集接口路径（拼出来的地址会 404，而错误信息只会说「返回的数据格式无效」）。
func TestQilinMirrorsStayUsableAfterDomainRotation(t *testing.T) {
	if len(qilinAPIBaseMirrors) < 2 {
		t.Fatalf("麒麟至少要两条镜像，否则域名被封就无路可退；当前只有 %d 条：%v",
			len(qilinAPIBaseMirrors), qilinAPIBaseMirrors)
	}
	if qilinAPIBaseMirrors[0] != qilinAPIBaseURL {
		t.Fatalf("镜像表首位必须是主域名 %s（顺序即优先级），实际是 %s",
			qilinAPIBaseURL, qilinAPIBaseMirrors[0])
	}
	// 表里的每条镜像都必须真的展开成一个候选，且**顺序一致**。条数对不上说明有重复镜像
	// 被去重掉（两个相同的域名＝实际仍只有一条退路），顺序对不上说明优先级语义被破坏。
	candidates := providerURLCandidates(qilinAPIBaseURL + "?ac=list")
	if len(candidates) != len(qilinAPIBaseMirrors) {
		t.Fatalf("镜像表 %d 条但只展开出 %d 个候选（%v），镜像轮换已经失效",
			len(qilinAPIBaseMirrors), len(candidates), candidates)
	}
	for index, mirror := range qilinAPIBaseMirrors {
		parsed, err := url.Parse(mirror)
		if err != nil || parsed.Host == "" {
			t.Fatalf("镜像 %q 不是合法的绝对地址", mirror)
		}
		if !strings.HasSuffix(mirror, "/api.php/provide/vod/") {
			t.Fatalf("镜像 %q 丢了采集接口路径 /api.php/provide/vod/", mirror)
		}
		if !qilinAPIHostAllowed(parsed.Hostname()) {
			t.Fatalf("镜像 host %q 没登记进 qilinAPIHosts：改了镜像表必须同步 hosts，"+
				"否则 providerSourceForURL 认不出新域名，镜像展开静默失效", parsed.Hostname())
		}
		candidate, parseErr := url.Parse(candidates[index])
		if parseErr != nil || !strings.EqualFold(candidate.Hostname(), parsed.Hostname()) {
			t.Fatalf("第 %d 个候选应当是镜像 %q，实际是 %q（顺序即优先级）",
				index, mirror, candidates[index])
		}
	}
}
