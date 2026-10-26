package core

import (
	"context"
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
	hits   int32
	fail   error
	body   string
	status int
	header http.Header
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
	header := transport.header
	if header == nil {
		header = make(http.Header)
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
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

// 伪装通道失败后，只有「原通道确实拿到了正常响应」才回落。
// 原通道本身也失败（没做阻断的网络不会失败，会正常返回 HTML 页面）时，
// 必须判失败并停止回落，而不是继续在死路上重试。
func TestHongguoSniMaskRejectsFailedBase(t *testing.T) {
	base := &countingTransport{status: http.StatusForbidden, body: "blocked"}
	masked := &countingTransport{fail: errors.New("握手失败")}
	transport := newHongguoSniMaskTransportWith(base, masked)

	if _, err := transport.RoundTrip(mustRequest(t, "https://hongguoduanju.com/")); err == nil {
		t.Fatal("原通道返回 403 时不应视为成功")
	}
	if got := base.count(); got != 1 {
		t.Fatalf("原通道探测 %d 次，期望 1 次", got)
	}
	// 已经确认 base 不可用，之后的失败不应再回落原通道。
	if _, err := transport.RoundTrip(mustRequest(t, "https://hongguoduanju.com/")); err == nil {
		t.Fatal("第二次请求不应成功")
	}
	if got := base.count(); got != 1 {
		t.Errorf("确认原通道不可用后仍在回落，命中 %d 次", got)
	}
}

// 页面类域名的原通道返回 HTML 属于正常（红果入口本身就是网页）；
// 媒体类域名的原通道返回 HTML 则是被劫持到反诈页，必须判失败。
func TestMaskBaseUsabilityChecks(t *testing.T) {
	page := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
	}
	if !maskBaseStatusUsable(page) {
		t.Error("页面类域名返回 HTML 应当视为可用")
	}
	if maskBaseMediaUsable(page) {
		t.Error("媒体类域名返回 HTML 反诈页必须视为不可用")
	}
	stream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/vnd.apple.mpegurl"}},
	}
	if !maskBaseMediaUsable(stream) {
		t.Error("媒体类域名返回 m3u8 应当视为可用")
	}
	blocked := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	if maskBaseStatusUsable(blocked) || maskBaseMediaUsable(blocked) {
		t.Error("非 2xx 一律不可用")
	}
}

// 本地取消（切分类 / 重新搜索 / 退出页面都会 cancelCatalog）不算通道故障，
// 不能因为用户点了两下就锁住整个域名。
func TestHongguoSniMaskIgnoresCancel(t *testing.T) {
	base := &countingTransport{}
	masked := &countingTransport{fail: context.Canceled}
	transport := newHongguoSniMaskTransportWith(base, masked)

	for index := 0; index < 3; index++ {
		if _, err := transport.RoundTrip(mustRequest(t, "https://hongguoduanju.com/")); !errors.Is(err, context.Canceled) {
			t.Fatalf("第 %d 次应当返回取消错误，实际 %v", index+1, err)
		}
	}
	if got := masked.count(); got != 3 {
		t.Errorf("取消不应触发冷却，伪装通道命中 %d 次，期望 3 次", got)
	}
	if got := base.count(); got != 0 {
		t.Errorf("本地取消不应回落原通道，命中 %d 次", got)
	}
}

// 冷却期必须快速失败并给出可读提示，而不是先白等一次超时再回落原通道。
func TestHongguoSniMaskCooldownFailsFast(t *testing.T) {
	base := &countingTransport{}
	masked := &countingTransport{fail: errors.New("握手失败")}
	transport := newHongguoSniMaskTransportWith(base, masked)
	transport.state.failures = 5
	transport.state.retryAt = time.Now().Add(8 * time.Second)

	started := time.Now()
	_, err := transport.RoundTrip(mustRequest(t, "https://hongguoduanju.com/"))
	if err == nil {
		t.Fatal("冷却期内应当直接失败")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("冷却期应当快速失败，实际耗时 %v", elapsed)
	}
	var cooldown *maskChannelCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("错误类型不是冷却提示: %v", err)
	}
	if !strings.Contains(err.Error(), "秒后自动重试") {
		t.Errorf("提示缺少剩余时间: %v", err)
	}
	if masked.count() != 0 || base.count() != 0 {
		t.Errorf("冷却期内不应发出任何请求（masked=%d base=%d）", masked.count(), base.count())
	}
}

// 被站点拒绝后的冷却阶梯：从 5 秒起步逐级拉长，Retry-After 只能加长不能缩短。
func TestBackoffLadderEscalates(t *testing.T) {
	response := &http.Response{Header: make(http.Header)}
	cases := []struct {
		strikes int
		want    time.Duration
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{5, 60 * time.Second},
		{99, 60 * time.Second},
	}
	for _, item := range cases {
		if got := backoffDelay(item.strikes, response); got != item.want {
			t.Errorf("第 %d 次被拒冷却 %v，期望 %v", item.strikes, got, item.want)
		}
	}
	response.Header.Set("Retry-After", "120")
	if got := backoffDelay(1, response); got != 120*time.Second {
		t.Errorf("Retry-After 应加长冷却，实际 %v", got)
	}
	response.Header.Set("Retry-After", "1")
	if got := backoffDelay(4, response); got != 40*time.Second {
		t.Errorf("Retry-After 不应缩短阶梯冷却，实际 %v", got)
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
