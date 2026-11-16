package core

// 红果 v5 取流的发送与解析。签名在 provider_hongguo_v5_sign.go。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// hongguoV5PlaybackRequest 走现行 v5 入口取一集的播放信息。
//
// 与旧入口的三点硬差异（改任何一点都只会拿到 200 + 0 字节）：
//   - body 必须是 gzip 之后的字节，签名与 x-ss-stub 都基于压缩后的内容；
//   - 头只留 Content-Type / Content-Encoding / x-ss-stub 与三元组，
//     多带 User-Agent、Accept 会被判成非官方客户端；
//   - query 里只有 13 项，且 ts/_rticket 参与签名，签完不能改。
func (downloader *Downloader) hongguoV5PlaybackRequest(ctx context.Context, state hongguoIdentityState, videoID string) (map[string]any, error) {
	raw, err := json.Marshal(hongguoV5Payload(videoID))
	if err != nil {
		return nil, errors.New("无法准备红果取流请求")
	}
	body, err := hongguoV5Gzip(raw)
	if err != nil {
		return nil, errors.New("无法压缩红果取流请求")
	}
	profile := hongguoV5DeviceFromState(state)
	query, header, err := hongguoV5Headers(profile, body, time.Now())
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		hongguoPlaybackBaseURL+hongguoV5Path+"?"+query, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("无法准备红果取流请求")
	}
	for key, values := range header {
		for _, value := range values {
			request.Header.Set(key, value)
		}
	}
	response, err := downloader.doPreparedCatalogRequest(request, 20*time.Second, nil)
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

// hongguoAppMediaFromAny 兼容 v5 响应的三种形态：
//
//	data[vid].video_model      —— 只请求一个 vid 时服务端有时直接铺平
//	data["1004"][vid].video_model —— 上游客户端的标准形态（键是短剧档位 1004）
//	data.video_model           —— 单集入口
//
// 只认本次请求的 vid，绝不退回「data 里唯一那一项」——张冠李戴比报错糟得多。
func hongguoAppMediaFromAny(result map[string]any, videoID string) (providerMedia, error) {
	var lastErr error = errHongguoAppNoMedia
	if media, err := hongguoAppMediaFromBatch(result, videoID); err == nil {
		return media, nil
	} else if !errors.Is(err, errHongguoAppNoMedia) {
		return providerMedia{}, err
	}
	if tier, ok := nestedMap(result, "data")["1004"].(map[string]any); ok {
		wrapped := map[string]any{"data": map[string]any{videoID: tier[videoID]}}
		if media, err := hongguoAppMediaFromBatch(wrapped, videoID); err == nil {
			return media, nil
		} else if !errors.Is(err, errHongguoAppNoMedia) {
			return providerMedia{}, err
		}
	}
	if media, err := hongguoAppMediaFromResult(result); err == nil {
		return media, nil
	} else if !errors.Is(err, errHongguoAppNoMedia) {
		lastErr = err
	}
	return providerMedia{}, lastErr
}

// v5 空响应的语义（2026-11-16 上游定论，见 N3urda/hongguoTV IDENTITY_RECOVERY.md）：
// HTTP 200 + 0 字节**不是**「签错了」，也不是限流，而是**当前持久化的取流身份被判为
// 不可用**。上游的修法是把这个身份换掉一次、用新登记的身份重试同一集；实测我们这边
// 同一个身份连打八次全空，换新身份后能出数据，与该结论一致。
//
// 换身份是有成本的（会新增一条设备登记），所以做进程内冷却：
// 换成功后 30 秒内不再换，换失败（登记本身失败）后 5 秒内不再换。
// 这两条与上游 ContentRepository 里的 5_000L / 30_000L 一致。
const (
	hongguoV5RotateCooldown = 30 * time.Second
	hongguoV5RotatePenalty  = 5 * time.Second
)

var hongguoV5RotateGate struct {
	mu    sync.Mutex
	until time.Time
}

func hongguoV5RotateAllowed(now time.Time) bool {
	hongguoV5RotateGate.mu.Lock()
	defer hongguoV5RotateGate.mu.Unlock()
	return !now.Before(hongguoV5RotateGate.until)
}

func hongguoV5RotateClosed(now time.Time, rotated bool) {
	wait := hongguoV5RotateCooldown
	if !rotated {
		wait = hongguoV5RotatePenalty
	}
	hongguoV5RotateGate.mu.Lock()
	hongguoV5RotateGate.until = now.Add(wait)
	hongguoV5RotateGate.mu.Unlock()
}

// hongguoV5Attempt 是完整的 v5 取流尝试：先用当前身份，空响应就换一次身份再试。
//
// 只有「空响应 / 没有本次 vid」才换身份；HTTP 异常、业务码非 0、JSON 坏掉都不换
// （上游同样明确：混合失败、只有 video-info 为空、服务端给了 Retry-After 都不触发）。
func (downloader *Downloader) hongguoV5Attempt(ctx context.Context, state hongguoIdentityState, videoID string) (providerMedia, error) {
	media, err := downloader.hongguoV5Once(ctx, state, videoID)
	if err == nil || !hongguoV5Retryable(err) || ctx.Err() != nil {
		return media, err
	}
	now := time.Now()
	if !hongguoV5RotateAllowed(now) {
		return providerMedia{}, err
	}
	replacement, registerErr := downloader.hongguoPlaybackIdentityState(ctx, true)
	if registerErr != nil || !hongguoIdentityValid(replacement) {
		hongguoV5RotateClosed(now, false)
		return providerMedia{}, err
	}
	hongguoV5RotateClosed(now, true)
	return downloader.hongguoV5Once(ctx, replacement, videoID)
}

func (downloader *Downloader) hongguoV5Once(ctx context.Context, state hongguoIdentityState, videoID string) (providerMedia, error) {
	result, err := downloader.hongguoV5PlaybackRequest(ctx, state, videoID)
	if err != nil {
		return providerMedia{}, err
	}
	return hongguoAppMediaFromAny(result, videoID)
}

// hongguoV5Retryable 判断这次失败值不值得换身份重试。
func hongguoV5Retryable(err error) bool {
	return errors.Is(err, errHongguoAppEmpty) || errors.Is(err, errHongguoAppNoMedia)
}
