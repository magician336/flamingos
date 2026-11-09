// config 包测试:唯一公开接缝是 config.Load,全部用例表驱动。
// 每个用例在 t.TempDir() 中动态生成 YAML 夹具,不依赖仓库内静态文件。
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name       string   // 用例名(中文,描述用户可见行为)
		yaml       string   // 夹具内容
		write      bool     // 是否写出夹具文件(false 即构造缺文件反例)
		filename   string   // 夹具文件名(供错误信息断言)
		want       Config   // 期望返回结构(成功用例)
		wantErr    []string // 期望错误信息包含的子串(失败用例;非空即期望报错)
		notWantErr []string // 期望错误信息不包含的子串(净化断言:防内部细节泄漏)
	}{
		{
			name: "合法最小配置:仅一个 http 上游,listen 与 timeout 走默认值",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n",
			write:    true,
			filename: "flamingos.yaml",
			want: Config{
				Listen: ":8080",
				Upstreams: []Upstream{
					{
						Name:    "calc",
						Type:    "http",
						URL:     "http://127.0.0.1:9001/mcp",
						Timeout: 30 * time.Second,
					},
				},
			},
		},
		{
			name: "双上游混合显式值:listen/timeout 显式生效,args 数组与 duration 字符串正确解析",
			// timeout: 10s 为字符串字面量,本例证实 yaml.v3 原生将其解析为 time.Duration
			yaml: "listen: \":9090\"\n" +
				"upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n" +
				"    timeout: 10s\n" +
				"  - name: fs\n" +
				"    type: stdio\n" +
				"    command: npx\n" +
				"    args: [\"-y\", \"@modelcontextprotocol/server-filesystem\", \".\"]\n",
			write:    true,
			filename: "flamingos.yaml",
			want: Config{
				Listen: ":9090",
				Upstreams: []Upstream{
					{
						Name:    "calc",
						Type:    "http",
						URL:     "http://127.0.0.1:9001/mcp",
						Timeout: 10 * time.Second,
					},
					{
						Name:    "fs",
						Type:    "stdio",
						Command: "npx",
						Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
						Timeout: 30 * time.Second,
					},
				},
			},
		},
		{
			name:     "配置文件缺失:错误信息包含文件路径并说明不存在",
			write:    false,
			filename: "flamingos.yaml",
			wantErr:  []string{"flamingos.yaml", "不存在"},
		},
		{
			name: "YAML 语法错误:错误信息包含文件路径与可读的中文说明",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"   type: http\n",
			write:    true,
			filename: "broken.yaml",
			wantErr:  []string{"broken.yaml", "不是合法 YAML"},
		},
		{
			name: "未知字段:键名 typo 报错而非静默忽略,错误含文件路径与字段名",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n" +
				"    unknownkey: true\n",
			write:      true,
			filename:   "flamingos.yaml",
			wantErr:    []string{"flamingos.yaml", "unknownkey"},
			notWantErr: []string{"not found in type", "rawUpstream", "rawConfig"},
		},
		{
			name: "字段类型错误:报错为中文表述,不泄漏解析库英文原文与内部类型名",
			yaml: "listen: [1, 2]\n" +
				"upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n",
			write:      true,
			filename:   "flamingos.yaml",
			wantErr:    []string{"flamingos.yaml", "类型不正确"},
			notWantErr: []string{"cannot unmarshal", "!!seq", "rawUpstream", "rawConfig"},
		},
		{
			name: "name 为空:报错并指出条目位置",
			yaml: "upstreams:\n" +
				"  - name: \"\"\n" +
				"    type: stdio\n" +
				"    command: npx\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "name 不能为空"},
		},
		{
			name: "name 含下划线:启动被拒,说明会造成路由拆分歧义",
			yaml: "upstreams:\n" +
				"  - name: github_main\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "github_main", "下划线"},
		},
		{
			name: "name 含空格:超出 R1 字符集,启动被拒并说明字符集规则",
			yaml: "upstreams:\n" +
				"  - name: \"github main\"\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "github main", "字母、数字、连字符"},
		},
		{
			name: "name 含点号:超出 R1 字符集,启动被拒并说明字符集规则",
			yaml: "upstreams:\n" +
				"  - name: github.com\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "github.com", "字母、数字、连字符"},
		},
		{
			name: "name 重复:第二个同名条目被拒,说明路由表会被覆盖",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n" +
				"  - name: calc\n" +
				"    type: stdio\n" +
				"    command: npx\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[1]", "calc", "重复"},
		},
		{
			name: "type 拼写错误:只接受 http 或 stdio",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: htpp\n" +
				"    url: http://127.0.0.1:9001/mcp\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "htpp", "只接受"},
		},
		{
			name: "http 上游缺 url:报错并指出条目位置",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "url"},
		},
		{
			name: "url scheme 非法:ftp 不是 http(s),启动被拒并指出条目位置",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: ftp://example.com/x\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "ftp://example.com/x"},
		},
		{
			name: "url 不可解析:无 scheme 的裸文本被拒并指出条目位置",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: \"not a url\"\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "not a url"},
		},
		{
			name: "stdio 上游缺 command:报错为 spec 例句原文(文件: upstreams[1]: type=stdio 缺少 command)",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n" +
				"  - name: fs\n" +
				"    type: stdio\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"flamingos.yaml: upstreams[1]: type=stdio 缺少 command"},
		},
		{
			name:     "未声明任何上游:拒绝空网关的误导状态",
			yaml:     "listen: \":8080\"\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams", "至少声明一个上游"},
		},
		{
			name:     "空文件:交由校验层报至少一个上游,而非暴露解析库 EOF",
			yaml:     "",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"至少声明一个上游"},
		},
		{
			name: "聚合断言:单夹具同时踩两个错,两条错误一次性全部列出",
			yaml: "upstreams:\n" +
				"  - name: github_main\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n" +
				"  - name: fs\n" +
				"    type: stdio\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr: []string{
				"共 2 处问题",
				"upstreams[0]", "下划线",
				"upstreams[1]", "缺少 command",
			},
		},
		{
			name: "timeout 非法字面量:进入聚合报错并指出条目位置与原始值",
			yaml: "upstreams:\n" +
				"  - name: calc\n" +
				"    type: http\n" +
				"    url: http://127.0.0.1:9001/mcp\n" +
				"    timeout: abc\n",
			write:    true,
			filename: "flamingos.yaml",
			wantErr:  []string{"upstreams[0]", "timeout", "abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.filename)
			if tt.write {
				if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
					t.Fatalf("写夹具失败: %v", err)
				}
			}

			got, err := Load(path)

			// 反例:期望报错,错误信息逐条包含 wantErr 且不包含 notWantErr 中的子串
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("期望报错,实际成功返回:%+v", got)
				}
				for _, substr := range tt.wantErr {
					if !strings.Contains(err.Error(), substr) {
						t.Errorf("错误信息缺少 %q:实际 %q", substr, err.Error())
					}
				}
				for _, substr := range tt.notWantErr {
					if strings.Contains(err.Error(), substr) {
						t.Errorf("错误信息泄漏 %q:实际 %q", substr, err.Error())
					}
				}
				return
			}

			// 正例:期望成功且结构与期望完全一致
			if err != nil {
				t.Fatalf("期望成功,实际报错: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("结构不符:\n期望 %+v\n实际 %+v", tt.want, got)
			}
		})
	}
}

