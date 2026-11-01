package core

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 站源 API 的「双通道」兜底。
//
// 背景（实测数据，不是猜的）：
//
//	某台机器上系统代理指向本机 127.0.0.1:20808，黄豆的接口域名 xqjurgek.top
//	经代理握手时被对端直接回 alert(40) handshake failure，
//	而同一时刻直连可以拿到 HTTP 200；反过来黄果的 huangguo.video 直连
//	（被投毒的 A/AAAA 记录指向 2a03:2880:face:b00c::/157.240.x.x）会 i/o timeout，
//	经代理却能拿到 40 条目录。
//
// 也就是说「走不走代理」对不同站源是相反的结论，App 里原来只有一个全局设置，
// 选错一次就是整源不可用。这里不改用户的代理设置，只做一件事：
// 某个站源域名的请求在传输层失败（拨号失败、握手失败、连接被重置）时，
// 换另一条通道再试一次，并把胜出的通道记在内存里，后续请求直接走它。
//
// 最后一级兜底是「跳过系统 DNS」：拿 dnsResolver 的备用解析结果逐个 IP 直连，
// 握手阶段仍然带正确 SNI，证书校验照常 —— 只解决投毒与 AAAA 空路由，
// 不降级 TLS，也不跳过校验。

const (
	// 记多久：够覆盖一轮目录/详情/取流，又不至于把临时抖动记成永久结论。
	providerRouteTTL = 10 * time.Minute
	// 每个域名最多试几个备用解析地址。
	providerRouteMaxAddresses = 3
	// 还没有「哪个通道可用」的结论时，第一条通道只给这么点预算。
	// 拨号到被投毒地址会一直挂到超时，不设上限的话第二条通道根本轮不到。
	providerInitialAttemptBudget = 15 * time.Second
)

type providerRouteKind int

const (
	providerRouteUnknown providerRouteKind = iota
	providerRouteDirect                    // 直连（不经过系统/手动代理）
	providerRouteProxy                     // 按用户设置走代理
)

type providerRoute struct {
	kind    providerRouteKind
	address string // 备用解析里胜出的 IP，空了表示用系统解析
	expires time.Time
}

type providerRetryTransport struct {
	base     http.RoundTripper // 完整链路：按用户代理设置
	direct   *http.Transport   // 与 base 同源，但强制直连
	resolver *dnsResolver
	router   *proxyRouter

	mu     sync.Mutex
	routes map[string]providerRoute
	pinned map[string]*http.Transport
}

func newProviderRetryTransport(base http.RoundTripper, plain *http.Transport, resolver *dnsResolver, router *proxyRouter) http.RoundTripper {
	direct := plain.Clone()
	direct.Proxy = nil
	return &providerRetryTransport{
		base: base, direct: direct, resolver: resolver, router: router,
		routes: map[string]providerRoute{}, pinned: map[string]*http.Transport{},
	}
}

