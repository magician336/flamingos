// cmd 的 policy 装配翻译测试(票 0005-01):config.Policy → policy.Policy
// 只发生在 cmd(架构 D7),经 policy 公开接线(RoleHeader/DefaultRole/
// Decide)断言翻译产物;段缺省 = 关闭态 nil,装配处不装任何挂点。
package main

import (
	"testing"

	"github.com/magician336/flamingos/internal/config"
)

func TestBuildPolicy(t *testing.T) {
	t.Run("段缺省:返回 nil,策略全关闭态", func(t *testing.T) {
		if got := buildPolicy(nil); got != nil {
			t.Fatalf("段缺省应返回 nil 哨兵,实际 %+v", got)
		}
	})

	t.Run("rbac 子段缺省:同样返回 nil(各子段独立开关)", func(t *testing.T) {
		if got := buildPolicy(&config.Policy{}); got != nil {
			t.Fatalf("rbac 子段缺省应返回 nil 哨兵,实际 %+v", got)
		}
	})

	t.Run("完整 rbac:头名/默认角色/规则表逐项翻译并按其判定", func(t *testing.T) {
		got := buildPolicy(&config.Policy{
			RBAC: &config.RBAC{
				RoleHeader:  "x-agent-role",
				DefaultRole: "viewer",
				Roles: map[string]config.RoleRules{
					"viewer": {Allow: []string{"calc_*"}, Deny: []string{"calc_div"}},
				},
			},
		})
		if got == nil {
			t.Fatal("rbac 段存在时应构造判定器,实际 nil")
		}
		if got.RoleHeader() != "x-agent-role" {
			t.Errorf("RoleHeader = %q,期望 x-agent-role", got.RoleHeader())
		}
		if got.DefaultRole() != "viewer" {
			t.Errorf("DefaultRole = %q,期望 viewer", got.DefaultRole())
		}
		// 判定行为按翻译后的规则表生效:deny 优先与 fail-close 各钉一例。
		if d := got.Decide("viewer", "calc_add"); !d.Allowed {
			t.Errorf("viewer 调 calc_add 应放行,实际 %+v", d)
		}
		if d := got.Decide("viewer", "calc_div"); d.Allowed {
			t.Errorf("viewer 调 calc_div 应被 deny 压过,实际 %+v", d)
		}
		if d := got.Decide("ghost", "calc_add"); d.Allowed {
			t.Errorf("未知角色应 fail-close,实际 %+v", d)
		}
	})
}
