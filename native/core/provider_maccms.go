package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 苹果 CMS v10（MacCMS provide/vod）采集接口的通用层。
//
// 目前有两个站源共用这套协议：暴风资源（bofeng）与麒麟短剧（qilin）。两者的接口形状、
// 播放串格式、以及「整剧单流」那几个坑完全一样，差别只有域名、渠道名和短剧分类的
// type_id。所以逻辑在这里只写一份，站源实例只提供 maccmsSite 参数：
//
//	provider_bofeng.go -> bofengSite（渠道「暴风资源」，短剧 type_id 58）
//	provider_qilin.go  -> qilinSite （渠道「麒麟短剧」，短剧 type_id 3）
//
// 上游接口（两家一致，实测 2026-11）：
//
//	GET /api.php/provide/vod/?ac=list                     分类表（class[]，带 type_pid）
//	GET /api.php/provide/vod/?ac=detail&pg=2&t=58          分类翻页（t 是 type_id）
//	GET /api.php/provide/vod/?ac=detail&wd=闪婚&pg=1       搜索（wd 必须 URL 编码，每页 20 条）
//	GET /api.php/provide/vod/?ac=detail&ids=148397         详情
//
// 统一返回 {code,msg,page,pagecount,limit,total,list[]}，单条是苹果 CMS 的 vod_* 字段。
// 播放走 m3u8 线路：vod_play_from="xxxm3u8"（线路名只要含 m3u8 就优先用），
// vod_play_url 里 $$$ 分隔线路、# 分隔条目、每条「名字$地址」，地址是明文 HLS。
//
// 两个必须记住的坑（暴风与麒麟都踩过）：
//  1. 播放/封面主机**不固定**（暴风见过 bfeng10.com、c1.rrcdnbf2.com、p.bfvp26.com、
//     s3.bfllvip.com，麒麟是 shandianpic.vip），所以 media 地址只能从 vod_play_url 里取，
//     不能写任何主机常量，也不能把播放主机算成「接口域名」（见 maccmsAPIHostAllowed）。
//  2. `$` 前面的名字**语义不可靠**：可能是集号（第01集）、也可能是线路名（百度云 /
//     HD国语）、也可能是整部剧（1 条播放串里 2655 个分片 = 132.7 分钟 = 66 集）。
//     章节与集数必须先分类再决定，见 bofengIsEpisodeTitle / maccmsWholeDrama。

// maccmsSite 描述一个「苹果 CMS 采集接口」站源的全部差异点。
// 除这里的 6 个字段外，其余处理逻辑对所有站源完全一致，任何新增站源只需要给出这 6 项。
type maccmsSite struct {
	// source 站源 id（sourceBofeng / sourceQilin），写进 Drama.ID / Chapter.ID / Chapter.Source。
	source string
	// channelName 给用户看的渠道名，也用于拼错误文案（"暴风资源" / "麒麟短剧"）。
	channelName string
	// apiBaseURL 采集接口根地址，进程内所有接口请求都以它为准（不含查询串）。
	apiBaseURL string
	// mirrors 同一接口的可用域名，**顺序即优先级**，供 providerURLCandidates 展开。
	mirrors []string
	// hosts 允许被当作「接口域名」的 host（小写、不含端口）。
	// 绝不能包含播放/封面主机：一旦认错，providerURLCandidates 会把媒体地址
	// 改写成接口域名，播放直接报废。
	hosts map[string]bool
	// shortTypeID 「短剧」父类的 type_id。整剧单流切片**只**在这个分类下做，
	// 写错的话每部短剧只会显示 1 个「完整版（约 84 分钟）」章节，选集功能等于没有。
	shortTypeID string
}

var bofengSite = &maccmsSite{
	source:      sourceBofeng,
	channelName: bofengChannelName,
	apiBaseURL:  bofengAPIBaseURL,
	mirrors:     bofengAPIBaseMirrors,
	hosts:       bofengAPIHosts,
	shortTypeID: bofengShortDramaTypeID,
}

