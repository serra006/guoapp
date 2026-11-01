package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// yeguoFixtureAccess 把野果客户端指到本地 fixture 服务器。
func yeguoFixtureAccess(server *httptest.Server) *yeguoAccess {
	return &yeguoAccess{base: server.URL, identifier: "fixture-trace", loadedAt: time.Now()}
}

// TestYeguoScriptAPIBaseDetection 覆盖 2026-11 之后的新发现方式：接口入口变成
// 打包 chunk 里的字面量（旧版是 Nuxt payload 的 apiBaseURL 字段）。
func TestYeguoScriptAPIBaseDetection(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "新版 chunk 字面量",
			script: "Cf=`production`,wf=()=>Cf===`production`,Tf=()=>`https://www.yeguodj.com/api.php`,Ef=/^(https?:)?\\/\\//",
			want:   "https://www.yeguodj.com/api.php",
		},
		{
			name:   "变量改名不影响",
			script: "const zz=()=>(\"https://api.example-drive.cc/gw/api.php\");",
			want:   "https://api.example-drive.cc/gw/api.php",
		},
		{
			name:   "带端口",
			script: "base=\"https://edge.example.cc:8443/api.php\"",
			want:   "https://edge.example.cc:8443/api.php",
		},
		{
			name:   "没有接口入口",
			script: "const apiBaseURL=null;fetch('/api/home/homePage')",
			want:   "",
		},
		{
			name:   "非法 scheme 不采信",
			script: "http://www.yeguodj.com/api.php",
			want:   "",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := yeguoScriptAPIBaseURL(item.script); got != item.want {
				t.Fatalf("got %q want %q", got, item.want)
			}
		})
	}
}

