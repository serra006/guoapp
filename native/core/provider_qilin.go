package core

import (
	"context"
)

// 麒麟短剧（qilin）：苹果 CMS v10 采集接口站源，协议与暴风资源完全一致。
//
// 这里**不重复实现任何逻辑**：接口请求、vod_* 字段映射、播放串解析、整剧单流切片、
// 分类表的父子重排、封面轻量路径、播放切片，全部走 provider_maccms.go 的 maccms* 通用函数，
// 本文件只声明「麒麟」这一站与暴风的差异（域名 / 渠道名 / 短剧分类 id）。
//
// 上游实测（2026-11，tools 侧巡检脚本可复现）：
//
//	GET /api.php/provide/vod/?ac=list                    分类表：短剧 type_id=3, type_name=短剧, type_pid=0
//	GET /api.php/provide/vod/?ac=detail&pg=N&t=3         目录：total=36668, pagecount=1834, limit=20
//	GET /api.php/provide/vod/?ac=detail&wd=闪婚&pg=1      搜索：378 条命中
//	GET /api.php/provide/vod/?ac=detail&ids=<vod_id>      详情：list 长度 1
//
// 两个必须记住的实测结论：
//  1. vod_play_from = "sdm3u8$$$wsym3u8"，vod_play_url = "全集$url$$$全集$url2"——
//     **只有「全集」一条**，也就是说每一部剧都是一条覆盖整部剧的长单流
//     （实测 2509 / 2988 / 4155 / 4827 个分片 = 83.6 / 99.6 / 138.5 / 160.9 分钟，
//     单分片 2 秒）。这类播放串必须靠「整剧单流切片」才能变出选集，
//     按 120 秒切片得 41 / 49 / 69 / 80 集，与真实短剧集数吻合。
//  2. 因此 qilinShortDramaTypeID 必须是 "3"：maccmsShortDrama 只有在 row 的
//     type_id / type_id_1 等于它时才认「这是短剧」，maccmsWholeDrama 才会放行切片。
//     写错（或留空）的后果是每部剧只显示 1 个「完整版（约 84 分钟）」章节，选集功能等于没有。
//
// 封面主机是 shandianpic.vip，**不得**算进 qilinAPIHosts：那是图片 CDN 不是采集接口，
// 一旦被 providerSourceForURL 认成 qilin，providerURLCandidates 会把封面地址改写成
// 接口域名，封面全挂（暴风在 s3.bfllvip.com 上踩过同一个坑）。
const (
	qilinChannelName = "麒麟短剧"
	// 采集接口根地址。www 前缀那条实测可用，apex 域名作镜像兜底（见 qilinAPIBaseMirrors）。
	qilinAPIBaseURL = "https://www.qlys.cc/api.php/provide/vod/"
	// 「短剧」父类的 type_id。**整剧单流切片只在这个分类下做**，见上面的实测结论 2。
	qilinShortDramaTypeID = "3"
)

// qilinAPIBaseMirrors 麒麟采集接口的可用域名，按「当前实测可用」排序。
// 两条都是 HTTP 200；顺序即优先级：providerURLCandidates 按这个顺序展开候选，
// 第一个能通的就被 providerHosts 记住，后续请求直接走它。
var qilinAPIBaseMirrors = []string{
	"https://www.qlys.cc/api.php/provide/vod/",
	"https://qlys.cc/api.php/provide/vod/",
}

// qilinAPIHosts 允许被当作「麒麟采集接口域名」的 host（小写、不含端口）。
// 改动域名时这里必须和 qilinAPIBaseMirrors 一起改，否则新域名会被当成普通外链：
// providerSourceForURL 认不出来 -> 不算本站源 -> 镜像展开失效。
var qilinAPIHosts = map[string]bool{
	"qlys.cc":     true,
	"www.qlys.cc": true,
}

var qilinSite = &maccmsSite{
	source:      sourceQilin,
	channelName: qilinChannelName,
	apiBaseURL:  qilinAPIBaseURL,
	mirrors:     qilinAPIBaseMirrors,
	hosts:       qilinAPIHosts,
	shortTypeID: qilinShortDramaTypeID,
}

// qilinAPIHostAllowed 判断 host 是不是麒麟采集接口所在域名。
func qilinAPIHostAllowed(host string) bool {
	return maccmsAPIHostAllowed(qilinSite, host)
}

// validQilinCategory 校验分类参数，语义与 validBofengCategory 一致：
// 空串（全部分类）与纯数字 type_id 合法，其余一律拒绝。
func validQilinCategory(category string) bool {
	return maccmsValidCategory(category)
}

// fetchQilinCatalogPage 拉一页目录（带关键词时走搜索，含拼音兜底）。
func (d *Downloader) fetchQilinCatalogPage(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	return d.maccmsCatalogPage(ctx, qilinSite, page, category, query)
}

// fetchQilinSearchPage 拉一页搜索结果，不带拼音兜底（兜底在 maccmsSearch 里）。
func (d *Downloader) fetchQilinSearchPage(ctx context.Context, query string, page int) ([]Drama, bool, error) {
	return d.maccmsSearchPage(ctx, qilinSite, query, page)
}

// fetchQilinCategories 用 ac=list 的分类表构造分类栏，「短剧(3)」排在最前。
func (d *Downloader) fetchQilinCategories(ctx context.Context) ([]nativeCategory, error) {
	return d.fetchMaccmsCategories(ctx, qilinSite)
}

// fetchQilinDetail 取详情与分集。整剧单流会在这里被切成逻辑集（靠 shortTypeID=3）。
func (d *Downloader) fetchQilinDetail(ctx context.Context, sourceID string) (Drama, []Chapter, error) {
	return d.fetchMaccmsDetail(ctx, qilinSite, sourceID)
}

// fetchQilinCover 只取封面，**不探测播放列表**（列表页会批量补封面）。
func (d *Downloader) fetchQilinCover(ctx context.Context, sourceID string) (string, error) {
	return d.fetchMaccmsCover(ctx, qilinSite, sourceID)
}

// resolveQilinMedia 解析播放地址；章节 ID 里带分片区间时顺手把整剧单流裁成本集。
func (d *Downloader) resolveQilinMedia(ctx context.Context, task Task) (providerMedia, error) {
	return d.resolveMaccmsMedia(ctx, qilinSite, task)
}
