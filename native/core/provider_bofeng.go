package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 暴风资源（bofeng）：苹果 CMS v10 采集接口（MacCMS provide/vod）。
//
// 上游接口（实测 2026-10）：
//
//	GET /api.php/provide/vod/?ac=list                     分类表（class[]，带 type_pid）
//	GET /api.php/provide/vod/?ac=detail&pg=2&t=58          分类翻页（t 是 type_id）
//	GET /api.php/provide/vod/?ac=detail&wd=闪婚&pg=1       搜索（wd 必须 URL 编码，每页 20 条）
//	GET /api.php/provide/vod/?ac=detail&ids=148397         详情
//
// 统一返回 {code,msg,page,pagecount,limit,total,list[]}，单条是苹果 CMS 的 vod_* 字段。
// 播放走 bfzym3u8 线路：vod_play_from="bfzym3u8"，vod_play_url 里 # 分隔条目、
// 每条「名字$地址」，地址是明文 HLS（首行 #EXTM3U、无 #EXT-X-KEY、分片是 mp2t），
// 不需要登录、签名、Referer、Cookie——这也是它比其它站源省事的地方。
//
// 两个必须记住的坑（都来自实测，tools/diag_bofeng.py 可复现）：
//  1. 播放/封面主机**不固定**（已见 bfeng10.com 与 c1.rrcdnbf2.com），
//     所以 media 地址只能从 vod_play_url 里取，不能写任何主机常量。
//  2. `$` 前面的名字**语义不可靠**：可能是集号（第01集），也可能是线路名
//     （百度云 / HD国语），也可能是「整部剧」（1 条播放串里 2655 个分片≈132.7 分钟，66 集）。
//     章节与集数必须先分类再决定，见 bofengIsEpisodeTitle / bofengWholeDrama。
const (
	bofengAPIBaseURL  = "https://bfzyapi.com/api.php/provide/vod/"
	bofengChannelName = "暴风资源"
	bofengPageSize    = 20
	// 分类上限：上游 ac=list 实测返回 49 条（6 个父类 + 43 个子类），上限必须装得下全部条目。
	// 原来取 40，正好把排在后面的「短剧大全(58)」截掉——那是本站 12000+ 部的主力分类，
	// 用户因此完全刷不到短剧内容。给到 64 留足余量。
	bofengMaxCategories = 64

	// 整剧单流的时长闸门。注意「单集长度」这个前提本身不可靠：实测多集短剧的**真单集**
	// 有 46.9 分钟（《母子连心》）、13.4 分钟（《弑流年》）。所以 >8 分钟只是必要条件，
	// 必须再叠加 bofengShortDrama（确认是短剧）与 bofengSliceAcceptable（集数对得上声明），
	// 单靠时长会把一部 47 分钟的正常单集切成 20 多个假集。
	bofengSingleEpisodeLimit = 8 * time.Minute
	// 上游「短剧大全」父类的 type_id。整剧单流只在这个父类下才切：电影分类下
	// 90 分钟是正常正片（《闪婚》老片实测 1377 分片≈91.5 分钟），只看时长会把电影切碎。
	bofengShortDramaTypeID = "58"
	// 逻辑分集的目标时长：短剧单集通常 60~120 秒，按 120 秒切出来的集数与真实集数最接近。
	bofengSliceTarget = 2 * time.Minute
	// 末集合并阈值：39 条真实整剧单流里有 11 条（28%）末集不足 20 秒，最短只有 2.1 秒
	// （《陆太太，进城闪个婚》切出 74 集）。用户看到「第 74 集」点进去两秒播完自动跳走，
	// 比少一集难受得多，所以末集不足这个长度时并入前一集。
	bofengTailMin = 20 * time.Second
	// 切片上限：同一部《闪婚遇上裴先生2》今天实测已是 2655 分片 = 132.7 分钟 = 66 集
	// （早期抓到的 662 分片≈33 分钟是同一部剧的旧状态）。已知最长 214.5 分钟 = 106 集，
	// 240 留 2.2 倍余量；再多说明上游数据异常，宁可不切。
	bofengSliceMax = 240
	// 探测播放列表的独立超时：详情页不该因为探测慢而卡住选集。
	bofengProbeTimeout = 8 * time.Second
	// 探测失败时单集的标题。上游给的名字可能是「第01集」，直接显示会让人以为这部
	// 66 集的短剧只有一集；这条文案至少说明「时长未知 + 可以重试」。
	bofengProbeFailedTitle = "正片（时长未知，可下拉刷新重试）"
	// 裸数字条目的最大集号。超过这个数的裸数字更可能是清晰度（720/1080/2160），
	// 而不是集数——短剧不存在 500 集以上。
	bofengMaxEpisodeNumber = 500
)

func bofengNumericID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 12 {
		return false
	}
	number, err := strconv.Atoi(value)
	return err == nil && number > 0
}

func validBofengCategory(category string) bool {
	if category == "" {
		return true
	}
	return bofengNumericID(category)
}

// bofengRequest 统一走 d.fetchProviderText：掩码通道、限流、DNS 优选都挂在 d.client 上，
// 自己发 http.Get 会绕过这些保护，也会丢掉既有站点被墙时的降级能力。
func (d *Downloader) bofengRequest(ctx context.Context, values url.Values) (string, error) {
	address := bofengAPIBaseURL
	if encoded := values.Encode(); encoded != "" {
		address += "?" + encoded
	}
	return d.fetchProviderText(ctx, address, bofengAPIBaseURL)
}

// bofengDecode 用 UseNumber 解码：vod_id 是数字字面量，走 float64 会变成 1.48397e+05，
// 之后的 ID 校验和 URL 拼接全会挂。
func bofengDecode(body string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, errors.New("暴风资源返回的数据格式无效")
	}
	row, _ := decoded.(map[string]any)
	if row == nil {
		return nil, errors.New("暴风资源返回的数据格式无效")
	}
	return row, nil
}

// bofengOK 判断接口是否正常返回。部分镜像不返回 code，缺失时按正常处理；
// code != 1 表示「没有数据」（关键词无命中、分类下无剧集），由调用方转成空列表。
func bofengOK(row map[string]any) bool {
	code := nativeText(row["code"])
	return code == "" || code == "1"
}

