package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const hongguoAppBaseURL = "https://api5-normal-sinfonlineb.fqnovel.com"

const hongguoAppUserAgent = "com.phoenix.read/73532 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"

// hongguoAppMultiVideoModelPath 是当前唯一稳定可用的取流入口（2026-11 实测）。
const hongguoAppMultiVideoModelPath = "/novel/player/multi_video_model/v1/"

// errHongguoAppEmpty 表示红果返回 HTTP 200 + 0 字节。这是「身份不被接受 / 触发风控」
// 的典型表现，取流链路据此换一个新登记身份重试一次（只一次，不循环）。
var errHongguoAppEmpty = errors.New("红果 App 接口未返回有效数据")

// errHongguoAppNoMedia 表示响应正常但不含本次请求 vid 的播放信息。
var errHongguoAppNoMedia = errors.New("红果 App 未返回该视频的播放信息")

type hongguoCatalogCursor struct {
	Offset        int       `json:"offset"`
	SessionID     string    `json:"sessionId,omitempty"`
	LastID        string    `json:"lastId,omitempty"`
	PageSignature string    `json:"pageSignature,omitempty"`
	Initialized   bool      `json:"initialized"`
	Exhausted     bool      `json:"exhausted"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type hongguoCatalogState struct {
	Version   int                             `json:"version"`
	DeviceID  string                          `json:"deviceId"`
	InstallID string                          `json:"installId"`
	Feeds     map[string]hongguoCatalogCursor `json:"feeds"`
}

type hongguoAppClient struct {
	mu             sync.Mutex
	catalogMu      sync.Mutex
	baseURL        string
	state          hongguoCatalogState
	details        map[string]hongguoDetailEntry
	pending        map[string]*hongguoDetailCall
	searches       map[string]hongguoSearchEntry
	searchPending  map[string]*hongguoSearchCall
	suggestions    map[string]hongguoSuggestionEntry
	suggestPending map[string]*hongguoSuggestionCall
	danmaku        map[string]hongguoDanmakuCacheEntry
	danmakuPending map[string]*hongguoDanmakuCall
	// 设备登记身份（取流用）。与 state.DeviceID/InstallID 分开存：目录/详情/弹幕
	// 继续用原有随机身份 + 三签名，取流切到登记身份 + 不签名，互不影响。
	identityMu     sync.Mutex
	identityLoaded bool
	identity       hongguoIdentityState
	identityCall   *hongguoIdentityCall
}

func (downloader *Downloader) hongguoClient() *hongguoAppClient {
	downloader.hongguoOnce.Do(func() {
		downloader.hongguo = &hongguoAppClient{
			baseURL: hongguoAppBaseURL,
			state:   hongguoCatalogState{Version: 1, DeviceID: newHongguoDeviceID(), InstallID: newHongguoDeviceID(), Feeds: map[string]hongguoCatalogCursor{}},
			details: map[string]hongguoDetailEntry{}, pending: map[string]*hongguoDetailCall{},
			searches: map[string]hongguoSearchEntry{}, searchPending: map[string]*hongguoSearchCall{},
			suggestions: map[string]hongguoSuggestionEntry{}, suggestPending: map[string]*hongguoSuggestionCall{},
		}
	})
	return downloader.hongguo
}

func cloneHongguoCatalogState(state *hongguoCatalogState) *hongguoCatalogState {
	if state == nil {
		return nil
	}
	cloned := *state
	cloned.Feeds = make(map[string]hongguoCatalogCursor, len(state.Feeds))
	for name, cursor := range state.Feeds {
		cloned.Feeds[name] = cursor
	}
	return &cloned
}

func (downloader *Downloader) restoreHongguoCatalog(state *hongguoCatalogState) {
	if state == nil || state.Version != 1 || !hongguoNumericID.MatchString(state.DeviceID) || !hongguoNumericID.MatchString(state.InstallID) {
		return
	}
	cloned := cloneHongguoCatalogState(state)
	for name, cursor := range cloned.Feeds {
		known := false
		for _, genre := range hongguoAppGenres {
			known = known || name == genre.key || name == "category:"+genre.key
		}
		if !known || !cursor.Initialized || cursor.Offset < 0 || cursor.Offset > 1_000_000 || len(cursor.SessionID) > 4096 || len(cursor.LastID) > 256 || len(cursor.PageSignature) > 64 {
			delete(cloned.Feeds, name)
		}
	}
	client := downloader.hongguoClient()
	client.catalogMu.Lock()
	defer client.catalogMu.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.state.Feeds) == 0 {
		client.state = *cloned
	}
}

func (downloader *Downloader) hongguoCatalogSnapshot() *hongguoCatalogState {
	client := downloader.hongguoClient()
	client.mu.Lock()
	defer client.mu.Unlock()
	return cloneHongguoCatalogState(&client.state)
}

func hongguoCatalogInitialized(state *hongguoCatalogState) bool {
	if state != nil && state.Version == 1 {
		for _, cursor := range state.Feeds {
			if cursor.Initialized {
				return true
			}
		}
	}
	return false
}

func hongguoCatalogHasMore(state *hongguoCatalogState) bool {
	for _, genre := range hongguoAppGenres {
		if state == nil || !state.Feeds[genre.key].Exhausted {
			return true
		}
	}
	return false
}

func (downloader *Downloader) hongguoAppRequest(ctx context.Context, method, path string, extra url.Values, payload any) (map[string]any, error) {
	client := downloader.hongguoClient()
	client.mu.Lock()
	base, deviceID, installID := client.baseURL, client.state.DeviceID, client.state.InstallID
	client.mu.Unlock()
	query := url.Values{
		"aid": {"8662"}, "app_name": {"novelread"}, "version_code": {"73532"}, "version_name": {"7.3.5.32"},
		"manifest_version_code": {"73532"}, "update_version_code": {"73532"}, "channel": {"update_64"},
		"device_platform": {"android"}, "os": {"android"}, "ssmix": {"a"}, "device_type": {"25053RT47C"},
		"device_brand": {"Redmi"}, "language": {"zh"}, "os_api": {"36"}, "os_version": {"16"},
		"resolution": {"1280*2772"}, "dpi": {"520"}, "ac": {"wifi"}, "device_id": {deviceID}, "iid": {installID},
	}
	for key, values := range extra {
		query[key] = append([]string(nil), values...)
	}
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	attempts := downloader.cfg.Retries
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 3 {
		attempts = 3
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", hongguoAppUserAgent)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("X-XS-From-Web", "0")
		request.Header.Set("Sdk-Version", "2")
		comment, _ := ctx.Value(hongguoCommentKey{}).(bool)
		// 红果自 2026-10 起对 App 全接口（目录/详情/取流）都要求完整三签名
		// X-Gorgon + X-Argus + X-Ladon，且**播放/目录与弹幕用的是两套不同签名**：
		// 弹幕签名（provider_hongguo_comment_sign.go）的 Argus 缺 device_id、
		// 机型环境等字段，打到 video_detail / landpage / video_model 一律返回
		// HTTP 200 + 0 字节；必须改用播放签名（provider_hongguo_player_sign.go）。
		// 该签名会对 body 计算 X-SS-STUB 并写入 Argus 的 body 摘要。
		var commentNonce hongguoCommentNonce
		if comment {
			commentNonce, err = newHongguoCommentNonce()
			if err != nil {
				return nil, errors.New("无法准备红果请求签名")
			}
		}
		if comment {
			request.Header.Set("Comment-Source", "601")
			request.Header.Set("Server-Channel", "1000")
		}
		if payload != nil {
			request.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
		response, err := downloader.doPreparedCatalogRequest(request, 20*time.Second, func(prepared *http.Request) {
			now := time.Now()
			query.Set("_rticket", strconv.FormatInt(now.UnixMilli(), 10))
			prepared.URL.RawQuery = query.Encode()
			// 弹幕走弹幕签名（aid=3019 那套），其余（目录/详情/取流）走播放签名。
			if comment {
				signHongguoCommentRequest(prepared, commentNonce, now)
			} else {
				signHongguoPlayerRequest(prepared, body, now)
			}
		})
		if err != nil {
			lastErr = err
			var backoff *requestBackoff
			if errors.As(err, &backoff) || ctx.Err() != nil {
				return nil, err
			}
			continue
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, providerMaxBodyBytes+1))
		response.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if response.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("红果 App 接口 HTTP %d", response.StatusCode)
			if response.StatusCode >= 400 && response.StatusCode < 500 {
				return nil, lastErr
			}
			continue
		}
		if len(content) == 0 || len(content) > providerMaxBodyBytes {
			return nil, errHongguoAppEmpty
		}
		var result map[string]any
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.UseNumber()
		if err := decoder.Decode(&result); err != nil || result == nil {
			return nil, errors.New("红果 App 接口返回格式异常")
		}
		code := firstNonEmpty(mapString(result, "code", "Code", "status_code"), mapString(nestedMap(result, "BaseResp"), "StatusCode"))
		if code != "" && code != "0" {
			return nil, fmt.Errorf("红果 App 接口暂不可用（%s）", truncate(code, 20))
		}
		return result, nil
	}
	return nil, lastErr
}

// hongguoPlaybackQuery 构造取流请求的 query。参数集合与 hongguoAppRequest 的公共
// 部分一致，但 device_id/iid 换成设备登记接口下发的身份。
func hongguoPlaybackQuery(deviceID, installID string) url.Values {
	return url.Values{
		"aid": {"8662"}, "app_name": {"novelread"}, "version_code": {hongguoRegisterVersion},
		"version_name": {hongguoRegisterVersionNa}, "manifest_version_code": {hongguoRegisterVersion},
		"update_version_code": {hongguoRegisterVersion}, "channel": {"update_64"},
		"device_platform": {"android"}, "os": {"android"}, "ssmix": {"a"},
		"device_type": {"25053RT47C"}, "device_brand": {"Redmi"}, "language": {"zh"},
		"os_api": {"36"}, "os_version": {"16"}, "resolution": {"1280*2772"},
		"dpi": {"520"}, "ac": {"wifi"},
		"device_id": {deviceID}, "iid": {installID},
	}
}

// hongguoAppPlaybackRequest 是取流专用的「不签名」发送路径。
//
// 红果 2026-11 起：播放接口若带 X-Gorgon/X-Argus/X-Ladon 三签名，即使身份合法也返回
// HTTP 200 + 0 字节；只有「UA + accept: application/json + x-xs-from-web: 0 +
// sdk-version: 2 + x-ss-stub（body 的 MD5 大写）」这一组头才正常返回 video_model。
// 因此这里**不能**复用 hongguoAppRequest（它一定签名，且目录/详情/弹幕依赖那套签名，
// 动了会连带把目录打挂）。可复用的只有限流器、d.client 传输层与 20 秒超时，
// 这三点都由 doPreparedCatalogRequest 统一负责。
func (downloader *Downloader) hongguoAppPlaybackRequest(ctx context.Context, path, deviceID, installID string, payload any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("无法准备红果取流请求")
	}
	// x-ss-stub 是 body 的 MD5（大写十六进制）。空 body 时不带，避免与服务端的
	// 「无 body」判断冲突。
	stub := ""
	if len(body) > 0 {
		sum := md5.Sum(body)
		stub = strings.ToUpper(hex.EncodeToString(sum[:]))
	}
	client := downloader.hongguoClient()
	client.mu.Lock()
	base := client.baseURL
	client.mu.Unlock()
	query := hongguoPlaybackQuery(deviceID, installID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("无法准备红果取流请求")
	}
	request.Header.Set("User-Agent", hongguoAppUserAgent)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Xs-From-Web", "0")
	request.Header.Set("Sdk-Version", "2")
	if stub != "" {
		request.Header.Set("X-Ss-Stub", stub)
	}
	response, err := downloader.doPreparedCatalogRequest(request, 20*time.Second, func(prepared *http.Request) {
		prepared.URL.RawQuery = query.Encode()
	})
	if err != nil {
		return nil, fmt.Errorf("红果取流请求失败: %w", publicError(err))
	}
	content, readErr := io.ReadAll(io.LimitReader(response.Body, providerMaxBodyBytes+1))
	response.Body.Close()
	if readErr != nil {
		return nil, errors.New("红果取流响应读取中断")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("红果取流接口 HTTP %d", response.StatusCode)
	}
	if len(content) == 0 || len(content) > providerMaxBodyBytes {
		return nil, errHongguoAppEmpty
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, errors.New("红果取流接口返回格式异常")
	}
	code := firstNonEmpty(mapString(result, "code", "Code", "status_code"), mapString(nestedMap(result, "BaseResp"), "StatusCode"))
	if code != "" && code != "0" {
		return nil, fmt.Errorf("红果取流接口暂不可用（%s）", truncate(code, 20))
	}
	return result, nil
}
