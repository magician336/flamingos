// config 的 origin/rate_limit 子段测试(票 0005-03/spec 0005):两子段随
// policy 段落地,子段缺省(含显式 null)= 各自关闭;段存在就要能用——
// origin 条目格式与 rate_limit 参数非法均启动即败、中文点名位置(spec
// 0005 Story 12),与其他 policy 问题一次性聚合(D8)。唯一公开接缝仍是
// config.Load,全部表驱动,夹具在 t.TempDir 动态生成。origin 条目语法
// 本体的测试面在 originrule 叶包(2026-11-05 架构评审 C1),本文件只测
// 管线:Parse 失败→中文点名位置→聚合,各留代表例。
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadFixture 写夹具并 Load,返回错误文本(空串 = 成功);origin/rate_limit
// 用例不关心成功时的完整 Config 结构,只关心子段值与错误话语。
func loadFixture(t *testing.T, yamlText string) (Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flamingos.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}
	cfg, err := Load(path)
	if err == nil {
		return cfg, ""
	}
	return cfg, err.Error()
}

// validUpstream 是合法最小上游清单夹具:policy 两子段正例也必须整体可加载。
const validUpstream = "upstreams:\n" +
	"  - name: calc\n" +
	"    type: http\n" +
	"    url: http://127.0.0.1:9001/mcp\n"

func TestLoadOrigin(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    *Origin // 期望的 cfg.Policy.Origin(成功用例)
		wantErr []string
	}{
		{
			name: "子段缺省:Origin 校验关闭,Origin 为 nil",
			yaml: "policy: {}\n" + validUpstream,
			want: nil,
		},
		{
			name: "子段显式 null:与缺省同义,关闭态",
			yaml: "policy:\n" +
				"  origin:\n" + validUpstream,
			want: nil,
		},
		{
			name: "合法列表:通配端口、精确端口、无端口条目逐条保留",
			yaml: "policy:\n" +
				"  origin:\n" +
				"    allowed:\n" +
				"      - http://localhost:*\n" +
				"      - https://app.example.com:8443\n" +
				"      - https://console.example.com\n" + validUpstream,
			want: &Origin{Allowed: []string{
				"http://localhost:*",
				"https://app.example.com:8443",
				"https://console.example.com",
			}},
		},
		{
			name: "IPv6 主机与显式端口:合法形式保留",
			yaml: "policy:\n" +
				"  origin:\n" +
				"    allowed:\n" +
				"      - http://[::1]:3000\n" + validUpstream,
			want: &Origin{Allowed: []string{"http://[::1]:3000"}},
		},
		{
			name:    "段存在而 allowed 缺失:启动即败,空列表等于全量 403 的误导状态",
			yaml:    "policy:\n  origin: {}\n" + validUpstream,
			wantErr: []string{"policy.origin", "allowed"},
		},
		{
			name: "allowed 空列表:同样拒绝",
			yaml: "policy:\n" +
				"  origin:\n" +
				"    allowed: []\n" + validUpstream,
			wantErr: []string{"policy.origin", "allowed"},
		},
		{
			name: "条目 scheme 非 http(s):ftp 被拒并点名条目",
			yaml: "policy:\n" +
				"  origin:\n" +
				"    allowed:\n" +
				"      - ftp://example.com\n" + validUpstream,
			wantErr: []string{"policy.origin.allowed[0]", "ftp://example.com", "不受支持"},
		},
		{
			name: "条目端口非数字且非通配:被拒并点名条目",
			yaml: "policy:\n" +
				"  origin:\n" +
				"    allowed:\n" +
				"      - http://localhost:8O80\n" + validUpstream,
			wantErr: []string{"policy.origin.allowed[0]", "8O80", "不合法"},
		},
		{
			name: "聚合断言:两条非法条目一次性全部列出",
			yaml: "policy:\n" +
				"  origin:\n" +
				"    allowed:\n" +
				"      - ftp://a.example.com\n" +
				"      - http://b.example.com:*\n" +
				"      - javascript:alert(1)\n" + validUpstream,
			wantErr: []string{
				"共 2 处问题",
				"policy.origin.allowed[0]", "ftp://a.example.com",
				"policy.origin.allowed[2]", "javascript:alert(1)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errMsg := loadFixture(t, tt.yaml)
			if len(tt.wantErr) > 0 {
				if errMsg == "" {
					t.Fatalf("期望报错,实际成功返回:%+v", cfg)
				}
				for _, substr := range tt.wantErr {
					if !strings.Contains(errMsg, substr) {
						t.Errorf("错误信息缺少 %q:实际 %q", substr, errMsg)
					}
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("期望成功,实际报错: %s", errMsg)
			}
			if !reflect.DeepEqual(cfg.Policy.Origin, tt.want) {
				t.Errorf("cfg.Policy.Origin:\n期望 %+v\n实际 %+v", tt.want, cfg.Policy.Origin)
			}
		})
	}
}

