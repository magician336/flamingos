// transport 的 Origin 校验测试(票 0005-03/spec 0005):中间件装在 HTTP 链
// 最外层,允许列表支持通配端口,匹配纯函数表驱动;不匹配 403,未配置 =
// 关闭放行(MCP Streamable HTTP 规范要求,防 DNS rebinding)。黑盒只从
// transport.New 产出的 http.Handler 接缝断言:裸 http.Client 发带 Origin
// 头的 POST,以状态码判定放行/拒绝——放行 case 的 SDK 层行为(对无会话
// 请求的应答)不属本票,断言"非 403"即穿透本层。
package transport_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/transport"
)

// originHarness 装配带 Origin 允许列表的网关 handler 并起测试服务;
// allowed 为 nil 即关闭态(不装 Origin 中间件)。
func originHarness(t *testing.T, allowed []string) *httptest.Server {
	t.Helper()
	handler, err := transport.New(context.Background(), gateway.New(policyTools()),
		transport.WithOrigin(allowed))
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// postWithOrigin 向 /mcp 发一个带 Origin 头的 POST(空 origin 即不带头),
// 返回状态码与响应体。
func postWithOrigin(t *testing.T, ts *httptest.Server, origin string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("读响应体失败: %v", err)
	}
	return res.StatusCode, string(body)
}

func TestTransportOrigin(t *testing.T) {
	tests := []struct {
		name    string   // 用例名(中文,描述用户可见行为)
		allowed []string // 允许列表配置(nil = 未配置,关闭态)
		origin  string   // 请求 Origin 头;空串 = 不带头(非浏览器客户端)
		want403 bool     // true = 应被 403
	}{
		// ---- 通配端口条目:本地开发 http://localhost:* ----
		{name: "通配端口:任意端口的同源同 scheme 请求放行", allowed: []string{"http://localhost:*"}, origin: "http://localhost:3000"},
		{name: "通配端口:默认端口(不写端口)同样放行", allowed: []string{"http://localhost:*"}, origin: "http://localhost"},
		{name: "通配端口:scheme 不同即拒,https 冒充 http 源被 403", allowed: []string{"http://localhost:*"}, origin: "https://localhost:3000", want403: true},
		{name: "通配端口:端口相同但 host 不同被 403", allowed: []string{"http://localhost:*"}, origin: "http://evil.example.com:3000", want403: true},
		{name: "通配端口:host 前缀混淆(localhosty)被 403", allowed: []string{"http://localhost:*"}, origin: "http://localhosty:3000", want403: true},
		{name: "大小写:scheme 与 host 比较不区分大小写", allowed: []string{"http://localhost:*"}, origin: "HTTP://LOCALHOST:3000"},

		// ---- 精确端口条目 ----
		{name: "精确端口:端口相等放行", allowed: []string{"http://localhost:3000"}, origin: "http://localhost:3000"},
		{name: "精确端口:端口不同被 403", allowed: []string{"http://localhost:3000"}, origin: "http://localhost:3001", want403: true},

		// ---- 无端口条目:生产收紧形态 ----
		{name: "无端口条目:请求也无显式端口放行", allowed: []string{"https://console.example.com"}, origin: "https://console.example.com"},
		{name: "无端口条目:请求带显式端口被 403", allowed: []string{"https://console.example.com"}, origin: "https://console.example.com:8443", want403: true},

		// ---- IPv6 条目 ----
		{name: "IPv6:方括号主机与显式端口精确匹配放行", allowed: []string{"http://[::1]:3000"}, origin: "http://[::1]:3000"},
		{name: "IPv6:端口不同被 403", allowed: []string{"http://[::1]:3000"}, origin: "http://[::1]:4000", want403: true},

		// ---- 无 Origin 头:非浏览器客户端(DNS rebinding 只能伪造浏览器请求)----
		{name: "无 Origin 头:非浏览器客户端照常放行", allowed: []string{"https://console.example.com"}, origin: ""},

		// ---- 非法 Origin 值:解析不了的一律拒绝 ----
		{name: "非法 Origin:无 scheme 的裸文本被 403", allowed: []string{"http://localhost:*"}, origin: "not a url", want403: true},
		{name: "非法 Origin:javascript 伪协议被 403", allowed: []string{"http://localhost:*"}, origin: "javascript:alert(1)", want403: true},

		// ---- 关闭态:未配置 = 零行为放行 ----
		{name: "关闭态:任意 Origin 都放行(未配置即关闭)", allowed: nil, origin: "http://evil.example.com:3000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := originHarness(t, tt.allowed)
			status, body := postWithOrigin(t, ts, tt.origin)
			if tt.want403 {
				if status != http.StatusForbidden {
					t.Fatalf("Origin %q 应被 403,实际状态码 %d", tt.origin, status)
				}
				if !strings.Contains(body, "Origin") {
					t.Errorf("403 响应体应点名 Origin 语义,实际 %q", body)
				}
				return
			}
			if status == http.StatusForbidden {
				t.Fatalf("Origin %q 应被放行,实际 403(响应体 %q)", tt.origin, body)
			}
		})
	}
}