// bofengRows 取 list。list 为 null / 类型不对时返回 nil，不当异常处理。
func bofengRows(row map[string]any) []any {
	value, found := row["list"]
	if !found || value == nil {
		return nil
	}
	rows, ok := value.([]any)
	if !ok || len(rows) > 2000 {
		return nil
	}
	return rows
}

func bofengHasMore(row map[string]any, page, count int) bool {
	if pageCount, err := strconv.Atoi(nativeText(row["pagecount"])); err == nil && pageCount > 0 {
		return page < pageCount && page < 100000
	}
	if limit, err := strconv.Atoi(nativeText(row["limit"])); err == nil && limit > 0 {
		return count >= limit
	}
	return count >= bofengPageSize
}

func bofengScore(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return ""
	}
	return value + "分"
}

// bofengReleaseStatus 从备注里推断连载状态（上游没有独立的状态字段）。
func bofengReleaseStatus(remark string) string {
	switch {
	case remark == "":
		return ""
	case strings.Contains(remark, "完结"), strings.Contains(remark, "全集"), strings.HasPrefix(remark, "全"):
		return "finished"
	case strings.Contains(remark, "更新"):
		return "ongoing"
	}
	return ""
}

// bofengDescription 简介优先取 blurb（短），没有再用 content。
// 上游无简介时 blurb 常被填成「暂无剧情介绍」，这不算简介，要继续往下找正文。
func bofengDescription(row map[string]any) string {
	for _, key := range []string{"vod_blurb", "vod_content"} {
		text := strings.TrimSpace(cleanText(nativeText(row[key])))
		if text == "" || strings.Contains(text, "暂无剧情介绍") || strings.Contains(text, "暂无简介") {
			continue
		}
		return truncate(text, 12000)
	}
	return ""
}

func bofengAppendTag(tags []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return tags
	}
	for _, existing := range tags {
		if existing == value {
			return tags
		}
	}
	if len(tags) >= 16 {
		return tags
	}
	return append(tags, value)
}

// bofengDramaFromRow 把一条 vod_* 记录映射成 Drama。
// 字段口径参考 qimaoDramaFromMap：ID/章节都用 providerDramaID / providerChapterID 拼，
// 这样 detail / resolve / download 三处都能靠 splitProviderDramaID 反解归属。
func bofengDramaFromRow(row map[string]any) (Drama, bool) {
	id := nativeText(row["vod_id"])
	title := strings.TrimSpace(cleanText(nativeText(row["vod_name"])))
	if !bofengNumericID(id) || title == "" {
		return Drama{}, false
	}
	cover := providerCoverAddress(row["vod_pic"], bofengAPIBaseURL)
	classText := strings.TrimSpace(cleanText(nativeText(row["vod_class"])))
	typeName := strings.TrimSpace(cleanText(nativeText(row["type_name"])))
	category := firstNonEmpty(typeName, classText)
	remark := truncate(strings.TrimSpace(cleanText(nativeText(row["vod_remarks"]))), 64)
	episodes := 0
	if remark != "" {
		// 「更新至30集」「全40集」这类备注里带集数；「完结」「HD」解析不出来就保持 0，
		// 详情页拿到分集列表后会覆盖成真实集数。
		episodes = episodeIndex(remark, 0)
	}
	tags := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(classText, func(r rune) bool {
		return r == ',' || r == '，' || r == '/' || r == '、' || r == '|'
	}) {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] || len([]rune(part)) > 24 || len(tags) >= 8 {
			continue
		}
		seen[part] = true
		tags = append(tags, part)
	}
	if year := strings.TrimSpace(nativeText(row["vod_year"])); year != "" && year != "0" {
		tags = bofengAppendTag(tags, year+"年")
	}
	description := bofengDescription(row)
	return Drama{
		ID: providerDramaID(sourceBofeng, id), Source: sourceBofeng, SourceID: id,
		Title: truncate(title, 256), Name: truncate(title, 256),
		Desc: description, Intro: description,
		Cover: cover, CoverURL: cover,
		TotalEpisode: episodes, EpisodeCount: episodes,
		ChannelName: bofengChannelName, Category: category, CategoryName: category, TypeName: typeName,
		Remark: remark, Score: bofengScore(nativeText(row["vod_score"])),
		OnlineDate: providerReleaseDate(nativeText(row["vod_pubdate"])),
		Tags:       tags, ReleaseStatus: bofengReleaseStatus(remark),
	}, true
}

func bofengDramas(rows []any) []Drama {
	items := make([]Drama, 0, len(rows))
	seen := map[string]bool{}
	for _, entry := range rows {
		row, _ := entry.(map[string]any)
		if len(row) == 0 {
			continue
		}
		drama, ok := bofengDramaFromRow(row)
		if !ok || seen[drama.ID] {
			continue
		}
		seen[drama.ID] = true
		items = append(items, drama)
	}
	return items
}

// bofengMediaOrigin 取线路里第一个绝对地址的源站，作为同线路相对路径的基准。
// 上游多数时候给绝对地址，个别镜像给 "/video/xxx/index.m3u8"，
// 这时唯一能确定归属的就是同一线路里别的分集用的域名。
func bofengMediaOrigin(group string) string {
	for _, part := range strings.Split(group, "#") {
		_, address, found := strings.Cut(part, "$")
		if !found {
			address = part
		}
		address = strings.TrimSpace(strings.ReplaceAll(address, `\/`, `/`))
		if strings.HasPrefix(address, "//") {
			address = "https:" + address
		}
		if !strings.HasPrefix(strings.ToLower(address), "http") {
			continue
		}
		parsed, err := url.Parse(address)
		if err != nil || parsed.Host == "" {
			continue
		}
		return parsed.Scheme + "://" + parsed.Host + "/"
	}
	return ""
}

func bofengMediaURL(base, raw string) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, `\/`, `/`))
	raw = strings.Trim(raw, "\"' \t")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	resolved := raw
	if !strings.HasPrefix(strings.ToLower(resolved), "http") && base != "" {
		reference, refErr := url.Parse(raw)
		origin, originErr := url.Parse(base)
		if refErr == nil && originErr == nil {
			resolved = origin.ResolveReference(reference).String()
		}
	}
	if !isProviderHTTPMediaURL(resolved) {
		return ""
	}
	return resolved
}

