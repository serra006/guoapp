package core

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// 中文站源（七猫、鬼片、韩小圈、青空…）的搜索接口只认汉字：把拼音原样提交上去
// 一律返回 0 条。而电视端只有遥控器，能打出来的就是拼音字母，红果的搜索框靠
// 「联想下拉」把字母转成中文名，其它站源没有联想，等于没有输入入口。
//
// 这里用红果的联想接口当「拼音→中文」词库：联想本身就是拼音检索，
// 返回的是通用短剧词/剧名（与站源无关），借它的转写能力给别的站源兜底。
const (
	pinyinCandidateLimit = 3
	pinyinCandidateTTL   = 10 * time.Minute
	pinyinCandidateCache = 128
	// 太短的拼音（a、xd）联想噪声太大，直接放弃。
	pinyinMinLength = 2
	pinyinMaxLength = 40
)

type pinyinCandidateEntry struct {
	words     []string
	expiresAt time.Time
}

// isPinyinQuery 判断搜索词是不是「纯字母的拼音」。
// 带空格、数字、标点的都算用户在精确输入，不做转写。
func isPinyinQuery(query string) bool {
	query = strings.TrimSpace(query)
	length := utf8.RuneCountInString(query)
	if length < pinyinMinLength || length > pinyinMaxLength {
		return false
	}
	for _, char := range query {
		if char > unicode.MaxASCII || !unicode.IsLetter(char) {
			return false
		}
	}
	return true
}

// pinyinSearchCandidates 把拼音转成若干个中文候选词。
// 按字数从短到长排序：短词更像「题材词/常用词」（闪婚、总裁），
// 在别的站源更容易命中；长的大多是红果独有的完整剧名。
func (downloader *Downloader) pinyinSearchCandidates(ctx context.Context, query string, limit int) []string {
	query = strings.TrimSpace(query)
	if limit <= 0 || !isPinyinQuery(query) {
		return nil
	}
	key := strings.ToLower(query)

	downloader.pinyinMu.Lock()
	if entry, found := downloader.pinyinCache[key]; found && time.Now().Before(entry.expiresAt) {
		words := append([]string(nil), entry.words...)
		downloader.pinyinMu.Unlock()
		return words
	}
	downloader.pinyinMu.Unlock()

	items, err := downloader.hongguoSearchSuggestions(ctx, query)
	if err != nil || len(items) == 0 {
		// 失败不缓存：下次搜索还应该再试一次转写。
		return nil
	}
	sort.SliceStable(items, func(left, right int) bool {
		return utf8.RuneCountInString(items[left].Name) < utf8.RuneCountInString(items[right].Name)
	})
	words := make([]string, 0, limit)
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		words = append(words, name)
		if len(words) >= limit {
			break
		}
	}

	downloader.pinyinMu.Lock()
	if downloader.pinyinCache == nil || len(downloader.pinyinCache) >= pinyinCandidateCache {
		downloader.pinyinCache = map[string]pinyinCandidateEntry{}
	}
	downloader.pinyinCache[key] = pinyinCandidateEntry{
		words:     append([]string(nil), words...),
		expiresAt: time.Now().Add(pinyinCandidateTTL),
	}
	downloader.pinyinMu.Unlock()
	return words
}
