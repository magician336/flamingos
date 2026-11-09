// OTLP 导出形态(票 0004-03):配置驱动的 TracerProvider,构造知识
// (导出器、endpoint 解析、资源、采样)全部收在 telemetry 包内——装配
// 层只做 config→OTLPConfig 的翻译并拿成品 provider(spec 0004 Story
// 23/24:三形态单一构造处,telemetry 是被装配的叶子能力)。
package telemetry

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// defaultOTLPPort 是 OTLP gRPC 的标准端口(Collector/Jaeger 的约定值)。
// URL 形式缺端口时补此值,裸形式必须自带端口。
const defaultOTLPPort = "4317"

// schemeHTTP/schemeHTTPS 是 endpoint URL 形式允许的 scheme(裸 host:port
// 之外的另一种受支持形式),且 https 是唯一启用 TLS 的 scheme。与
// config 包的同名常量各持一份(设计 D7:校验属配置知识、规范化属构造
// 知识,不跨包共享);两处对"受支持形式"认定的一致性由 cmd 装配侧的
// 跨包等价测试钉死。
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// OTLPConfig 是 OTLP 导出形态的构造参数,由装配层从 telemetry 配置段
// 翻译而来。ServiceName 须非空——默认值 flamingos 由配置层负责填充,
// 此处不重复默认值知识。
type OTLPConfig struct {
	Endpoint    string // OTLP gRPC 接收端:裸 host:port 或 http(s):// URL
	ServiceName string // Jaeger 服务名(资源 service.name)
}

// NewOTLPTracerProvider 构造以 OTLP gRPC 批量导出的 TracerProvider:
//   - 连接惰性建立:后端不可达不影响构造、不阻塞 span 收尾,导出失败
//     只经 OTel 全局错误处理器记日志——可观测后端故障不反噬数据面
//     (spec 0004 Story 17);
//   - 采样固定 always-on(配置段定稿:不做采样策略);
//   - 资源带 service.name,Jaeger 服务列表据此识别(Story 15);
//   - 在途 span 的导出经 Shutdown 完成——BatchSpanProcessor 停机前
//     先清空队列(Story 18),超时预算由调用方经 ctx 施加。
func NewOTLPTracerProvider(ctx context.Context, cfg OTLPConfig) (Provider, error) {
	hostPort, useTLS, err := parseOTLPEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(hostPort)}
	if !useTLS {
		// 裸 host:port 与 http:// 视为明文(本地 Jaeger/collector 的常态);
		// TLS 是显式选择:https:// 才启用。
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("构造 OTLP gRPC 导出器失败: %w", err)
	}
	// 资源块:以显式 service.name 覆盖 SDK 默认的 unknown_service。
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceNameKey.String(cfg.ServiceName),
	))
	if err != nil {
		return nil, fmt.Errorf("构造 OTLP 资源失败: %w", err)
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	), nil
}

// parseOTLPEndpoint 把配置 endpoint 的两种形式解析为 otlptracegrpc 参数:
// http(s):// URL 取 host:port(缺端口补 defaultOTLPPort)并由 scheme 决定
// 是否 TLS;裸 host:port 原样使用且不启用 TLS。config 层的 endpoint 校验
// (otlpEndpointOK)与本函数对"受支持形式"的认定保持一致——两处各自
// 持有,是"校验属配置知识、规范化属导出器构造知识"的边界结果。
func parseOTLPEndpoint(endpoint string) (hostPort string, useTLS bool, err error) {
	if u, perr := url.Parse(endpoint); perr == nil &&
		(u.Scheme == schemeHTTP || u.Scheme == schemeHTTPS) && u.Host != "" {
		hostPort = u.Host
		if u.Port() == "" {
			hostPort = net.JoinHostPort(u.Hostname(), defaultOTLPPort)
		}
		return hostPort, u.Scheme == schemeHTTPS, nil
	}
	host, port, serr := net.SplitHostPort(endpoint)
	if serr == nil && host != "" && port != "" {
		if n, aerr := strconv.Atoi(port); aerr == nil && n >= 0 && n <= 65535 {
			return endpoint, false, nil
		}
	}
	return "", false, fmt.Errorf("OTLP endpoint %q 不合法(应为 host:port 或 http(s)://host:port)", endpoint)
}
