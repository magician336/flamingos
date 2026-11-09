# 02:gateway 聚合与路由(假 Client 驱动)

**Spec:** [0002 HTTP 上游连接、聚合与转发](../../../specs/0002-http-upstream-aggregation-forwarding.md)
**What to build:** 向 gateway 注入任意多个上游客户端(测试用假实现,不碰网络),一次 ListTools 并发扇出全部上游、合并为带"命名空间_"前缀的聚合清单(多上游同名原始 tool 不冲突);一次 CallTool 按第一个下划线拆出命名空间、查表选出上游、以原始 tool 名与原始参数调用;名字无下划线或前缀无匹配时得到明确中文报错;聚合后的完整名自校验,非法 tool 跳过并警示而不拖死整体;错误语义保持透传纪律。
**Blocked by:** [01 HTTP 上游 adapter 跑通](01-http-upstream-adapter.md)
**Status:** done (2026-11-03)

- [x] New(clients map) 依赖注入;HandleToolsList 并发扇出全部上游 → 合并 → 加前缀(架构 §4.2)
- [x] 按第一个下划线拆分命名空间(原始名自身含下划线也不歧义,与 config R1 字符集咬合)→ 以原始名调用
- [x] 名字无下划线 / 前缀无匹配 → 明确中文错误
- [x] 聚合名自校验(非空、≤128、字符集);非法 tool 跳过并输出警示(SDK 对非法名只打日志不拒绝,网关自守)
- [x] 错误契约:TransportError → 对 Agent 表达为点名上游"不可用";上游 IsError 原样透传;Close 后 ErrClosed
- [x] 表驱动假 Client 单元测试覆盖以上全部分支;`go test ./... -race` 全绿
