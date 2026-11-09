# 02:装配接通与混合上游端到端

**Spec:** [0003 stdio 上游适配器(简化版)](../../../specs/0003-stdio-upstream-adapter.md)
**What to build:** 配置里的 stdio 条目不再启动即败:翻译层把 stdio 条目投影为构造参数(名称、command、args、timeout),dial 按类型选择 NewHTTP/NewStdio,"暂未支持"拒绝路径删除。网关对 Agent 呈现 http+stdio 混合聚合清单,双命名空间的 tools/call 经网关往返;stdio 子进程死亡后,指向它的调用得到点名"不可用",另一上游照常(故障隔离)。
**Blocked by:** 01 (stdio adapter 落地).
**Status:** done (2026-11-04)

- [x]翻译层接受 stdio 条目并投影构造参数;既有 stdio 拒绝路径删除,退出码契约(0/1/2)不变
- [x]dial 按上游类型构造对应 adapter;混合配置(http 假上游 + stdio 夹具上游)全部拨通,任一失败全撤不留孤儿
- [x]端到端:混合配置经网关 initialize → tools/list(两命名空间聚合、描述与 schema 原样)→ tools/call 双通道往返,结果与直连一致
- [x]故障隔离:杀掉 stdio 子进程后,指向它的调用点名"不可用",http 上游调用照常
- [x]相关契约/表驱动测试同步更新(翻译正反例、摘要渲染含 stdio 条目)
- [x]`go test ./... -race` 全绿;`go build ./...` 成功