// maccmsSites 是全部「苹果 CMS 采集接口」站源。新增这类站源时只需要加到这里，
// 接口域名判定与镜像展开（providerSourceForURL / providerURLCandidates）就自动生效。
var maccmsSites = []*maccmsSite{bofengSite, qilinSite}

// maccmsSiteForHost 按 host 找站源，认不出时返回 nil。
// 只匹配 site.hosts 里的接口域名，播放/封面主机永远匹配不上（见 maccmsSite.hosts 的注释）。
func maccmsSiteForHost(host string) *maccmsSite {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil
	}
	for _, site := range maccmsSites {
		if site.hosts[host] {
			return site
		}
	}
	return nil
}

// maccmsAPIHostAllowed 判断 host 是否属于该站源的「采集接口域名」。
func maccmsAPIHostAllowed(site *maccmsSite, host string) bool {
	if site == nil {
		return false
	}
	return site.hosts[strings.ToLower(strings.TrimSpace(host))]
}

// maccmsValidCategory 校验分类参数。苹果 CMS 的分类就是上游的 type_id（纯数字），
// 分类表是动态返回的，这里只做格式校验，避免把任意字符串塞进缓存键和 t= 参数。
// 空串表示「全部分类」，合法。
func maccmsValidCategory(category string) bool {
	if category == "" {
		return true
	}
	return bofengNumericID(category)
}

// maccmsDecode 用 UseNumber 解码：vod_id 是数字字面量，走 float64 会变成 1.48397e+05，
// 之后的 ID 校验和 URL 拼接全会挂。
//
// 解码本身复用 bofengDecode（纯字符串处理，与站源无关），这里只把错误文案换成
// 当前站源的渠道名——否则麒麟短剧返回脏数据时用户会看到「暴风资源返回的数据格式无效」。
func maccmsDecode(site *maccmsSite, body string) (map[string]any, error) {
	row, err := bofengDecode(body)
	if err != nil {
		return nil, errors.New(site.channelName + "返回的数据格式无效")
	}
	return row, nil
}

// maccmsRequest 统一走 d.fetchProviderText：掩码通道、限流、DNS 优选都挂在 d.client 上，
// 自己发 http.Get 会绕过这些保护，也会丢掉既有站点被墙时的降级能力。
func (d *Downloader) maccmsRequest(ctx context.Context, site *maccmsSite, values url.Values) (string, error) {
	address := site.apiBaseURL
	if encoded := values.Encode(); encoded != "" {
		address += "?" + encoded
	}
	return d.fetchProviderText(ctx, address, site.apiBaseURL)
}

// maccmsDramaFromRow 把一条 vod_* 记录映射成 Drama。
// 字段口径参考 qimaoDramaFromMap：ID/章节都用 providerDramaID / providerChapterID 拼，
// 这样 detail / resolve / download 三处都能靠 splitProviderDramaID 反解归属。
func maccmsDramaFromRow(site *maccmsSite, row map[string]any) (Drama, bool) {
	id := nativeText(row["vod_id"])
	title := strings.TrimSpace(cleanText(nativeText(row["vod_name"])))
	if !bofengNumericID(id) || title == "" {
		return Drama{}, false
	}
	cover := providerCoverAddress(row["vod_pic"], site.apiBaseURL)
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
		ID: providerDramaID(site.source, id), Source: site.source, SourceID: id,
		Title: truncate(title, 256), Name: truncate(title, 256),
		Desc: description, Intro: description,
		Cover: cover, CoverURL: cover,
		TotalEpisode: episodes, EpisodeCount: episodes,
		ChannelName: site.channelName, Category: category, CategoryName: category, TypeName: typeName,
		Remark: remark, Score: bofengScore(nativeText(row["vod_score"])),
		OnlineDate: providerReleaseDate(nativeText(row["vod_pubdate"])),
		Tags:       tags, ReleaseStatus: bofengReleaseStatus(remark),
	}, true
}

