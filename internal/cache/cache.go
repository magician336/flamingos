// Package cache 实现配置驱动的工具结果缓存引擎(spec 0006):存储引擎
// (mutex+map,TTL 惰性过期,容量上限超限先清一轮已过期再按最早插入 FIFO
// 逐出)与 upstream.Client 装饰器 Wrap。装饰器挂在每个 upstream 客户端上,
// 而规则与缓存键都面向聚合工具名(命名空间 + "_" + 原始名,架构 §4.2 R2)
// ——由 cmd 装配层按命名空间逐个包装、全部命名空间共享同一引擎实例;包装
// 发生在 gateway.New 之前,gateway/transport 零改动。规则语法单一出处是
// toolpattern 叶包(New 编译期经 toolpattern.Parse 定档,查缓存与写缓存用
// 同一份编译产物,同输入必同判定——决策桥同口径先例)。private 键控(票
// 03):scope=private 的键拼入角色(角色+"\0"+聚合名+args 哈希,与 public
// 键同域隔离),角色经构造注入的 RoleOf 闭包提取——本包不认识角色声明的
// ctx 键,那是 policy 的知识。依赖方向:仅 import upstream 与 toolpattern,
// 不 import gateway/transport/policy/audit/telemetry——cmd 是它与其他模块
// 知识的唯一相遇点(架构 D7)。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/magician336/flamingos/internal/toolpattern"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 缓存范围取值白名单(spec 0006 触发语义:public 全局共享——键不含角色;
// private 键含角色,同角色命中、跨角色隔离)。
const (
	ScopePublic  = "public"
	ScopePrivate = "private"
)

// Rule 是一条缓存规则的声明态(Options.Rules 的元素):Tools 内任一通配
// 模式命中即按本条 TTL/Scope 定档;规则表按声明顺序首条命中生效(优先级
// =声明序,spec 0006 Story 11)。
type Rule struct {
	Tools []string      // 聚合工具名通配模式表(三形态语法,经 toolpattern.Parse 编译)
	TTL   time.Duration // 缓存存活时长,必须 > 0
	Scope string        // ScopePublic | ScopePrivate
}

// Options 是缓存引擎的构造参数:cmd 的单一构造处(buildCache)从 config.Cache
// 翻译而来。Now 为时间来源(nil 落 time.Now,限流假时钟先例),测试注入假
// 时钟以获得确定性;RoleOf/OnHit 是两条构造注入缝(票 03,cmd 闭包翻译——
// cache 包不 import policy/telemetry,audit ctx 键先例:cmd 是唯一相遇点):
//   - RoleOf 从请求 ctx 提取角色声明,ok=false 表示取不到。语义定档:仅
//     private 路径咨询——private 键必须含角色,取不到角色(闭包未注入或
//     返回 false)一律查/写都跳过,宁 miss 不可错 hit(把 A 的 private 结果
//     给 B 是缓存层唯一能造出的错误命中,正确性优先;取不到角色说明缓存
//     判定脱离了 RBAC 同源口径,不缓存是唯一安全侧)。public 路径不咨询:
//     public 键不含角色,结果对所有调用者一致,无错误命中风险——配了
//     cache 未配 policy 的部署 public 缓存不因角色缺席静默失效。RoleOf 为
//     nil 时 private 退化为不缓存(票 02 降级形态)。
//   - OnHit 在每次真正查过缓存(规则命中且键已定)时被调,hit=true/false
//     与引擎 hits/misses 计数同口径;规则未命中与 private 取不到角色的跳过
//     路径不查缓存,不回调。nil = 不回调。
type Options struct {
	Rules      []Rule                                   // 规则表,声明顺序即优先级
	MaxEntries int                                      // 容量上限,必须 ≥ 1(config 缺省已兜底,直构时调用方负责)
	Now        func() time.Time                         // 时间来源;nil = time.Now
	RoleOf     func(ctx context.Context) (string, bool) // private 键控的角色提取(cmd 闭包翻译 policy 角色访问器)
	OnHit      func(ctx context.Context, hit bool)      // 缓存查询完成回调(cmd 闭包接到 telemetry 导出入口)
}

