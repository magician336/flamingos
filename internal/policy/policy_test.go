// policy 包测试:接缝①匹配纯函数表驱动穷举(spec 0005 Testing Decisions)。
// 只从公开接缝断言:New 构造 → Decide 纯函数(角色, 聚合工具名 → 决策)。
// 通配三形态(* / 前缀 / 精确)的语法与匹配用例已随语法单一出处迁入
// toolpattern 叶包(spec 0006 票 01,originrule C1 先例:消费方只测各自
// 管线),本表保留判定管线语义:deny 优先、不误伤、fail-close、未知角色。
package policy

import (
	"reflect"
	"testing"
)

// TestDecide 把 RBAC0 判定管线语义钉死为票面契约:同一角色下 deny 命中
// 即拒、未被 allow 放行即拒、未知角色拒(fail-close);语法夹具(全量/
// 前缀/精确)只是管线的驱动输入,其匹配语义由 toolpattern 的语法表负责。
func TestDecide(t *testing.T) {
	p := New(Config{
		DefaultRole: "viewer",
		Roles: map[string]RoleRules{
			// 全量通配 + deny 优先:admin 放行一切,但点名工具仍被拒。
			"admin": {
				Allow: []string{"*"},
				Deny:  []string{"calc_div", "fs_rm*"},
			},
			// 前缀 + 精确混合:viewer 只放行 calc_ 前缀与点名工具 fs_echo。
			"viewer": {
				Allow: []string{"calc_*", "fs_echo"},
			},
			// 仅 deny:除点名拒绝外全部 fail-close 拒绝。
			"auditor": {
				Deny: []string{"fs_*"},
			},
		},
	})

	tests := []struct {
		name string
		role string
		tool string
		want Decision
	}{
		{
			name: "deny 优先:allow * 命中也被点名的 deny 精确规则压过",
			role: "admin",
			tool: "calc_div",
			want: Decision{Role: "admin", Tool: "calc_div", Allowed: false},
		},
		{
			name: "deny 前缀:deny fs_rm* 压过 allow *,前缀命中即拒",
			role: "admin",
			tool: "fs_rm_rf",
			want: Decision{Role: "admin", Tool: "fs_rm_rf", Allowed: false},
		},
		{
			name: "deny 前缀不误伤同前缀之外的工具:fs_cat 仍放行",
			role: "admin",
			tool: "fs_cat",
			want: Decision{Role: "admin", Tool: "fs_cat", Allowed: true},
		},
		{
			name: "fail-close:viewer 的 allow 未覆盖 fs_cat,默认拒绝",
			role: "viewer",
			tool: "fs_cat",
			want: Decision{Role: "viewer", Tool: "fs_cat", Allowed: false},
		},
		{
			name: "仅 deny 角色:deny 命中即拒",
			role: "auditor",
			tool: "fs_cat",
			want: Decision{Role: "auditor", Tool: "fs_cat", Allowed: false},
		},
		{
			name: "仅 deny 角色:未被 deny 覆盖也因无 allow 而 fail-close",
			role: "auditor",
			tool: "calc_add",
			want: Decision{Role: "auditor", Tool: "calc_add", Allowed: false},
		},
		{
			name: "未知角色:未声明任何规则的角色一律拒绝(fail-close)",
			role: "ghost",
			tool: "calc_add",
			want: Decision{Role: "ghost", Tool: "calc_add", Allowed: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.Decide(tt.role, tt.tool)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Decide(%q, %q) = %+v,期望 %+v", tt.role, tt.tool, got, tt.want)
			}
		})
	}
}
