// parseOTLPEndpoint 的纯函数表驱动直测(spec 0004 Testing Decisions:
// 格式边界不靠 e2e 兜底)。本文件用包内测试——解析是构造导出器的内部
// 知识,不因测试需要而公开;期望值全部为独立写定的字面量。
package telemetry

import (
	"strings"
	"testing"
)

func TestParseOTLPEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		wantHost   string // 期望解析出的 host:port(错误用例忽略)
		wantTLS    bool   // 期望是否启用 TLS
		wantErr    bool   // 期望报错
		errWantSub string // 期望错误文案包含的子串
	}{
		{
			name:     "裸 host:port:原样使用,不启用 TLS(本地明文默认)",
			endpoint: "localhost:4317",
			wantHost: "localhost:4317",
			wantTLS:  false,
		},
		{
			name:     "裸 IPv6 方括号形式:原样使用",
			endpoint: "[::1]:4317",
			wantHost: "[::1]:4317",
			wantTLS:  false,
		},
		{
			name:     "http URL:剥 scheme 与路径,不启用 TLS",
			endpoint: "http://127.0.0.1:4317",
			wantHost: "127.0.0.1:4317",
			wantTLS:  false,
		},
		{
			name:     "https URL:剥 scheme,启用 TLS",
			endpoint: "https://jaeger.example:4317",
			wantHost: "jaeger.example:4317",
			wantTLS:  true,
		},
		{
			name:     "http URL 缺端口:补默认 gRPC 端口 4317",
			endpoint: "http://jaeger.example",
			wantHost: "jaeger.example:4317",
			wantTLS:  false,
		},
		{
			name:     "https URL 缺端口:补默认 gRPC 端口 4317 且启用 TLS",
			endpoint: "https://jaeger.example",
			wantHost: "jaeger.example:4317",
			wantTLS:  true,
		},
		{
			name:     "URL 带 path 与查询:只用 host:port,其余丢弃",
			endpoint: "http://collector.internal:4317/v1/traces?extra=1",
			wantHost: "collector.internal:4317",
			wantTLS:  false,
		},
		{
			name:       "裸主机名无端口:拒绝",
			endpoint:   "jaeger",
			wantErr:    true,
			errWantSub: "不合法",
		},
		{
			name:       "非 http(s) scheme:拒绝",
			endpoint:   "ftp://jaeger:4317",
			wantErr:    true,
			errWantSub: "不合法",
		},
		{
			name:       "端口非数字:拒绝",
			endpoint:   "localhost:4317x",
			wantErr:    true,
			errWantSub: "不合法",
		},
		{
			name:       "端口为空:拒绝",
			endpoint:   "jaeger:",
			wantErr:    true,
			errWantSub: "不合法",
		},
		{
			name:       "主机为空:拒绝",
			endpoint:   ":4317",
			wantErr:    true,
			errWantSub: "不合法",
		},
		{
			name:       "空串:拒绝",
			endpoint:   "",
			wantErr:    true,
			errWantSub: "不合法",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, tls, err := parseOTLPEndpoint(tt.endpoint)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错,实际成功:host=%q tls=%v", host, tls)
				}
				if !strings.Contains(err.Error(), tt.errWantSub) {
					t.Errorf("错误文案应包含 %q,实际 %q", tt.errWantSub, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("期望成功,实际报错: %v", err)
			}
			if host != tt.wantHost {
				t.Errorf("host:port = %q,期望 %q", host, tt.wantHost)
			}
			if tls != tt.wantTLS {
				t.Errorf("TLS = %v,期望 %v", tls, tt.wantTLS)
			}
		})
	}
}
