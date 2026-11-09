// cmd 的可观测装配测试(票 0004-03):TracerProvider 单一构造处的三形态
// 翻译(buildTracerProvider)、停机导出的有界与容错(shutdownTracer)、
// 以及"OTLP 后端不可达时网关照常服务"的装配级 e2e——经真实装配路径
// (翻译 → 拨测 → gateway → transport)接一个死端点 OTLP provider,
// Agent 调用必须照常成功(spec 0004 Story 17/23)。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/config"
	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"
)

// fakeProvider 记录 Shutdown 调用的 provider 替身:Tracer 能力借 noop
// provider 嵌入,只替身 Shutdown 的可观察行为。
type fakeProvider struct {
	trace.TracerProvider
	mu        sync.Mutex
	shutdowns int
	err       error // Shutdown 的应答(注入失败形态)
}

func (f *fakeProvider) Shutdown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns++
	return f.err
}

func (f *fakeProvider) shutdownCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdowns
}

func TestBuildTracerProvider(t *testing.T) {
	t.Run("段缺省:返回 nil 关闭态(nil 接口值,非类型化 nil)", func(t *testing.T) {
		tp, err := buildTracerProvider(nil)
		if err != nil {
			t.Fatalf("段缺省不应报错,实际: %v", err)
		}
		if tp != nil { // 接口值判 nil:telemetry.New 靠它识别关闭态哨兵
			t.Fatalf("段缺省应返回 nil provider,实际 %T", tp)
		}
	})

	t.Run("段存在:产出非 nil provider(构造不因后端不可达失败)", func(t *testing.T) {
		tp, err := buildTracerProvider(&config.Telemetry{
			OTLPEndpoint: "127.0.0.1:1", ServiceName: "flamingos-test",
		})
		if err != nil {
			t.Fatalf("段存在时应构造成功(连接惰性建立),实际: %v", err)
		}
		if tp == nil {
			t.Fatal("段存在应返回非 nil provider")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tp.Shutdown(ctx) // 测试收尾,不泄漏后台导出
	})

	t.Run("非法 endpoint:构造即败(配置校验漏网的防御边界)", func(t *testing.T) {
		_, err := buildTracerProvider(&config.Telemetry{
			OTLPEndpoint: "not an endpoint", ServiceName: "flamingos-test",
		})
		if err == nil {
			t.Fatal("非法 endpoint 应构造失败,实际成功")
		}
	})
}

func TestShutdownTracer(t *testing.T) {
	t.Run("nil provider:关闭态无 provider 可停,不 panic", func(t *testing.T) {
		shutdownTracer(nil) // 契约:关闭态装配处持 nil,停机路径直接跳过
	})

	t.Run("正常路径:恰好调用一次 Shutdown", func(t *testing.T) {
		fp := &fakeProvider{TracerProvider: trace.NewNoopTracerProvider()}
		shutdownTracer(fp)
		if got := fp.shutdownCalls(); got != 1 {
			t.Errorf("Shutdown 应被调用 1 次,实际 %d 次", got)
		}
	})

	t.Run("停机导出失败:错误只记日志,不外溢为停机失败", func(t *testing.T) {
		fp := &fakeProvider{
			TracerProvider: trace.NewNoopTracerProvider(),
			err:            context.DeadlineExceeded,
		}
		shutdownTracer(fp) // 不 panic、不上抛即为契约(可观测故障不反噬退出路径)
		if got := fp.shutdownCalls(); got != 1 {
			t.Errorf("失败的 Shutdown 也应已执行,实际 %d 次", got)
		}
	})
}

// gatewayURLWithTelemetry 是 gatewayURL 的可观测接线变体:同一真实装配
// 路径,另把 telemetry 传给上游拨测(出站)与 transport(入站)。
func gatewayURLWithTelemetry(t *testing.T, upstreams []config.Upstream, tel *telemetry.Telemetry) string {
	t.Helper()
	params, err := translateUpstreams(upstreams)
	if err != nil {
		t.Fatalf("翻译上游配置失败: %v", err)
	}
	clients, err := dialUpstreams(context.Background(), params, upstream.WithTelemetry(tel))
	if err != nil {
		t.Fatalf("拨测上游失败: %v", err)
	}
	t.Cleanup(func() { closeClients(clients) })

	handler, err := transport.New(context.Background(), gateway.New(clients), transport.WithTelemetry(tel))
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// TestGatewayServesWithUnreachableOTLP 验证数据面隔离(Story 17 的装配级
// 闭环):OTLP 指向死端点、span 照产、导出必败,Agent 经网关的调用仍
// 必须照常成功且往返内容不失真——可观测后端故障只配得上日志。
func TestGatewayServesWithUnreachableOTLP(t *testing.T) {
	tp, err := buildTracerProvider(&config.Telemetry{
		OTLPEndpoint: "127.0.0.1:1", ServiceName: "flamingos-test",
	})
	if err != nil {
		t.Fatalf("构造死端点 provider 失败: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tp.Shutdown(ctx)
	})

	fakeUp := newFakeUpstream(t)
	endpoint := gatewayURLWithTelemetry(t, []config.Upstream{
		{Name: "calc", Type: config.TypeHTTP, URL: fakeUp, Timeout: dialTimeout},
	}, telemetry.New(tp))
	agent := newAgent(t, endpoint)

	started := time.Now()
	res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "calc_echo",
		Arguments: json.RawMessage(`{"msg":"后端死了也照常"}`),
	})
	if err != nil {
		t.Fatalf("OTLP 不可达时调用应照常成功,实际报错: %v", err)
	}
	if got := textOf(res); got != `{"msg":"后端死了也照常"}` {
		t.Errorf("往返内容应不失真,实际 %q", got)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("死端点 OTLP 不应拖慢数据面,调用耗时 %s", elapsed)
	}
}

