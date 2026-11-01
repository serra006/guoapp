package core

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 一批采集站把视频/密钥挂在 <前缀>.<随机域>.<tld> 这类云厂商 CDN 上，例如
// 野果的 op.udhhzr.cn / as.oolrvd.cn。它们在中转网络里会被**按 SNI 精确阻断**：
// TCP 能连上、DNS 也返回真实的 CDN 地址，但 ClientHello 里带真域名时握手期被 RST
// （调用方看到的是 `EOF` 或 `wsarecv: An existing connection was forcibly closed`）。
//
// 实测这些 CDN 只需要把握手阶段的 SNI 换成「剥掉最左一层子域」的裸域
// （op.udhhzr.cn → SNI=udhhzr.cn）就能握手成功，证书本来就是 *.cdn.myqcloud.com
// 这类跨域证书，校验本就不成立，所以伪装通道必须跳过证书校验（与鬼片 CDN 掩码同一
// 口径）；Host 头与 URL 保持原样，回源定位不受影响。
//
// 与鬼片/红果那两条掩码通道的区别：那些域名是**必然被掐**、伪装通道是唯一通路，
// 所以「优先走伪装」；这里的域名在多数网络里本来是好的，伪装只是兜底，所以
// **先走原链路，失败了才用伪装重试一次**，成功后对该主机名记 30 分钟偏好。
// 这样既不会给正常网络平白加一次握手失败，也不会在被阻断的网络里彻底播不了。

// cdnSniFallbackTLD 限制兜底通道的作用范围：只对常见 tld 的多层域名生效。
var cdnSniFallbackTLD = regexp.MustCompile(`(?i)\.(?:com|net|cn|cc|io|xyz|top|vip|tv|me|org|info|co|site|club|link|live|pro)$`)

// cdnSniFallbackPrefer 是「该主机名已被证实需要伪装通道」的记忆时长。
const cdnSniFallbackPrefer = 30 * time.Minute

// cdnSniMaskedServerName 把 op.udhhzr.cn 归一成 udhhzr.cn；不足三层或明显不是
// 域名（IP、单段主机名）时返回空，表示「这条链路没有可用的掩码域」。
func cdnSniMaskedServerName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || net.ParseIP(host) != nil || !cdnSniFallbackTLD.MatchString(host) {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) < 3 {
		return ""
	}
	mask := strings.Join(parts[1:], ".")
	if net.ParseIP(mask) != nil {
		return ""
	}
	return mask
}

// cdnSniMaskedDialer 用真实 IP 建 TCP，再用裸域 SNI 完成握手。
func cdnSniMaskedDialer(resolver *dnsResolver, base *tls.Config) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		mask := cdnSniMaskedServerName(host)
		if mask == "" {
			return nil, &net.AddrError{Err: "无可用掩码域", Addr: host}
		}
		addresses := []string{host}
		if resolver != nil {
			if entry, lookupErr := resolver.lookupEntry(ctx, host, ""); lookupErr == nil && len(entry.addresses) > 0 {
				addresses = entry.addresses
			}
		}
		var lastErr error
		for index, address := range addresses {
			if index >= 3 {
				break
			}
			raw, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address, port))
			if dialErr != nil {
				lastErr = dialErr
				continue
			}
			config := &tls.Config{MinVersion: tls.VersionTLS12}
			if base != nil {
				config = base.Clone()
			}
			config.InsecureSkipVerify = true // 见文件头：这类 CDN 的证书与域名本就不匹配
			// 口径说明：这条通道只在「原链路已经失败」之后才被启用，且只对 ≥3 层
			// 域名的媒体/接口主机生效。裸域 SNI 拿到的必然是 CDN 的跨域证书
			// （*.cdn.myqcloud.com 之类），按真实主机名校验一定失败，所以这里无法
			// 做证书绑定 —— 与鬼片/红果两条掩码通道同一取舍：宁可降级也不让整源播不了。
			// 站源正常时的抓页、登录态全部走原链路，不经过这里。
			config.ServerName = mask
			config.NextProtos = []string{"http/1.1"}
			conn := tls.Client(raw, config)
			if handshakeErr := conn.HandshakeContext(ctx); handshakeErr != nil {
				raw.Close()
				lastErr = handshakeErr
				continue
			}
			return conn, nil
		}
		if lastErr == nil {
			lastErr = &net.AddrError{Err: "无可用地址", Addr: host}
		}
		return nil, lastErr
	}
}

// cdnSniChannel 记录单个主机名的兜底状态。
type cdnSniChannel struct {
	state       maskChannelState
	preferUntil time.Time
}

