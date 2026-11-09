// OTLP 导出形态的接缝级测试(票 0004-03):不起 Jaeger,进程内起真实
// OTLP gRPC 接收器(collector 协议的 TraceService),从导出线路上黑盒
// 断言——span 真的经 OTLP/gRPC 落到接收端、service.name 资源正确、
// span 属性在线路上可见、Shutdown 前完成在途 span 的导出(flush)、
// 后端不可达不反噬数据面(构造即返、span 收尾不阻塞、停机有界)。
// 这是 Jaeger 人工验收的 CI 等价物(spec 0004 Testing Decisions:Jaeger
// 不进 CI)。
package telemetry_test

import (
	"context"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
)

// shutdownTimeout 是停机导出的测试预算:远大于进程内往返,又足以兜住
// 偶发调度延迟。
const shutdownTimeout = 10 * time.Second

// otlpSink 是进程内 OTLP 接收器:实现 collector trace 协议的 Export,
// 把收到的资源属性与 span 原样收存,作为导出线路的观察面。
type otlpSink struct {
	coltracepb.UnimplementedTraceServiceServer
	mu          sync.Mutex
	serviceName []string          // 每个资源块的 service.name 属性值(按收包顺序)
	spans       []*tracepb.Span   // 收到的全部 span
	attrs       map[string]string // 首个 span 的属性快照(键→字符串值)
}

// Export 实现 TraceService.Export:collector 应答成功并把数据收进观察面。
func (s *otlpSink) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rs := range req.ResourceSpans {
		if rs.Resource != nil {
			for _, kv := range rs.Resource.Attributes {
				if kv.GetKey() != "service.name" {
					continue
				}
				if v, ok := kv.Value.GetValue().(*commonpb.AnyValue_StringValue); ok {
					s.serviceName = append(s.serviceName, v.StringValue)
				}
			}
		}
		for _, ss := range rs.ScopeSpans {
			for _, span := range ss.Spans {
				s.spans = append(s.spans, span)
				if s.attrs == nil {
					s.attrs = map[string]string{}
					for _, kv := range span.Attributes {
						if v, ok := kv.Value.GetValue().(*commonpb.AnyValue_StringValue); ok {
							s.attrs[kv.GetKey()] = v.StringValue
						}
					}
				}
			}
		}
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// serviceNames 返回已收资源块 service.name 的副本。
func (s *otlpSink) serviceNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.serviceName...)
}

// allSpans 返回已收 span 的副本。
func (s *otlpSink) allSpans() []*tracepb.Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*tracepb.Span(nil), s.spans...)
}

// firstSpanAttrs 返回首个收到 span 的字符串属性副本。
func (s *otlpSink) firstSpanAttrs() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.attrs))
	for k, v := range s.attrs {
		out[k] = v
	}
	return out
}

// newOTLPSink 起真实 gRPC 服务承载观察面,监听 127.0.0.1 随机端口,
// 生命周期挂靠 t.Cleanup,返回给 provider 用的地址(host:port 形式)。
func newOTLPSink(t *testing.T) (*otlpSink, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	sink := &otlpSink{}
	srv := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(srv, sink)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return sink, ln.Addr().String()
}

// recordSpan 在 provider 上开/收一个带属性的 span,返回其 traceID 的
// 十六进制串(独立真值,接收端断言据此比对)。
func recordSpan(tp telemetry.Provider, name string) string {
	_, span := tp.Tracer("test").Start(context.Background(), name)
	span.SetAttributes(attribute.String("flamingos.method", "tools/call"))
	traceID := span.SpanContext().TraceID()
	span.End()
	return traceID.String()
}

// TestOTLPExportAndFlush 验证导出与 flush 语义:构造 → 开/收 span →
// Shutdown,接收端必须已收到同一 traceID 的 span,且 span 属性在线路上
// 可见——"停机前在途 span 不丢"(spec 0004 Story 18)的机械等价物。
func TestOTLPExportAndFlush(t *testing.T) {
	sink, addr := newOTLPSink(t)

	tp, err := telemetry.NewOTLPTracerProvider(context.Background(), telemetry.OTLPConfig{
		Endpoint:    addr,
		ServiceName: "flamingos-test",
	})
	if err != nil {
		t.Fatalf("构造 OTLP provider 失败: %v", err)
	}
	traceID := recordSpan(tp, "冒烟 span")

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := tp.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown 应完成停机导出,实际报错: %v", err)
	}

	spans := sink.allSpans()
	if len(spans) == 0 {
		t.Fatal("Shutdown 后接收端应收到 span(flush 不丢在途 span),实际零个")
	}
	found := false
	for _, s := range spans {
		if hex.EncodeToString(s.TraceId) == traceID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("接收端应收到 traceID %s 的 span,实际收到 %d 个 span 无一匹配", traceID, len(spans))
	}
	if got := sink.firstSpanAttrs()["flamingos.method"]; got != "tools/call" {
		t.Errorf("span 属性应随导出落到线路(flamingos.method=tools/call),实际 %q", got)
	}
}

