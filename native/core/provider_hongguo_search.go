package core

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	hongguoSearchTimeout      = 40 * time.Second
	hongguoSearchPageTimeout  = 12 * time.Second
	hongguoSearchPendingLimit = 32
	hongguoSearchNameCount    = 50
	// 二级联想最多补查多少个剧名词（并发），兼顾结果数量与请求量。
	hongguoSearchExpandLimit = 5
	// 联想结果达到这个数量就不再提示「搜索页已改版」，避免每次搜索都报警告。
	hongguoSearchQuietThreshold = 6
)

type hongguoSearchEntry struct {
	Dramas    []Drama
	Total     int
	Limited   bool
	Warning   string
	ExpiresAt time.Time
}

type hongguoSearchCall struct {
	done     chan struct{}
	changed  chan struct{}
	entry    hongguoSearchEntry
	snapshot hongguoSearchEntry
	revision uint64
	err      error
}

type hongguoSearchProgressKey struct{}

func reportHongguoSearchProgress(ctx context.Context, entry hongguoSearchEntry) {
	if progress, ok := ctx.Value(hongguoSearchProgressKey{}).(func(hongguoSearchEntry)); ok && len(entry.Dramas) > 0 {
		entry.Total = max(entry.Total, len(entry.Dramas))
		progress(entry)
	}
}

func hongguoSearchKeyword(keyword string) (string, error) {
	keyword = norm.NFKC.String(strings.TrimSpace(keyword))
	if keyword == "" || !utf8.ValidString(keyword) || utf8.RuneCountInString(keyword) > 80 || strings.IndexFunc(keyword, unicode.IsControl) >= 0 {
		return "", errors.New("请输入 1 至 80 个字符的搜索词")
	}
	return keyword, nil
}

func (downloader *Downloader) searchHongguoDramas(ctx context.Context, keyword string) (hongguoSearchEntry, error) {
	return downloader.searchHongguoDramasProgress(ctx, keyword, nil)
}

