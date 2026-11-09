// 规则匹配纯函数:RBAC0 判定(spec 0005/research/06 §6)。通配模式的语法
// 单一出处是 toolpattern 叶包(spec 0006 票 01,originrule C1 先例:一份
// 语法、多个消费方),本包只消费编译产物做 deny 优先判定。判定不触网络、
// 不触时钟——表驱动穷举即可全覆盖(spec 0005 接缝①)。
package policy

import (
	"fmt"

	"github.com/magician336/flamingos/internal/toolpattern"
)

// rules 是编译后的角色规则表:模式在构造期经 toolpattern.Parse 编译为匹配
// 器,判定路径零字符串解析。
type rules struct {
	byRole map[string]roleRules
}

// roleRules 是单个角色的编译产物:deny/allow 各自成表,判定按 deny 优先。
type roleRules struct {
	deny  []toolpattern.Pattern
	allow []toolpattern.Pattern
}

// compileRules 编译角色规则表;模式语法由 config 校验保证(设计 D8),
// 非法模式到不了编译期。编译对 deny 的 fail-close 语义不可让步:静默丢弃
// 编译失败的 deny 模式等于放行,故不可达的错误分支显式快速失败。
func compileRules(src map[string]RoleRules) rules {
	byRole := make(map[string]roleRules, len(src))
	for role, rr := range src {
		byRole[role] = roleRules{
			deny:  compilePatterns(rr.Deny),
			allow: compilePatterns(rr.Allow),
		}
	}
	return rules{byRole: byRole}
}

// compilePatterns 逐条编译模式表:全量 `*`、尾星前缀、精确(三形态的编译
// 产物均出自 toolpattern.Parse)。
func compilePatterns(raws []string) []toolpattern.Pattern {
	ps := make([]toolpattern.Pattern, 0, len(raws))
	for _, raw := range raws {
		p, err := toolpattern.Parse(raw)
		if err != nil {
			panic(fmt.Sprintf("policy: 模式 %q 未通过 config 校验即到达编译期: %v(设计 D8 不变量被破坏,deny 语义不允许静默跳过)", raw, err))
		}
		ps = append(ps, p)
	}
	return ps
}

// Decision 是一次 RBAC 判定的结果:Role/Tool 回显判定的输入,Allowed 为
// 放行与否(审计与追踪以 ctx 传递的同一决策为口径,spec 0005 Story 18)。
type Decision struct {
	Role    string // 判定所用角色
	Tool    string // 聚合工具名
	Allowed bool   // true=放行,false=策略拒绝
}

// Decide 是规则匹配纯函数:(角色, 聚合工具名) → 决策。判定语义(fail-close):
// ①同角色下 deny 命中即拒;②未被任何 allow 放行即拒;③未知角色没有任何
// allow,自然落入 ②——新增上游工具不会因规则缺席而静默开放(Story 5)。
func (p *Policy) Decide(role, tool string) Decision {
	rr := p.rules.byRole[role]
	for _, d := range rr.deny {
		if d.Matches(tool) {
			return Decision{Role: role, Tool: tool, Allowed: false}
		}
	}
	for _, a := range rr.allow {
		if a.Matches(tool) {
			return Decision{Role: role, Tool: tool, Allowed: true}
		}
	}
	return Decision{Role: role, Tool: tool, Allowed: false}
}
