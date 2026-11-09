// transport 的 HTTP 准入对顺序定点(2026-11-05 架构评审 C2):两对相邻约束
// 此前靠 assembly e2e 与注释,升为独立定点——①Origin→限流:桶空后坏 Origin
// 仍 403 且不消耗令牌(若限流在 Origin 外层,坏 Origin 请求会先吃 429 并把
// 令牌耗掉,下一笔好 Origin 必然 429,当场失败);②限流→trace:429 的过载
// 流量不产生网关请求 span(准入装在可观测之外)。令牌账与状态码用裸 POST
// 断言(SDK 会话遇 403/429 会自毁,不能连续切换 Origin);span 只由 SDK 会
// 话的真实 JSON-RPC 调用产生,以连接后的基线做相对断言。时钟冻结,令牌只
// 在通过准入时消耗。W3 插缓存时只跑这里。
package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestHTTPAdmissionPairOrder(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	now := time.Unix(0, 0) // 冻结时钟:令牌无恢复,消耗事实可精确归因
	handler, err := transport.New(context.Background(), gateway.New(policyTools()),
		transport.WithTelemetry(telemetry.New(tp)),
		transport.WithOrigin([]string{"http://localhost:*"}),
		transport.WithRateLimit(transport.RateLimitConfig{
			RPS:   1,
			Burst: 3, // 恰好容得下:SDK 握手一笔 + tools/call 一笔 + 403 后的裸 POST 一笔
			Now:   func() time.Time { return now },
		}))
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// SDK 会话只负责产生真实 span:握手与一笔 tools/call;429 之后会话会被
	// SDK 关闭,故 429 断言放在最后一笔。
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0.0.1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:             ts.URL + "/mcp",
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	baseline := len(sr.Ended()) // 握手产生的 span 数,后续以相对口径断言
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "calc_greet",
		Arguments: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("tools/call 应成功: %v", err)
	}
	if got := len(sr.Ended()); got != baseline+1 {
		t.Fatalf("tools/call 后 span 数应 %d,实际 %d", baseline+1, got)
	}

	// 裸 POST 直接断言状态码:令牌账与准入顺序的口径。
	raw := func(origin string) int {
		t.Helper()
		req, rerr := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{}`))
		if rerr != nil {
			t.Fatalf("构造请求失败: %v", rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, derr := ts.Client().Do(req)
		if derr != nil {
			t.Fatalf("发送请求失败: %v", derr)
		}
		defer res.Body.Close()
		return res.StatusCode
	}

	// ① Origin→限流:坏 Origin 在桶空时仍 403(不是 429)。
	if got := raw("http://evil.example.com:3000"); got != http.StatusForbidden {
		t.Fatalf("坏 Origin 应 403,实际 %d(若为 429 说明限流在 Origin 外层,顺序违约)", got)
	}
	// ② 403 不消耗令牌:下一笔好 Origin 不是 429(冻结时钟,令牌不可能恢复)。
	if got := raw("http://localhost:3000"); got == http.StatusTooManyRequests {
		t.Fatal("403 之后好 Origin 竟 429——403 消耗了令牌,顺序违约")
	}
	// ③ 令牌耗尽:好 Origin 也 429(裸 POST 每次过准入都消耗)。
	if got := raw("http://localhost:3000"); got != http.StatusTooManyRequests {
		t.Fatalf("令牌耗尽后应 429,实际 %d", got)
	}
	// ④ 限流→trace:429 不产生 span(准入在可观测之外),span 数停在 baseline+1。
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "calc_greet",
		Arguments: json.RawMessage(`{}`),
	}); err == nil {
		t.Fatal("令牌耗尽后的 SDK 调用应被 429 拒绝")
	}
	if got := len(sr.Ended()); got != baseline+1 {
		t.Fatalf("429 不应产生 span:span 数应仍 %d,实际 %d", baseline+1, got)
	}
}
