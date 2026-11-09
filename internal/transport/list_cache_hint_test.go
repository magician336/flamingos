// transport 的 tools/list 缓存 hint 产出测试(票 0006-03/spec 0006 触发
// 语义裁决):WithListCacheHint 经 ServerOptions.SetCacheable 对携带
// Cacheable 的应答统一置 TTLMs(cacheScope 不设,SDK normalize 缺省
// public——清单对所有角色一致,spec 0005 既有裁决);缺省/≤0 = 不安装
// (关闭态零行为)。黑盒断言经 SDK client 侧 ListToolsResult 的
// CacheableResult 读取面(getTTLMs/GetCacheScope)。
package transport_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// hintHarness 以给定选项装配进程内网关并完成 initialize,返回客户端会话
// (tools/list 应答的可观察面即 client 侧解码结果)。
func hintHarness(t *testing.T, opts ...transport.Option) *mcp.ClientSession {
	t.Helper()
	handler, err := transport.New(context.Background(), gateway.New(map[string]upstream.Client{
		"calc": &fakeClient{tools: []*mcp.Tool{
			{Name: "greet", InputSchema: map[string]any{"type": "object"}},
		}},
	}), opts...)
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "hint-agent", Version: "0.0.1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:             ts.URL + "/mcp",
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestListCacheHint(t *testing.T) {
	t.Run("ttl>0:tools/list 应答携带 ttlMs(毫秒换算)且 cacheScope=public", func(t *testing.T) {
		cs := hintHarness(t, transport.WithListCacheHint(1500*time.Millisecond))

		list, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("列举 tool 失败: %v", err)
		}
		if got := list.GetTTLMs(); got != 1500 {
			t.Errorf("应答 TTL hint = %d ms,期望 1500(配置毫秒数原样宣告)", got)
		}
		if got := list.GetCacheScope(); got != "public" {
			t.Errorf("cacheScope = %q,期望 public(normalize 协议缺省:清单对所有角色一致)", got)
		}
	})

	t.Run("缺省不传:应答不宣告 TTL hint(GetTTLMs==0)", func(t *testing.T) {
		cs := hintHarness(t)

		list, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("列举 tool 失败: %v", err)
		}
		if got := list.GetTTLMs(); got != 0 {
			t.Errorf("缺省应不宣告 TTL hint(GetTTLMs==0),实际 %d", got)
		}
	})

	t.Run("ttl≤0:关闭态哨兵,与缺省同形", func(t *testing.T) {
		for name, ttl := range map[string]time.Duration{"零值": 0, "负值": -time.Second} {
			t.Run(name, func(t *testing.T) {
				cs := hintHarness(t, transport.WithListCacheHint(ttl))

				list, err := cs.ListTools(context.Background(), nil)
				if err != nil {
					t.Fatalf("列举 tool 失败: %v", err)
				}
				if got := list.GetTTLMs(); got != 0 {
					t.Errorf("ttl≤0 应不安装 hint 产出(GetTTLMs==0),实际 %d", got)
				}
			})
		}
	})
}