// TestOTLPServiceNameResource 验证资源块:接收端看到的 service.name
// 与构造参数一致(Jaeger 服务列表据此识别,spec 0004 Story 15)。
func TestOTLPServiceNameResource(t *testing.T) {
	sink, addr := newOTLPSink(t)

	tp, err := telemetry.NewOTLPTracerProvider(context.Background(), telemetry.OTLPConfig{
		Endpoint:    addr,
		ServiceName: "flamingos-test",
	})
	if err != nil {
		t.Fatalf("构造 OTLP provider 失败: %v", err)
	}
	recordSpan(tp, "s")

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := tp.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown 失败: %v", err)
	}

	if len(sink.allSpans()) == 0 {
		t.Fatal("接收端应收到 span,实际零个")
	}
	found := false
	for _, n := range sink.serviceNames() {
		if n == "flamingos-test" {
			found = true
		}
	}
	if !found {
		t.Errorf("接收端 service.name 应含 flamingos-test,实际 %v", sink.serviceNames())
	}
}

// TestOTLPUnreachableBackend 验证"后端不可达不反噬数据面"(spec 0004
// Story 17):构造指向无人监听端口的 provider 必须立即成功(连接惰性
// 建立),span 收尾不得阻塞,Shutdown 在调用方预算内返回——可观测后端
// 故障的影响止步于导出失败日志。预算量级是停机路径的装配知识(沿用
// shutdownTimeout 量级),本用例以 2s 小预算验证"有界放弃"语义本身。
func TestOTLPUnreachableBackend(t *testing.T) {
	tp, err := telemetry.NewOTLPTracerProvider(context.Background(), telemetry.OTLPConfig{
		Endpoint:    "127.0.0.1:1", // 1 号端口:保留端口,必然无人监听
		ServiceName: "flamingos-test",
	})
	if err != nil {
		t.Fatalf("后端不可达时构造也应成功(连接惰性建立),实际报错: %v", err)
	}

	started := time.Now()
	recordSpan(tp, "s")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("后端不可达时 span 开/收不应阻塞,实际耗时 %s", elapsed)
	}

	const budget = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	shutdownStart := time.Now()
	if err := tp.Shutdown(ctx); err != nil {
		// OTLP gRPC 默认重试(InitialInterval 5s、MaxElapsedTime 1 分钟)会让
		// 导出在死端点上持续重试;停机预算的意义就是有界放弃——错误可接受,
		// 断言只关心"不挂死"。
		t.Logf("不可达后端的 Shutdown 返回错误(有界放弃,属可接受行为): %v", err)
	}
	// 预算 = budget,另给 1s 调度余量:Shutdown 必须返回而非挂死。
	if elapsed := time.Since(shutdownStart); elapsed > budget+time.Second {
		t.Errorf("Shutdown 应在预算 %s(+1s 余量)内返回,实际 %s", budget, elapsed)
	}
}

// TestOTLPInvalidEndpointRejected 构造参数的边界:非法 endpoint 在构造
// 瞬间被拒(而非等到运行期导出全灭才发现),错误文案含语义说明。
func TestOTLPInvalidEndpointRejected(t *testing.T) {
	for _, endpoint := range []string{"", "jaeger", "ftp://x:4317"} {
		tp, err := telemetry.NewOTLPTracerProvider(context.Background(), telemetry.OTLPConfig{
			Endpoint: endpoint, ServiceName: "flamingos-test",
		})
		if err == nil {
			_ = tp.Shutdown(context.Background()) // 不让 provider 泄漏
			t.Errorf("endpoint %q 应被构造拒绝,实际成功", endpoint)
			continue
		}
		if !strings.Contains(err.Error(), "不合法") {
			t.Errorf("endpoint %q 的错误文案应含\"不合法\",实际 %q", endpoint, err.Error())
		}
	}
}
