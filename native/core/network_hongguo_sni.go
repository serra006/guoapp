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
	// 伪装通道失败后的冷却时间：期间直接走原通道，避免每次请求都多等一次超时。
	hongguoSniCooldown = 3 * time.Minute
)

// hongguoSniMaskTransport 按目标域名分流：只有被封的那个域名走伪装 SNI 通道，
// 其余请求原样交给 base（CDN 优选 + 浏览器指纹链路保持不变）。
type hongguoSniMaskTransport struct {
	base   http.RoundTripper
	masked http.RoundTripper

	mu            sync.Mutex
	degradedUntil time.Time
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
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
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
	degraded := time.Now().Before(transport.degradedUntil)
	transport.mu.Unlock()
	if !degraded {
		response, err := transport.masked.RoundTrip(request)
		if err == nil {
			return response, nil
		}
		// 伪装通道失效时进入冷却，避免后续每个请求都白等一次；
		// 冷却结束后会自动再试一次，网络恢复即可自行回到伪装通道。
		transport.mu.Lock()
		transport.degradedUntil = time.Now().Add(hongguoSniCooldown)
		transport.mu.Unlock()
	}
	return transport.base.RoundTrip(request)
}
