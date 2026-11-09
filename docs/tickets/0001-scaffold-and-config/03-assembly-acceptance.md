# 03:装配收口与机械验收

**Spec:** [0001 脚手架与 config 模块](../../../specs/0001-scaffold-and-config.md)
**What to build:** spec 完成判据全部机械达成:构建与全量测试(含 -race)绿;退出码约定成型(0 成功 / 1 配置错误)——这套"退出码 + 全绿判据"作为后续所有任务与 CI 门禁的模板。
**Blocked by:** [02 九条校验 + 一次性聚合报错 + 严格解析](02-validation-and-aggregated-errors.md)
**Status:** done (2026-11-02)

- [x] `go build ./...` 成功
- [x] `go test ./... -race` 全绿
- [x] 合法夹具实测装配入口:摘要输出正确
- [x] 任一非法夹具实测:一次性列出全部错误、退出码 1
- [x] 退出码约定写定(0/1 语义),供 CI 与后续任务复用
- [x] spec 0001 勾选完成判据;修订记录追记