func (downloader *Downloader) searchHongguoDramasProgress(ctx context.Context, keyword string, progress func(hongguoSearchEntry)) (hongguoSearchEntry, error) {
	keyword, err := hongguoSearchKeyword(keyword)
	if err != nil {
		return hongguoSearchEntry{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, hongguoSearchTimeout)
	defer cancel()
	client := downloader.hongguoClient()
	for {
		if err := ctx.Err(); err != nil {
			return hongguoSearchEntry{}, err
		}
		client.mu.Lock()
		if cached, found := client.searches[keyword]; found && time.Now().Before(cached.ExpiresAt) {
			client.mu.Unlock()
			return cloneHongguoSearchEntry(cached), nil
		}
		if pending := client.searchPending[keyword]; pending != nil {
			client.mu.Unlock()
			entry, err := downloader.waitHongguoSearch(ctx, pending, progress)
			if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() == nil {
				continue
			}
			return entry, err
		}
		if len(client.searchPending) >= hongguoSearchPendingLimit {
			client.mu.Unlock()
			return hongguoSearchEntry{}, errors.New("红果搜索请求较多，请稍后重试")
		}
		pending := &hongguoSearchCall{done: make(chan struct{}), changed: make(chan struct{})}
		client.searchPending[keyword] = pending
		client.mu.Unlock()
		fetchCtx := context.WithValue(ctx, hongguoSearchProgressKey{}, func(entry hongguoSearchEntry) {
			client.mu.Lock()
			pending.snapshot = cloneHongguoSearchEntry(entry)
			pending.revision++
			close(pending.changed)
			pending.changed = make(chan struct{})
			client.mu.Unlock()
			if progress != nil {
				progress(cloneHongguoSearchEntry(entry))
			}
		})
		entry, err := downloader.fetchHongguoSearch(fetchCtx, keyword)
		client.mu.Lock()

		if err == nil && entry.Warning == "" {
			if len(client.searches) >= 64 {
				client.searches = map[string]hongguoSearchEntry{}
			}
			entry.ExpiresAt = time.Now().Add(5 * time.Minute)
			client.searches[keyword] = entry
		}
		pending.entry, pending.err = entry, err
		delete(client.searchPending, keyword)
		close(pending.done)
		client.mu.Unlock()
		return cloneHongguoSearchEntry(entry), err
	}
}

func (downloader *Downloader) waitHongguoSearch(ctx context.Context, pending *hongguoSearchCall, progress func(hongguoSearchEntry)) (hongguoSearchEntry, error) {
	client := downloader.hongguoClient()
	var revision uint64
	for {
		client.mu.Lock()
		changed := pending.changed
		var update *hongguoSearchEntry
		if progress != nil && pending.revision > revision {
			entry := cloneHongguoSearchEntry(pending.snapshot)
			update = &entry
			revision = pending.revision
		}
		client.mu.Unlock()
		if update != nil {
			progress(*update)
		}
		select {
		case <-ctx.Done():
			return hongguoSearchEntry{}, ctx.Err()
		case <-pending.done:
			return cloneHongguoSearchEntry(pending.entry), pending.err
		case <-changed:
		}
	}
}

func (downloader *Downloader) fetchHongguoSearch(ctx context.Context, keyword string) (hongguoSearchEntry, error) {

	names, namesErr := downloader.fetchHongguoSearchNames(ctx, keyword)
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && len(names) > 0 {
			return hongguoSearchEntry{Dramas: names, Total: len(names), Limited: true, Warning: "搜索已到时限，已保留名称匹配结果，可重试"}, nil
		}
		return hongguoSearchEntry{}, err
	}
	reportHongguoSearchProgress(ctx, hongguoSearchEntry{Dramas: mergeHongguoSearchDramas([]Drama{}, names), Limited: true})
	pageCtx, cancel := context.WithTimeout(ctx, hongguoSearchPageTimeout)
	defer cancel()
	page, pageErr := downloader.fetchHongguoSearchPage(pageCtx, keyword)
	if errors.Is(ctx.Err(), context.Canceled) {
		return hongguoSearchEntry{}, ctx.Err()
	}
	if pageErr != nil && namesErr != nil || len(page.Dramas)+len(names) == 0 && (pageErr != nil || namesErr != nil) {
		return hongguoSearchEntry{}, errors.Join(pageErr, namesErr)
	}
	entry := hongguoSearchEntry{Dramas: make([]Drama, 0, len(page.Dramas)+len(names)), Total: page.Total, Limited: page.Limited}
	for _, batch := range [][]Drama{page.Dramas, names} {
		entry.Dramas = mergeHongguoSearchDramas(entry.Dramas, batch)
	}
	reportHongguoSearchProgress(ctx, entry)
	seasonsLimited := downloader.completeHongguoSearchSeasons(ctx, keyword, &entry)
	if errors.Is(ctx.Err(), context.Canceled) {
		return hongguoSearchEntry{}, ctx.Err()
	}
	seasonsLimited = seasonsLimited || errors.Is(ctx.Err(), context.DeadlineExceeded)
	query := keyword
	sort.SliceStable(entry.Dramas, func(left, right int) bool {
		leftTitle, rightTitle := entry.Dramas[left].DisplayTitle(), entry.Dramas[right].DisplayTitle()
		leftRank, rightRank := hongguoTitleSearchRank(leftTitle, query), hongguoTitleSearchRank(rightTitle, query)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftBase, leftSeason, leftUnit, _ := hongguoSearchSeason(leftTitle)
		rightBase, rightSeason, rightUnit, _ := hongguoSearchSeason(rightTitle)
		if leftSeason > 0 && rightSeason > 0 && leftUnit == rightUnit && hongguoSearchText(leftBase) == hongguoSearchText(rightBase) {
			return leftSeason < rightSeason
		}
		return false
	})
	if pageErr != nil {
		// 搜索页已改客户端渲染，这条腿基本拿不到数据；只要联想结果够看就不打扰用户，
		// 命中太小时才提示（否则每次搜索都挂一条警告，反而像故障）。
		if len(entry.Dramas) < hongguoSearchQuietThreshold {
			entry.Warning = "红果搜索页已改版，当前仅显示联想命中的剧集，可重试"
		}
	} else if namesErr != nil {
		entry.Warning = "红果名称检索暂不可用，结果可能缺少部分剧集，可重试"
	}
	if seasonsLimited {
		if entry.Warning != "" {
			entry.Warning += "；"
		}
		entry.Warning += "部分季数暂未补齐，已保留当前结果，可重试"
	}
	entry.Limited = entry.Limited || pageErr != nil || namesErr != nil || seasonsLimited
	if entry.Total < len(entry.Dramas) {
		entry.Total = len(entry.Dramas)
	}
	return entry, nil
}

