package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 七猫短剧（qmplaylet）：开放接口签名直连，无登录态、无鉴权 Cookie。
// 目录 /api/v1/playlet/index（按 tag 取单页，不支持分页），
// 详情 /player/api/v1/playlet/info，播放为 wtzw CDN 上的标准 HLS。
const (
	qimaoStoreBaseURL = "https://api-store.qmplaylet.com"
	qimaoReadBaseURL  = "https://api-read.qmplaylet.com"
	qimaoSiteBaseURL  = "https://www.qmplaylet.com"
	qimaoKeys         = "d3dGiJc651gSQ8w1"
	qimaoUserAgent    = "okhttp/3.12.11"
	qimaoMaxTags      = 18
)

// 七猫客户端把 qm-params（设备 JSON 的 base64）逐字符替换，服务端据此还原。
var qimaoCharMap = map[rune]rune{
	'+': 'P', '/': 'X', '0': 'M', '1': 'U', '2': 'l', '3': 'E', '4': 'r', '5': 'Y', '6': 'W', '7': 'b', '8': 'd', '9': 'J',
	'A': '9', 'B': 's', 'C': 'a', 'D': 'I', 'E': '0', 'F': 'o', 'G': 'y', 'H': '_', 'I': 'H', 'J': 'G', 'K': 'i', 'L': 't',
	'M': 'g', 'N': 'N', 'O': 'A', 'P': '8', 'Q': 'F', 'R': 'k', 'S': '3', 'T': 'h', 'U': 'f', 'V': 'R', 'W': 'q', 'X': 'C',
	'Y': '4', 'Z': 'p', 'a': 'm', 'b': 'B', 'c': 'O', 'd': 'u', 'e': 'c', 'f': '6', 'g': 'K', 'h': 'x', 'i': '5', 'j': 'T',
	'k': '-', 'l': '2', 'm': 'z', 'n': 'S', 'o': 'Z', 'p': '1', 'q': 'V', 'r': 'v', 's': 'j', 't': 'Q', 'u': '7', 'v': 'D',
	'w': 'w', 'x': 'n', 'y': 'L', 'z': 'e',
}

func validQimaoID(value string) bool {
	return value != "" && len(value) <= 20 && strings.TrimSpace(value) == value && isQimaoDigits(value)
}

func isQimaoDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validQimaoCategory 只接受数字 tag（含 -1 这类负数分类）。
func validQimaoCategory(category string) bool {
	if category == "" {
		return true
	}
	if len(category) > 12 {
		return false
	}
	value := strings.TrimPrefix(category, "-")
	return value != "" && isQimaoDigits(value)
}

