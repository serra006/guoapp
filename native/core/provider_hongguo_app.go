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

// hongguoPlaybackBaseURL 是取流专用集群（a 集群）。两个集群的准入策略不同：
// 目录/详情/弹幕在 b 集群、取流在 a 集群，同一个 path 换集群调用不通用。
const hongguoPlaybackBaseURL = "https://api5-normal-sinfonlinea.fqnovel.com"

const hongguoAppUserAgent = "com.phoenix.read/73532 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"

// hongguoAppMultiVideoModelPath 是 b 集群的批量取流入口，现作为回退入口。
// 首选入口是 a 集群的 hongguoPlaybackVideoModelPath（见 provider_hongguo_playback_sign.go），
// 两条的集群与 payload 形状都不同，不能互相顶替。
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
	// 继续用原有随机身份 + 播放签名打 b 集群，取流用登记身份 + Argus 551 打 a 集群，
	// 两边互不影响。
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
		// 红果自 2026-10 起对 App 目录/详情接口要求完整三签名
		// X-Gorgon + X-Argus + X-Ladon，且**目录/详情与弹幕用的是两套不同签名**：
		// 弹幕签名（provider_hongguo_comment_sign.go）的 Argus 缺 device_id、
		// 机型环境等字段，打到 video_detail / landpage 一律返回
		// HTTP 200 + 0 字节；必须改用播放签名（provider_hongguo_player_sign.go）。
		// 该签名会对 body 计算 X-SS-STUB 并写入 Argus 的 body 摘要。
		// 取流是第三套（Argus 551，provider_hongguo_playback_sign.go），
		// 走 hongguoAppPlaybackRequest，不在这里。
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

// hongguoPlaybackQuery 构造取流请求的 query。
//
// 机型、cdid、openudid 一律取自身份状态 —— 服务端会把它们与登记记录逐字段比对，
// 换值等于换设备，结果是 HTTP 200 + 0 字节空体，从外面看与接口挂掉没有区别。
// 所以这里一个字段都不写死，"25053RT47C" 那台固定机早就被静默拉黑了。
//
// ts / _rticket 是时间戳，发送前由调用方用**同一次 now** 重写成签名用的那一秒，
// 这里不预先填：填了也会被覆盖，留着反而容易误以为它已经定了。
func hongguoPlaybackQuery(state hongguoIdentityState) url.Values {
	device := state.device()
	values := url.Values{
		"iid": {state.InstallID}, "device_id": {state.DeviceID}, "ac": {"wifi"}, "channel": {device.Channel},
		"aid": {hongguoPlaybackAID}, "app_name": {"novelread"}, "cdid": {state.CDID},
		"version_code": {hongguoRegisterVersion}, "version_name": {hongguoRegisterVersionNa},
		"device_platform": {"android"}, "os": {"android"}, "ssmix": {"a"},
		"device_type": {device.Model}, "device_brand": {device.Brand}, "language": {"zh"},
		"os_api": {device.OSAPI}, "os_version": {device.OSVersion},
		"manifest_version_code": {hongguoRegisterVersion}, "resolution": {device.queryResolution()},
		"dpi": {device.DPI}, "update_version_code": {hongguoRegisterVersion},
		"host_abi": {"arm64-v8a"}, "dragon_device_type": {"phone"},
		"pv_player": {hongguoRegisterVersion}, "compliance_status": {"0"},
		"need_personal_recommend": {"1"}, "player_so_load": {"1"}, "is_android_pad_screen": {"0"},
		"openudid": {state.OpenUDID},
	}
	// 回落到内存随机身份时没有 cdid/openudid，空串比「不带这个参数」更容易被判异常。
	if state.CDID == "" {
		values.Del("cdid")
	}
	if state.OpenUDID == "" {
		values.Del("openudid")
	}
	return values
}

// hongguoAppPlaybackRequest 是取流专用的发送路径：**登记身份 + 登记机型 + 551 签名**。
//
// 与 hongguoAppRequest（目录/详情/弹幕）的分工是刻意分开的：那边用内存随机身份 +
// 播放签名（ac1adaae 那套）打 b 集群，这边用登记身份 + Argus 551 打 a 集群。
// 两者的身份、机型、UA、域名、签名四件事都不一样，混用任何一件都只会拿到空体。
//
// signed=false 的路径保留着：红果在播放接口上对签名的要求历史上开合过两轮
// （2026-11-01 要无签名，2026-11-03 要签名），把某一轮结论写死会在下一次开合时
// 整体失效。调用方按「先签、空响应再退不签」的顺序试，见 hongguoPlaybackAttempt。
func (downloader *Downloader) hongguoAppPlaybackRequest(ctx context.Context, base, path string, state hongguoIdentityState, payload any, signed bool) (map[string]any, error) {
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
	device := state.device()
	query := hongguoPlaybackQuery(state)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("无法准备红果取流请求")
	}
	// UA 必须是登记时那台机，UA 里的机型与服务端记录不一致同样判异常。
	request.Header.Set("User-Agent", device.userAgent())
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Xs-From-Web", "0")
	request.Header.Set("Sdk-Version", "2")
	// x-ss-dp 不带时服务端不认 app 归属；lc 与 x-reading-request 是阅读侧客户端的
	// 标配头，缺任何一个都会被当作非官方客户端。
	request.Header.Set("X-SS-DP", hongguoPlaybackAID)
	request.Header.Set("lc", "101")
	if stub != "" {
		request.Header.Set("X-Ss-Stub", stub)
	}
	// 登记下发的游客票据必须带回服务端：只有 device_id/iid 而没有 ttreq 时，
	// 播放接口返回 HTTP 200 + 0 字节。没有票据（老身份文件）就不带这个头。
	if state.TTReq != "" {
		request.Header.Set("Cookie", hongguoGuestCookie(state))
	}
	response, err := downloader.doPreparedCatalogRequest(request, 20*time.Second, func(prepared *http.Request) {
		now := time.Now()
		ticket := now.UnixMilli()
		// ts / _rticket 必须写在签名之前：签名把 query 一起算进 X-Argus，
		// 签完再改 query 等于自毁签名。
		query.Set("ts", strconv.FormatUint(uint64(now.Unix()), 10))
		query.Set("_rticket", strconv.FormatInt(ticket, 10))
		prepared.URL.RawQuery = query.Encode()
		prepared.Header.Set("x-reading-request", hongguoPlaybackReadingRequest(ticket))
		if signed {
			signHongguoPlaybackRequest(prepared, body, now, state.DeviceID, state.InstallID, hongguoRegisterVersionNa)
		}
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