// configAcceptsEndpoint 经 config.Load 全路径判定 endpoint 是否被接受:
// 写出仅 telemetry.otlp_endpoint 取值不同的最小合法配置,Load 成功即
// 接受——config 侧的形式校验(otlpEndpointOK)是包内知识,此处不经
// 导出面单独触发,而是走部署者会走的同一条路。
func configAcceptsEndpoint(t *testing.T, endpoint string) bool {
	t.Helper()
	content := fmt.Sprintf(
		"upstreams:\n  - name: calc\n    type: http\n    url: http://127.0.0.1:1\ntelemetry:\n  otlp_endpoint: %q\n  service_name: flamingos-test\n",
		endpoint)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写出临时配置失败: %v", err)
	}
	_, err := config.Load(path)
	return err == nil
}

// telemetryAcceptsEndpoint 经 NewOTLPTracerProvider 构造路径判定 endpoint
// 是否被接受:构造成功即接受(telemetry 侧的形式认定收在 parseOTLPEndpoint);
// provider 随即带预算 Shutdown,不泄漏后台导出 goroutine。
func telemetryAcceptsEndpoint(t *testing.T, endpoint string) bool {
	t.Helper()
	tp, err := telemetry.NewOTLPTracerProvider(context.Background(), telemetry.OTLPConfig{
		Endpoint: endpoint, ServiceName: "flamingos-test",
	})
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tp.Shutdown(ctx)
	return true
}

// TestOTLPEndpointFormsAgree 钉死同一知识的两份实现:config 的
// otlpEndpointOK(校验)与 telemetry 的 parseOTLPEndpoint(规范化)
// 对"endpoint 受支持形式"的认定必须逐输入一致。两份实现按设计 D7
// 不合并——校验属配置知识、规范化属导出器构造知识,包内各自自持;
// 此前同步只靠注释,本表把等价性变成断言:任一侧改动形式认定而另一侧
// 未跟,此测试立即失败。cmd 是唯一同时装配两者的位置,故测试在此。
// 表值同时锚定期望:wantAccepted 是两份实现共同的当前认定(接受形:
// 裸 host:port、http(s):// URL;拒绝形:无端口、非 http(s) scheme、
// 端口非数字/越界/为空、主机为空、空串)。
func TestOTLPEndpointFormsAgree(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		wantAccepted bool
	}{
		{
			name:         "裸 host:port:接受",
			endpoint:     "localhost:4317",
			wantAccepted: true,
		},
		{
			name:         "裸 IPv6 方括号形式:接受",
			endpoint:     "[::1]:4317",
			wantAccepted: true,
		},
		{
			name:         "http URL:接受(telemetry 侧剥 scheme)",
			endpoint:     "http://127.0.0.1:4317",
			wantAccepted: true,
		},
		{
			name:         "https URL:接受(telemetry 侧启用 TLS)",
			endpoint:     "https://jaeger.example:4317",
			wantAccepted: true,
		},
		{
			name:         "URL 缺端口:接受(telemetry 侧补默认 4317)",
			endpoint:     "http://jaeger.example",
			wantAccepted: true,
		},
		{
			name:         "URL 带 path 与查询:接受(telemetry 侧只用 host:port)",
			endpoint:     "http://collector.internal:4317/v1/traces?extra=1",
			wantAccepted: true,
		},
		{
			name:     "裸主机无端口:拒绝",
			endpoint: "jaeger",
		},
		{
			name:     "非 http(s) scheme:拒绝",
			endpoint: "ftp://jaeger:4317",
		},
		{
			name:     "端口非数字:拒绝",
			endpoint: "localhost:4317x",
		},
		{
			name:     "端口为空:拒绝",
			endpoint: "jaeger:",
		},
		{
			name:     "端口超 65535:拒绝",
			endpoint: "localhost:99999",
		},
		{
			name:     "主机为空:拒绝",
			endpoint: ":4317",
		},
		{
			name:     "空串:拒绝(config 侧报缺少 otlp_endpoint)",
			endpoint: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgOK := configAcceptsEndpoint(t, tt.endpoint)
			telOK := telemetryAcceptsEndpoint(t, tt.endpoint)
			if cfgOK != tt.wantAccepted {
				t.Errorf("config 侧判定 = 接受 %v,期望 %v(endpoint %q)", cfgOK, tt.wantAccepted, tt.endpoint)
			}
			if telOK != tt.wantAccepted {
				t.Errorf("telemetry 侧判定 = 接受 %v,期望 %v(endpoint %q)", telOK, tt.wantAccepted, tt.endpoint)
			}
			if cfgOK != telOK {
				t.Errorf("两侧对受支持形式的认定漂移:config 接受=%v,telemetry 接受=%v(endpoint %q)", cfgOK, telOK, tt.endpoint)
			}
		})
	}
}