type bofengPlayItem struct {
	title string
	url   string
}

// bofengRouteWords 是「线路名/清晰度/语言/来源」这类标记，不是集号。
// 上游实测：《闪婚》的 vod_play_url 是「百度云$…#HD国语$…」，声明 2 条但
// 其实是同一部电影的 2 条线路，而《闪婚遇上裴先生2》是「第01集$…」的整剧单流——
// 也就是说 $ 前面的名字**语义不可靠**，必须分类后再决定怎么用。
var bofengRouteWords = []string{
	"hd", "bd", "4k", "1080p", "720p", "480p", "svip", "vip",
	"蓝光", "超清", "高清", "标清", "原画",
	"国语", "粤语", "英语", "日语", "韩语", "中字", "中英", "字幕", "配音",
	"百度云", "百度", "网盘", "夸克", "迅雷", "阿里云",
	"线路", "备用", "默认", "正片", "完整版", "全集", "完结",
	"tv", "tc", "web", "hdrip", "bdrip", "mp4",
	"抢先", "预告", "花絮", "片花",
}

// bofengDeclaredTotal 匹配 vod_remarks 里的**总集数**声明：
// 「更新至 66 集」「共 66 集」「全 66 集」。
//
// 尾缀（集/话/期）**必须存在**，所以镜像站的 `全集36` 这种写法解析不出来。这是刻意的：
// 一旦把尾缀改成可选，`更新至2024` 会被 \d{1,3} 截成 `202`，`全 5`、`共 3` 也会变成集数；
// 而 bofengDeclaredEpisodes 的返回值直接决定 bofengSliceAcceptable 会不会判「切出来的集数
// 超过声明×3」→ 把一部 66 集的整剧退回成一条「完整版 132 分钟」。宁可漏解析（fail-open：
// 不设限、照切），也不要误解析。别为了多认一种写法去放宽这个正则。
//
// 也刻意不含「第 47 集」——那是单集编号，不是总集数；`更新至第12集` 同理不认。
var bofengDeclaredTotal = regexp.MustCompile(`(?:更新至|更新到|共|全集|全)\s*0*(\d{1,3})\s*(?:集|话|期)`)

// bofengIsEpisodeTitle 判断 $ 前面那一段是不是真的集号。
func bofengIsEpisodeTitle(title string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	lower := strings.ToLower(title)
	for _, word := range bofengRouteWords {
		if strings.Contains(lower, word) {
			return false
		}
	}
	// 「第01集」「更新至12集」「EP03」这类走公共正则；
	// 少数镜像只写裸数字（1 / 01），长度 1~4 位且是正整数才算集号。
	if reEpisodeNumber.MatchString(title) {
		return true
	}
	// 裸数字要再挡一层：上游偶尔用 `720$url#1080$url` 表示两条**清晰度**线路，
	// 直接按「裸数字=集号」处理会凭空造出一部「720 集」的剧。500 集以上的短剧不存在，
	// 所以 >500 的裸数字一律不当集号（带中文的「第480集」「更新至480集」在上面走正则，不受影响）。
	if len(title) > 4 || !bofengNumericID(title) {
		return false
	}
	number, err := strconv.Atoi(title)
	return err == nil && number <= bofengMaxEpisodeNumber
}

// bofengEpisodeItems 只保留真集条目。线路名条目（百度云/HD国语…）会被剔除：
// 它们是同一部片的不同来源，当成「第 2 集」会让选集页凭空多出几集。
func bofengEpisodeItems(items []bofengPlayItem) []bofengPlayItem {
	episodes := make([]bofengPlayItem, 0, len(items))
	for _, item := range items {
		if bofengIsEpisodeTitle(item.title) {
			episodes = append(episodes, item)
		}
	}
	return episodes
}

// bofengPrimaryRoute 在没有真集号时挑一条真正能播的线路：优先 m3u8，其次 mp4，
// 最后才退回第一条（上游偶尔把网盘分享页混在 mp4/m3u8 之前）。
func bofengPrimaryRoute(items []bofengPlayItem) (bofengPlayItem, bool) {
	if len(items) == 0 {
		return bofengPlayItem{}, false
	}
	for _, item := range items {
		if strings.HasSuffix(strings.ToLower(bofengURLPath(item.url)), ".m3u8") {
			return item, true
		}
	}
	for _, item := range items {
		if strings.HasSuffix(strings.ToLower(bofengURLPath(item.url)), ".mp4") {
			return item, true
		}
	}
	return items[0], true
}

// bofengChapters 把真集条目转成章节。key 用位次而不是集名：集名可能是
// 「正片」「第01集」这类重复/带格式的文本，拿它当 key 会撞 ID。
func bofengChapters(sourceID string, items []bofengPlayItem) []Chapter {
	chapters := make([]Chapter, 0, len(items))
	for index, item := range items {
		number := episodeIndex(item.title, index+1)
		title := strings.TrimSpace(item.title)
		if title == "" {
			title = fmt.Sprintf("第 %d 集", number)
		}
		chapters = append(chapters, Chapter{
			ID:             providerChapterID(sourceBofeng, sourceID, strconv.Itoa(index+1)),
			Source:         sourceBofeng,
			Title:          truncate(title, 128),
			CurrentEpisode: rawEpisode(number),
			VideoURL:       item.url,
		})
	}
	return chapters
}

// bofengSingleChapterTitle 给「只有一条播不了分集的播放串」起个诚实的名字。
// 探测到真实时长就写「完整版（约 XX 分钟）」——用户点之前就知道会看多久，
// 也不会以为这是「第 1 集」。
func bofengSingleChapterTitle(item bofengPlayItem, total time.Duration, probed bool) string {
	if probed && total > bofengSingleEpisodeLimit {
		minutes := int(total.Minutes() + 0.5)
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("完整版（约 %d 分钟）", minutes)
	}
	title := strings.TrimSpace(item.title)
	if title == "" {
		return "正片"
	}
	return title
}