func TestLoadRateLimit(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    *RateLimit // 期望的 cfg.Policy.RateLimit(成功用例)
		wantErr []string
	}{
		{
			name: "子段缺省:限流关闭,RateLimit 为 nil",
			yaml: "policy: {}\n" + validUpstream,
			want: nil,
		},
		{
			name: "子段显式 null:与缺省同义,关闭态",
			yaml: "policy:\n" +
				"  rate_limit:\n" + validUpstream,
			want: nil,
		},
		{
			name: "合法参数:整数 rps 与 burst 原样保留",
			yaml: "policy:\n" +
				"  rate_limit:\n" +
				"    rps: 100\n" +
				"    burst: 200\n" + validUpstream,
			want: &RateLimit{RPS: 100, Burst: 200},
		},
		{
			name: "小数 rps:合法(低速上游按小数速率补充)",
			yaml: "policy:\n" +
				"  rate_limit:\n" +
				"    rps: 0.5\n" +
				"    burst: 1\n" + validUpstream,
			want: &RateLimit{RPS: 0.5, Burst: 1},
		},
		{
			name:    "段存在而 rps 缺失:启动即败(0 不是合法速率)",
			yaml:    "policy:\n  rate_limit:\n    burst: 10\n" + validUpstream,
			wantErr: []string{"policy.rate_limit", "rps"},
		},
		{
			name: "rps 为负:启动即败",
			yaml: "policy:\n" +
				"  rate_limit:\n" +
				"    rps: -1\n" +
				"    burst: 10\n" + validUpstream,
			wantErr: []string{"policy.rate_limit", "rps"},
		},
		{
			name:    "段存在而 burst 缺失:启动即败(0 不是合法容量)",
			yaml:    "policy:\n  rate_limit:\n    rps: 10\n" + validUpstream,
			wantErr: []string{"policy.rate_limit", "burst"},
		},
		{
			name: "burst 为负:启动即败",
			yaml: "policy:\n" +
				"  rate_limit:\n" +
				"    rps: 10\n" +
				"    burst: -3\n" + validUpstream,
			wantErr: []string{"policy.rate_limit", "burst"},
		},
		{
			name: "聚合断言:rps 与 burst 同时非法,两条错误一次性列出",
			yaml: "policy:\n" +
				"  rate_limit:\n" +
				"    rps: 0\n" +
				"    burst: 0\n" + validUpstream,
			wantErr: []string{"共 2 处问题", "policy.rate_limit", "rps", "burst"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errMsg := loadFixture(t, tt.yaml)
			if len(tt.wantErr) > 0 {
				if errMsg == "" {
					t.Fatalf("期望报错,实际成功返回:%+v", cfg)
				}
				for _, substr := range tt.wantErr {
					if !strings.Contains(errMsg, substr) {
						t.Errorf("错误信息缺少 %q:实际 %q", substr, errMsg)
					}
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("期望成功,实际报错: %s", errMsg)
			}
			if !reflect.DeepEqual(cfg.Policy.RateLimit, tt.want) {
				t.Errorf("cfg.Policy.RateLimit:\n期望 %+v\n实际 %+v", tt.want, cfg.Policy.RateLimit)
			}
		})
	}
}
