package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestProviderChannelName 逐个校验站源 id → 中文渠道名的映射，
// 防止「黄果」这类历史渠道名串到麒麟/暴风等站源的错误提示上。
func TestProviderChannelName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		source string
		want   string
	}{
		{sourceHuangguoAI, "黄果AI"},
		{sourceHuangguoVideo, "黄果视频"},
		{sourceHuangdou, "黄豆"},
		{sourceHongguo, "红果"},
		{sourceHuangju, "剧果"},
		{sourceYeguo, "野果"},
		{sourceDSD, "帝果"},
		{sourceCloudFront, "黄果旧版"},
		{sourceSorani, "青空次元"},
		{sourceGuipian, "鬼片网"},
		{sourceHanxiaoquan, "韩小圈"},
		{sourceQimao, "七猫"},
		{sourceBofeng, bofengChannelName},
		{sourceQilin, qilinChannelName},
		// 兜底值：映射函数本身只返回空串，由请求错误文案补「站源」。
		{"", ""},
		{"not-a-source", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.source, func(t *testing.T) {
			t.Parallel()
			if got := providerChannelName(tc.source); got != tc.want {
				t.Fatalf("providerChannelName(%q) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
	// 两个常量化的渠道名不能被映射表写死的字符串绕过。
	if got := providerChannelName("bofeng"); got != "暴风资源" {
		t.Fatalf("bofeng channel name = %q, want 暴风资源", got)
	}
	if got := providerChannelName("qilin"); got != "麒麟短剧" {
		t.Fatalf("qilin channel name = %q, want 麒麟短剧", got)
	}
}

// TestProviderTextRequestErrorUsesCurrentSourceName 证明多域名请求失败时，
// 前缀随 URL 所属站源变化，且旧文案「黄果来源请求失败」不再出现。
func TestProviderRequestErrorTextUsesCurrentSourceName(t *testing.T) {
	t.Parallel()
	cause := errors.New("dial tcp: i/o timeout")
	cases := []struct {
		name      string
		rawURL    string
		wantName  string
		wantTried int
	}{
		{"qilin", "https://www.qlys.cc/api.php/provide/vod/?ac=list", "麒麟短剧", 3},
		{"qilin-apex", "https://qlys.cc/api.php/provide/vod/", "麒麟短剧", 2},
		{"bofeng", "https://bfzy3.tv/api.php/provide/vod/", "暴风资源", 4},
		{"huangguoai", "https://huangguoai.com/video/12/", "黄果AI", 5},
		{"sorani", "https://api.sorani.cc/sorani-cms/api/video/1", "青空次元", 6},
		{"unknown", "https://example.test/index.html", "站源", 7},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := providerTextRequestError(tc.rawURL, tc.wantTried, cause)
			if err == nil {
				t.Fatal("providerTextRequestError returned nil")
			}
			want := tc.wantName + "请求失败：已尝试 " + strconv.Itoa(tc.wantTried) + " 个域名，最后错误："
			if !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("error = %q, want prefix %q", err.Error(), want)
			}
			if strings.Contains(err.Error(), "黄果来源请求失败") {
				t.Fatalf("legacy misleading text leaked into error: %q", err.Error())
			}
			if !errors.Is(err, cause) {
				t.Fatalf("error does not wrap original cause: %q", err.Error())
			}
		})
	}
}

// TestSoraniPlayErrorLoginGate 用纯函数覆盖 canPlay 门控：
// 只有「字段存在且为 false」才报需要登录，缺字段/类型不符都不能误报。
func TestSoraniPlayErrorLoginGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		row         map[string]any
		envelope    map[string]any
		want        string
		wantMessage string
		notMessage  string
	}{
		{
			name:        "false with message",
			row:         map[string]any{"canPlay": false, "message": "请先登录", "playUrl": ""},
			want:        "需要登录账号",
			wantMessage: "请先登录",
		},
		{
			name:        "false with msg",
			row:         map[string]any{"canPlay": false, "msg": "登录已过期"},
			want:        "需要登录账号",
			wantMessage: "登录已过期",
		},
		{
			name:        "false with reason",
			row:         map[string]any{"canPlay": false, "reason": "会员专属"},
			want:        "需要登录账号",
			wantMessage: "会员专属",
		},
		{
			name:        "false without message falls back to envelope message",
			row:         map[string]any{"canPlay": false},
			envelope:    map[string]any{"code": 200, "message": "请先登录"},
			want:        "需要登录账号",
			wantMessage: "请先登录",
		},
		{
			name:        "false without message falls back to envelope msg",
			row:         map[string]any{"canPlay": false},
			envelope:    map[string]any{"code": 200, "msg": "会话失效"},
			want:        "需要登录账号",
			wantMessage: "会话失效",
		},
		{
			name:        "false without message falls back to envelope msgText",
			row:         map[string]any{"canPlay": false},
			envelope:    map[string]any{"code": 200, "msgText": "需要会员"},
			want:        "需要登录账号",
			wantMessage: "需要会员",
		},
		{
			name:        "inner message wins over envelope",
			row:         map[string]any{"canPlay": false, "message": "内层提示"},
			envelope:    map[string]any{"message": "外层提示"},
			want:        "需要登录账号",
			wantMessage: "内层提示",
			notMessage:  "外层提示",
		},
		{
			name:     "false without message anywhere",
			row:      map[string]any{"canPlay": false},
			envelope: map[string]any{"code": 200},
			want:     "需要登录账号",
		},
		{name: "true with empty playUrl", row: map[string]any{"canPlay": true, "playUrl": ""}, want: ""},
		{name: "field missing", row: map[string]any{"playUrl": "", "url": ""}, want: ""},
		{name: "wrong type", row: map[string]any{"canPlay": "false", "message": "请先登录"}, want: ""},
		{name: "nil field", row: map[string]any{"canPlay": nil, "message": "请先登录"}, want: ""},
		{name: "nil row", row: nil, want: ""},
		{
			name:     "canPlay only in envelope must not trigger gate",
			row:      map[string]any{"playUrl": ""},
			envelope: map[string]any{"canPlay": false, "message": "请先登录"},
			want:     "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := soraniPlayError(tc.row, tc.envelope)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("soraniPlayError(%v, %v) = %v, want nil", tc.row, tc.envelope, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("soraniPlayError(%v, %v) = %v, want containing %q", tc.row, tc.envelope, err, tc.want)
			}
			if tc.wantMessage != "" && !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("soraniPlayError(%v, %v) = %q, want containing message %q", tc.row, tc.envelope, err.Error(), tc.wantMessage)
			}
			if tc.notMessage != "" && strings.Contains(err.Error(), tc.notMessage) {
				t.Fatalf("soraniPlayError(%v, %v) = %q, should not contain %q", tc.row, tc.envelope, err.Error(), tc.notMessage)
			}
		})
	}
}