func qimaoQMParams() string {
	device := map[string]string{
		"static_score": "0.8",
		"uuid":         "00000000-7fc7-08dc-0000-000000000000",
		"device-id":    "20250220125449b9b8cac84c2dd3d035c9052a2572f7dd0122edde3cc42a70",
		"sourceuid":    "aa7de295aad621a6",
		"refresh-type": "0",
		"model":        "22021211RC",
		"client-id":    "aa7de295aad621a6",
		"brand":        "Redmi",
		"sys-ver":      "12",
		"phone-level":  "H",
		"wlb-uid":      "aa7de295aad621a6",
		"session-id":   strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	raw, err := json.Marshal(device)
	if err != nil {
		return ""
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	var builder strings.Builder
	builder.Grow(len(encoded))
	for _, char := range encoded {
		if mapped, ok := qimaoCharMap[char]; ok {
			builder.WriteRune(mapped)
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// qimaoParamSign 对业务参数串（键名按字典序拼接，不含 qm-params）求签名。
func qimaoParamSign(params string) string {
	sum := md5.Sum([]byte(params + qimaoKeys))
	return hex.EncodeToString(sum[:])
}

// qimaoHeaderSign 对含 qm-params 的整套请求头求签名。
func qimaoHeaderSign(params string) string {
	sum := md5.Sum([]byte("AUTHORIZATION=app-version=10001application-id=com.duoduo.readchannel=unknownis-white=net-env=5platform=androidqm-params=" + params + "reg=" + qimaoKeys))
	return hex.EncodeToString(sum[:])
}

func qimaoData(value any) map[string]any {
	row, _ := value.(map[string]any)
	if row == nil {
		return nil
	}
	if data, ok := row["data"].(map[string]any); ok {
		return data
	}
	return nil
}

func qimaoPayloadError(value any) error {
	row, _ := value.(map[string]any)
	if row == nil {
		return errors.New("七猫返回的数据格式无效")
	}
	if detail, ok := row["errors"].(map[string]any); ok {
		message := firstNonEmpty(mapString(detail, "title"), mapString(detail, "details"), mapString(detail, "code"))
		if message == "" {
			message = "接口拒绝访问"
		}
		return fmt.Errorf("七猫接口返回错误：%s", truncate(message, 64))
	}
	if qimaoData(value) == nil {
		return errors.New("七猫返回的数据格式无效")
	}
	return nil
}

func qimaoGet(ctx context.Context, client *http.Client, limiter *requestLimiter, address string, limit int64) (any, error) {
	base, err := url.Parse(qimaoStoreBaseURL)
	if err != nil {
		return nil, errors.New("七猫接口地址无效")
	}
	parsed, err := url.Parse(address)
	if err != nil || !isProviderHTTPMediaURL(address) || parsed.User != nil {
		return nil, errors.New("七猫接口地址无效")
	}
	timeout := 15 * time.Second
	if background, _ := ctx.Value(backgroundCatalogKey{}).(bool); background {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("无法创建七猫请求")
	}
	params := qimaoQMParams()
	if params == "" {
		return nil, errors.New("无法初始化七猫设备参数")
	}
	request.Header.Set("User-Agent", qimaoUserAgent)
	request.Header.Set("content-type", "application/json; charset=utf-8")
	request.Header.Set("net-env", "5")
	request.Header.Set("reg", "")
	request.Header.Set("channel", "unknown")
	request.Header.Set("is-white", "")
	request.Header.Set("platform", "android")
	request.Header.Set("application-id", "com.duoduo.read")
	request.Header.Set("authorization", "")
	request.Header.Set("app-version", "10001")
	request.Header.Set("qm-params", params)
	request.Header.Set("sign", qimaoHeaderSign(params))
	if limiter != nil {
		release, err := limiter.acquire(ctx, request)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	transport := *client
	transport.Jar = nil
	previousRedirect := transport.CheckRedirect
	transport.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || providerMediaOrigin(next.URL) != providerMediaOrigin(base) && providerMediaOrigin(next.URL) != providerMediaOrigin(parsed) {
			return errors.New("七猫接口重定向地址异常")
		}
		if previousRedirect != nil {
			return previousRedirect(next, via)
		}
		return nil
	}
	response, err := transport.Do(request)
	if err != nil {
		return nil, fmt.Errorf("七猫连接失败：%w", publicError(err))
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	blocked := catalogResponseBlockReason(response, data) != ""
	if limiter != nil {
		limiter.observe(request, response)
	}
	observeSourceResponse(ctx, response)
	if readErr != nil || int64(len(data)) > limit {
		return nil, errors.New("七猫返回的数据过大或读取失败")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || blocked {
		if response.StatusCode == http.StatusNotFound {
			return nil, errors.New("七猫接口暂不可用，请稍后重试")
		}
		return nil, errors.New("七猫接口暂时不可用，请稍后重试")
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		return nil, errors.New("七猫返回的数据格式无效")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("七猫返回的数据格式无效")
	}
	return decoded, nil
}

func (d *Downloader) qimaoRequest(ctx context.Context, address string, limit int64) (any, error) {
	return qimaoGet(ctx, d.client, d.limiter, address, limit)
}

func qimaoCategoryAddress(tag string) string {
	if tag == "" {
		tag = "0"
	}
	return qimaoStoreBaseURL + "/api/v1/playlet/index?tag_id=" + url.QueryEscape(tag) +
		"&playlet_privacy=1&operation=1&sign=" + qimaoParamSign("operation=1playlet_privacy=1tag_id="+tag)
}

func qimaoDetailAddress(sourceID string) string {
	return qimaoReadBaseURL + "/player/api/v1/playlet/info?playlet_id=" + url.QueryEscape(sourceID) +
		"&sign=" + qimaoParamSign("playlet_id="+sourceID)
}

// qimaoSearchAddress 七猫真正的搜索接口。早期版本误判为「已停用」——
// 其实一直存在，只是参数名是 wd（不是 keyword/query），签名按参数名字母序拼接：
// 先 page 后 wd。每页 10 条，meta.next_page 表示还有下一页。
func qimaoSearchAddress(keyword string, page int) string {
	if page < 1 {
		page = 1
	}
	value := strconv.Itoa(page)
	return qimaoStoreBaseURL + "/api/v1/playlet/search?page=" + value +
		"&wd=" + url.QueryEscape(keyword) +
		"&sign=" + qimaoParamSign("page="+value+"wd="+keyword)
}

// qimaoCleanTitle 去掉搜索返回标题里的 <font> 高亮标签。
func qimaoCleanTitle(text string) string {
	if !strings.Contains(text, "<") {
		return strings.TrimSpace(text)
	}
	var builder strings.Builder
	depth := 0
	for _, r := range text {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			builder.WriteRune(r)
		}
	}
	return strings.TrimSpace(builder.String())
}

// qimaoEpisodeNumber 从「78集」这类文本里取集数。
func qimaoEpisodeNumber(text string) int {
	digits := make([]rune, 0, 6)
	for _, r := range strings.TrimSpace(text) {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
			continue
		}
		if len(digits) > 0 {
			break
		}
	}
	if len(digits) == 0 {
		return 0
	}
	number, err := strconv.Atoi(string(digits))
	if err != nil || number < 0 || number > 100000 {
		return 0
	}
	return number
}

// qimaoSearchRows 把搜索接口的行规整成目录接口的字段口径后复用通用映射：
// 搜索给 id（目录给 playlet_id）、total_num 形如「78集」、标题带高亮标签、
// 题材放在 sub_title（目录放在 tags）。
func qimaoSearchRows(rows []any) []Drama {
	items := make([]Drama, 0, len(rows))
	seen := map[string]bool{}
	for _, entry := range rows {
		row, _ := entry.(map[string]any)
		if len(row) == 0 {
			continue
		}
		normalized := make(map[string]any, len(row)+4)
		for key, value := range row {
			normalized[key] = value
		}
		if nativeText(normalized["playlet_id"]) == "" {
			normalized["playlet_id"] = row["id"]
		}
		normalized["title"] = qimaoCleanTitle(nativeText(row["title"]))
		if nativeText(normalized["total_episode_num"]) == "" {
			if number := qimaoEpisodeNumber(nativeText(row["total_num"])); number > 0 {
				normalized["total_episode_num"] = strconv.Itoa(number)
			}
		}
		if nativeText(normalized["tags"]) == "" {
			normalized["tags"] = nativeText(row["sub_title"])
		}
		drama, err := qimaoDramaFromMap(normalized)
		if err != nil || drama.ID == "" || seen[drama.ID] {
			continue
		}
		seen[drama.ID] = true
		items = append(items, drama)
	}
	return items
}

// searchQimaoNative 调真实搜索接口；失败或空结果由调用方决定是否降级。
func (d *Downloader) searchQimaoNative(ctx context.Context, keyword string, page int) ([]Drama, bool, error) {
	value, err := d.qimaoRequest(ctx, qimaoSearchAddress(keyword, page), 4<<20)
	if err != nil {
		return nil, false, err
	}
	if err := qimaoPayloadError(value); err != nil {
		return nil, false, err
	}
	data := qimaoData(value)
	rows, err := qimaoRows(data, "list", 200)
	if err != nil {
		return nil, false, err
	}
	more := false
	if meta, ok := data["meta"].(map[string]any); ok {
		more = nativeText(meta["next_page"]) != ""
	}
	return qimaoSearchRows(rows), more, nil
}

func qimaoDramaFromMap(row map[string]any) (Drama, error) {
	id := nativeText(row["playlet_id"])
	title := strings.TrimSpace(nativeText(row["title"]))
	if !validQimaoID(id) || title == "" {
		return Drama{}, errors.New("七猫返回的剧集信息不完整")
	}
	episodes := 0
	if row["total_episode_num"] != nil {
		value := strings.TrimSpace(nativeText(row["total_episode_num"]))
		number, err := strconv.Atoi(value)
		if err != nil || number < 0 || number > 100000 {
			return Drama{}, errors.New("七猫集数格式无效")
		}
		episodes = number
	}
	tags := make([]string, 0, 8)
	seen := map[string]bool{}
	addTag := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" || len([]rune(text)) > 24 || seen[text] || len(tags) >= 14 {
			return
		}
		seen[text] = true
		tags = append(tags, text)
	}
	for _, part := range strings.FieldsFunc(nativeText(row["tags"]), func(r rune) bool {
		return r == ',' || r == '，' || r == '/' || r == '|' || r == '、'
	}) {
		addTag(part)
	}
	addTag(nativeText(row["hot_value"]))
	if entries, ok := row["sort_tag"].([]any); ok {
		for _, entry := range entries {
			addTag(nativeText(entry))
		}
	}
	category := "短剧"
	if len(tags) > 0 {
		category = tags[0]
	}
	status := ""
	switch nativeText(row["is_over"]) {
	case "1":
		status = "finished"
	case "0":
		status = "ongoing"
	}
	intro := strings.TrimSpace(nativeText(row["intro"]))
	cover := providerCoverAddress(row["image_link"], qimaoSiteBaseURL+"/")
	return Drama{
		ID: providerDramaID(sourceQimao, id), Source: sourceQimao, SourceID: id,
		Title: truncate(title, 256), Name: truncate(title, 256),
		Desc: truncate(intro, 12000), Intro: truncate(intro, 12000),
		Cover: cover, CoverURL: cover, TotalEpisode: episodes, EpisodeCount: episodes,
		Category: category, ChannelName: "七猫", Tags: tags, ReleaseStatus: status,
	}, nil
}

func qimaoRows(data map[string]any, key string, maximum int) ([]any, error) {
	value, exists := data[key]
	if !exists || value == nil {
		return nil, nil
	}
	rows, ok := value.([]any)
	if !ok || len(rows) > maximum {
		return nil, errors.New("七猫目录数据格式无效")
	}
	return rows, nil
}

func qimaoDramasFromRows(rows []any) ([]Drama, error) {
	items := make([]Drama, 0, len(rows))
	seen := map[string]bool{}
	for _, entry := range rows {
		row, _ := entry.(map[string]any)
		if len(row) == 0 {
			return nil, errors.New("七猫目录数据格式无效")
		}
		drama, err := qimaoDramaFromMap(row)
		if err != nil {
			return nil, err
		}
		if seen[drama.ID] {
			continue
		}
		seen[drama.ID] = true
		items = append(items, drama)
	}
	return items, nil
}

// fetchQimaoCatalogPage 目录接口不支持分页（page>1 返回空表示到底）；
// 搜索接口支持分页，由 meta.next_page 决定 hasMore。
func (d *Downloader) fetchQimaoCatalogPage(ctx context.Context, page int, category, query string) ([]Drama, bool, error) {
	if page < 1 || page > 1000000 || len(query) > 1024 || !validQimaoCategory(category) {
		return nil, false, errors.New("七猫目录查询参数无效")
	}
	if strings.TrimSpace(query) != "" {
		return d.searchQimaoDramas(ctx, query, page)
	}
	if page > 1 {
		return nil, false, nil
	}
	value, err := d.qimaoRequest(ctx, qimaoCategoryAddress(category), 4<<20)
	if err != nil {
		return nil, false, err
	}
	if err := qimaoPayloadError(value); err != nil {
		return nil, false, err
	}
	rows, err := qimaoRows(qimaoData(value), "list", 200)
	if err != nil {
		return nil, false, err
	}
	items, err := qimaoDramasFromRows(rows)
	if err != nil {
		return nil, false, err
	}
	return items, false, nil
}

// searchQimaoDramas 先走官方搜索接口；该接口异常或当页无结果时，
// 退回「推荐页 + 命中分类页的标题匹配」这条兜底路径（覆盖度差，但不会空白）。
func (d *Downloader) searchQimaoDramas(ctx context.Context, query string, page int) ([]Drama, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 64 {
		return nil, false, errors.New("七猫搜索关键词无效")
	}
	if page < 1 {
		page = 1
	}
	items, more, nativeErr := d.searchQimaoNative(ctx, query, page)
	if nativeErr == nil && len(items) > 0 {
		return items, more, nil
	}
	if page > 1 {
		// 翻页阶段拿不到就不再降级，否则会把兜底结果重复堆在后面的页里。
		if nativeErr != nil {
			return nil, false, nativeErr
		}
		return nil, false, nil
	}
	// 七猫搜索只认汉字：电视遥控器只能打拼音，字母原样提交必然 0 条
	// （搜 dushi 返回空、搜「都市」才有 10 条）。接口本身正常但结果为 0 时，
	// 先把拼音转成中文候选词再搜一遍，否则遥控器用户等于搜不了东西。
	if nativeErr == nil && isPinyinQuery(query) {
		for _, word := range d.pinyinSearchCandidates(ctx, query, pinyinCandidateLimit) {
			if word == query {
				continue
			}
			found, foundMore, foundErr := d.searchQimaoNative(ctx, word, 1)
			if foundErr == nil && len(found) > 0 {
				return found, foundMore, nil
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	fallbackItems, _, fallbackErr := d.searchQimaoFallback(ctx, query)
	if len(fallbackItems) == 0 {
		// 搜索接口本身出错、兜底也没捞到东西，才是真的失败（界面显示「暂时无法加载」）；
		// 接口正常但没有匹配剧集时返回空列表，界面显示「没有找到匹配的短剧」。
		if nativeErr != nil {
			return nil, false, nativeErr
		}
		if fallbackErr != nil {
			return nil, false, fallbackErr
		}
	}
	return fallbackItems, false, nil
}

// searchQimaoFallback 在推荐页与命中分类页的标题里匹配（搜索接口不可用时的兜底）。
func (d *Downloader) searchQimaoFallback(ctx context.Context, query string) ([]Drama, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 64 {
		return nil, false, errors.New("七猫搜索关键词无效")
	}
	value, err := d.qimaoRequest(ctx, qimaoCategoryAddress("0"), 4<<20)
	if err != nil {
		return nil, false, err
	}
	if err := qimaoPayloadError(value); err != nil {
		return nil, false, err
	}
	data := qimaoData(value)
	rows, err := qimaoRows(data, "list", 200)
	if err != nil {
		return nil, false, err
	}
	needle := hongguoSearchText(query)
	matched := make([]Drama, 0, 12)
	seen := map[string]bool{}
	// includeAll 用于命中分类名的标签：七猫标题多不含题材词，靠分类名命中的整类都要给。
	collect := func(rows []any, includeAll bool) error {
		items, err := qimaoDramasFromRows(rows)
		if err != nil {
			return err
		}
		for _, drama := range items {
			if seen[drama.ID] || !includeAll && !strings.Contains(hongguoSearchText(drama.Title), needle) {
				continue
			}
			seen[drama.ID] = true
			matched = append(matched, drama)
			if len(matched) >= 30 {
				return nil
			}
		}
		return nil
	}
	if err := collect(rows, false); err != nil {
		return nil, false, err
	}
	if tags, ok := data["tag_items"].([]any); ok && len(matched) < 30 {
		fetched := 0
		for _, entry := range tags {
			if fetched >= 2 || len(matched) >= 30 {
				break
			}
			tag, _ := entry.(map[string]any)
			id := nativeText(tag["tag_id"])
			name := nativeText(tag["tag_name"])
			if id == "" || id == "0" || !validQimaoCategory(id) || !strings.Contains(hongguoSearchText(name), needle) {
				continue
			}
			fetched++
			extra, err := d.qimaoRequest(ctx, qimaoCategoryAddress(id), 4<<20)
			if err != nil {
				continue
			}
			if qimaoPayloadError(extra) != nil {
				continue
			}
			extraData := qimaoData(extra)
			if extraData == nil {
				continue
			}
			extraRows, err := qimaoRows(extraData, "list", 200)
			if err != nil {
				continue
			}
			if err := collect(extraRows, true); err != nil {
				continue
			}
		}
	}
	return matched, false, nil
}

func (d *Downloader) fetchQimaoCategories(ctx context.Context) ([]nativeCategory, error) {
	value, err := d.qimaoRequest(ctx, qimaoCategoryAddress("0"), 4<<20)
	if err != nil {
		return nil, err
	}
	if err := qimaoPayloadError(value); err != nil {
		return nil, err
	}
	rows, _ := qimaoData(value)["tag_items"].([]any)
	categories := make([]nativeCategory, 0, qimaoMaxTags)
	seen := map[string]bool{}
	for _, entry := range rows {
		tag, _ := entry.(map[string]any)
		id := nativeText(tag["tag_id"])
		name := strings.TrimSpace(nativeText(tag["tag_name"]))
		if id == "" || id == "0" || name == "" || len([]rune(name)) > 12 || seen[id] || !validQimaoCategory(id) {
			continue
		}
		seen[id] = true
		categories = append(categories, nativeCategory{ID: id, Name: name})
		if len(categories) >= qimaoMaxTags {
			break
		}
	}
	if len(categories) == 0 {
		return nil, errors.New("七猫暂未返回内容分类，请稍后重试")
	}
	return categories, nil
}

func (d *Downloader) fetchQimaoDetail(ctx context.Context, sourceID string) (Drama, []Chapter, error) {
	if !validQimaoID(sourceID) {
		return Drama{}, nil, errors.New("七猫剧集地址无效")
	}
	value, err := d.qimaoRequest(ctx, qimaoDetailAddress(sourceID), 8<<20)
	if err != nil {
		return Drama{}, nil, err
	}
	if err := qimaoPayloadError(value); err != nil {
		return Drama{}, nil, err
	}
	data := qimaoData(value)
	drama, err := qimaoDramaFromMap(data)
	if err != nil {
		return Drama{}, nil, err
	}
	if drama.SourceID != sourceID {
		return Drama{}, nil, errors.New("七猫详情与请求剧集不符")
	}
	rows, err := qimaoRows(data, "play_list", 10000)
	if err != nil {
		return Drama{}, nil, err
	}
	type episode struct {
		number  int
		index   int
		id      string
		address string
		seconds int
	}
	episodes := make([]episode, 0, len(rows))
	seenIDs, seenNumbers := map[string]bool{}, map[int]bool{}
	for index, entry := range rows {
		row, _ := entry.(map[string]any)
		id := nativeText(row["video_id"])
		address := strings.TrimSpace(nativeText(row["video_url"]))
		if !validQimaoID(id) || seenIDs[id] {
			return Drama{}, nil, errors.New("七猫分集目录包含无效或重复分集")
		}
		if !isProviderHTTPMediaURL(address) {
			continue
		}
		number := index + 1
		if row["sort"] != nil {
			value := strings.TrimSpace(nativeText(row["sort"]))
			if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= 100000 {
				number = parsed
			}
		}
		if seenNumbers[number] {
			return Drama{}, nil, errors.New("七猫分集编号重复，请刷新目录")
		}
		seconds := 0
		if parsed, err := strconv.Atoi(strings.TrimSpace(nativeText(row["duration"]))); err == nil && parsed > 0 && parsed <= 86400 {
			seconds = parsed
		}
		seenIDs[id], seenNumbers[number] = true, true
		episodes = append(episodes, episode{number: number, index: index, id: id, address: address, seconds: seconds})
	}
	if len(episodes) == 0 {
		return Drama{}, nil, errors.New("该剧暂时没有可播放的分集")
	}
	sort.SliceStable(episodes, func(i, j int) bool {
		if episodes[i].number == episodes[j].number {
			return episodes[i].index < episodes[j].index
		}
		return episodes[i].number < episodes[j].number
	})
	chapters := make([]Chapter, 0, len(episodes))
	for _, entry := range episodes {
		chapters = append(chapters, Chapter{
			ID: providerChapterID(sourceQimao, sourceID, entry.id), Source: sourceQimao,
			Title: fmt.Sprintf("第 %d 集", entry.number), CurrentEpisode: json.RawMessage(strconv.Itoa(entry.number)),
			VideoURL: entry.address, Referer: qimaoSiteBaseURL + "/",
		})
	}
	if len(chapters) > 0 {
		drama.Episodes = len(chapters)
		if total := strings.TrimSpace(nativeText(drama.TotalEpisode)); total == "" || total == "0" {
			drama.TotalEpisode = len(chapters)
			drama.EpisodeCount = len(chapters)
		}
	}
	return drama, chapters, nil
}

func (d *Downloader) resolveQimaoMedia(ctx context.Context, task Task) (providerMedia, error) {
	source, sourceID, valid := splitProviderDramaID(task.DramaID)
	prefix := providerChapterID(sourceQimao, sourceID, "")
	if !valid || source != sourceQimao || !validQimaoID(sourceID) || !strings.HasPrefix(task.Chapter.ID, prefix) {
		return providerMedia{}, errors.New("七猫播放分集信息无效，请刷新详情")
	}
	address := strings.TrimSpace(task.Chapter.VideoURL)
	if address == "" {
		address = strings.TrimSpace(task.Chapter.PageURL)
	}
	if !isProviderHTTPMediaURL(address) {
		return providerMedia{}, errors.New("七猫播放地址无效，请刷新详情后重试")
	}
	media := providerMedia{URL: address, Referer: firstNonEmpty(task.Chapter.Referer, qimaoSiteBaseURL+"/")}
	if parsed, err := url.Parse(address); err == nil && strings.HasSuffix(strings.ToLower(parsed.Path), ".m3u8") {
		return d.prepareWebProviderMedia(ctx, media, "七猫短剧")
	}
	return media, nil
}