// bofengPlayItems 拆 vod_play_url。
// 线路之间用 $$$ 分隔（与 vod_play_from 的线路名一一对应），线路内部用 # 分隔分集，
// 每集是「集名$地址」，用第一个 $ 切（地址本身不会再出现 $）。
// 有 m3u8 线路优先用 m3u8（本 App 的播放器只吃 HLS/MP4，别的线路可能是网盘或专用协议）。
func bofengPlayItems(playFrom, playURL string) []bofengPlayItem {
	playURL = strings.TrimSpace(playURL)
	if playURL == "" {
		return nil
	}
	groups := strings.Split(playURL, "$$$")
	froms := strings.Split(strings.TrimSpace(playFrom), "$$$")
	pick := 0
	for index, name := range froms {
		if index < len(groups) && strings.Contains(strings.ToLower(name), "m3u8") {
			pick = index
			break
		}
	}
	if pick >= len(groups) || strings.TrimSpace(groups[pick]) == "" {
		return nil
	}
	base := bofengMediaOrigin(groups[pick])
	items := make([]bofengPlayItem, 0, 64)
	seen := map[string]bool{}
	for _, part := range strings.Split(groups[pick], "#") {
		title, address, found := strings.Cut(part, "$")
		if !found {
			title, address = "", part
		}
		address = bofengMediaURL(base, address)
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		items = append(items, bofengPlayItem{title: strings.TrimSpace(title), url: address})
		if len(items) >= 2000 {
			break
		}
	}
	return items
}

// ---- 整剧单流处理 ----
//
// 问题：上游有一批短剧只给 1 条播放串，而那条 m3u8 里其实是一整部剧
// （《闪婚遇上裴先生2》实测 2655 个分片 × 3 秒 ≈ 132.7 分钟 = 66 集）。不处理的话，用户点
// 「第 1 集」会从头播完整部、选集页也只有 1 条，「选集」这个交互等于没有。
// 同一类坑还有反向情况：《闪婚》(2022 老片) 声明 2 条，但两条是线路名
// （百度云 / HD国语），合起来只是一部 91.5 分钟的电影正片 —— 把它当「2 集」
// 或按时长切片都是错的，见 bofengIsEpisodeTitle / bofengShortDrama。
//
// 选择的方案：**逻辑分集（分片区间切片）**，对象仅限「确认是短剧的长单流」。
//   1. 详情阶段拉一次 m3u8，解析出分片列表与总时长；
//   2. 判定为整剧单流后，按 120 秒把连续分片切成 N 个逻辑集，
//      章节 ID 里只存「起始分片下标-结束分片下标」（bofengSliceKey）；
//   3. 播放阶段重新拉一次 m3u8（地址可能换域名/换分片名，所以不缓存分片 URL），
//      按区间把分片行原样拼成一份子播放列表交给本机代理——
//      播放器看到的就是一集正常长度的点播列表，起播即本集开头、播完即本集结尾。
//
// 为什么不直接把整部剧标成「完整版（约 133 分钟）」：那只是把问题讲清楚，没解决
// 「没法选集、进度条 133 分钟、误触就跳集」的体验问题；而切片在 ExoPlayer 侧零成本
// （它只看 #EXTINF 与分片 URI），风险集中在本机拼串这一层，出问题还能退回整条播放列表。
// 但电影不切：切了就是灾难，宁可只显示一条「完整版（约 92 分钟）」。
//
// 保守条件（任一不满足就不切，退回「单集」并如实标注为完整版）：
//   - 没有 ≥2 条真集条目（有真集就是正常多集；线路名条目不算集，见 bofengRouteWords）；
//   - 是点播列表（带 #EXT-X-ENDLIST，直播切片没有意义）；
//   - 没有密钥轮换（出现 **多个不同的** #EXT-X-KEY URI 时子列表可能拿错 key；
//     同一把 key 被重复写多行不算轮换，见 bofengKeyURI）；
//   - 剧种确实是短剧（type_id_1/type_id == 58「短剧大全」，或题材里明说「短剧」）
//     且总时长 > 8 分钟。**不按时长单独判定**：电影 90 分钟是正常的，
//     只看时长会把《闪婚》这类正片切成几十段。
//   - 切出来的集数落在 [3, 240]；
//   - 切出来的集数不超过 vod_remarks 声明总集数的 3 倍（见 bofengSliceAcceptable），
//     否则说明这条流其实是「一条正常的长单集」而不是整部剧。
// 另外末集不足 bofengTailMin（20 秒）时并入前一集，宁可少一集也不要 2 秒的假集。

type bofengSegment struct {
	lines    []string
	duration time.Duration
}

type bofengManifest struct {
	header   []string
	segments []bofengSegment
	vod      bool
	rotated  bool
}

func bofengSegmentDuration(lines []string) time.Duration {
	for _, line := range lines {
		if !strings.HasPrefix(line, "#EXTINF:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "#EXTINF:"))
		if comma := strings.IndexByte(value, ','); comma >= 0 {
			value = value[:comma]
		}
		seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || seconds <= 0 || seconds > 3600 {
			return 0
		}
		return time.Duration(seconds * float64(time.Second))
	}
	return 0
}

// bofengKeyURI 从一条 #EXT-X-KEY / #EXT-X-SESSION-KEY 里取出 URI="..." 的值。
// 没有 URI 的（METHOD=NONE，表示后续明文）返回空串。
func bofengKeyURI(line string) string {
	index := strings.Index(line, `URI="`)
	if index < 0 {
		return ""
	}
	rest := line[index+len(`URI="`):]
	if end := strings.IndexByte(rest, '"'); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return ""
}

// bofengParseManifest 把播放列表拆成「头部标签 + 每个分片自己的原始行」。
// 保留原始行而不是重新生成标签，是为了不丢 #EXT-X-MAP / #EXT-X-BYTERANGE /
// #EXT-X-DISCONTINUITY 这些会影响解码的标记。
//
// 头部与分片的边界是第一个 #EXTINF：它之前的标签（VERSION/TARGETDURATION/MAP/KEY…）
// 属于整份列表，之后的标签属于紧随其后的那个分片。
func bofengParseManifest(raw string) bofengManifest {
	var manifest bofengManifest
	var pending []string
	// 按 **URI 去重** 而不是按行数：镜像站常把同一把 key 重复写好几行，
	// 按行数会判成「多 key 轮换」→ 整剧不切 → 用户只拿到一条 130 分钟的「完整版」。
	// 只有出现两个不同的 URI 才是真的密钥轮换，那时子列表才可能拿错 key。
	keys := map[string]bool{}
	inSegments := false
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if text == "#EXT-X-ENDLIST" {
			manifest.vod = true
			continue
		}
		if !strings.HasPrefix(text, "#") {
			// 媒体分片：把挂在前面的标签一并收进本分片。
			pending = append(pending, text)
			manifest.segments = append(manifest.segments, bofengSegment{lines: pending, duration: bofengSegmentDuration(pending)})
			pending = nil
			inSegments = true
			continue
		}
		if strings.HasPrefix(text, "#EXT-X-KEY:") || strings.HasPrefix(text, "#EXT-X-SESSION-KEY:") {
			if uri := bofengKeyURI(text); uri != "" {
				keys[uri] = true
			}
		}
		if !inSegments && !strings.HasPrefix(text, "#EXTINF:") {
			manifest.header = append(manifest.header, text)
			continue
		}
		inSegments = true
		pending = append(pending, text)
	}
	manifest.rotated = len(keys) > 1
	return manifest
}

