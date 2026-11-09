// Package policy 提供 RBAC 判定(spec 0005):规则匹配纯函数(角色,
// 聚合工具名 → 决策)与 mcp 层 Receiving 判定中间件。规则来源经构造注入
// (cmd 从 config 翻译,架构 D7);本包不 import transport/gateway/upstream
// (spec 0005 依赖纪律),角色与决策经 ctx 传递供 mcp 层各中间件消费。
//
// 不引 cerbos(需独立部署)/CEL(新增一门表达式语言):RBAC0 三种模式
// (`*`/`ns_*`/精确)+ deny 优先即覆盖本网关的工具放行需求,引擎的部署与
// 学习成本大于收益——取舍与证据见 research/06 §6、spec 0005 修订表
// (Story 19,W4 README 里程碑再外化)。
//
// 通配模式语法单一出处是 toolpattern 叶包(spec 0006 票 01,originrule
// C1 先例):本包是其判定消费方,cache 是第二个消费方(缓存规则命中判定)
// ——一份语法、多个消费方,一致性由编译期类型保证。
package policy

// RoleRules 是单个角色的放行/拒绝通配规则表(research/06 §6 RBAC0)。
// 模式三形态:`*` 全量、`ns_*` 前缀、精确名(语法与匹配语义单一出处见
// toolpattern);语法合法性由 config 校验(启动即败,设计 D8),本包编译
// 时不再防御。
type RoleRules struct {
	Allow []string // 放行模式表;空表 = 该角色不放行任何工具
	Deny  []string // 拒绝模式表;命中即拒,优先于 allow
}

// Config 是 RBAC 判定器的构造参数:config.Policy 在 cmd 装配层的投影。
type Config struct {
	RoleHeader  string               // 角色声明 HTTP 头名(transport 提取用)
	DefaultRole string               // 未带头时的默认角色
	Roles       map[string]RoleRules // 角色 → 规则表
}

// Policy 是装配层持有的 RBAC 判定器:不可变规则表 + 三形态产件入口
// (与 telemetry.Telemetry 同构)。nil *Policy 是关闭态哨兵——产件方法
// 对 nil 接收者返回 nil,装配处据"非 nil 才安装"跳过挂点,关闭态零开销。
type Policy struct {
	roleHeader  string
	defaultRole string
	rules       rules
}

// New 以经校验的配置构造判定器:编译通配模式为匹配器并持有;配置语法
// 已由 config.Load 保证(spec 0005 校验进启动流水线),此处零防御代码
// (设计 D8)。
func New(cfg Config) *Policy {
	return &Policy{
		roleHeader:  cfg.RoleHeader,
		defaultRole: cfg.DefaultRole,
		rules:       compileRules(cfg.Roles),
	}
}

// RoleHeader 返回角色声明的 HTTP 头名:transport 的角色提取中间件据此
// 读取声明(头名知识单一出处 = config)。
func (p *Policy) RoleHeader() string { return p.roleHeader }

// DefaultRole 返回未带头时的默认角色:transport 提取层与判定层的兜底
// 口径共用此值。
func (p *Policy) DefaultRole() string { return p.defaultRole }
