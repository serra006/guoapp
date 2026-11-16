package core

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 红果取流走 Argus 551 信封（provider_hongguo_playback_sign.go），与目录/详情的
// 播放签名是两套独立实现。这几个用例钉住三件事：
//   - 头部五件套确实发出去了，且 Argus 是 551 那套（f281 帧前缀）；
//   - 签名参与的是**实际发出去的** query（ts 与 X-Khronos、X-SS-Req-Ticket 同源）；
//   - 签名被拒（空响应）时会退回不签名再试一次。

func newHongguoPlaybackTestEngine(t *testing.T, handler http.HandlerFunc) (*Downloader, string) {
	t.Helper()
	engine, err := newNativeEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.downloads.close)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	engine.downloader.hongguoClient().baseURL = server.URL
	return engine.downloader, server.URL
}

// hongguoPlaybackTestState 造一套完整身份：机型、指纹、票据都要有。
// 少了任何一项，query 就缺字段，测出来的就不是线上那套请求了。
func hongguoPlaybackTestState() hongguoIdentityState {
	return hongguoIdentityState{
		DeviceID: "7702409114256690238", InstallID: "7702409114256690238", Registered: true,
		CDID: "6c1f3a0e2b7d4958a1c0e5f42d8b7a13", OpenUDID: "0f1e2d3c4b5a6978",
		TTReq: "ttreq-test-ticket", StoreRegion: "cn", StoreRegionSrc: "cn",
		DeviceModel: "22021211RC", DeviceBrand: "Xiaomi", DeviceManufacturer: "Xiaomi",
		DeviceOSVersion: "13", DeviceOSAPI: "33", DeviceBuild: "TKQ1.221114.001",
		DeviceResolution: "1080x2400", DeviceDPI: "440", DeviceChannel: "update_64",
	}
}

