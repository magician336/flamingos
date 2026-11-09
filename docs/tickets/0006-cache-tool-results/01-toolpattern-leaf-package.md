# 01:toolpattern 叶包抽出(工具名通配语法单一出处)

**Spec:** [0006 缓存层:配置驱动的工具结果缓存引擎与 tools/list 协议 hint 产出](../../../specs/0006-cache-tool-results.md)
**What to build:** 新叶包 toolpattern 承载聚合工具名通配语法:`Parse` 编译 + `Matches` 匹配的最小接口,三形态(`*` 全量/`ns_*` 前缀/精确)自 policy 规则匹配迁出;policy 重构为消费方(等价重构,行为零变化);语法测试表随迁进叶包,policy 只留判定管线用例。这是 spec 0006 的 prefactor:02 的缓存规则编译将作为第二个消费方复用同一份语法,不重演 origin 双份实现(C1 先例:一份语法、两个消费方、一致性由编译期类型保证)。本票不触碰 config/transport/gateway/upstream/cmd。
**Blocked by:** None (can start immediately).
**Status:** done (2026-11-08)

- [x] toolpattern 叶包:Parse(非法模式显式报错)+ Pattern.Matches 最小接口;语法测试表自 policy 随迁(`*`/`ns_*`/精确/边界正反例)
- [x] policy 等价重构为消费方,判定语义(deny 优先、fail-close)与错误文案零变化;既有 policy 测试零改动通过(仅语法表对应用例按规划迁出)
- [x] diff 纪律:仅 toolpattern 新包 + policy 内部;config/transport/gateway/upstream/telemetry/audit/cmd 零改动
- [x] `go build ./...` + `go test ./... -race` 全绿;toolpattern 覆盖率 ≥80%(实现 100.0%)

> 定档留痕:非法模式在编译期 panic(config 校验保证不可达,D8;deny 语义下静默跳过=fail-open 不可接受);config.checkPattern 未在本票切换消费方(票面禁触 config),移交票 02 收口。
