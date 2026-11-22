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

// hongguoV5PlaybackRequest 走**旧形态**（29 项 query、`player/v1`、7.4.3.32）。
//
// 2026-11-22 起这条入口已被站方置空（实测：连 GET 与完全不签名的请求都回同一个
// 200 + 0 字节），保留它只为「形态再变回来时自动接上」。
func (downloader *Downloader) hongguoV5PlaybackRequest(ctx context.Context, state hongguoIdentityState, videoID string) (map[string]any, error) {
	return downloader.hongguoV5Post(ctx, hongguoV5Path, hongguoV5Payload(videoID),
		hongguoV5DeviceFromState(state), hongguoV5Headers)
}

// hongguoV5V1Request 走**7.4.5.32 形态**（13 项 query、`multi_video_model/v1`）。
// 这是 2026-11-20 起官方客户端实际在用的取流形态。
func (downloader *Downloader) hongguoV5V1Request(ctx context.Context, state hongguoIdentityState, videoID string) (map[string]any, error) {
	return downloader.hongguoV5Post(ctx, hongguoV5V1Path, hongguoV5V1Payload(videoID),
		hongguoV5V1DeviceFromState(state), hongguoV5V1Headers)
}

// hongguoV5Signer 是「给定 profile 与 gzip 体，算出 query 与整套头」的签名函数。
// 两套形态只在这一步分叉，其余（压缩、发送、状态码、空体、业务码）完全共用，
// 免得两份实现慢慢漂移出差异。
type hongguoV5Signer func(profile hongguoV5Profile, body []byte, now time.Time) (string, http.Header, error)

// hongguoV5Post 是两套形态共用的发送与解析。
//
// 与旧入口的三点硬差异（改任何一点都只会拿到 200 + 0 字节）：
//   - body 必须是 gzip 之后的字节，签名与 x-ss-stub 都基于压缩后的内容；
//   - query 由签名函数产出，`_rticket` 参与签名，签完不能改；
//   - 状态码 200 但 0 字节一律归为 errHongguoAppEmpty（站方的软拒绝形态）。
func (downloader *Downloader) hongguoV5Post(ctx context.Context, path string, payload map[string]any,
	profile hongguoV5Profile, sign hongguoV5Signer) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("无法准备红果取流请求")
	}
	body, err := hongguoV5Gzip(raw)
	if err != nil {
		return nil, errors.New("无法压缩红果取流请求")
	}
	query, header, err := sign(profile, body, time.Now())
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		hongguoPlaybackBaseURL+path+"?"+query, bytes.NewReader(body))
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

// hongguoAppMediaFromV1 解析 7.4.5.32 形态（13 项 query）的响应。
//
// 标准形态是 data["1004"][vid].video_model —— 键 1004 是短剧档位（与请求体里
// mixed_video_id_map 的键对应）。取不到再退回 hongguoAppMediaFromAny 的三形态兼容
// 分支（只认本次请求的 vid，绝不退回「data 里唯一那一项」）。
func hongguoAppMediaFromV1(result map[string]any, videoID string) (providerMedia, error) {
	if tier, ok := nestedMap(result, "data")["1004"].(map[string]any); ok {
		if entry, ok := tier[videoID].(map[string]any); ok && len(entry) > 0 {
			wrapped := map[string]any{"data": map[string]any{videoID: entry}}
			if media, err := hongguoAppMediaFromBatch(wrapped, videoID); err == nil {
				return media, nil
			} else if !errors.Is(err, errHongguoAppNoMedia) {
				return providerMedia{}, err
			}
		}
	}
	return hongguoAppMediaFromAny(result, videoID)
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

// 两个只影响「换号策略」的可调项，默认值就是正式逻辑：
//   - hongguoV5RotateAttempts：一次取流最多用几个身份。正式值 1 = **不换号**。
//     2026-11-22 已实测证明「200 + 0 字节」与身份无关（完全不签名的请求、空
//     device_id/iid 的请求拿到的是同一个响应），所以「空响应说明这个号坏了、
//     换一个号再试」这个前提不成立。继续换号只会每次播放失败都白烧一条设备登记，
//     有把整机登记配额打到被站方封禁的风险。**不要改回 2 以上。**
//   - hongguoV5RotateBypass：跳过 30 秒冷却闸门。**只给排查用**。
var (
	// 1 = 不换号。见上面的说明。
	hongguoV5RotateAttempts = 1
	hongguoV5RotateBypass   = false
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

// hongguoV5Attempt 是完整的 v5 取流尝试：先用当前身份，空响应就换身份再试。
//
// 只有「空响应 / 没有本次 vid」才换身份；HTTP 异常、业务码非 0、JSON 坏掉都不换
// （上游同样明确：混合失败、只有 video-info 为空、服务端给了 Retry-After 都不触发）。
//
// 换号次数由 hongguoV5RotateAttempts 控制（默认 1 = 不换号，与当前线上行为一致）。
func (downloader *Downloader) hongguoV5Attempt(ctx context.Context, state hongguoIdentityState, videoID string) (providerMedia, error) {
	media, err := downloader.hongguoV5Once(ctx, state, videoID)
	for attempt := 1; attempt < hongguoV5RotateAttempts; attempt++ {
		if err == nil || !hongguoV5Retryable(err) || ctx.Err() != nil {
			return media, err
		}
		now := time.Now()
		if !hongguoV5RotateBypass && !hongguoV5RotateAllowed(now) {
			return providerMedia{}, err
		}
		replacement, registerErr := downloader.hongguoPlaybackIdentityState(ctx, true)
		if registerErr != nil || !hongguoIdentityValid(replacement) {
			hongguoV5RotateClosed(now, false)
			return providerMedia{}, err
		}
		hongguoV5RotateClosed(now, true)
		media, err = downloader.hongguoV5Once(ctx, replacement, videoID)
	}
	return media, err
}

func (downloader *Downloader) hongguoV5Once(ctx context.Context, state hongguoIdentityState, videoID string) (providerMedia, error) {
	// 首选 7.4.5.32 形态（13 项 query、入口不带 player 段）——2026-11-20 起官方在用。
	// 解析口径按上游来：先取 data["1004"][vid]["video_model"]，取不到再走兼容分支。
	v1Result, v1Err := downloader.hongguoV5V1Request(ctx, state, videoID)
	if v1Err == nil {
		if media, err := hongguoAppMediaFromV1(v1Result, videoID); err == nil {
			return media, nil
		} else if !errors.Is(err, errHongguoAppNoMedia) {
			return providerMedia{}, err
		}
	}
	// 回退旧形态（29 项 query、player/v1、7.4.3.32）。
	// 它自 2026-11-22 起被站方置空，留着是为了形态再变回来时能自动接上。
	result, err := downloader.hongguoV5PlaybackRequest(ctx, state, videoID)
	if err != nil {
		if v1Err != nil {
			return providerMedia{}, v1Err
		}
		return providerMedia{}, err
	}
	return hongguoAppMediaFromAny(result, videoID)
}

// hongguoV5Retryable 判断这次失败值不值得换身份重试。
func hongguoV5Retryable(err error) bool {
	return errors.Is(err, errHongguoAppEmpty) || errors.Is(err, errHongguoAppNoMedia)
}
