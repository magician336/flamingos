# 02:九条校验 + 一次性聚合报错 + 严格解析

**Spec:** [0001 脚手架与 config 模块](../../../specs/0001-scaffold-and-config.md)
**What to build:** 部署者的任何配置错误在启动时一次性全部列出(非首错即停),信息含具体位置(如 upstreams[1]);九条校验规则(name 非空/无下划线/不重复、type ∈ {http,stdio}、http→合法 url、stdio→command 非空、upstreams ≥ 1、timeout 可解析)加未知字段拒绝全部生效。
**Blocked by:** [01 最小装配路径跑通](01-minimal-assembly-path.md)
**Status:** done (2026-11-02)

- [x] 九条校验规则全量实现,错误文案含条目位置与规则原因
- [x] 聚合机制:单夹具同时踩两个错,输出里两条都在(演示即验收)
- [x] 严格解析:未知字段报错,键名 typo 不静默
- [x] duration 解析:非法 timeout 字面量进聚合报错
- [x] 表驱动测试补齐 spec 测试清单:九条反例 + 聚合断言 + 未知字段断言 + name 含下划线反例
- [x] 校验只存在于 config 模块;装配入口与下游零防御(D8 纪律)
