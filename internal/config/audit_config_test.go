// config 的 policy.audit 子段测试(票 0005-02/spec 0005 配置段):子段
// 缺省(含显式 null)= 审计关闭(Audit 为 nil);段存在而 path 缺失 =
// 启动即败(中文点名);与 rbac 子段并存互不干扰。表驱动,与 policy 段
// 测试同构。
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadForTest 把 YAML 文本落进临时目录并走真实 Load 流水线(与既有 config
// 测试同款:动态夹具,不依赖仓库内静态文件)。
func loadForTest(t *testing.T, yamlText string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flamingos.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	return Load(path)
}

// auditValidUpstream 是合法最小上游清单夹具:audit 正例也必须整体可加载。
const auditValidUpstream = "upstreams:\n" +
	"  - name: calc\n" +
	"    type: http\n" +
	"    url: http://127.0.0.1:9001/mcp\n"

func TestLoadPolicyAudit(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		want       *Policy // 期望的 cfg.Policy(成功用例)
		wantErr    []string
		notWantErr []string
	}{
		{
			name: "audit 子段缺省:policy 段成立但审计关闭",
			yaml: "policy: {}\n" + auditValidUpstream,
			want: &Policy{},
		},
		{
			name: "audit 子段显式 null:与缺省同义,关闭态",
			yaml: "policy:\n  audit: null\n" + auditValidUpstream,
			want: &Policy{},
		},
		{
			name: "audit 完整正例:path 逐字段生效",
			yaml: "policy:\n" +
				"  audit:\n" +
				"    path: /var/log/flamingos-audit.jsonl\n" + auditValidUpstream,
			want: &Policy{Audit: &Audit{Path: "/var/log/flamingos-audit.jsonl"}},
		},
		{
			name: "audit 与 rbac 并存:两子段互不干扰,各自生效",
			yaml: "policy:\n" +
				"  audit:\n" +
				"    path: /tmp/audit.jsonl\n" +
				"  rbac:\n" +
				"    default_role: viewer\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        allow: [\"calc_*\"]\n" + auditValidUpstream,
			want: &Policy{
				Audit: &Audit{Path: "/tmp/audit.jsonl"},
				RBAC: &RBAC{
					RoleHeader:  DefaultRoleHeader, // 缺省头名由 applyDefaults 填充
					DefaultRole: "viewer",
					Roles:       map[string]RoleRules{"viewer": {Allow: []string{"calc_*"}}},
				},
			},
		},
		{
			name: "audit 段存在而 path 缺失:启动即败,中文点名 policy.audit",
			yaml: "policy:\n" +
				"  audit: {}\n" + auditValidUpstream,
			wantErr: []string{"policy.audit", "path", "审计"},
		},
		{
			name: "audit path 为空串:与缺失同罪,启动即败",
			yaml: "policy:\n" +
				"  audit:\n" +
				"    path: \"\"\n" + auditValidUpstream,
			wantErr: []string{"policy.audit", "path", "审计"},
		},
		{
			name: "audit 校验错误与 rbac 校验错误一次性聚合",
			yaml: "policy:\n" +
				"  audit: {}\n" +
				"  rbac: {}\n" + auditValidUpstream,
			wantErr:    []string{"policy.audit", "policy.rbac", "default_role"},
			notWantErr: []string{"审计文件无法打开"}, // 配置层不碰文件系统,可写性归 audit 包构造期
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadForTest(t, tt.yaml)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("期望报错 %v,实际成功: %+v", tt.wantErr, cfg.Policy)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("错误应含 %q,实际 %q", want, err.Error())
					}
				}
				for _, notWant := range tt.notWantErr {
					if strings.Contains(err.Error(), notWant) {
						t.Errorf("错误不应含 %q(config 层不碰文件系统),实际 %q", notWant, err.Error())
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("期望成功,实际报错: %v", err)
			}
			if !reflect.DeepEqual(cfg.Policy, tt.want) {
				t.Errorf("cfg.Policy = %+v,期望 %+v", cfg.Policy, tt.want)
			}
		})
	}
}