func mergeHongguoSearchDramas(dramas, batch []Drama) []Drama {
	positions := make(map[string]int, len(dramas)+len(batch))
	for index, drama := range dramas {
		positions[drama.ID] = index
	}
	for _, drama := range batch {
		if drama.ID == "" {
			continue
		}
		if index, found := positions[drama.ID]; found {
			dramas[index] = mergeDramaMetadata(dramas[index], drama)
		} else {
			positions[drama.ID] = len(dramas)
			dramas = append(dramas, drama)
		}
	}
	return dramas
}

func (downloader *Downloader) fetchHongguoSearchNames(ctx context.Context, keyword string) ([]Drama, error) {
	return downloader.fetchHongguoSearchNamesWith(ctx, keyword, true)
}

// fetchHongguoSearchNamesLight 只取一级联想，不做二级扩展。
// 补季逻辑会对同一关键词反复查询（上限 32 次），若每次都带二级扩展，
// 请求量会放大到近 200 个，既慢又容易触发风控。
func (downloader *Downloader) fetchHongguoSearchNamesLight(ctx context.Context, keyword string) ([]Drama, error) {
	return downloader.fetchHongguoSearchNamesWith(ctx, keyword, false)
}

func (downloader *Downloader) fetchHongguoSearchNamesWith(ctx context.Context, keyword string, expand bool) ([]Drama, error) {
	records, err := downloader.fetchHongguoSuggestionRecords(ctx, keyword, hongguoSearchNameCount, hongguoSearchNameTimeout)
	if err != nil {
		return nil, err
	}
	dramas := hongguoSuggestionDramas(records)
	if !expand {
		return dramas, nil
	}
	// 一级联想通常只回几条带 series_id 的短剧，其余是剧名/热词/分类词。
	// 红果搜索页改成客户端渲染后 HTML 里已经没有结果，这里用这些剧名词再
	// 各查一次联想（二级联想），把相关剧集补到十几条，搜索才真正可用。
	queries := hongguoSuggestionQueries(records, hongguoSearchExpandLimit)
	if len(queries) == 0 {
		return dramas, nil
	}
	batches := make([][]Drama, len(queries))
	var wg sync.WaitGroup
	for index, query := range queries {
		wg.Add(1)
		go func(index int, query string) {
			defer wg.Done()
			extra, extraErr := downloader.fetchHongguoSuggestionRecords(
				ctx, query, hongguoSearchNameCount, hongguoSearchNameTimeout)
			if extraErr != nil {
				return // 二级联想失败不影响一级结果
			}
			batches[index] = hongguoSuggestionDramas(extra)
		}(index, query)
	}
	wg.Wait()
	for _, batch := range batches {
		dramas = mergeHongguoSearchDramas(dramas, batch)
	}
	return dramas, nil
}

// hongguoSuggestionDramas 从联想条目里取出可识别的剧集。
// 判据只看 series_id 是否合法，不再依赖 word_type 白名单：红果调整过类型枚举，
// 白名单对不上就会把结果静默过滤干净（表现为「搜索暂时无法加载」而接口其实有数据）。
func hongguoSuggestionDramas(records []hongguoSuggestionRecord) []Drama {
	dramas := make([]Drama, 0, len(records))
	for _, record := range records {
		if !hongguoNumericID.MatchString(mapString(record.VideoData, "series_id_str", "series_id")) {
			continue
		}
		drama := hongguoDramaFromAny(map[string]any{"video_data": record.VideoData, "name": record.Name, "keyword": record.Keyword}, "短剧")
		if drama.ID != "" && drama.DisplayTitle() != drama.SourceID {
			dramas = append(dramas, drama)
		}
	}
	return dramas
}