func maccmsDramas(site *maccmsSite, rows []any) []Drama {
	items := make([]Drama, 0, len(rows))
	seen := map[string]bool{}
	for _, entry := range rows {
		row, _ := entry.(map[string]any)
		if len(row) == 0 {
			continue
		}
		drama, ok := maccmsDramaFromRow(site, row)
		if !ok || seen[drama.ID] {
			continue
		}
		seen[drama.ID] = true
		items = append(items, drama)
	}
	return items
}

// maccmsChapters 把真集条目转成章节。key 用位次而不是集名：集名可能是
// 「正片」「第01集」这类重复/带格式的文本，拿它当 key 会撞 ID。
func maccmsChapters(site *maccmsSite, sourceID string, items []bofengPlayItem) []Chapter {
	chapters := make([]Chapter, 0, len(items))
	for index, item := range items {
		number := episodeIndex(item.title, index+1)
		title := strings.TrimSpace(item.title)
		if title == "" {
			title = fmt.Sprintf("第 %d 集", number)
		}
		chapters = append(chapters, Chapter{
			ID:             providerChapterID(site.source, sourceID, strconv.Itoa(index+1)),
			Source:         site.source,
			Title:          truncate(title, 128),
			CurrentEpisode: rawEpisode(number),
			VideoURL:       item.url,
		})
	}
	return chapters
}

// maccmsProbe 探测一条播放串的分片结构。失败一律返回 ok=false，由调用方按单集处理：
// 详情页不该因为探测失败而报错。
func (d *Downloader) maccmsProbe(ctx context.Context, address string) (bofengManifest, time.Duration, bool) {
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

// maccmsShortDrama 判断该条目是否属于「短剧」。
// 优先看上游的 type_id_1 / type_id 是否等于本站源的短剧分类 id，拿不到时才退回题材文字——
// 兜底方向刻意保守：识别不出短剧就不切，宁可显示「完整版」，也不能把电影切碎。
//
// 两个站的短剧 type_id 不同（暴风 58、麒麟 3），所以这里必须读 site.shortTypeID；
// 写死一个值会让另一个站的整剧单流永远切不开。
func maccmsShortDrama(site *maccmsSite, row map[string]any) bool {
	if site != nil && site.shortTypeID != "" {
		for _, key := range []string{"type_id_1", "type_id"} {
			if nativeText(row[key]) == site.shortTypeID {
				return true
			}
		}
	}
	category := nativeText(row["type_name"]) + " " + nativeText(row["vod_class"])
	return strings.Contains(category, "短剧")
}

// maccmsWholeDrama 判定这条「长单流」是不是整部短剧。
func maccmsWholeDrama(site *maccmsSite, row map[string]any, total time.Duration) bool {
	if total <= bofengSingleEpisodeLimit {
		return false
	}
	return maccmsShortDrama(site, row)
}

// maccmsSliceChapters 按 bofengSliceTarget 把分片切成逻辑集，返回 nil 表示不切。
func maccmsSliceChapters(site *maccmsSite, sourceID, address string, manifest bofengManifest) []Chapter {
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
			ID:             providerChapterID(site.source, sourceID, bofengSliceKey(start, end)),
			Source:         site.source,
			Title:          fmt.Sprintf("第 %d 集", number+1),
			CurrentEpisode: rawEpisode(number + 1),
			VideoURL:       address,
		})
		start = end
	}
	return chapters
}

// maccmsSlicePlaylist 在播放阶段把整剧单流裁成本集那一段分片。
func (d *Downloader) maccmsSlicePlaylist(ctx context.Context, site *maccmsSite, address string, from, to int) (string, string, error) {
	playlist, finalURL, err := d.fetchMediaPlaylist(ctx, address, "")
	if err != nil {
		return "", "", err
	}
	manifest := bofengParseManifest(playlist)
	if !manifest.vod || from < 0 || to > len(manifest.segments) || to <= from {
		return "", "", errors.New(site.channelName + "播放列表已变化，请刷新详情")
	}
	sliced := bofengRenderManifest(manifest, from, to)
	if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(sliced, "\ufeff")), "#EXTM3U") {
		return "", "", errors.New(site.channelName + "播放列表无效")
	}
	return sliced, finalURL, nil
}