// Stats 是命中/未命中计数的一次快照(压测报告与故障演练取命中率数字,
// spec 0006 Story 15)。计数只覆盖真正查过缓存的调用:规则未命中或 private
// 降级跳过缓存的调用不计数。
type Stats struct {
	Hits   uint64 // 命中次数(含过期前各次命中)
	Misses uint64 // 未命中次数(键不存在与过期各计一次)
}

// compiledRule 是单条规则的编译产物:模式经 toolpattern.Parse 编译为匹配
// 器,判定路径零字符串解析。
type compiledRule struct {
	patterns []toolpattern.Pattern
	ttl      time.Duration
	scope    string
}

// classification 是规则命中的定档产出:该聚合工具名按哪条规则的 TTL 与
// Scope 缓存。
type classification struct {
	ttl   time.Duration
	scope string
}

// entry 是单条缓存条目:结果指针、过期时点与插入序号(FIFO 位次)。
type entry struct {
	res     *mcp.CallToolResult
	expires time.Time // 存活区间 [写入, 写入+TTL):恰好到期即过期
	seq     uint64    // 插入序号,单调递增;同键覆写保留原值(FIFO 位次不刷新)
}

// Engine 是缓存存储引擎:单实例全网关共享,并发安全(互斥锁保护条目表,
// 计数为原子操作)。判定知识(规则表编译产物)也在此,供装饰器 classify;
// RoleOf/OnHit 是构造注入的两条缝(nil 已按 Options 语义定档,见上)。
type Engine struct {
	mu         sync.Mutex
	entries    map[string]*entry // 键 = cacheKey/privateKey 装饰器产出
	maxEntries int               // 容量上限,put 超限触发"先清过期再 FIFO 逐出"
	nextSeq    uint64            // 下一个插入序号
	now        func() time.Time  // 时间来源(nil 已在 New 落 time.Now)
	rules      []compiledRule    // 编译后的规则表,声明顺序即优先级
	roleOf     func(ctx context.Context) (string, bool)
	onHit      func(ctx context.Context, hit bool)
	hits       atomic.Uint64
	misses     atomic.Uint64
}

// New 构造缓存引擎:规则表逐条编译(语法经 toolpattern.Parse,ttl/scope/
// tools 同站校验),MaxEntries 必须 ≥ 1。config 已在启动流水线校验全部规则
// (设计 D8),此处的校验是构造参数的防御性兜底——直构方(测试、未来调用
// 方)拿到显式错误而非带病引擎。
func New(opts Options) (*Engine, error) {
	if opts.MaxEntries < 1 {
		return nil, fmt.Errorf("cache: max_entries 必须 ≥ 1(容量上限是逐出的触发线),当前 %d", opts.MaxEntries)
	}
	rules := make([]compiledRule, 0, len(opts.Rules))
	for i, r := range opts.Rules {
		compiled, err := compileRule(r)
		if err != nil {
			return nil, fmt.Errorf("cache: 第 %d 条规则不合法: %w", i, err)
		}
		rules = append(rules, compiled)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Engine{
		entries:    make(map[string]*entry),
		maxEntries: opts.MaxEntries,
		now:        now,
		rules:      rules,
		roleOf:     opts.RoleOf,
		onHit:      opts.OnHit,
	}, nil
}

// compileRule 编译并校验单条规则:ttl>0、scope 白名单、tools 非空(永不
// 命中的死规则是误导状态)、模式语法经 toolpattern.Parse。
func compileRule(r Rule) (compiledRule, error) {
	if r.TTL <= 0 {
		return compiledRule{}, fmt.Errorf("ttl 必须为正(当前 %s:没有正 TTL 的规则永不生效,是误导状态)", r.TTL)
	}
	switch r.Scope {
	case ScopePublic, ScopePrivate:
	default:
		return compiledRule{}, fmt.Errorf("scope %q 不受支持(只接受 %s 或 %s)", r.Scope, ScopePublic, ScopePrivate)
	}
	if len(r.Tools) == 0 {
		return compiledRule{}, errors.New("缺少 tools(规则至少声明一个聚合工具名通配模式)")
	}
	patterns := make([]toolpattern.Pattern, 0, len(r.Tools))
	for _, raw := range r.Tools {
		p, err := toolpattern.Parse(raw)
		if err != nil {
			return compiledRule{}, fmt.Errorf("模式编译失败: %w", err)
		}
		patterns = append(patterns, p)
	}
	return compiledRule{patterns: patterns, ttl: r.TTL, scope: r.Scope}, nil
}

// classify 按声明顺序对编译后的规则表做首条命中判定:规则内任一模式命中
// 即按该规则定档;无命中返回 false(调用方按"不缓存"处理)。查缓存与写
// 缓存共用本判定(同一份编译产物),同输入必同判定。
func (e *Engine) classify(tool string) (classification, bool) {
	for _, r := range e.rules {
		for _, p := range r.patterns {
			if p.Matches(tool) {
				return classification{ttl: r.ttl, scope: r.scope}, true
			}
		}
	}
	return classification{}, false
}

// get 查缓存:命中返回结果并计 hits;键不存在或已过期计 misses 并清除
// 过期条目(TTL 惰性过期——读时判定,不起后台清扫,spec 0006)。
func (e *Engine) get(key string) (*mcp.CallToolResult, bool) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	en, ok := e.entries[key]
	if !ok {
		e.misses.Add(1)
		return nil, false
	}
	if !now.Before(en.expires) {
		delete(e.entries, key)
		e.misses.Add(1)
		return nil, false
	}
	e.hits.Add(1)
	return en.res, true
}