// TestLoadTelemetry 钉死 telemetry 段契约(票 0004-03/spec 0004 配置段定稿):
// 段缺省(含显式 null)= tracing 关闭(Telemetry 为 nil);段存在而
// otlp_endpoint 缺失/非法 = 启动即败且与其他问题一次性汇总(D8);
// service_name 缺省 flamingos;不为未建功能预埋其他字段(未知键报错)。
func TestLoadTelemetry(t *testing.T) {
	// validUpstream 是合法最小上游清单夹具:telemetry 正例也必须整体可加载。
	const validUpstream = "upstreams:\n" +
		"  - name: calc\n" +
		"    type: http\n" +
		"    url: http://127.0.0.1:9001/mcp\n"

	tests := []struct {
		name       string
		yaml       string
		want       *Telemetry // 期望的 cfg.Telemetry(成功用例)
		wantErr    []string   // 期望错误信息包含的子串(失败用例;非空即期望报错)
		notWantErr []string
	}{
		{
			name: "段缺省:tracing 关闭,Telemetry 为 nil 指针",
			yaml: validUpstream,
			want: nil,
		},
		{
			name: "段显式 null:与缺省同义,关闭态",
			yaml: "telemetry:\n" + validUpstream,
			want: nil,
		},
		{
			name: "裸 host:port 形式:段合法,endpoint 原样保留(规范化属装配层)",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: localhost:4317\n" +
				"  service_name: my-flamingos\n" + validUpstream,
			want: &Telemetry{OTLPEndpoint: "localhost:4317", ServiceName: "my-flamingos"},
		},
		{
			name: "URL 形式:http(s)://host:port 同样合法,endpoint 原样保留",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: http://127.0.0.1:4317\n" + validUpstream,
			want: &Telemetry{OTLPEndpoint: "http://127.0.0.1:4317", ServiceName: DefaultServiceName},
		},
		{
			name: "service_name 缺省:默认 flamingos",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: localhost:4317\n" + validUpstream,
			want: &Telemetry{OTLPEndpoint: "localhost:4317", ServiceName: DefaultServiceName},
		},
		{
			name: "段存在而 endpoint 缺失:启动即败,错误定位到 telemetry 段",
			yaml: "telemetry:\n" +
				"  service_name: my-flamingos\n" + validUpstream,
			wantErr: []string{"telemetry", "otlp_endpoint"},
		},
		{
			name:    "段为空映射:等价于缺 endpoint,同样拒绝",
			yaml:    "telemetry: {}\n" + validUpstream,
			wantErr: []string{"telemetry", "otlp_endpoint"},
		},
		{
			name: "endpoint 端口非数字:非法形式拒绝",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: localhost:4317x\n" + validUpstream,
			wantErr: []string{"telemetry", "localhost:4317x"},
		},
		{
			name: "endpoint 为非 http(s) scheme 的 URL:非法形式拒绝",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: ftp://jaeger:4317\n" + validUpstream,
			wantErr: []string{"telemetry", "ftp://jaeger:4317"},
		},
		{
			name: "endpoint 为裸主机名无端口:非法形式拒绝",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: jaeger\n" + validUpstream,
			wantErr: []string{"telemetry", "jaeger"},
		},
		{
			name: "聚合断言:telemetry 错误与上游错误一次性全部列出(D8)",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: ftp://jaeger:4317\n" +
				"upstreams:\n" +
				"  - name: fs\n" +
				"    type: stdio\n",
			wantErr: []string{"共 2 处问题", "telemetry", "upstreams[0]", "缺少 command"},
		},
		{
			name: "未知字段:不为未建功能预埋,键名 typo 报错",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: localhost:4317\n" +
				"  sampling: always-on\n" + validUpstream,
			wantErr:    []string{"字段", "sampling", "无法识别"},
			notWantErr: []string{"not found in type", "rawTelemetry"},
		},
		{
			name: "字段类型错误:中文表述,不泄漏解析库原文与内部类型名",
			yaml: "telemetry:\n" +
				"  otlp_endpoint: [1, 2]\n" + validUpstream,
			wantErr:    []string{"类型不正确"},
			notWantErr: []string{"cannot unmarshal", "rawTelemetry"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "flamingos.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatalf("写夹具失败: %v", err)
			}

			got, err := Load(path)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("期望报错,实际成功返回:%+v", got)
				}
				for _, substr := range tt.wantErr {
					if !strings.Contains(err.Error(), substr) {
						t.Errorf("错误信息缺少 %q:实际 %q", substr, err.Error())
					}
				}
				for _, substr := range tt.notWantErr {
					if strings.Contains(err.Error(), substr) {
						t.Errorf("错误信息泄漏 %q:实际 %q", substr, err.Error())
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("期望成功,实际报错: %v", err)
			}
			if !reflect.DeepEqual(got.Telemetry, tt.want) {
				t.Errorf("Telemetry 不符:\n期望 %+v\n实际 %+v", tt.want, got.Telemetry)
			}
			// 段缺省契约的回归钉子:关闭态不得影响其余字段。
			if got.Telemetry == nil && len(got.Upstreams) != 1 {
				t.Errorf("段缺省时上游清单应不受影响,实际 %+v", got.Upstreams)
			}
		})
	}
}