// providerHostNeedsFallback 只对站源接口域名启用双通道，
// 其余域名（含红果、媒体 CDN）完全走原链路，绝不改变既有行为。
func providerHostNeedsFallback(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	for _, domain := range []string{
		"xqjurgek.top",   // 黄豆接口（主）
		"tideember.cc",   // 黄豆接口（旧域名，源站 DNS 已失效）
		"huangguo.video", // 黄果视频站
		"huangguoai.com", // 黄果 AI 站
		"cloudfront.net", // 黄果旧版前端与接口
		"ediayikma.cc",   // 黄果 AI 站的跳板域名
		"agdkczeyx.cc",   // 同上
		"dsqqahhno.cc",   // 黄果 AI 站的播放页跳板
		"hzjvlo.cn",      // 黄果 AI 站的 HLS 域名
	} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// usesProxy 判断这条请求在当前设置下会不会真的走代理。
func (transport *providerRetryTransport) usesProxy(request *http.Request) bool {
	if transport.router == nil {
		return false
	}
	proxy, err := transport.router.proxy(request)
	return err == nil && proxy != nil
}

func (transport *providerRetryTransport) remember(host string, route providerRoute) {
	if host == "" {
		return
	}
	route.expires = time.Now().Add(providerRouteTTL)
	transport.mu.Lock()
	if len(transport.routes) >= 128 {
		transport.routes = map[string]providerRoute{}
	}
	transport.routes[host] = route
	transport.mu.Unlock()
}

func (transport *providerRetryTransport) preferred(host string) providerRoute {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	route := transport.routes[host]
	if route.kind == providerRouteUnknown {
		return route
	}
	if time.Now().After(route.expires) {
		delete(transport.routes, host)
		return providerRoute{}
	}
	return route
}

func (transport *providerRetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || !providerHostNeedsFallback(request.URL.Hostname()) {
		return transport.base.RoundTrip(request)
	}
	// 请求体不能重放时不做双通道：宁可失败，也不要发出半个请求。
	if request.Body != nil && request.GetBody == nil {
		return transport.base.RoundTrip(request)
	}
	host := strings.ToLower(request.URL.Hostname())
	route := transport.preferred(host)

	// 只有真的配了代理时才有两条通道；直连模式下两条通道是同一件事，
	// 再去试第二遍纯属浪费预算（会把后面的备用解析兜底挤掉）。
	order := []providerRouteKind{providerRouteDirect}
	if transport.usesProxy(request) {
		order = []providerRouteKind{providerRouteProxy, providerRouteDirect}
	}
	if route.kind != providerRouteUnknown {
		preferredOrder := make([]providerRouteKind, 0, len(order))
		preferredOrder = append(preferredOrder, route.kind)
		for _, kind := range order {
			if kind != route.kind {
				preferredOrder = append(preferredOrder, kind)
			}
		}
		order = preferredOrder
	}

	parent := request.Context()
	var lastErr error
	for index, kind := range order {
		attemptRequest := request
		cancel := func() {}
		budgeted := route.kind == providerRouteUnknown && index == 0
		if budgeted {
			var ctx context.Context
			ctx, cancel = context.WithTimeout(parent, providerInitialAttemptBudget)
			attemptRequest = request.WithContext(ctx)
		}
		response, err := transport.attempt(attemptRequest, kind, route.address)
		cancel()
		if err == nil {
			if route.kind != kind {
				transport.remember(host, providerRoute{kind: kind, address: route.address})
			}
			return response, nil
		}
		lastErr = err
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		if budgeted {
			// 是上面那条 15s 预算自己到点了，不是调用方取消，换通道继续。
			continue
		}
		if providerRouteAborted(err) {
			return nil, err
		}
	}

	// 两条通道都失败：绕开系统 DNS，用备用解析结果逐个 IP 直连。
	if transport.resolver != nil {
		for _, subnet := range []string{dnsAlternateSubnet, ""} {
			entry, lookupErr := transport.resolver.lookupEntry(parent, host, subnet)
			if lookupErr != nil {
				continue
			}
			for index, address := range entry.addresses {
				if index >= providerRouteMaxAddresses {
					break
				}
				response, err := transport.attemptAddress(request, host, address)
				if err == nil {
					transport.remember(host, providerRoute{kind: providerRouteDirect, address: address})
					return response, nil
				}
				lastErr = err
				if parent.Err() != nil {
					return nil, parent.Err()
				}
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("站源地址不可用")
	}
	return nil, lastErr
}

func (transport *providerRetryTransport) attempt(request *http.Request, kind providerRouteKind, address string) (*http.Response, error) {
	if kind == providerRouteDirect && address != "" {
		return transport.attemptAddress(request, request.URL.Hostname(), address)
	}
	upstream, err := providerCloneRequest(request)
	if err != nil {
		return nil, err
	}
	if kind == providerRouteDirect {
		return transport.direct.RoundTrip(upstream)
	}
	return transport.base.RoundTrip(upstream)
}

// attemptAddress 直连指定 IP，握手仍用真实域名做 SNI，证书校验照常。
func (transport *providerRetryTransport) attemptAddress(request *http.Request, host, address string) (*http.Response, error) {
	upstream, err := providerCloneRequest(request)
	if err != nil {
		return nil, err
	}
	if port := request.URL.Port(); port != "" {
		upstream.URL.Host = net.JoinHostPort(address, port)
	} else {
		upstream.URL.Host = address
	}
	pinned := transport.pinnedTransport(host, address)
	response, err := pinned.RoundTrip(upstream)
	if response != nil {
		response.Request = request
	}
	return response, err
}

func (transport *providerRetryTransport) pinnedTransport(host, address string) *http.Transport {
	key := host + "|" + address
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if existing := transport.pinned[key]; existing != nil {
		return existing
	}
	pinned := transport.direct.Clone()
	if pinned.TLSClientConfig == nil {
		pinned.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	pinned.TLSClientConfig.ServerName = host
	if len(transport.pinned) >= 16 {
		for oldKey, oldTransport := range transport.pinned {
			oldTransport.CloseIdleConnections()
			delete(transport.pinned, oldKey)
			break
		}
	}
	transport.pinned[key] = pinned
	return pinned
}

func (transport *providerRetryTransport) CloseIdleConnections() {
	if closer, ok := transport.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	transport.direct.CloseIdleConnections()
	transport.mu.Lock()
	for key, pinned := range transport.pinned {
		pinned.CloseIdleConnections()
		delete(transport.pinned, key)
	}
	transport.mu.Unlock()
}

// providerCloneRequest 复制请求以便重放，保证 URL、Host 头与请求体都可再用。
func providerCloneRequest(request *http.Request) (*http.Request, error) {
	upstream := request.Clone(request.Context())
	if request.URL != nil {
		cloned := *request.URL
		upstream.URL = &cloned
	}
	if upstream.Host == "" && request.URL != nil {
		upstream.Host = request.URL.Host
	}
	if request.GetBody != nil && request.Body != nil {
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		upstream.Body = body
	}
	return upstream, nil
}

// providerRouteAborted 区分「可以换通道再试」和「明确被拒绝」。
func providerRouteAborted(err error) bool {
	if err == nil {
		return false
	}
	// 注意：这里不能把 DeadlineExceeded 当成「别试了」——候选通道自己的预算
	// 用完恰恰是最该换通道的时候；调用方真取消由 parent.Err() 单独判断。
	if errors.Is(err, context.Canceled) {
		return true
	}
	var cooldown *maskChannelCooldownError
	if errors.As(err, &cooldown) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// 代理认证失败、证书错误属于配置/信任问题，换通道没有意义。
		message := strings.ToLower(urlErr.Err.Error())
		for _, fatal := range []string{"proxyconnect", "407", "certificate", "x509"} {
			if strings.Contains(message, fatal) {
				return true
			}
		}
	}
	return false
}