// put 写缓存:同键覆写保留原 FIFO 位次(插入序号不刷新);新键分配序号,
// 写入后超容量上限时先清一轮已过期,仍超限按最早插入逐出(FIFO——LRU 留
// 作 Limitations,当前规模够)。
func (e *Engine) put(key string, res *mcp.CallToolResult, ttl time.Duration) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if en, ok := e.entries[key]; ok {
		en.res = res
		en.expires = now.Add(ttl)
		return
	}
	e.entries[key] = &entry{res: res, expires: now.Add(ttl), seq: e.nextSeq}
	e.nextSeq++
	if len(e.entries) > e.maxEntries {
		e.evictLocked(now)
	}
}

// evictLocked 收缩条目表到容量上限内(调用方持锁):先清一轮已过期条目,
// 仍超限再按最小插入序号逐出。逐出需扫全表找最早插入(O(n)),仅在超限
// 时发生,当前规模(缺省 1024)可接受。
func (e *Engine) evictLocked(now time.Time) {
	for key, en := range e.entries {
		if !now.Before(en.expires) {
			delete(e.entries, key)
		}
	}
	for len(e.entries) > e.maxEntries {
		var oldestKey string
		var oldestSeq uint64
		found := false
		for key, en := range e.entries {
			if !found || en.seq < oldestSeq {
				found, oldestKey, oldestSeq = true, key, en.seq
			}
		}
		delete(e.entries, oldestKey)
	}
}

// Stats 返回命中/未命中计数快照:原子读取,不加条目锁。
func (e *Engine) Stats() Stats {
	return Stats{Hits: e.hits.Load(), Misses: e.misses.Load()}
}

// cacheKeyFor 按分类定档推导缓存键:public 键不含角色(与角色无关);private
// 键含角色,经构造注入的 RoleOf 提取——闭包未注入或返回 false 时返回 false,
// 调用方按"查/写都跳过"处理(Options.RoleOf 的定档注释:宁 miss 不可错 hit)。
func (e *Engine) cacheKeyFor(ctx context.Context, cls classification, tool string, args json.RawMessage) (string, bool) {
	if cls.scope != ScopePrivate {
		return cacheKey(tool, args), true
	}
	if e.roleOf == nil {
		return "", false
	}
	role, ok := e.roleOf(ctx)
	if !ok {
		return "", false
	}
	return privateKey(role, tool, args), true
}

// reportHit 触发命中回调(nil 安全):hit 与引擎 hits/misses 计数同口径,
// 仅在真正查过缓存的调用上触发;回调错误无——回调是观测通道,不反噬调用。
func (e *Engine) reportHit(ctx context.Context, hit bool) {
	if e.onHit != nil {
		e.onHit(ctx, hit)
	}
}