func TestHongguoPlaybackRequestSignsWhenAsked(t *testing.T) {
	type snapshot struct {
		argus, ladon, khronos, stub, gorgon, reqTicket, cookie, ua, rawQuery string
	}
	var mu sync.Mutex
	var seen []snapshot
	downloader, base := newHongguoPlaybackTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, snapshot{
			argus:     r.Header.Get("X-Argus"),
			ladon:     r.Header.Get("X-Ladon"),
			khronos:   r.Header.Get("X-Khronos"),
			stub:      r.Header.Get("X-Ss-Stub"),
			gorgon:    r.Header.Get("X-Gorgon"),
			reqTicket: r.Header.Get("X-SS-Req-Ticket"),
			cookie:    r.Header.Get("Cookie"),
			ua:        r.Header.Get("User-Agent"),
			rawQuery:  r.URL.RawQuery,
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"ok":1}}`))
	})
	state := hongguoPlaybackTestState()
	payload := map[string]any{
		"video_id":  "7702409209337367576",
		"biz_param": map[string]any{"video_id_type": 0, "need_all_video_definition": true, "video_platform": 3},
	}

	result, err := downloader.hongguoAppPlaybackRequest(context.Background(), base, hongguoPlaybackVideoModelPath, state, payload, true)
	if err != nil {
		t.Fatalf("带签名的取流请求失败: %v", err)
	}
	if len(nestedMap(result, "data")) == 0 {
		t.Fatal("带签名的取流响应没有被解析出来")
	}

	mu.Lock()
	signed := seen[0]
	mu.Unlock()
	if signed.argus == "" || signed.ladon == "" || signed.khronos == "" || signed.stub == "" {
		t.Fatalf("signed=true 时签名五件套不全: %+v", signed)
	}
	// 官方客户端同时带 Gorgon；缺头是被拒、多头只是浪费，所以必须发。
	if !strings.HasPrefix(signed.gorgon, "8404401c0000") {
		t.Fatalf("X-Gorgon 前缀不是 8404 401c 0000: %q", signed.gorgon)
	}
	if len(signed.gorgon) != 52 {
		t.Fatalf("X-Gorgon 应该是 6 字节前缀 + 20 字节 payload 的十六进制（52 字符），实际 %d", len(signed.gorgon))
	}
	// X-SS-Req-Ticket 与 X-Khronos 必须同源（同一次 now，不允许再取一次时间）。
	ticket, ticketErr := strconv.ParseInt(signed.reqTicket, 10, 64)
	if ticketErr != nil {
		t.Fatalf("X-SS-Req-Ticket 不是毫秒时间戳: %q", signed.reqTicket)
	}
	if strconv.FormatInt(ticket/1000, 10) != signed.khronos {
		t.Fatalf("X-SS-Req-Ticket(%s) 与 X-Khronos(%s) 不同源", signed.reqTicket, signed.khronos)
	}
	// 551 信封的帧前缀固定是 f2 81。
	rawArgus, decodeErr := base64.StdEncoding.DecodeString(signed.argus)
	if decodeErr != nil {
		t.Fatalf("X-Argus 不是合法 base64: %v", decodeErr)
	}
	if len(rawArgus) < 2 || rawArgus[0] != 0xf2 || rawArgus[1] != 0x81 {
		t.Fatalf("X-Argus 帧前缀不是 f2 81（可能拿错成了播放签名那套）: % X", rawArgus[:2])
	}
	if (len(rawArgus)-2)%16 != 0 {
		t.Fatalf("X-Argus 密文长度不是 16 的整数倍: %d", len(rawArgus)-2)
	}
	rawLadon, decodeErr := base64.StdEncoding.DecodeString(signed.ladon)
	if decodeErr != nil {
		t.Fatalf("X-Ladon 不是合法 base64: %v", decodeErr)
	}
	if len(rawLadon) <= 4 || (len(rawLadon)-4)%16 != 0 {
		t.Fatalf("X-Ladon 长度不是「4 字节 nonce + 16 的整数倍」: %d", len(rawLadon))
	}
	// ts 必须与 X-Khronos 同源：签名算的是实际发出去的 query。
	query, queryErr := url.ParseQuery(signed.rawQuery)
	if queryErr != nil {
		t.Fatalf("取流 query 解析失败: %v", queryErr)
	}
	if query.Get("ts") != signed.khronos {
		t.Fatalf("query 里的 ts(%s) 与 X-Khronos(%s) 不一致", query.Get("ts"), signed.khronos)
	}
	if query.Get("device_type") != state.DeviceModel || query.Get("device_id") != state.DeviceID {
		t.Fatal("取流 query 没有复用登记身份的机型/设备号")
	}
	if !strings.Contains(signed.cookie, "ttreq=ttreq-test-ticket") {
		t.Fatalf("登记票据没有带回服务端: %q", signed.cookie)
	}
	if !strings.Contains(signed.ua, state.DeviceModel) {
		t.Fatalf("UA 里的机型与登记机型不一致: %q", signed.ua)
	}

	if _, err := downloader.hongguoAppPlaybackRequest(context.Background(), base, hongguoPlaybackVideoModelPath, state, payload, false); err != nil {
		t.Fatalf("不签名的取流请求失败: %v", err)
	}
	mu.Lock()
	unsigned := seen[1]
	mu.Unlock()
	if unsigned.argus != "" {
		t.Fatal("signed=false 时不应该带 X-Argus")
	}
	if unsigned.khronos != "" {
		t.Fatal("signed=false 时不应该带 X-Khronos")
	}
	if unsigned.gorgon != "" {
		t.Fatal("signed=false 时不应该带 X-Gorgon")
	}
}

func TestHongguoPlaybackAttemptFallsBackToUnsigned(t *testing.T) {
	var requests int32
	downloader, base := newHongguoPlaybackTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		// 模拟「当前只要不签名」的那一轮：带签名 200 + 0 字节，不带签名正常返回。
		if r.Header.Get("X-Argus") != "" {
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"ok":1}}`))
	})

	media, err := downloader.hongguoPlaybackAttempt(context.Background(), base, hongguoPlaybackVideoModelPath,
		hongguoPlaybackTestState(),
		map[string]any{"video_id": "7702409209337367576"},
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

// TestSignHongguoPlaybackRequestBindsQuery 钉住「签的是实际发出去的 query」这条约定：
// query 改一个字节，X-Argus 就必须整体变，否则说明签名读的不是 request 上的 query。
func TestSignHongguoPlaybackRequestBindsQuery(t *testing.T) {
	body := []byte(`{"video_id":"7702409209337367576"}`)
	now := time.Unix(1764550000, 0)
	build := func(rawQuery string) *http.Request {
		request, err := http.NewRequest(http.MethodPost, "https://example.com/novel/player/video_model/v1/?"+rawQuery, strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		signHongguoPlaybackRequest(request, body, now, "7702409114256690238", "7702409114256690239", hongguoRegisterVersionNa)
		return request
	}
	first := build("aid=8662&ts=1764550000&_rticket=1764550000000")
	second := build("aid=8662&ts=1764550000&_rticket=1764550000001")
	if first.Header.Get("X-Argus") == second.Header.Get("X-Argus") {
		t.Fatal("query 变了 X-Argus 却没变，说明签名没绑定实际 query")
	}
	if got, want := first.Header.Get("X-Khronos"), fmt.Sprintf("%d", now.Unix()); got != want {
		t.Fatalf("X-Khronos 应该是秒级时间戳: got %s want %s", got, want)
	}
	// X-Ss-Stub 是 body 的 MD5 大写十六进制。
	if got, want := first.Header.Get("X-Ss-Stub"), fmt.Sprintf("%X", md5.Sum(body)); got != want {
		t.Fatalf("X-Ss-Stub 与 body MD5 不一致: got %s want %s", got, want)
	}
	// 同一 query 反复签名必须每次不同（nonce 随机），否则等于把 nonce 写死了。
	third := build("aid=8662&ts=1764550000&_rticket=1764550000000")
	if first.Header.Get("X-Ladon") == third.Header.Get("X-Ladon") {
		t.Fatal("同一 query 两次签名得到了相同的 X-Ladon，nonce 没有随机化")
	}
	// Gorgon 只吃 query/body 摘要与时间戳，所以同输入必须同输出、异输入必须异输出。
	if first.Header.Get("X-Gorgon") != third.Header.Get("X-Gorgon") {
		t.Fatal("同一 query 两次签名得到了不同的 X-Gorgon，说明它掺进了随机量")
	}
	if first.Header.Get("X-Gorgon") == second.Header.Get("X-Gorgon") {
		t.Fatal("query 变了 X-Gorgon 却没变，说明 Gorgon 没绑定实际 query")
	}
	if first.URL.RawQuery != "aid=8662&ts=1764550000&_rticket=1764550000000" {
		t.Fatal("签名不应改写 request 的 query")
	}
}
