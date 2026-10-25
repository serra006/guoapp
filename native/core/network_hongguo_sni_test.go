package core

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingTransport 记录命中次数，并可按需返回错误。
type countingTransport struct {
	hits int32
	fail error
	body string
}

func (transport *countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	atomic.AddInt32(&transport.hits, 1)
	if transport.fail != nil {
		return nil, transport.fail
	}
	body := transport.body
	if body == "" {
		body = "ok"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (transport *countingTransport) count() int32 { return atomic.LoadInt32(&transport.hits) }

func mustRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	return request
}

// 只有被封的那个域名应该走伪装通道，其他域名必须原样透传。
func TestHongguoSniMaskRoutesOnlyBlockedHost(t *testing.T) {
	base := &countingTransport{}
	masked := &countingTransport{}
	transport := newHongguoSniMaskTransportWith(base, masked)

	cases := []struct {
		url       string
		wantMask  int32
		wantPlain int32
	}{
		{"https://hongguoduanju.com/category/real-drama", 1, 0},
		{"https://www.hongguoduanju.com/", 0, 1},
		{"https://api5-normal-sinfonlineb.fqnovel.com/reading", 0, 1},
		{"https://api-store.qmplaylet.com/api/v1/playlet/index", 0, 1},
	}
	for _, item := range cases {
		response, err := transport.RoundTrip(mustRequest(t, item.url))
		if err != nil {
			t.Fatalf("%s 请求失败: %v", item.url, err)
		}
		response.Body.Close()
	}
	if got, want := masked.count(), int32(1); got != want {
		t.Errorf("伪装通道命中 %d 次，期望 %d 次", got, want)
	}
	if got, want := base.count(), int32(3); got != want {
		t.Errorf("原通道命中 %d 次，期望 %d 次", got, want)
	}
}

// 伪装通道出错时必须回落原通道，不能因为新增逻辑让原本可用的路径变差。
func TestHongguoSniMaskFallsBackWhenMaskedFails(t *testing.T) {
	base := &countingTransport{body: "base"}
	masked := &countingTransport{fail: errors.New("握手失败")}
	transport := newHongguoSniMaskTransportWith(base, masked)

	response, err := transport.RoundTrip(mustRequest(t, "https://hongguoduanju.com/"))
	if err != nil {
		t.Fatalf("应当回落成功，实际报错: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "base" {
		t.Errorf("回落内容为 %q，期望 base", body)
	}

	// 冷却期内不再尝试伪装通道，避免每个请求都白等一次超时。
	before := masked.count()
	if _, err := transport.RoundTrip(mustRequest(t, "https://hongguoduanju.com/")); err != nil {
		t.Fatalf("第二次请求失败: %v", err)
	}
	if masked.count() != before {
		t.Errorf("冷却期内仍访问了伪装通道")
	}
}

// 真实网络验证：需要 LIVE_NET_TEST=1 才执行，CI 默认跳过。
func TestHongguoSniMaskLive(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") == "" {
		t.Skip("设置 LIVE_NET_TEST=1 才跑真实网络验证")
	}
	plain := http.DefaultTransport.(*http.Transport).Clone()
	plain.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	plain.DisableKeepAlives = true
	client := &http.Client{
		Transport: newHongguoSniMaskTransport(plain, plain),
		Timeout:   25 * time.Second,
	}
	response, err := client.Get("https://hongguoduanju.com/")
	if err != nil {
		t.Fatalf("伪装 SNI 请求失败: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 400000))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200", response.StatusCode)
	}
	if !strings.Contains(string(body), "8662") {
		t.Errorf("响应里没找到红果 aid 8662，可能拿到的不是红果页面（%d 字节）", len(body))
	}
	t.Logf("伪装 SNI 成功: HTTP %d, %d 字节", response.StatusCode, len(body))
}