// hongguoSuggestionQueries 挑出一级联想里值得二次查询的「剧名类」词。
func hongguoSuggestionQueries(records []hongguoSuggestionRecord, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		name := strings.TrimSpace(record.Name)
		if name == "" || utf8.RuneCountInString(name) < 3 {
			continue
		}
		switch record.WordType {
		case "title", "short_play_category", "common_query":
		default:
			continue
		}
		key := hongguoSearchText(name)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func cloneHongguoSearchEntry(entry hongguoSearchEntry) hongguoSearchEntry {
	entry.Dramas = append([]Drama{}, entry.Dramas...)
	for index := range entry.Dramas {
		entry.Dramas[index].Tags = append([]string(nil), entry.Dramas[index].Tags...)
	}
	return entry
}

func hongguoSearchText(text string) string {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFKC.String(text))
	if normalized == "" {
		return strings.ToLower(strings.TrimSpace(text))
	}
	return normalized
}

func hongguoTitleSearchRank(title, query string) int {
	words := strings.FieldsFunc(norm.NFKC.String(query), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
	query = hongguoSearchText(query)
	title = hongguoSearchText(title)
	switch {
	case title == query:
		return 0
	case strings.HasPrefix(title, query):
		return 1
	case strings.Contains(title, query):
		return 2
	default:
		matches := 0
		for _, word := range words {
			if strings.Contains(title, hongguoSearchText(word)) {
				matches++
			}
		}
		if len(words) > 0 && matches == len(words) {
			return 3
		}
		return 4
	}
}

func (downloader *Downloader) fetchHongguoSearchPage(ctx context.Context, keyword string) (hongguoSearchEntry, error) {
	pageURL := hongguoBaseURL + "/search/" + url.PathEscape(keyword)
	body, err := downloader.fetchProviderText(ctx, pageURL, hongguoBaseURL+"/")
	if err != nil {
		return hongguoSearchEntry{}, err
	}
	if strings.Contains(body, "<?- html ?>") {
		// 红果 2026-10 起把 /search/ 改成纯客户端渲染（React SPA）：服务端只回
		// SSR 模板壳 <div id="root"><!--<?- html ?>--></div>，结果由前端 JS 现取，
		// HTML 里已经没有 routerData / searchList。首页与 /detail 仍是服务端渲染。
		return hongguoSearchEntry{}, errors.New("红果搜索页已改为客户端渲染，页面不再内嵌结果")
	}
	page := routerLoaderMap(parseRouterData(body), "search_(keyword)/page", "search_")
	rows, valid := page["searchList"].([]any)
	if page["isSuccess"] != true || !valid || mapString(page, "query") != keyword {
		return hongguoSearchEntry{}, errors.New("红果搜索未返回有效结果")
	}
	entry := hongguoSearchEntry{Dramas: make([]Drama, 0, len(rows))}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if len(nestedMap(row, "video_data")) == 0 {
			continue
		}
		drama := hongguoDramaFromAny(row, "短剧")
		if drama.ID != "" && !seen[drama.ID] {
			seen[drama.ID] = true
			entry.Dramas = append(entry.Dramas, drama)
		}
	}
	if len(rows) > 0 && len(entry.Dramas) == 0 {
		return hongguoSearchEntry{}, errors.New("红果搜索结果中没有可识别的剧集")
	}
	entry.Total, _ = strconv.Atoi(mapString(page, "totalCount"))

	entry.Limited = entry.Total > len(entry.Dramas)
	if entry.Total < len(entry.Dramas) {
		entry.Total = len(entry.Dramas)
	}
	return entry, nil
}