// TestYeguoAPIBaseKeepsNuxtFallback 确认旧的 Nuxt payload 路径仍然可用（回滚兼容）。
func TestYeguoAPIBaseKeepsNuxtFallback(t *testing.T) {
	document, err := html.Parse(strings.NewReader(`<html><body>
<script id="__NUXT_DATA__" type="application/json">[{"apiBaseURL":1},"https://api.legacy-yeguo.cc"]</script>
</body></html>`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := yeguoAPIBase(document, ""); got != "https://api.legacy-yeguo.cc" {
		t.Fatalf("Nuxt 回退失效: %q", got)
	}
	if got := yeguoAPIBase(document, "https://configured.example.cc/api.php"); got != "https://configured.example.cc/api.php" {
		t.Fatalf("显式配置优先级失效: %q", got)
	}
	empty, _ := html.Parse(strings.NewReader(`<html><body></body></html>`))
	if got := yeguoAPIBase(empty, ""); got != "" {
		t.Fatalf("无入口时应返回空串，得到 %q", got)
	}
}

// TestYeguoCategoriesFromFilters 覆盖新分类来源：exploreList 的 data.filters。
func TestYeguoCategoriesFromFilters(t *testing.T) {
	payload := []any{
		map[string]any{"key": "theme", "title": "主题", "options": []any{
			map[string]any{"name": "野果原创", "value": float64(7)},
			map[string]any{"name": "真人短剧", "value": "8"},
			map[string]any{"name": "全部", "value": float64(0)},
		}},
		map[string]any{"key": "time", "title": "时间", "options": []any{
			map[string]any{"name": "7天内", "value": float64(1)},
		}},
		map[string]any{"key": "unknown_dimension", "title": "未知", "options": []any{
			map[string]any{"name": "忽略", "value": float64(9)},
		}},
	}
	categories := yeguoCategoriesFromFilters(payload)
	if len(categories) != 3 {
		t.Fatalf("分类数量不符: %+v", categories)
	}
	if categories[0].ID != "theme:7" || categories[0].Name != "野果原创" {
		t.Fatalf("首条分类不符: %+v", categories[0])
	}
	if categories[1].ID != "theme:8" {
		t.Fatalf("字符串 value 未归一: %+v", categories[1])
	}
	if categories[2].ID != "time:1" {
		t.Fatalf("未知维度未被跳过或顺序错误: %+v", categories)
	}
	if got := yeguoCategoriesFromFilters(nil); len(got) != 0 {
		t.Fatalf("空输入应返回空: %+v", got)
	}
}

// TestFetchYeguoCategoriesUsesExploreFilters 端到端（本地 fixture）：分类必须来自
// exploreList，且不再依赖已下线的 /api/home/contentOptions。
func TestFetchYeguoCategoriesUsesExploreFilters(t *testing.T) {
	var sawLegacy bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/theater/exploreList":
			if request.Method != http.MethodPost {
				t.Errorf("exploreList 用了 %s", request.Method)
			}
			_, _ = io.WriteString(response, `{"status":"1","data":{"list":[],"filters":[{"key":"setting","title":"设定","options":[{"name":"禁忌伦理","value":23}]}]}}`)
		case "/api/home/contentOptions":
			sawLegacy = true
			_, _ = io.WriteString(response, `{"status":"1","data":{}}`)
		default:
			t.Errorf("意外路由: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	downloader := &Downloader{}
	client := downloader.yeguoClient()
	client.access = yeguoFixtureAccess(server)
	categories, err := downloader.fetchYeguoCategories(context.Background())
	if err != nil {
		t.Fatalf("分类失败: %v", err)
	}
	if len(categories) != 1 || categories[0].ID != "setting:23" {
		t.Fatalf("分类解析不符: %+v", categories)
	}
	if sawLegacy {
		t.Fatal("新结构可用时不应再去打已下线的旧接口")
	}
}

// TestFetchYeguoCategoriesFallsBackToLegacy 旧接口回退仍然可用。
func TestFetchYeguoCategoriesFallsBackToLegacy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/theater/exploreList":
			_, _ = io.WriteString(response, `{"status":"1","data":{"list":[]}}`)
		case "/api/home/contentOptions":
			_, _ = io.WriteString(response, `{"status":"1","data":{"video_filter":{"theme":{"list":[{"name":"野果原创","value":"7"}]}}}}`)
		default:
			t.Errorf("意外路由: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	downloader := &Downloader{}
	client := downloader.yeguoClient()
	client.access = yeguoFixtureAccess(server)
	categories, err := downloader.fetchYeguoCategories(context.Background())
	if err != nil {
		t.Fatalf("分类失败: %v", err)
	}
	if len(categories) != 1 || categories[0].ID != "theme:7" {
		t.Fatalf("旧接口回退解析不符: %+v", categories)
	}
}

// TestDecodeYeguoResponseSurfacesErrcode 新版外层信封的 errcode 要能报出来。
func TestDecodeYeguoResponseSurfacesErrcode(t *testing.T) {
	body := []byte(`{"errcode":10012,"timestamp":1793525425,"data":"","sign":""}`)
	_, err := decodeYeguoResponse(body, nil)
	if err == nil || !strings.Contains(err.Error(), "10012") {
		t.Fatalf("errcode 未透出: %v", err)
	}
	// 旧信封（无 errcode、明文 data）仍要原样返回。
	legacy, err := decodeYeguoResponse([]byte(`{"status":"1","data":{"ok":"1"}}`), nil)
	if err != nil || mapString(legacy, "status") != "1" {
		t.Fatalf("旧信封解析回归: %v %+v", err, legacy)
	}
}

// TestYeguoCallRejectsWrongStatus 业务失败（status=0）仍按错误处理。
func TestYeguoCallRejectsWrongStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"status":0,"msg":"标签无效","data":[]}`)
	}))
	defer server.Close()
	downloader := &Downloader{}
	client := downloader.yeguoClient()
	client.access = yeguoFixtureAccess(server)
	if _, err := client.call(context.Background(), "/api/theater/tagList", url.Values{"page": {"1"}}); err == nil || !strings.Contains(err.Error(), "标签无效") {
		t.Fatalf("业务失败未透出: %v", err)
	}
}
