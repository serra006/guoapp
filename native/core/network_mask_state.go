package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 伪装通道（红果 SNI 掩码 / 鬼片 CDN 掩码）的失败退避阶梯。
//
// 这类通道和普通的「优化线路」有本质区别：当目标域名被按 SNI 精确阻断、
// 或 DNS 被劫持到反诈页时，原通道（base）本来就是死的，伪装通道是**唯一通路**。
// 旧实现把它当成可选优化：失败一次就锁 3 分钟、期间回落 base —— 等于把唯一
// 通路关掉，去连那条本就死掉的路。叠加「冷启动首次握手容易拖过 12s 请求超时」，
// 表现出来就是：新装 App 后头几分钟搜索、播放、连返回首页全都「暂时无法加载」，
// 过两三分钟又自己好了。
//
// 现在改成秒级阶梯（1s → 2s → 3s → 5s → 8s → 15s 封顶）。失败一次只锁很短时间，
// 网络一恢复下一条请求就能重新试通；连续失败才逐步拉长，避免持续无效重试。
var maskRetryLadder = []time.Duration{
	0,
	1 * time.Second,
	2 * time.Second,
	3 * time.Second,
	5 * time.Second,
	8 * time.Second,
	15 * time.Second,
}

// maskTransparentWait 是「值得原地等一等」的上限。冷却剩余时间不超过它时，
// 请求会等冷却结束再走伪装通道，而不是直接报错 —— 这样一次瞬时抖动对用户不可见。
// 超过它就快速失败，免得把一次点击拖成十几秒的白等。
const maskTransparentWait = 3 * time.Second

// maskChannelState 记录单条伪装通道的连续失败次数与下次可重试时间。
// 由调用方的 mutex 保护，自身不加锁。
type maskChannelState struct {
	failures int
	retryAt  time.Time

	// baseUsable / baseChecked 只在「伪装通道失败过一次」时才用得上：
	// 用一次性的探测确认这个网络下原通道是否其实可用，可用就继续回落，
	// 避免把某些没做阻断的网络里本来能走的路径也一起关掉。
	baseUsable  bool
	baseChecked bool
}

// retryAfter 返回还需等待多久；0 表示可以立即尝试伪装通道。
func (state *maskChannelState) retryAfter() time.Duration {
	if state.failures == 0 {
		return 0
	}
	if remaining := time.Until(state.retryAt); remaining > 0 {
		return remaining
	}
	return 0
}

// succeed 清空失败计数，下一次失败从最短的冷却重新开始。
func (state *maskChannelState) succeed() {
	state.failures = 0
	state.retryAt = time.Time{}
}

// fail 记一次失败并按阶梯安排下一次重试时间。
func (state *maskChannelState) fail() {
	state.failures++
	index := min(max(state.failures, 1), len(maskRetryLadder)-1)
	state.retryAt = time.Now().Add(maskRetryLadder[index])
}

// maskRequestAborted 判断错误是否来自「本地主动取消」。
//
// 这类错误绝不能算通道失效：前端每次切换分类、重新搜索、退出页面都会
// cancelCatalog 取消在飞的请求，旧实现把它也当成伪装通道故障，
// 于是「切一次分类」就能让整个域名锁 3 分钟。
func maskRequestAborted(err error) bool {
	return err != nil && errors.Is(err, context.Canceled)
}

// maskSleep 是可被 ctx 打断的等待，用于「冷却所剩无几，原地等一等再试」。
func maskSleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// maskChannelCooldownError 表示伪装通道正处于冷却，请求没有真正发出去。
// 文案直接展示给用户，所以要能看懂、并带出还需等待的时间。
type maskChannelCooldownError struct {
	host  string
	after time.Duration
}

func (err *maskChannelCooldownError) Error() string {
	seconds := int(err.after.Seconds() + 0.999)
	seconds = max(seconds, 1)
	return fmt.Sprintf("%s 通道刚断开，正在恢复，%d 秒后自动重试", err.host, seconds)
}

// maskBaseStatusUsable 判断「回落原通道」是否拿到了正常响应，用于**页面类**域名。
// 红果入口本身就是 HTML 站点，所以这里只看状态码，不能按内容类型过滤。
func maskBaseStatusUsable(response *http.Response) bool {
	return response != nil && response.StatusCode >= 200 && response.StatusCode < 300
}

// maskBaseMediaUsable 用于**媒体类**域名（鬼片 CDN）。
// 必须额外排除 text/html：这些域名的 DNS 被劫持后，原通道会返回 HTTP 200 +
// 反诈提示页的 HTML，光看状态码会误判成「可用」，等于把一页广告当视频流往下传。
func maskBaseMediaUsable(response *http.Response) bool {
	if !maskBaseStatusUsable(response) {
		return false
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	return !strings.Contains(contentType, "text/html")
}
