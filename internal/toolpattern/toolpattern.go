// Package toolpattern 是聚合工具名通配模式的语法单一出处:解析(Parse
// 编译)与匹配(Matches)的纯函数叶包。三形态:`*` 全量、`ns_*` 前缀、
// 精确名。消费方各取所需——policy 编译 RBAC 规则表做放行判定,cache 编
// 缓存规则做命中判定(spec 0006 票 02)——一份语法、多个消费方,一致性
// 由编译期类型保证(2026-11-05 架构评审 C1 先例:origin 语法双份实现付
// 过学费,通配语法不再犯第二次)。语法判定与错误文案冻结自 config.check
// Pattern 现校验(config 未来切换消费时补位置前缀后原样上报,文案零变化)。
// 本包不 import 任何业务包,依赖方向不破(设计 D1/D7:语法知识归叶包,
// 校验话语权仍归 config)。
package toolpattern

import (
	"fmt"
	"strings"
)

// Pattern 是单个编译后的通配模式:构造期编译为匹配器,判定路径零字符串
// 解析。三形态——wildcard(`*` 全量)、isPrefix(`ns_*` 前缀,prefix 为
// 星号前的字面量)、exact(精确完整工具名)。
type Pattern struct {
	prefix   string // kind=前缀 时的前缀字面量
	exact    string // kind=精确 时的完整工具名
	wildcard bool   // 全量通配 `*`
	isPrefix bool   // 前缀通配 `ns_*`
}

// Parse 把单个通配模式解析为编译后的模式。语法:三形态——`*` 全量、
// `ns_*` 前缀(星号只在尾部且仅一个)、精确名(不含星号);其余形状拒绝。
// 错误信息面向配置编写者(中文、点名原模式):config 侧补位置前缀后原样
// 上报,消费方用于防御性处理。
func Parse(pattern string) (Pattern, error) {
	if pattern == "" {
		return Pattern{}, fmt.Errorf("模式不能为空(受支持三形态:全量 *、前缀 ns_*、精确工具名)")
	}
	if stars := strings.Count(pattern, "*"); stars > 1 || (stars == 1 && !strings.HasSuffix(pattern, "*")) {
		return Pattern{}, fmt.Errorf("模式 %q 不合法(受支持三形态:全量 *、前缀 ns_*、精确工具名;星号只允许单独出现或出现在尾部)", pattern)
	}
	switch {
	case pattern == "*":
		return Pattern{wildcard: true}, nil
	case strings.HasSuffix(pattern, "*"):
		return Pattern{prefix: strings.TrimSuffix(pattern, "*"), isPrefix: true}, nil
	default:
		return Pattern{exact: pattern}, nil
	}
}

// Matches 判断聚合工具名是否命中该模式:全量恒真、前缀按字面量 HasPrefix、
// 精确按字面比较。语义冻结自 policy 规则匹配的现行为(spec 0006 票 01
// 等价迁出,判定结果零变化)。
func (p Pattern) Matches(tool string) bool {
	switch {
	case p.wildcard:
		return true
	case p.isPrefix:
		return strings.HasPrefix(tool, p.prefix)
	default:
		return tool == p.exact
	}
}
