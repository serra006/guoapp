package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// 红果在播放接口上对三签名的要求反复开合（2026-11-01 要无签名，2026-11-03 又要签名）。
// 这两个用例把「签名确实发出去了」和「签名被拒时会退回不签名」钉住，
// 免得下一次上游开合时又只能靠线上实测才发现。
func newHongguoPlaybackTestEngine(t *testing.T, handler http.HandlerFunc) (*Downloader, *int32) {
	t.Helper()
	engine, err := newNativeEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.downloads.close)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	engine.downloader.hongguoClient().baseURL = server.URL
	var calls int32
	return engine.downloader, &calls
}

func TestHongguoPlaybackRequestSignsWhenAsked(t *testing.T) {
	var sawGorgon, sawStub atomic.Value
	downloader, _ := newHongguoPlaybackTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		sawGorgon.Store(r.Header.Get("X-Gorgon"))
		sawStub.Store(r.Header.Get("X-Ss-Stub"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"ok":1}}`))
	})
	ctx := context.Background()
	const deviceID, installID = "7702409114256690238", "7702409114256690238"

	result, err := downloader.hongguoAppPlaybackRequest(ctx, hongguoAppMultiVideoModelPath, deviceID, installID,
		map[string]any{"mixed_video_id_map": map[string]any{"1": []string{"7702409209337367576"}}}, true)
	if err != nil {
		t.Fatalf("带签名的取流请求失败: %v", err)
	}
	if len(nestedMap(result, "data")) == 0 {
		t.Fatal("带签名的取流响应没有被解析出来")
	}
	if gorgon, _ := sawGorgon.Load().(string); gorgon == "" {
		t.Fatal("signed=true 时没有带 X-Gorgon")
	}
	if stub, _ := sawStub.Load().(string); stub == "" {
		t.Fatal("signed=true 时没有带 X-Ss-Stub")
	}

	sawGorgon.Store("")
	if _, err := downloader.hongguoAppPlaybackRequest(ctx, hongguoAppMultiVideoModelPath, deviceID, installID,
		map[string]any{"mixed_video_id_map": map[string]any{"1": []string{"7702409209337367576"}}}, false); err != nil {
		t.Fatalf("不签名的取流请求失败: %v", err)
	}
	if gorgon, _ := sawGorgon.Load().(string); gorgon != "" {
		t.Fatal("signed=false 时不应该带 X-Gorgon")
	}
}

func TestHongguoPlaybackAttemptFallsBackToUnsigned(t *testing.T) {
	var requests int32
	downloader, _ := newHongguoPlaybackTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		// 模拟「当前只要不签名」的那一轮：带签名 200 + 0 字节，不带签名正常返回。
		if r.Header.Get("X-Gorgon") != "" {
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"ok":1}}`))
	})

	media, err := downloader.hongguoPlaybackAttempt(context.Background(), hongguoAppMultiVideoModelPath,
		"7702409114256690238", "7702409114256690238",
		map[string]any{"mixed_video_id_map": map[string]any{"1": []string{"7702409209337367576"}}},
		func(result map[string]any) (providerMedia, error) {
			if len(nestedMap(result, "data")) == 0 {
				return providerMedia{}, errHongguoAppNoMedia
			}
			return providerMedia{URL: "https://example.com/a.mp4"}, nil
		})
	if err != nil {
		t.Fatalf("签名被拒后没有退回不签名: %v", err)
	}
	if media.URL == "" {
		t.Fatal("退回不签名后没有拿到媒体地址")
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("应该恰好请求两次（先签后退），实际 %d 次", got)
	}
}
