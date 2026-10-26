package core

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 部分网络环境会按 TLS ClientHello 里的 SNI 字段做精确匹配阻断：TCP 能建立，
// 但在握手期间被 RST，客户端侧表现为 `read: connection reset by peer`。
// 红果入口域名 hongguoduanju.com 属于这种情况 —— 搜索、分区、首页全走它，
// 一旦被掐整块功能不可用，而红果的 App 接口（fqnovel.com）、媒体域名
// （snssdk.com）都不受影响。
//
// 实测该服务器接受用 www 子域作为 SNI 完成握手（证书同样覆盖 www），
// 因此这里只替换握手阶段的 SNI 字符串：URL、Host 头、证书校验全部保持原样，
// 既不跳过校验也不降级 TLS 版本。其余域名原样透传，不受影响。
const (
	hongguoSniBlockedDomain = "hongguoduanju.com"
	hongguoSniMaskedServer  = "www.hongguoduanju.com"
)

// hongguoSniMaskTransport 按目标域名分流：只有被封的那个域名走伪装 SNI 通道，
// 其余请求原样交给 base（CDN 优选 + 浏览器指纹链路保持不变）。
type hongguoSniMaskTransport struct {
	base   http.RoundTripper
	masked http.RoundTripper

	mu    sync.Mutex
	state maskChannelState
}

// newHongguoSniMaskTransport 用 plain（未包装的底层 transport）克隆出伪装通道，
// 以继承其代理、连接池与 TLS 配置；base 仍负责所有其他域名。
func newHongguoSniMaskTransport(base http.RoundTripper, plain *http.Transport) http.RoundTripper {
	masked := plain.Clone()
	masked.DialTLSContext = hongguoSniMaskedDialer(plain.TLSClientConfig)
	return &hongguoSniMaskTransport{base: base, masked: masked}
}

// newHongguoSniMaskTransportWith 供测试注入两条通道，便于在不联网的情况下验证分流。
func newHongguoSniMaskTransportWith(base, masked http.RoundTripper) *hongguoSniMaskTransport {
	return &hongguoSniMaskTransport{base: base, masked: masked}
}

func hongguoSniMaskedDialer(base *tls.Config) func(context.Context, string, string) (net.Conn, error) {
	// 单次拨号上限压到 10s：请求侧 ctx 是 12s，留出握手余量。
	// 之前写 20s，冷启动那次握手一旦慢下来就会被 12s 的 ctx 砍掉、又被记成
	// 「通道故障」，从而连锁触发整段冷却。
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if base != nil {
			cfg = base.Clone()
		}
		cfg.ServerName = hongguoSniMaskedServer
		// 固定 http/1.1：手动握手后再协商 h2 容易与 Transport 的连接复用判定冲突，
		// 且这些页面请求本身没有并发压榨的必要。
		cfg.NextProtos = []string{"http/1.1"}
		conn := tls.Client(raw, cfg)
		if err := conn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return conn, nil
	}
}

func (transport *hongguoSniMaskTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil ||
		!strings.EqualFold(request.URL.Hostname(), hongguoSniBlockedDomain) {
		return transport.base.RoundTrip(request)
	}

	transport.mu.Lock()
	wait := transport.state.retryAfter()
	useBase := transport.state.baseUsable
	transport.mu.Unlock()

	switch {
	case useBase && wait > 0:
		// 这个网络下原通道实测可用（没做 SNI 阻断），按老逻辑直接回落即可。
		return transport.base.RoundTrip(request)
	case !useBase && wait > maskTransparentWait:
		// 冷却还长，快速失败：base 对这个域名同样是断的，回落只是把
		// 「快失败」换成「慢失败」。错误里带出剩余秒数，前端可据此重试。
		return nil, &maskChannelCooldownError{host: hongguoSniBlockedDomain, after: wait}
	case !useBase && wait > 0:
		// 只剩一两秒，原地等完再走伪装通道，别把一次瞬时抖动放大成一次失败。
		if err := maskSleep(request.Context(), wait); err != nil {
			return nil, err
		}
	}

	response, err := transport.masked.RoundTrip(request)
	if err == nil {
		transport.mu.Lock()
		transport.state.succeed()
		transport.mu.Unlock()
		return response, nil
	}
	if maskRequestAborted(err) {
		// 本地取消（切分类 / 重新搜索 / 退出页面）不算通道故障。
		return nil, err
	}

	transport.mu.Lock()
	transport.state.fail()
	// 只在第一次失败时探一次原通道：万一这个网络其实没做阻断，base 是能用的，
	// 不能让伪装通道的偶发失败把唯一可用的路径也永久关掉。
	probe := !transport.state.baseChecked
	transport.state.baseChecked = true
	transport.mu.Unlock()
	if probe {
		baseResponse, baseErr := transport.base.RoundTrip(request)
		if baseErr == nil {
			if maskBaseStatusUsable(baseResponse) {
				transport.mu.Lock()
				transport.state.baseUsable = true
				transport.mu.Unlock()
				return baseResponse, nil
			}
			baseResponse.Body.Close()
		}
	}
	return nil, err
}