// ---- 目录 / 搜索 / 分类 / 详情 / 播放 ----

func (d *Downloader) maccmsCatalogPage(ctx context.Context, site *maccmsSite, page int, category, query string) ([]Drama, bool, error) {
	if page < 1 || page > 100000 {
		return nil, false, errors.New(site.channelName + "目录页码无效")
	}
	if strings.TrimSpace(query) != "" {
		return d.maccmsSearch(ctx, site, page, query)
	}
	if !maccmsValidCategory(category) {
		return nil, false, errors.New(site.channelName + "内容分类无效")
	}
	values := url.Values{"ac": {"detail"}, "pg": {strconv.Itoa(page)}}
	if category != "" {
		values.Set("t", category)
	}
	return d.maccmsCatalogRequest(ctx, site, values, page)
}

func (d *Downloader) maccmsCatalogRequest(ctx context.Context, site *maccmsSite, values url.Values, page int) ([]Drama, bool, error) {
	body, err := d.maccmsRequest(ctx, site, values)
	if err != nil {
		return nil, false, err
	}
	row, err := maccmsDecode(site, body)
	if err != nil {
		return nil, false, err
	}
	// code != 1 / list 为 null 是「没有数据」，不是故障：抛错会让界面弹
	// 「暂时无法加载」，而用户实际只是搜不到或该分类暂时没有剧。
	if !bofengOK(row) {
		return []Drama{}, false, nil
	}
	items := maccmsDramas(site, bofengRows(row))
	return items, bofengHasMore(row, page, len(items)), nil
}

func (d *Downloader) maccmsSearchPage(ctx context.Context, site *maccmsSite, query string, page int) ([]Drama, bool, error) {
	values := url.Values{"ac": {"detail"}, "wd": {query}, "pg": {strconv.Itoa(page)}}
	return d.maccmsCatalogRequest(ctx, site, values, page)
}

