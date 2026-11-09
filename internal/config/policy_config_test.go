package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// config 的 policy 段测试(票 0005-01/spec 0005 配置段):段缺省(含显式
// null)= 策略全关闭(Policy 为 nil);rbac 子段存在而 default_role/roles
// 缺失 = 启动即败(中文点名);通配语法正反例表驱动,错误与其他段一次性
// 聚合(D8)。
func TestLoadPolicy(t *testing.T) {
	// validUpstream 是合法最小上游清单夹具:policy 正例也必须整体可加载。
	const validUpstream = "upstreams:\n" +
		"  - name: calc\n" +
		"    type: http\n" +
		"    url: http://127.0.0.1:9001/mcp\n"

	tests := []struct {
		name       string
		yaml       string
		want       *Policy // 期望的 cfg.Policy(成功用例)
		wantErr    []string
		notWantErr []string
	}{
		{
			name: "段缺省:策略全关闭,Policy 为 nil 指针",
			yaml: validUpstream,
			want: nil,
		},
		{
			name: "段显式 null:与缺省同义,关闭态",
			yaml: "policy:\n" + validUpstream,
			want: nil,
		},
		{
			name: "段存在而 rbac 子段缺省:policy 段成立但 RBAC 关闭",
			yaml: "policy: {}\n" + validUpstream,
			want: &Policy{},
		},
		{
			name: "rbac 完整正例:头名/默认角色/规则表逐字段生效,通配三形态保留",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    role_header: x-agent-role\n" +
				"    default_role: viewer\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        allow: [\"calc_*\", \"fs_echo\"]\n" +
				"        deny: [\"calc_div\"]\n" +
				"      admin:\n" +
				"        allow: [\"*\"]\n" + validUpstream,
			want: &Policy{RBAC: &RBAC{
				RoleHeader:  "x-agent-role",
				DefaultRole: "viewer",
				Roles: map[string]RoleRules{
					"viewer": {Allow: []string{"calc_*", "fs_echo"}, Deny: []string{"calc_div"}},
					"admin":  {Allow: []string{"*"}},
				},
			}},
		},
		{
			name: "role_header 缺省:默认 mcp-role",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    default_role: viewer\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        allow: [\"*\"]\n" + validUpstream,
			want: &Policy{RBAC: &RBAC{
				RoleHeader:  DefaultRoleHeader,
				DefaultRole: "viewer",
				Roles:       map[string]RoleRules{"viewer": {Allow: []string{"*"}}},
			}},
		},
		{
			name: "default_role 缺失:启动即败,错误定位到 rbac 子段(段存在就要能用)",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        allow: [\"*\"]\n" + validUpstream,
			wantErr: []string{"policy.rbac", "default_role"},
		},
		{
			name:    "roles 缺失:空规则表 = 全量拒绝的误导状态,启动即败",
			yaml:    "policy:\n  rbac:\n    default_role: viewer\n" + validUpstream,
			wantErr: []string{"policy.rbac", "roles", "角色"},
		},
		{
			name:    "roles 空映射:同样拒绝",
			yaml:    "policy:\n  rbac:\n    default_role: viewer\n    roles: {}\n" + validUpstream,
			wantErr: []string{"policy.rbac", "roles", "角色"},
		},
		{
			name: "角色名为空串:无法被任何请求命中,启动即败并点名角色位置",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    default_role: viewer\n" +
				"    roles:\n" +
				"      \"\":\n" +
				"        allow: [\"*\"]\n" + validUpstream,
			wantErr: []string{"policy.rbac.roles", "角色名不能为空"},
		},
		{
			name: "通配反例:中间含星号的前缀拒绝(只支持 * / ns_* / 精确)",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    default_role: viewer\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        allow: [\"calc_*_add\"]\n" + validUpstream,
			wantErr: []string{"policy.rbac.roles.viewer.allow[0]", "calc_*_add"},
		},
		{
			name: "通配反例:星号不在尾部的模式拒绝",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    default_role: viewer\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        deny: [\"*div\"]\n" + validUpstream,
			wantErr: []string{"policy.rbac.roles.viewer.deny[0]", "*div"},
		},
		{
			name: "通配反例:连续星号拒绝",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    default_role: viewer\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        allow: [\"**\"]\n" + validUpstream,
			wantErr: []string{"policy.rbac.roles.viewer.allow[0]", "**"},
		},
		{
			name: "通配反例:空字符串模式拒绝",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    default_role: viewer\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        allow: [\"\"]\n" + validUpstream,
			wantErr: []string{"policy.rbac.roles.viewer.allow[0]", "不能为空"},
		},
		{
			name: "聚合断言:default_role 缺失与两个坏模式一次性全部列出(D8)",
			yaml: "policy:\n" +
				"  rbac:\n" +
				"    roles:\n" +
				"      viewer:\n" +
				"        allow: [\"calc_*_add\", \"*div\"]\n" + validUpstream,
			wantErr: []string{
				"共 3 处问题",
				"default_role",
				"allow[0]", "calc_*_add",
				"allow[1]", "*div",
			},
		},
		{
			name: "policy 段未知字段:键名 typo 报错,不泄漏解析库原文与内部类型名",
			yaml: "policy:\n" +
				"  rabc:\n" +
				"    default_role: viewer\n" + validUpstream,
			wantErr:    []string{"字段", "rabc", "无法识别"},
			notWantErr: []string{"not found in type", "rawPolicy"},
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
			if !reflect.DeepEqual(got.Policy, tt.want) {
				t.Errorf("Policy 不符:\n期望 %+v\n实际 %+v", tt.want, got.Policy)
			}
			// 关闭态契约的回归钉子:policy 缺省时其余字段不受影响。
			if got.Policy == nil && len(got.Upstreams) != 1 {
				t.Errorf("段缺省时上游清单应不受影响,实际 %+v", got.Upstreams)
			}
		})
	}
}