// cdnSniFallbackTransport 按主机名兜底：原链路失败且能推导出掩码域时，用伪装
// SNI 的克隆通道重试一次；成功后在一段时间内优先走伪装通道。
type cdnSniFallbackTransport struct {
	base     http.RoundTripper
	masked   http.RoundTripper
	resolver *dnsResolver

	mu    sync.Mutex
	hosts map[string]*cdnSniChannel
}

func newCdnSniFallbackTransport(base http.RoundTripper, plain *http.Transport, resolver *dnsResolver) http.RoundTripper {
	masked := plain.Clone()
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if plain.TLSClientConfig != nil {
		config = plain.TLSClientConfig.Clone()
	}
	masked.TLSClientConfig = config
	// 伪装通道必须直连：它自己拨真实 IP + 换 SNI 完成握手，一旦继承上层代理，
	// 请求会被代理接管（CONNECT 隧道），DialTLSContext 根本不会执行 —— 实测
	// 走代理时这里拿到的是代理侧回的 EOF，等于白兜底。
	//
	// 注意继承的是**代理函数**而不是代理地址：上层 Proxy 是 router.proxy，每次
	// 请求都会重新读系统代理设置（本机 127.0.0.1:20808），所以「当时没配代理」
	// 不代表克隆体真的直连 —— 它会在运行时被重新判定成走代理。必须显式置 nil。
	masked.Proxy = nil
	masked.DialTLSContext = cdnSniMaskedDialer(resolver, config)
	return &cdnSniFallbackTransport{base: base, masked: masked, resolver: resolver, hosts: map[string]*cdnSniChannel{}}
}

// newCdnSniFallbackTransportWith 供测试注入两条通道，无需联网。
func newCdnSniFallbackTransportWith(base, masked http.RoundTripper) *cdnSniFallbackTransport {
	return &cdnSniFallbackTransport{base: base, masked: masked, hosts: map[string]*cdnSniChannel{}}
}

func (transport *cdnSniFallbackTransport) channelLocked(host string) *cdnSniChannel {
	channel := transport.hosts[host]
	if channel == nil {
		if len(transport.hosts) >= 64 {
			transport.hosts = map[string]*cdnSniChannel{}
		}
		channel = &cdnSniChannel{}
		transport.hosts[host] = channel
	}
	return channel
}

func (transport *cdnSniFallbackTransport) recordSuccess(host string) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	channel := transport.channelLocked(host)
	channel.state.succeed()
	channel.preferUntil = time.Now().Add(cdnSniFallbackPrefer)
}

func (transport *cdnSniFallbackTransport) recordFailure(host string) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	channel := transport.channelLocked(host)
	channel.state.fail()
	channel.preferUntil = time.Time{}
}

// retryRequest 复制一份可重放的请求。GET/HEAD 直接克隆；带 body 的请求只有
// 声明了 GetBody 才能重放，否则返回 nil 表示「不值得重试」。
func cdnSniRetryRequest(request *http.Request) *http.Request {
	if request.Body != nil && request.Body != http.NoBody {
		if request.GetBody == nil {
			return nil
		}
		body, err := request.GetBody()
		if err != nil {
			return nil
		}
		retry := request.Clone(request.Context())
		retry.Body = body
		return retry
	}
	return request.Clone(request.Context())
}

func (transport *cdnSniFallbackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return transport.base.RoundTrip(request)
	}
	host := strings.ToLower(request.URL.Hostname())
	if cdnSniMaskedServerName(host) == "" {
		return transport.base.RoundTrip(request)
	}

	transport.mu.Lock()
	channel := transport.channelLocked(host)
	prefer := time.Now().Before(channel.preferUntil)
	wait := channel.state.retryAfter()
	transport.mu.Unlock()

	if prefer && wait == 0 {
		if retry := cdnSniRetryRequest(request); retry != nil {
			response, err := transport.masked.RoundTrip(retry)
			if err == nil {
				transport.mu.Lock()
				transport.channelLocked(host).state.succeed()
				transport.mu.Unlock()
				return response, nil
			}
			if maskRequestAborted(err) {
				return nil, err
			}
			transport.recordFailure(host)
		}
	}

	response, err := transport.base.RoundTrip(request)
	if err == nil {
		return response, nil
	}
	if maskRequestAborted(err) || errors.Is(err, context.Canceled) {
		return nil, err
	}
	retry := cdnSniRetryRequest(request)
	if retry == nil {
		return nil, err
	}
	maskedResponse, maskedErr := transport.masked.RoundTrip(retry)
	if maskedErr != nil {
		if maskRequestAborted(maskedErr) {
			return nil, maskedErr
		}
		// 两条路都不通：把原链路的错误抛给上层，语义更贴近「原来的毛病」。
		return nil, err
	}
	transport.recordSuccess(host)
	return maskedResponse, nil
}