func bofengRenderManifest(manifest bofengManifest, from, to int) string {
	var builder strings.Builder
	for _, line := range manifest.header {
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	for _, segment := range manifest.segments[from:to] {
		for _, line := range segment.lines {
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("#EXT-X-ENDLIST\n")
	return builder.String()
}

func bofengURLPath(address string) string {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return ""
	}
	return parsed.Path
}

// bofengProbe 探测一条播放串的分片结构。失败一律返回 ok=false，由调用方按单集处理：
// 详情页不该因为探测失败而报错。
func (d *Downloader) bofengProbe(ctx context.Context, address string) (bofengManifest, time.Duration, bool) {
	if !strings.HasSuffix(strings.ToLower(bofengURLPath(address)), ".m3u8") {
		return bofengManifest{}, 0, false
	}
	probeCtx, cancel := context.WithTimeout(ctx, bofengProbeTimeout)
	defer cancel()
	playlist, _, err := d.fetchMediaPlaylist(probeCtx, address, "")
	if err != nil {
		return bofengManifest{}, 0, false
	}
	manifest := bofengParseManifest(playlist)
	total := time.Duration(0)
	for _, segment := range manifest.segments {
		total += segment.duration
	}
	if len(manifest.segments) == 0 || total <= 0 {
		return manifest, 0, false
	}
	return manifest, total, true
}

// bofengShortDrama 判断该条目是否属于「短剧」。
// 优先看上游的 type_id_1（苹果 CMS 的父类 id），拿不到时才退回题材文字——
// 兜底方向刻意保守：识别不出短剧就不切，宁可显示「完整版」，也不能把电影切碎。
func bofengShortDrama(row map[string]any) bool {
	for _, key := range []string{"type_id_1", "type_id"} {
		if nativeText(row[key]) == bofengShortDramaTypeID {
			return true
		}
	}
	category := nativeText(row["type_name"]) + " " + nativeText(row["vod_class"])
	return strings.Contains(category, "短剧")
}

// bofengWholeDrama 判定这条「长单流」是不是整部短剧。
func bofengWholeDrama(row map[string]any, total time.Duration) bool {
	if total <= bofengSingleEpisodeLimit {
		return false
	}
	return bofengShortDrama(row)
}

func bofengSliceKey(from, to int) string {
	return fmt.Sprintf("s%d-%d", from, to)
}

func bofengSliceRange(key string) (int, int, bool) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "s") {
		return 0, 0, false
	}
	left, right, found := strings.Cut(strings.TrimPrefix(key, "s"), "-")
	if !found {
		return 0, 0, false
	}
	from, fromErr := strconv.Atoi(left)
	to, toErr := strconv.Atoi(right)
	if fromErr != nil || toErr != nil || from < 0 || to <= from || to > 20000 {
		return 0, 0, false
	}
	return from, to, true
}

// bofengSliceCuts 计算逻辑集的结束下标（区间右开）。
func bofengSliceCuts(manifest bofengManifest) []int {
	ends := make([]int, 0, 32)
	accumulated := time.Duration(0)
	for index, segment := range manifest.segments {
		accumulated += segment.duration
		if accumulated >= bofengSliceTarget {
			ends = append(ends, index+1)
			accumulated = 0
		}
	}
	if len(ends) == 0 || ends[len(ends)-1] != len(manifest.segments) {
		ends = append(ends, len(manifest.segments))
	}
	// 末端是「余数」：整剧时长不是 120 秒的整数倍，最后一段常常只剩几秒。
	// 实测 28% 的整剧单流末集不足 20 秒，最短只有 2.1 秒——那种「第 74 集」点进去
	// 两秒就播完自动跳走，比少一集难受得多，所以直接并进前一集。
	if len(ends) >= 2 {
		start := ends[len(ends)-2]
		tail := time.Duration(0)
		for _, segment := range manifest.segments[start:ends[len(ends)-1]] {
			tail += segment.duration
		}
		if tail < bofengTailMin {
			ends = ends[:len(ends)-1]
			ends[len(ends)-1] = len(manifest.segments)
		}
	}
	return ends
}

// bofengDeclaredEpisodes 从 vod_remarks 里解析上游声明的**总集数**。
// 只认「更新至 66 集」「共 66 集」「全集 36」这类表述；刻意不认「第 47 集」——
// 那是单集编号而不是总集数，拿它当闸门会把正常的多集剧全判成异常。
//
// **实测局限（别高估它）**：抽查的 44 条整剧单流里，vod_remarks 几乎全是「完结」，
// 一条都解析不出总集数，所以这道闸门在真实语料上的触发率是 0。它防的是
// 「上游恰好写了集数」的情况，不是主力防线——真正的防线是 bofengShortDrama（只在
// type_id_1=58 下切）与 bofengSliceChapters 的 [3,240] 区间。
// 另外 value < 2 直接把「更新至1集」也归为未知：那正是「一部 46.9 分钟的真单集」
// 最可能的写法，所以这里宁可 fail-open（不设限），也不要误伤。
func bofengDeclaredEpisodes(remark string) int {
	match := bofengDeclaredTotal.FindStringSubmatch(remark)
	if len(match) < 2 {
		return 0
	}
	value, err := strconv.Atoi(match[1])
	if err != nil || value < 2 || value > bofengSliceMax {
		return 0
	}
	return value
}

