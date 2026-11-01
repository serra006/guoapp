package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubTransport 记录被调用的次数与请求，并按脚本返回结果。
type stubTransport struct {
	calls    int
	requests []*http.Request
	bodies   []string
	status   int
	err      error
}

func (stub *stubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	stub.calls++
	stub.requests = append(stub.requests, request)
	body := ""
	if request.Body != nil {
		if raw, err := io.ReadAll(request.Body); err == nil {
			body = string(raw)
			request.Body.Close()
		}
	}
	stub.bodies = append(stub.bodies, body)
	if stub.err != nil {
		return nil, stub.err
	}
	status := stub.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     http.Header{},
		Request:    request,
	}, nil
}

func newCdnSniTestRequest(t *testing.T, method, address string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, address, nil)
	if err != nil {
		t.Fatalf("构建请求失败: %v", err)
	}
	return request
}

func TestCdnSniMaskedServerName(t *testing.T) {
	cases := []struct{ host, want string }{
		{"op.udhhzr.cn", "udhhzr.cn"},
		{"as.oolrvd.cn", "oolrvd.cn"},
		{"a.b.c.top", "b.c.top"},
		{"OP.UDHHZR.CN", "udhhzr.cn"},
		{"udhhzr.cn", ""},
		{"127.0.0.1", ""},
		{"cdn.local", ""},
		{"op.udhhzr.zzz", ""},
		{"", ""},
	}
	for _, testCase := range cases {
		if got := cdnSniMaskedServerName(testCase.host); got != testCase.want {
			t.Errorf("cdnSniMaskedServerName(%q) = %q, want %q", testCase.host, got, testCase.want)
		}
	}
}

func TestCdnSniFallbackRetriesMaskedAfterBaseFailure(t *testing.T) {
	base := &stubTransport{err: errors.New("read tcp: EOF")}
	masked := &stubTransport{status: http.StatusOK}
	transport := newCdnSniFallbackTransportWith(base, masked)

	response, err := transport.RoundTrip(newCdnSniTestRequest(t, http.MethodGet, "https://op.udhhzr.cn/videos5/a/index.m3u8"))
	if err != nil {
		t.Fatalf("兜底重试应成功: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", response.StatusCode)
	}
	if base.calls != 1 || masked.calls != 1 {
		t.Fatalf("base=%d masked=%d, want 1/1", base.calls, masked.calls)
	}
	transport.mu.Lock()
	channel := transport.hosts["op.udhhzr.cn"]
	transport.mu.Unlock()
	if channel == nil || !time.Now().Before(channel.preferUntil) {
		t.Fatalf("成功后应记录伪装偏好, got %+v", channel)
	}
}

func TestCdnSniFallbackPrefersMaskedForKnownHost(t *testing.T) {
	base := &stubTransport{err: errors.New("read tcp: EOF")}
	masked := &stubTransport{status: http.StatusOK}
	transport := newCdnSniFallbackTransportWith(base, masked)
	transport.recordSuccess("op.udhhzr.cn")

	response, err := transport.RoundTrip(newCdnSniTestRequest(t, http.MethodGet, "https://op.udhhzr.cn/videos5/a/index.m3u8"))
	if err != nil {
		t.Fatalf("已知主机应直接走伪装通道: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", response.StatusCode)
	}
	if base.calls != 0 || masked.calls != 1 {
		t.Fatalf("base=%d masked=%d, want 0/1", base.calls, masked.calls)
	}
}

func TestCdnSniFallbackSkipsHostWithoutMask(t *testing.T) {
	base := &stubTransport{status: http.StatusOK}
	masked := &stubTransport{status: http.StatusCreated}
	transport := newCdnSniFallbackTransportWith(base, masked)

	response, err := transport.RoundTrip(newCdnSniTestRequest(t, http.MethodGet, "https://example.com/a.m3u8"))
	if err != nil {
		t.Fatalf("二层域名不应触发兜底: %v", err)
	}
	if response.StatusCode != http.StatusOK || masked.calls != 0 {
		t.Fatalf("status=%d masked=%d, want 200/0", response.StatusCode, masked.calls)
	}
}

func TestCdnSniFallbackReturnsBaseErrorWhenBothFail(t *testing.T) {
	baseErr := errors.New("read tcp: EOF")
	base := &stubTransport{err: baseErr}
	masked := &stubTransport{err: errors.New("tls: handshake failure")}
	transport := newCdnSniFallbackTransportWith(base, masked)

	_, err := transport.RoundTrip(newCdnSniTestRequest(t, http.MethodGet, "https://op.udhhzr.cn/a.m3u8"))
	if !errors.Is(err, baseErr) {
		t.Fatalf("两条路都失败时应回原链路错误, got %v", err)
	}
	if masked.calls != 1 {
		t.Fatalf("masked 应被尝试一次, got %d", masked.calls)
	}
}

func TestCdnSniFallbackReplaysRequestBody(t *testing.T) {
	base := &stubTransport{err: errors.New("read tcp: EOF")}
	masked := &stubTransport{status: http.StatusOK}
	transport := newCdnSniFallbackTransportWith(base, masked)

	request, err := http.NewRequest(http.MethodPost, "https://op.udhhzr.cn/a.m3u8", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("构建请求失败: %v", err)
	}
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("带 GetBody 的请求应可重放: %v", err)
	}
	if len(masked.bodies) != 1 || masked.bodies[0] != "payload" {
		t.Fatalf("重放 body = %q, want payload", masked.bodies)
	}
}

func TestCdnSniFallbackIgnoresCanceled(t *testing.T) {
	base := &stubTransport{err: context.Canceled}
	masked := &stubTransport{status: http.StatusOK}
	transport := newCdnSniFallbackTransportWith(base, masked)

	_, err := transport.RoundTrip(newCdnSniTestRequest(t, http.MethodGet, "https://op.udhhzr.cn/a.m3u8"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消不应触发兜底, got %v", err)
	}
	if masked.calls != 0 {
		t.Fatalf("masked 不应被调用, got %d", masked.calls)
	}
}