// TestSoraniResolveMediaErrorText 用本地 httptest 桩走完整解析链路，
// 证明登录门控错误与「暂无可用播放地址」两条文案分别落在正确场景。
func TestSoraniResolveMediaErrorText(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "login gate",
			body: `{"code":200,"data":{"canPlay":false,"message":"请先登录"}}`,
			want: "青空次元需要登录账号才能播放：请先登录",
		},
		{
			name: "login gate without message",
			body: `{"code":200,"data":{"canPlay":false}}`,
			want: "青空次元需要登录账号才能播放",
		},
		{
			name: "outer message fallback",
			body: `{"code":200,"message":"请先登录","data":{"canPlay":false}}`,
			want: "青空次元需要登录账号才能播放：请先登录",
		},
		{
			name: "outer msgText fallback",
			body: `{"code":200,"msgText":"需要会员","data":{"canPlay":false}}`,
			want: "青空次元需要登录账号才能播放：需要会员",
		},
		{
			name: "inner message wins",
			body: `{"code":200,"message":"外层提示","data":{"canPlay":false,"message":"请先登录"}}`,
			want: "青空次元需要登录账号才能播放：请先登录",
		},
		{
			name: "outer message alone is not a gate",
			body: `{"code":200,"message":"请先登录","data":{"playUrl":""}}`,
			want: "青空次元该分集暂无可用播放地址（线路 anime_jp_m3u8）",
		},
		{
			name: "no canPlay field",
			body: `{"code":200,"data":{"playUrl":""}}`,
			want: "青空次元该分集暂无可用播放地址（线路 anime_jp_m3u8）",
		},
		{
			name: "canPlay true empty url",
			body: `{"code":200,"data":{"canPlay":true,"playUrl":""}}`,
			want: "青空次元该分集暂无可用播放地址（线路 anime_jp_m3u8）",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if !strings.HasPrefix(request.URL.Path, "/video/episode/456/play") {
					t.Errorf("unexpected sorani play path: %s", request.URL.Path)
				}
				if got := request.URL.Query().Get("lineCode"); got != soraniLineCodeDefault {
					t.Errorf("lineCode = %q, want %q", got, soraniLineCodeDefault)
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, tc.body)
			}))
			defer server.Close()
			d := &Downloader{
				cfg:           Config{SoraniAPIURL: server.URL, SoraniURL: "https://www.sorani.net"},
				client:        server.Client(),
				providerHosts: map[string]string{},
			}
			task := Task{
				DramaID: providerDramaID(sourceSorani, "123"),
				Chapter: Chapter{ID: providerChapterID(sourceSorani, "123", "456")},
			}
			_, err := d.resolveSoraniMedia(context.Background(), task)
			if err == nil {
				t.Fatalf("resolveSoraniMedia returned nil, want %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("resolveSoraniMedia error = %q, want %q", err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), "暂无可用播放地址") && strings.Contains(err.Error(), "需要登录账号") {
				t.Fatalf("login gate and missing address text mixed: %q", err.Error())
			}
		})
	}
}