// bofengSliceAcceptable 决定切出来的集数能不能用。
// declared 是声明的总集数，0 表示没声明（不设限）。切出集数超过声明 3 倍时，说明这条流
// 其实是「一条正常的长单集」而不是整部剧（实测真单集最长 46.9 分钟 = 23 个假集），
// 那还不如老老实实标一条「完整版」。
// 实测语料里 declared 恒为 0（remarks 全是「完结」），所以这条实际上没生效过——
// 见 bofengDeclaredEpisodes 的说明，别把它当成已经防住了假集切片。
func bofengSliceAcceptable(count, declared int) bool {
	if declared < 2 {
		return true
	}
	return count <= declared*3
}

// bofengSliceChapters 按 bofengSliceTarget 把分片切成逻辑集，返回 nil 表示不切。
func bofengSliceChapters(sourceID, address string, manifest bofengManifest) []Chapter {
	if !manifest.vod || manifest.rotated {
		return nil
	}
	ends := bofengSliceCuts(manifest)
	// 少于 3 集说明这条流本来就不长（切出来没意义），多于上限说明上游数据异常。
	if len(ends) < 3 || len(ends) > bofengSliceMax {
		return nil
	}
	chapters := make([]Chapter, 0, len(ends))
	start := 0
	for number, end := range ends {
		chapters = append(chapters, Chapter{
			ID:             providerChapterID(sourceBofeng, sourceID, bofengSliceKey(start, end)),
			Source:         sourceBofeng,
			Title:          fmt.Sprintf("第 %d 集", number+1),
			CurrentEpisode: rawEpisode(number + 1),
			VideoURL:       address,
		})
		start = end
	}
	return chapters
}

// bofengSlicePlaylist 在播放阶段把整剧单流裁成本集那一段分片。
func (d *Downloader) bofengSlicePlaylist(ctx context.Context, address string, from, to int) (string, string, error) {
	playlist, finalURL, err := d.fetchMediaPlaylist(ctx, address, "")
	if err != nil {
		return "", "", err
	}
	manifest := bofengParseManifest(playlist)
	if !manifest.vod || from < 0 || to > len(manifest.segments) || to <= from {
		return "", "", errors.New("暴风资源播放列表已变化，请刷新详情")
	}
	sliced := bofengRenderManifest(manifest, from, to)
	if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(sliced, "\ufeff")), "#EXTM3U") {
		return "", "", errors.New("暴风资源播放列表无效")
	}
	return sliced, finalURL, nil
}

// ---- 目录 / 搜索 / 分类 / 详情 / 播放 ----

func (d *Downloader) fetchBofengCatalogPage(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	if page < 1 || page > 100000 {
		return nil, false, errors.New("暴风资源目录页码无效")
	}
	if strings.TrimSpace(query) != "" {
		return d.searchBofeng(ctx, page, query)
	}
	if !validBofengCategory(category) {
		return nil, false, errors.New("暴风资源内容分类无效")
	}
	values := url.Values{"ac": {"detail"}, "pg": {strconv.Itoa(page)}}
	if category != "" {
		values.Set("t", category)
	}
	return d.bofengCatalogRequest(ctx, values, page)
}

func (d *Downloader) bofengCatalogRequest(ctx context.Context, values url.Values, page int) ([]Drama, bool, error) {
	body, err := d.bofengRequest(ctx, values)
	if err != nil {
		return nil, false, err
	}
	row, err := bofengDecode(body)
	if err != nil {
		return nil, false, err
	}
	// code != 1 / list 为 null 是「没有数据」，不是故障：抛错会让界面弹
	// 「暂时无法加载」，而用户实际只是搜不到或该分类暂时没有剧。
	if !bofengOK(row) {
		return []Drama{}, false, nil
	}
	items := bofengDramas(bofengRows(row))
	return items, bofengHasMore(row, page, len(items)), nil
}

func (d *Downloader) bofengSearchPage(ctx context.Context, query string, page int) ([]Drama, bool, error) {
	values := url.Values{"ac": {"detail"}, "wd": {query}, "pg": {strconv.Itoa(page)}}
	return d.bofengCatalogRequest(ctx, values, page)
}