// maccmsSearch 搜索。上游只认汉字，而电视遥控器只能打拼音（字母原样提交必然 0 条），
// 所以纯字母关键词在「接口正常但没结果」时用红果联想转成中文候选词再搜一遍——
// 与七猫搜索的兜底逻辑保持一致，用户不用为了搜剧去换输入法。
func (d *Downloader) maccmsSearch(ctx context.Context, site *maccmsSite, page int, query string) ([]Drama, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 256 {
		return nil, false, errors.New(site.channelName + "搜索关键词无效")
	}
	items, more, err := d.maccmsSearchPage(ctx, site, query, page)
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
			found, foundMore, foundErr := d.maccmsSearchPage(ctx, site, word, 1)
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

// fetchMaccmsCategories 用 ac=list 的分类表构造分类栏。
// 苹果 CMS 的分类是两级：type_pid=0 是父类（如「短剧大全」），子类挂在它下面。
//
// 这里**父类和子类都要保留**。早先的实现以为「父类自身不挂剧集、点进去是空列表」，
// 于是有子类时就只展开子类、把父类丢掉——结果把暴风的「短剧大全(58)」整个丢了。
// 实测 ac=detail&t=58&pg=1 返回 total=12183 / pagecount=610，它恰恰是那一站最肥的分类。
// 现在父类排在它自己的子类前面：用户先看到聚合入口，再往下逐个子类挑。
//
// 麒麟短剧同理：它的主力分类是「短剧(3)」（实测 total=36668 / pagecount=1834），
// 由 site.shortTypeID 决定谁排在第一位。
func (d *Downloader) fetchMaccmsCategories(ctx context.Context, site *maccmsSite) ([]nativeCategory, error) {
	body, err := d.maccmsRequest(ctx, site, url.Values{"ac": {"list"}})
	if err != nil {
		return nil, err
	}
	row, err := maccmsDecode(site, body)
	if err != nil {
		return nil, err
	}
	entries, _ := row["class"].([]any)
	type maccmsClass struct {
		id     string
		parent string
		name   string
	}
	classes := make([]maccmsClass, 0, len(entries))
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
		classes = append(classes, maccmsClass{id: id, parent: parent, name: name})
	}
	children := map[string][]maccmsClass{}
	for _, entry := range classes {
		if entry.parent != "" {
			children[entry.parent] = append(children[entry.parent], entry)
		}
	}
	categories := make([]nativeCategory, 0, bofengMaxCategories)
	seen := map[string]bool{}
	add := func(entry maccmsClass) {
		if len(categories) >= bofengMaxCategories || seen[entry.id] {
			return
		}
		seen[entry.id] = true
		categories = append(categories, nativeCategory{ID: entry.id, Name: entry.name})
	}
	// 主力短剧分类先铺：暴风是「短剧大全(58)」（实测 t=58 有 12183 部），麒麟是「短剧(3)」
	// （实测 t=3 有 36668 部）。上游 ac=list 按 type_id 排序，这个分类往往落在分类表的中后段，
	// 用户第一眼看到的是电影/动作片之类，可这两个源的定位就是短剧。add 自带去重，
	// 所以下面主循环再遇到它会被直接跳过，不会重复。
	if site.shortTypeID != "" {
		for _, entry := range classes {
			if entry.parent != "" || entry.id != site.shortTypeID {
				continue
			}
			add(entry)
			for _, child := range children[entry.id] {
				add(child)
			}
			break
		}
	}
	// 其余父类按上游顺序入（父类自己是可翻页的大分类），紧接着它的子类。
	// 暴风上游实测共 49 条 = 9 个父类 + 40 个子类，上限 64 装得下，不会被截断。
	//
	// 取舍：ac=list 不返回各分类的 total，要判断某个父类是不是空容器就得多发最多 9 次
	// ac=detail&t=<pid>&pg=1。产品口径已定：**分类栏允许出现空目录**，不为它加启动开销。
	// 理由很简单——丢上万部内容比多点一次空目录严重得多。
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

func (d *Downloader) maccmsDetailRow(ctx context.Context, site *maccmsSite, sourceID string) (map[string]any, error) {
	body, err := d.maccmsRequest(ctx, site, url.Values{"ac": {"detail"}, "ids": {sourceID}})
	if err != nil {
		return nil, err
	}
	row, err := maccmsDecode(site, body)
	if err != nil {
		return nil, err
	}
	rows := bofengRows(row)
	if len(rows) == 0 {
		return nil, errors.New(site.channelName + "未返回该剧详情，请稍后重试")
	}
	first, _ := rows[0].(map[string]any)
	if len(first) == 0 {
		return nil, errors.New(site.channelName + "未返回该剧详情，请稍后重试")
	}
	return first, nil
}

// fetchMaccmsCover 只取封面，**不探测播放列表**。
//
// 封面补齐是在列表页批量触发的（app_cover_metadata.go 的 nativeCoverAddress 会对
// 缺封面的条目逐个调用）。如果这里走 fetchMaccmsDetail，每补一个封面都会顺带跑一次
// 完整 m3u8 下载 + 解析（暴风实测 2655 分片 / 60KB / 单次 1.84 秒，麒麟单条更是
// 2509~4827 分片），列表页批量补齐时会直接把界面拖死。这里只做一次 ac=detail 请求，
// 直接读 vod_pic。
func (d *Downloader) fetchMaccmsCover(ctx context.Context, site *maccmsSite, sourceID string) (string, error) {
	if !bofengNumericID(sourceID) {
		return "", errors.New(site.channelName + "剧集地址无效")
	}
	row, err := d.maccmsDetailRow(ctx, site, sourceID)
	if err != nil {
		return "", err
	}
	drama, ok := maccmsDramaFromRow(site, row)
	if !ok {
		return "", errors.New(site.channelName + "返回的剧集信息不完整")
	}
	if drama.SourceID != sourceID {
		return "", errors.New(site.channelName + "详情与请求剧集不符")
	}
	// 与 maccmsDramaFromRow 用同一个取址口径（vod_pic + 接口域名兜底相对路径），
	// 但只算封面、不碰播放列表。
	cover := providerCoverAddress(row["vod_pic"], site.apiBaseURL)
	if strings.TrimSpace(cover) == "" {
		return "", errors.New("详情暂未返回海报地址")
	}
	return cover, nil
}

func (d *Downloader) fetchMaccmsDetail(ctx context.Context, site *maccmsSite, sourceID string) (Drama, []Chapter, error) {
	if !bofengNumericID(sourceID) {
		return Drama{}, nil, errors.New(site.channelName + "剧集地址无效")
	}
	row, err := d.maccmsDetailRow(ctx, site, sourceID)
	if err != nil {
		return Drama{}, nil, err
	}
	drama, ok := maccmsDramaFromRow(site, row)
	if !ok {
		return Drama{}, nil, errors.New(site.channelName + "返回的剧集信息不完整")
	}
	if drama.SourceID != sourceID {
		return Drama{}, nil, errors.New(site.channelName + "详情与请求剧集不符")
	}
	items := bofengPlayItems(nativeText(row["vod_play_from"]), nativeText(row["vod_play_url"]))
	if len(items) == 0 {
		return Drama{}, nil, errors.New("该剧暂时没有可播放的分集")
	}
	var chapters []Chapter
	// 探测失败时置位：表示「我们压根不知道这条流到底是不是整部剧」。
	// 上游常态性抖动（同一个 URL 时而 200 时而 404），所以这条降级路径会被反复触发，
	// 必须让结果是**可自愈**的，不能把「1 集」当事实存下来。
	probeUnknown := false
	// $ 前面的名字可能是集号，也可能是线路名（百度云 / HD国语 / 正片 / 全集…）。
	// 只有凑到 ≥2 条真集才当多集剧处理，否则整条播放串只代表一部片。
	if episodes := bofengEpisodeItems(items); len(episodes) >= 2 {
		chapters = maccmsChapters(site, sourceID, episodes)
	} else {
		route, ok := bofengPrimaryRoute(items)
		if !ok {
			return Drama{}, nil, errors.New("该剧暂时没有可播放的分集")
		}
		// 探测一次真实时长：只有「短剧 + 时长明显超过单集」才切片，
		// 电影/长片走「完整版」分支（见 maccmsWholeDrama 的注释）。
		//
		// 「探测过但失败」和「根本不用探测」必须分开：mp4 单文件本来就只有一集，
		// 集数 1 是事实；m3u8 拉不下来则是**未知**，1 是猜的。
		wantsPlaylist := strings.HasSuffix(strings.ToLower(bofengURLPath(route.url)), ".m3u8")
		manifest, total, probed := d.maccmsProbe(ctx, route.url)
		probeUnknown = wantsPlaylist && !probed
		if probed && maccmsWholeDrama(site, row, total) {
			// 声明集数闸门：上游声明了总集数、而我们切出的集数远超它，说明这条流
			// 其实是「一条正常的长单集」而不是整部剧，切下去全是假集。
			declared := bofengDeclaredEpisodes(nativeText(row["vod_remarks"]))
			sliced := maccmsSliceChapters(site, sourceID, route.url, manifest)
			if len(sliced) > 0 && bofengSliceAcceptable(len(sliced), declared) {
				// 在标签里写清楚这是合成分集，用户在详情页就能看到来源说明。
				drama.Tags = bofengAppendTag(drama.Tags, "整剧单流·按 2 分钟合成分集")
				chapters = sliced
			}
		}
		if chapters == nil {
			title := bofengSingleChapterTitle(route, total, probed)
			if probeUnknown {
				// 不能把上游的「第01集」原样端给用户：那会让人以为这部几十集的短剧
				// 只有一集。如实说「时长未知、可重试」，用户下拉刷新就能拿回分集。
				title = bofengProbeFailedTitle
			}
			chapters = []Chapter{{
				ID:             providerChapterID(site.source, sourceID, "1"),
				Source:         site.source,
				Title:          truncate(title, 128),
				CurrentEpisode: rawEpisode(1),
				VideoURL:       route.url,
			}}
		}
	}
	sortProviderChapters(chapters)
	if probeUnknown {
		// 探测失败：保留 maccmsDramaFromRow 从 vod_remarks 解析出的声明集数
		// （「更新至66集」→ 66；解析不出就是 0 = 未知），绝不写 len(chapters)=1。
		// 配套地标上 EpisodeCountUnknown，nativeDetail 才不会拿 1 覆盖缓存里的集数。
		drama.EpisodeCountUnknown = true
	} else {
		drama.TotalEpisode, drama.EpisodeCount = len(chapters), len(chapters)
	}
	return drama, chapters, nil
}

func (d *Downloader) resolveMaccmsMedia(ctx context.Context, site *maccmsSite, task Task) (providerMedia, error) {
	source, sourceID, valid := splitProviderDramaID(task.DramaID)
	prefix := providerChapterID(site.source, sourceID, "")
	if !valid || source != site.source || !bofengNumericID(sourceID) || !strings.HasPrefix(task.Chapter.ID, prefix) {
		return providerMedia{}, errors.New(site.channelName + "播放分集信息无效，请刷新详情")
	}
	address := strings.TrimSpace(firstNonEmpty(task.Chapter.VideoURL, task.Chapter.PageURL))
	if !isProviderHTTPMediaURL(address) {
		return providerMedia{}, errors.New(site.channelName + "播放地址无效，请刷新详情后重试")
	}
	// 上游播放明文 HLS，不需要 Referer/Cookie，这里刻意留空：
	// 带 site Referer 反而可能被 CDN 拒绝。
	media := providerMedia{URL: address}
	parsed, err := url.Parse(address)
	if err != nil || !strings.HasSuffix(strings.ToLower(parsed.Path), ".m3u8") {
		return media, nil
	}
	if from, to, slicedChapter := bofengSliceRange(strings.TrimPrefix(task.Chapter.ID, prefix)); slicedChapter {
		if sliced, finalURL, sliceErr := d.maccmsSlicePlaylist(ctx, site, address, from, to); sliceErr == nil {
			media.URL = finalURL
			media.Playlist = sliced
			media.Duration = m3u8Duration(sliced)
			return media, nil
		}
		// 切片失败（上游重传导致分片数变了）就退回整条播放列表：用户至少能看到画面，
		// 而不是收到「播放失败」。章节里只存区间下标的代价就体现在这里。
	}
	return d.prepareWebProviderMedia(ctx, media, site.channelName)
}

// maccmsURLCandidates 把一条采集接口地址展开成按优先级排序的镜像候选。
// 只替换 scheme+host，路径与查询串（ac / pg / t / wd / ids）原样保留，
// 否则翻页与搜索会在镜像之间失去意义。首项恒为 site.mirrors 的第一条（当前实测可用），
// 所以老链接（旧域名还在本地缓存里）不会先白等一次超时。
func maccmsURLCandidates(site *maccmsSite, u *url.URL, rawURL string) []string {
	out := make([]string, 0, len(site.mirrors)+1)
	seen := map[string]bool{}
	add := func(candidate string) {
		if candidate == "" || seen[candidate] {
			return
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	for _, mirror := range site.mirrors {
		add(rehostProviderURL(u, mirror))
	}
	if len(out) == 0 {
		return []string{rawURL}
	}
	return out
}
