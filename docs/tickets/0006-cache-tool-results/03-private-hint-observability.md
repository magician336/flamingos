# 03:private 键含角色 + tools/list hint 产出 + 命中可观测收口

**Spec:** [0006 缓存层:配置驱动的工具结果缓存引擎与 tools/list 协议 hint 产出](../../../specs/0006-cache-tool-results.md)
**What to build:** 三件收口:①private 语义升级——scope=private 规则命中的工具,缓存键拼入角色(同角色命中、跨角色隔离);角色提取经构造注入闭包(与 RBAC 判定同源:未带头落配置默认角色),cache 包不 import policy(audit ctx 键先例:cmd 是唯一相遇点);②协议 hint 产出——transport 构造 ServerOptions.SetCacheable,对携带 Cacheable 的应答(tools/list 等)统一置 ttl_ms = 配置 list_ttl_ms(cache 段新字段,缺省 0 = 不宣告),cacheScope=public(清单对所有角色一致,spec 0005 既有裁决);两条中间件链零改动(C2 逐对定点单测不动);③命中可观测——telemetry 增"向当前请求 span 写缓存命中属性"导出入口,cmd 以闭包注入 cache 命中回调,e2e 断言缓存命中请求的 span 带命中属性、审计照常落一行(判定 allow、无错误)。**降级裁决:本票超时砍 private 键控(private 命中即不缓存 = 02 行为),保 public 共享缓存与 hint 产出;降级发生须回写 spec 修订表。**
**Blocked by:** 02(缓存引擎与装配已贯通,private 键控在其上扩展,hint 与可观测共用同一装配)。
**Status:** done (2026-11-08)

- [x] private 键控:同角色二次调用 hit(上游 1 次)、跨角色 miss(各上游 1 次)表驱动 + e2e;角色闭包注入,cache 不 import policy(编译器守门)
- [x] hint:配置 list_ttl_ms>0 时 tools/list 应答携带 ttlMs(SDK client 侧可读);缺省 0 应答不宣告;e2e 断言两形态;transport 仅增构造参数,链顺序定点单测零改动
- [x] 可观测:命中请求 span 带命中属性(SpanRecorder 断言);命中调用审计行照常(allow、无错误);telemetry 仅增导出入口,cache 不 import telemetry
- [x] 关闭态(cache 段缺省)全量回归;`go build ./...` + `go test ./... -race` 全绿;diff 终检(新包×2 + policy 等价重构 + config 段 + cmd 装配行 + transport 构造参数 + telemetry 导出入口)

> 定档留痕(无降级——private 键控完整落地):①RoleOf 仅 private 路径咨询(public 键不含角色、结果对所有调用者一致,不咨询可避免"配 cache 未配 policy 时 public 缓存静默失效");取不到角色查/写都跳过不计数不回调,宁 miss 不可错 hit;②命中属性名 flamingos.cache.hit(telemetry 私有前缀纪律),hit/miss 两态都写(属性存在性=该调用经过缓存层);③SetCacheable 闭包只赋值不回调 server(SDK 持锁站约束)。
