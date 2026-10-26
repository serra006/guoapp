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
// 通道只在「主机名形如 v<数字>.<域>」时才启用，其余域名完全走原链路。
// 失败退避按主机名单独计算，走秒级阶梯（见 network_mask_state.go）：
// 这些域名的 base 会被 DNS 劫持到反诈页，回落等于拿到一页广告 HTML，
// 所以冷却期宁可快速失败，也不要浪费时间回落到一条必然不可用的路上。
var guipianMaskedCdnHost = regexp.MustCompile(`(?i)^v\d{1,3}\.[a-z0-9-]+\.(?:com|net|cc|io|xyz|top|vip|cn|tv|me)$`)

type guipianCdnMaskTransport struct {
	base     http.RoundTripper
	masked   http.RoundTripper
	resolver *dnsResolver

	mu    sync.Mutex
	state map[string]*maskChannelState
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
		state: map[string]*maskChannelState{},
	}
}

// guipianMaskedDialer 先用真实 IP 建 TCP，再用裸域 SNI 完成握手。
func guipianMaskedDialer(resolver *dnsResolver, base *tls.Config) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
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

// guipianChannelState 取（或建）某个 CDN 主机名的通道状态，调用方需持有 mu。
func (transport *guipianCdnMaskTransport) channelStateLocked(host string) *maskChannelState {
	state := transport.state[host]
	if state == nil {
		if len(transport.state) >= 64 {
			transport.state = map[string]*maskChannelState{}
		}
		state = &maskChannelState{}
		transport.state[host] = state
	}
	return state
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
	state := transport.channelStateLocked(host)
	wait := state.retryAfter()
	useBase := state.baseUsable
	transport.mu.Unlock()

	switch {
	case useBase && wait > 0:
		return transport.base.RoundTrip(request)
	case !useBase && wait > maskTransparentWait:
		return nil, &maskChannelCooldownError{host: host, after: wait}
	case !useBase && wait > 0:
		if err := maskSleep(request.Context(), wait); err != nil {
			return nil, err
		}
	}

	response, err := transport.masked.RoundTrip(request)
	if err == nil {
		transport.mu.Lock()
		state.succeed()
		transport.mu.Unlock()
		return response, nil
	}
	if maskRequestAborted(err) {
		return nil, err
	}

	transport.mu.Lock()
	state.fail()
	probe := !state.baseChecked
	state.baseChecked = true
	transport.mu.Unlock()
	if probe {
		// 一次性的原通道探测，且必须校验内容不是反诈页 —— 这些域名的 base 在
		// 被劫持时会返回 HTTP 200 的 text/html，光看状态码会误判成「可用」。
		baseResponse, baseErr := transport.base.RoundTrip(request)
		if baseErr == nil {
			if maskBaseMediaUsable(baseResponse) {
				transport.mu.Lock()
				state.baseUsable = true
				transport.mu.Unlock()
				return baseResponse, nil
			}
			baseResponse.Body.Close()
		}
	}
	return nil, err
}