// searchBofeng 搜索。上游只认汉字，而电视遥控器只能打拼音（字母原样提交必然 0 条），
// 所以纯字母关键词在「接口正常但没结果」时用红果联想转成中文候选词再搜一遍——
// 与七猫搜索的兜底逻辑保持一致，用户不用为了搜剧去换输入法。
func (d *Downloader) searchBofeng(ctx context.Context, page int, query string) ([]Drama, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 256 {
		return nil, false, errors.New("暴风资源搜索关键词无效")
	}
	items, more, err := d.bofengSearchPage(ctx, query, page)
	if err == nil && len(items) > 0 {
		return items, more, nil
	}
	if page > 1 {
		// 翻页阶段拿到空就结束，不再走拼音兜底：候选词只对应第 1 页，
		// 往后翻会把同一批结果重复堆在后面的页里。
		if err != nil {
			return nil, false, err
		}
		return []Drama{}, false, nil
	}
	if err == nil && isPinyinQuery(query) {
		for _, word := range d.pinyinSearchCandidates(ctx, query, pinyinCandidateLimit) {
			word = strings.TrimSpace(word)
			if word == "" || word == query {
				continue
			}
			found, foundMore, foundErr := d.bofengSearchPage(ctx, word, 1)
			if foundErr == nil && len(found) > 0 {
				return found, foundMore, nil
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	if err != nil {
		return nil, false, err
	}
	return []Drama{}, false, nil
}

// fetchBofengCategories 用 ac=list 的分类表构造分类栏。
// 苹果 CMS 的分类是两级：type_pid=0 是父类（如「短剧大全」），子类挂在它下面。
//
// 这里**父类和子类都要保留**。早先的实现以为「父类自身不挂剧集、点进去是空列表」，
// 于是有子类时就只展开子类、把父类丢掉——结果把「短剧大全(58)」整个丢了。
// 实测 ac=detail&t=58&pg=1 返回 total=12183 / pagecount=610，它恰恰是这一站最肥的分类。
// 现在父类排在它自己的子类前面：用户先看到「短剧大全」这个 1.2 万部的聚合入口，
// 再往下逐个子类挑。
func (d *Downloader) fetchBofengCategories(ctx context.Context) ([]nativeCategory, error) {
	body, err := d.bofengRequest(ctx, url.Values{"ac": {"list"}})
	if err != nil {
		return nil, err
	}
	row, err := bofengDecode(body)
	if err != nil {
		return nil, err
	}
	entries, _ := row["class"].([]any)
	type bofengClass struct {
		id     string
		parent string
		name   string
	}
	classes := make([]bofengClass, 0, len(entries))
	known := map[string]bool{}
	for _, entry := range entries {
		class, _ := entry.(map[string]any)
		if len(class) == 0 {
			continue
		}
		id := nativeText(class["type_id"])
		if !bofengNumericID(id) || known[id] {
			continue
		}
		parent := nativeText(class["type_pid"])
		if parent == "0" {
			parent = ""
		}
		name := strings.TrimSpace(cleanText(nativeText(class["type_name"])))
		if name == "" || len([]rune(name)) > 16 {
			continue
		}
		known[id] = true
		classes = append(classes, bofengClass{id: id, parent: parent, name: name})
	}
	children := map[string][]bofengClass{}
	for _, entry := range classes {
		if entry.parent != "" {
			children[entry.parent] = append(children[entry.parent], entry)
		}
	}
	categories := make([]nativeCategory, 0, bofengMaxCategories)
	seen := map[string]bool{}
	add := func(entry bofengClass) {
		if len(categories) >= bofengMaxCategories || seen[entry.id] {
			return
		}
		seen[entry.id] = true
		categories = append(categories, nativeCategory{ID: entry.id, Name: entry.name})
	}
	// 「短剧大全(58)」先铺：这是本站的主力分类（实测 t=58 有 12183 部），而上游 ac=list
	// 按 type_id 排序，58 落在 49 条里的第 38 位——分类栏要横滚 38 格才看得到，用户第一眼
	// 是「电影片 / 动作片 / 喜剧片」，可这个源的定位就是短剧。add 自带去重，
	// 所以下面主循环再遇到 58 会被直接跳过，不会重复。
	for _, entry := range classes {
		if entry.parent != "" || entry.id != bofengShortDramaTypeID {
			continue
		}
		add(entry)
		for _, child := range children[entry.id] {
			add(child)
		}
		break
	}
	// 其余父类按上游顺序入（父类自己是可翻页的大分类），紧接着它的子类。
	// 上游实测共 49 条 = 9 个父类 + 40 个子类，上限 64 装得下，不会被截断。
	//
	// 取舍：ac=list 不返回各分类的 total，要判断某个父类是不是空容器就得多发最多 9 次
	// ac=detail&t=<pid>&pg=1。产品口径已定：**分类栏允许出现空目录**，不为它加启动开销。
	// 理由很简单——丢 1.2 万部内容比多点一次空目录严重得多。
	for _, entry := range classes {
		if entry.parent != "" {
			continue
		}
		add(entry)
		for _, child := range children[entry.id] {
			add(child)
		}
	}
	// 兜底：多级分类或 type_pid 指向本次没返回的分类时，子类会被漏掉，按原顺序补上，
	// 免得用户在分类栏里只能看到一半内容。
	for _, entry := range classes {
		if entry.parent == "" || known[entry.parent] {
			continue
		}
		add(entry)
	}
	return categories, nil
}

func (d *Downloader) bofengDetailRow(ctx context.Context, sourceID string) (map[string]any, error) {
	body, err := d.bofengRequest(ctx, url.Values{"ac": {"detail"}, "ids": {sourceID}})
	if err != nil {
		return nil, err
	}
	row, err := bofengDecode(body)
	if err != nil {
		return nil, err
	}
	rows := bofengRows(row)
	if len(rows) == 0 {
		return nil, errors.New("暴风资源未返回该剧详情，请稍后重试")
	}
	first, _ := rows[0].(map[string]any)
	if len(first) == 0 {
		return nil, errors.New("暴风资源未返回该剧详情，请稍后重试")
	}
	return first, nil
}

// fetchBofengCover 只取封面，**不探测播放列表**。
//
// 封面补齐是在列表页批量触发的（app_cover_metadata.go 的 nativeCoverAddress 会对
// 缺封面的条目逐个调用）。如果这里走 fetchBofengDetail，每补一个封面都会顺带跑一次
// 完整 m3u8 下载 + 解析（实测 2655 分片 / 60KB / 单次 1.84 秒），列表页批量补齐时会
// 直接把界面拖死。这里只做一次 ac=detail 请求，直接读 vod_pic。
func (d *Downloader) fetchBofengCover(ctx context.Context, sourceID string) (string, error) {
	if !bofengNumericID(sourceID) {
		return "", errors.New("暴风资源剧集地址无效")
	}
	row, err := d.bofengDetailRow(ctx, sourceID)
	if err != nil {
		return "", err
	}
	drama, ok := bofengDramaFromRow(row)
	if !ok {
		return "", errors.New("暴风资源返回的剧集信息不完整")
	}
	if drama.SourceID != sourceID {
		return "", errors.New("暴风资源详情与请求剧集不符")
	}
	// 与 bofengDramaFromRow 用同一个取址口径（vod_pic + 接口域名兜底相对路径），
	// 但只算封面、不碰播放列表。
	cover := providerCoverAddress(row["vod_pic"], bofengAPIBaseURL)
	if strings.TrimSpace(cover) == "" {
		return "", errors.New("详情暂未返回海报地址")
	}
	return cover, nil
}

func (d *Downloader) fetchBofengDetail(ctx context.Context, sourceID string) (Drama, []Chapter, error) {
	if !bofengNumericID(sourceID) {
		return Drama{}, nil, errors.New("暴风资源剧集地址无效")
	}
	row, err := d.bofengDetailRow(ctx, sourceID)
	if err != nil {
		return Drama{}, nil, err
	}
	drama, ok := bofengDramaFromRow(row)
	if !ok {
		return Drama{}, nil, errors.New("暴风资源返回的剧集信息不完整")
	}
	if drama.SourceID != sourceID {
		return Drama{}, nil, errors.New("暴风资源详情与请求剧集不符")
	}
	items := bofengPlayItems(nativeText(row["vod_play_from"]), nativeText(row["vod_play_url"]))
	if len(items) == 0 {
		return Drama{}, nil, errors.New("该剧暂时没有可播放的分集")
	}
	var chapters []Chapter
	// 探测失败时置位：表示「我们压根不知道这条流到底是不是整部剧」。
	// bfeng10.com 有 4 个 IP、上游常态性抖动（同一个 URL 时而 200 时而 404），
	// 所以这条降级路径会被反复触发，必须让结果是**可自愈**的，不能把「1 集」当事实存下来。
	probeUnknown := false
	// $ 前面的名字可能是集号，也可能是线路名（百度云 / HD国语 / 正片…）。
	// 只有凑到 ≥2 条真集才当多集剧处理，否则整条播放串只代表一部片。
	if episodes := bofengEpisodeItems(items); len(episodes) >= 2 {
		chapters = bofengChapters(sourceID, episodes)
	} else {
		route, ok := bofengPrimaryRoute(items)
		if !ok {
			return Drama{}, nil, errors.New("该剧暂时没有可播放的分集")
		}
		// 探测一次真实时长：只有「短剧 + 时长明显超过单集」才切片，
		// 电影/长片走「完整版」分支（见 bofengWholeDrama 的注释）。
		//
		// 「探测过但失败」和「根本不用探测」必须分开：mp4 单文件本来就只有一集，
		// 集数 1 是事实；m3u8 拉不下来则是**未知**，1 是猜的。
		wantsPlaylist := strings.HasSuffix(strings.ToLower(bofengURLPath(route.url)), ".m3u8")
		manifest, total, probed := d.bofengProbe(ctx, route.url)
		probeUnknown = wantsPlaylist && !probed
		if probed && bofengWholeDrama(row, total) {
			// 声明集数闸门：上游声明了总集数、而我们切出的集数远超它，说明这条流
			// 其实是「一条正常的长单集」而不是整部剧，切下去全是假集。
			declared := bofengDeclaredEpisodes(nativeText(row["vod_remarks"]))
			sliced := bofengSliceChapters(sourceID, route.url, manifest)
			if len(sliced) > 0 && bofengSliceAcceptable(len(sliced), declared) {
				// 在标签里写清楚这是合成分集，用户在详情页就能看到来源说明。
				drama.Tags = bofengAppendTag(drama.Tags, "整剧单流·按 2 分钟合成分集")
				chapters = sliced
			}
		}
		if chapters == nil {
			title := bofengSingleChapterTitle(route, total, probed)
			if probeUnknown {
				// 不能把上游的「第01集」原样端给用户：那会让人以为这部 66 集的短剧
				// 只有一集。如实说「时长未知、可重试」，用户下拉刷新就能拿回分集。
				title = bofengProbeFailedTitle
			}
			chapters = []Chapter{{
				ID:             providerChapterID(sourceBofeng, sourceID, "1"),
				Source:         sourceBofeng,
				Title:          truncate(title, 128),
				CurrentEpisode: rawEpisode(1),
				VideoURL:       route.url,
			}}
		}
	}
	sortProviderChapters(chapters)
	if probeUnknown {
		// 探测失败：保留 bofengDramaFromRow 从 vod_remarks 解析出的声明集数
		// （「更新至66集」→ 66；解析不出就是 0 = 未知），绝不写 len(chapters)=1。
		// 配套地标上 EpisodeCountUnknown，nativeDetail 才不会拿 1 覆盖缓存里的集数。
		drama.EpisodeCountUnknown = true
	} else {
		drama.TotalEpisode, drama.EpisodeCount = len(chapters), len(chapters)
	}
	return drama, chapters, nil
}

func (d *Downloader) resolveBofengMedia(ctx context.Context, task Task) (providerMedia, error) {
	source, sourceID, valid := splitProviderDramaID(task.DramaID)
	prefix := providerChapterID(sourceBofeng, sourceID, "")
	if !valid || source != sourceBofeng || !bofengNumericID(sourceID) || !strings.HasPrefix(task.Chapter.ID, prefix) {
		return providerMedia{}, errors.New("暴风资源播放分集信息无效，请刷新详情")
	}
	address := strings.TrimSpace(firstNonEmpty(task.Chapter.VideoURL, task.Chapter.PageURL))
	if !isProviderHTTPMediaURL(address) {
		return providerMedia{}, errors.New("暴风资源播放地址无效，请刷新详情后重试")
	}
	// 上游播放明文 HLS，不需要 Referer/Cookie，这里刻意留空：
	// 带 site Referer 反而可能被 CDN 拒绝。
	media := providerMedia{URL: address}
	parsed, err := url.Parse(address)
	if err != nil || !strings.HasSuffix(strings.ToLower(parsed.Path), ".m3u8") {
		return media, nil
	}
	if from, to, slicedChapter := bofengSliceRange(strings.TrimPrefix(task.Chapter.ID, prefix)); slicedChapter {
		if sliced, finalURL, sliceErr := d.bofengSlicePlaylist(ctx, address, from, to); sliceErr == nil {
			media.URL = finalURL
			media.Playlist = sliced
			media.Duration = m3u8Duration(sliced)
			return media, nil
		}
		// 切片失败（上游重传导致分片数变了）就退回整条播放列表：用户至少能看到画面，
		// 而不是收到「播放失败」。章节里只存区间下标的代价就体现在这里。
	}
	return d.prepareWebProviderMedia(ctx, media, bofengChannelName)
}
