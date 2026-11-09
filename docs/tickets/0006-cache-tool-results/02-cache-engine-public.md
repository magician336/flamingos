# 02:缓存引擎与 public 贯通(装饰器 + cache: 段 + cmd 装配 + e2e 钉死)

**Spec:** [0006 缓存层:配置驱动的工具结果缓存引擎与 tools/list 协议 hint 产出](../../../specs/0006-cache-tool-results.md)
**What to build:** cache 新包两件:①存储引擎——mutex+map,TTL 惰性过期(读时判定并清除),容量 max_entries(缺省 1024)超限先清一轮已过期再按最早插入 FIFO 逐出,时钟经构造注入,命中/未命中原子计数器可快照;②upstream.Client 装饰器 Wrap——键 = 聚合工具名 + `\0` + args 字节 SHA-256 截 128 位,仅存上游 err==nil 且 IsError==false 的结果,规则 classify 编译期经 toolpattern 定档(首条命中生效,查缓存与写缓存用同一份编译产物)。config 增顶级 `cache:` 段(rules:tools 通配/ttl_ms>0/scope ∈ {public,private};段缺省含显式 null = 关闭零行为;校验启动即败中文点名,与既有问题一次性聚合);cmd 装配层完成规则编译、时钟注入与装饰器包装(在 gateway.New 之前包 clients)。e2e 钉死:同参两次调用上游恰一次、异参各自 miss、假时钟拨过 TTL 后重调 miss、IsError 与传输错误不存、deny 调用上游零调用(装饰器在 RBAC 判定内层的结构判据)。**本票 scope=private 规则命中暂按"不缓存"处理(=spec 降级形态,03 升级为键含角色)。**
**Blocked by:** 01(toolpattern 叶包——classify 规则编译的语法出处)。
**Status:** done (2026-11-08)

- [x] 引擎表驱动(假时钟):写入/命中/异参 miss/过期 miss/容量超限先清过期再 FIFO 逐出/计数快照数字正确
- [x] 装饰器表驱动(计数假 upstream.Client):首调 miss 上游 1 次,同参二调 hit 上游仍 1 次;err≠nil 与 IsError=true 不写入;classify 首条命中生效、无命中不缓存
- [x] config:`cache:` 段四站齐备(rules 语法经 toolpattern、ttl_ms≤0、scope 非法、max_entries≤0 各启动即败中文点名);段缺省 = 关闭,既有测试零改动通过
- [x] cmd 装配:装饰器包装 + 时钟注入;gateway/upstream/telemetry/audit/policy 零改动(除 01 已迁移部分)
- [x] e2e:同参两次调用上游恰一次;deny 调用上游零调用;关闭态全量回归;`go build ./...` + `go test ./... -race` 全绿,cache 覆盖率 ≥80%(实现 100.0%)

> 定档留痕:①max_entries 经 yaml.Node 保留原始字面量(显式 0/负数与字段缺省可区分,timeout 先例),缺省含显式 null 兜底 1024;②rules 条目缺 tools(死规则)启动即败(空 allowed/空 roles 同款纪律);③cache.New 返回 error 上抛为启动错误(buildAudit 同形,exit 2);④键=聚合名+NUL+SHA-256 截 128 位 hex;⑤同键覆写只更新内容与 TTL、保留原 FIFO 位次。票 01 移交项已收口:config.checkPattern 切换为 toolpattern.Parse 消费方,上报文案零变化。
