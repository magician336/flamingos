# 01:最小装配路径跑通(tracer bullet)

**Spec:** [0001 脚手架与 config 模块](../../../specs/0001-scaffold-and-config.md)
**What to build:** 部署者写一个只含 1 个 http 上游的最小 YAML,运行装配入口,看到装配摘要(监听地址、上游清单);配置文件缺失或语法错误时,得到带文件路径的友好报错并以非零码退出。
**Blocked by:** None (can start immediately).
**Status:** done (2026-11-02)

- [x] 完整类型形状落地(D6 单列表 + type 判别;字段集与 spec 类型形状一致,暂只做解析不做校验)
- [x] Load 支持默认值:listen 缺省 :8080、timeout 缺省 30s(常量化)
- [x] 装配入口:参数解析(-config,缺省 flamingos.yaml)→ Load → 输出装配摘要 → 正常退出;不启动任何服务
- [x] 错误路径:文件不存在、YAML 语法错误 → 带文件路径的友好报错、退出码非零
- [x] 表驱动测试(唯一接缝 config.Load):合法最小夹具断言结构、默认值断言、缺文件与坏 YAML 各一反例
- [x] 演示命令可复现:合法夹具输出摘要;非法夹具非零退出
