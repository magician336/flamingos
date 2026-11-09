// 缓存装饰器:upstream.Client 的 Wrap 形态(项目真接缝,架构 §1/§6 D2)。
// 装饰器只做"分类→查→穿透→写"四步编排,存储与判定知识全在引擎;失败
// 结果(err≠nil / IsError=true)结构性不入缓存,拒绝与失败不会被缓存复用。
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// nameSep 是命名空间与原始 tool 名的分隔符:与 gateway 的拆分互为逆操作
// (架构 §4.2 R1/R2——命名空间不含下划线,聚合名 = 命名空间 + "_" + 原始
// 名,拆分永无歧义)。分隔符字面量与 gateway 各持一份,一致性由 config 的
// 命名空间字符集校验(R1)与 e2e 钉住。
const nameSep = "_"

// keyHashBytes 是缓存键截取的哈希字节数:SHA-256 截 128 位 = 16 字节(错误
// 命中=把 A 的结果给 B,正确性优先;128 位使随机错误命中概率可忽略,同时
// 键长固定)。
const keyHashBytes = 16

// keySep 是缓存键内字段的分隔符(NUL):工具名与角色声明均不含 NUL,public
// 键恰含一个 NUL、private 键恰含两个,两域永不重合(spec 0006 键控定档,
// 宁 miss 不可错 hit)。
const keySep = "\x00"

// Wrap 把 next 包装为带缓存的 upstream.Client:ns 是该客户端所属命名空间,
// 聚合工具名 = ns + "_" + 原始名(规则与缓存键都面向聚合名)。引擎在全部
// 命名空间间共享(调用方对同一引擎逐客户端包装),本调用只捕获各自的 ns。
func (e *Engine) Wrap(next upstream.Client, ns string) upstream.Client {
	return &cachedClient{eng: e, next: next, ns: ns}
}

// cachedClient 是缓存装饰器:规则命中时查/写缓存——public 键全局共享;
// private 键含角色(经构造注入的 RoleOf 提取,取不到角色查/写都跳过,宁
// miss 不可错 hit);规则无命中等同未配置——三者都照常下探。
type cachedClient struct {
	eng  *Engine
	next upstream.Client
	ns   string
}

// ListTools 原样透传:缓存只作用于 tools/call,清单聚合不受影响。
func (c *cachedClient) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	return c.next.ListTools(ctx)
}

// Close 原样透传:装饰器不持有底层资源,生命周期归内层客户端。
func (c *cachedClient) Close() error { return c.next.Close() }

// CallTool 的缓存逻辑:classify 无命中或 private 取不到角色一律直接下探
// (不查不写、不计数、不回调);键已定的调用以缓存键查/写——查到即返回
// 缓存结果(err 为 nil)并触发 OnHit(true);未查到触发 OnHit(false)后
// 穿透内层,仅 err==nil 且 IsError==false 的成功结果入缓存。缓存键 =
// public:聚合工具名 + "\0" + args 字节的 SHA-256 截 128 位十六进制;
// private:角色 + "\0" + 聚合工具名 + "\0" + args 摘要(角色在前,与
// public 键同域隔离——public 键恰含一个 NUL、private 恰含两个,工具名与
// 角色声明都不含 NUL,两域永不重合)。选十六进制而非原始字节:键可打印、
// 定长 32 字符,排查日志可读;args 字节级精确匹配,不做 JSON 规范化——
// 规范化是记录在案的边界,spec 0006。
func (c *cachedClient) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	full := c.ns + nameSep + name
	cls, ok := c.eng.classify(full)
	if !ok {
		return c.next.CallTool(ctx, name, args)
	}
	key, ok := c.eng.cacheKeyFor(ctx, cls, full, args)
	if !ok {
		return c.next.CallTool(ctx, name, args)
	}
	if res, ok := c.eng.get(key); ok {
		c.eng.reportHit(ctx, true)
		return res, nil
	}
	c.eng.reportHit(ctx, false)
	res, err := c.next.CallTool(ctx, name, args)
	if err == nil && res != nil && !res.IsError {
		c.eng.put(key, res, cls.ttl)
	}
	return res, err
}

// cacheKey 推导 public 缓存键:聚合工具名与 args 摘要以 "\0" 分隔。
func cacheKey(tool string, args json.RawMessage) string {
	return tool + keySep + argsHash(args)
}

// privateKey 推导 private 缓存键:角色拼在键前(角色+"\0"+聚合工具名+
// "\0"+args 摘要),同角色命中、跨角色隔离(spec 0006 Story 4)。
func privateKey(role, tool string, args json.RawMessage) string {
	return role + keySep + tool + keySep + argsHash(args)
}

// argsHash 计算 args 字节的 SHA-256 截 128 位十六进制摘要(错误命中=把 A
// 的结果给 B,正确性优先;128 位使随机错误命中概率可忽略,同时键长固定)。
func argsHash(args json.RawMessage) string {
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:keyHashBytes])
}
