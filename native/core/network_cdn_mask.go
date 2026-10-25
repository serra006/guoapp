package core

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 鬼片网（以及同类的采集站）把视频挂在 vNN.<裸域> 这种 CDN 上，例如
// v1.qqqrst.com、v10.ppqrrs.com、v13.rstu6.com、v15.rstu6.com。
// 这些域名在本机会同时遭到两重干扰：
//
//  1. DNS 被劫持 —— 系统解析一律返回 183.192.65.101（上海反诈提示页，HTTP 200 + text/html）；
//  2. SNI 被阻断 —— 直连真实 IP 且 ClientHello 里带真域名时，握手期被 RST。
//
// 实测可行的绕法是：用真实 IP 直连，但握手阶段的 SNI 只发裸域（qqqrst.com /
// ppqrrs.com / rstu6.com），Host 头仍保持原域名。这类 CDN 的证书本来就是跨域的
// （例如 rstu6.com 的 IP 返回的是 *.bxgbnet.com 的证书），校验本来就不成立，
// 所以这里必须跳过证书校验——否则连不上的前提下谈校验没有意义。
//
// 通道只在「主机名形如 v<数字>.<域>」时才启用，其余域名完全走原链路；
// 掩码通道失败会记冷却，期间直接回落 base，不会给正常请求增加额外等待。
var (
	guipianMaskedCdnHost = regexp.MustCompile(`(?i)^v\d{1,3}\.[a-z0-9-]+\.(?:com|net|cc|io|xyz|top|vip|cn|tv|me)$`)
	guipianMaskedCooldown = 3 * time.Minute
)

type guipianCdnMaskTransport struct {
	base     http.RoundTripper
	masked   http.RoundTripper
	resolver *dnsResolver

	mu       sync.Mutex
	degraded map[string]time.Time
}

func newGuipianCdnMaskTransport(base http.RoundTripper, plain *http.Transport, resolver *dnsResolver) http.RoundTripper {
	masked := plain.Clone()
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if plain.TLSClientConfig != nil {
		config = plain.TLSClientConfig.Clone()
	}
	config.InsecureSkipVerify = true // 见文件头：这类 CDN 的证书与域名本就不匹配
	masked.TLSClientConfig = config
	masked.DialTLSContext = guipianMaskedDialer(resolver, config)
	return &guipianCdnMaskTransport{
		base: base, masked: masked, resolver: resolver,
		degraded: map[string]time.Time{},
	}
}

// guipianMaskedDialer 先用真实 IP 建 TCP，再用裸域 SNI 完成握手。
func guipianMaskedDialer(resolver *dnsResolver, base *tls.Config) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		mask := guipianMaskedServerName(host)
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
			config := base.Clone()
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

// guipianMaskedServerName 把 v13.rstu6.com 归一成 rstu6.com；不匹配返回空。
func guipianMaskedServerName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if !guipianMaskedCdnHost.MatchString(host) {
		return ""
	}
	if index := strings.Index(host, "."); index > 0 {
		return host[index+1:]
	}
	return ""
}

func (transport *guipianCdnMaskTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return transport.base.RoundTrip(request)
	}
	host := strings.ToLower(request.URL.Hostname())
	if guipianMaskedServerName(host) == "" {
		return transport.base.RoundTrip(request)
	}
	transport.mu.Lock()
	until, degraded := transport.degraded[host]
	transport.mu.Unlock()
	if degraded && time.Now().Before(until) {
		return transport.base.RoundTrip(request)
	}
	response, err := transport.masked.RoundTrip(request)
	if err == nil {
		return response, nil
	}
	transport.mu.Lock()
	if len(transport.degraded) >= 64 {
		transport.degraded = map[string]time.Time{}
	}
	transport.degraded[host] = time.Now().Add(guipianMaskedCooldown)
	transport.mu.Unlock()
	return transport.base.RoundTrip(request)
}
