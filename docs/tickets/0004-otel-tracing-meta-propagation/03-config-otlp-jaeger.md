# 03:配置驱动 OTLP 导出与 Jaeger 人工验收环境

**Spec:** [0004 OTel 全链路追踪与 SEP-414 `_meta` 传播](../../../specs/0004-otel-tracing-meta-propagation.md)
**What to build:** tracing 从测试注入变为配置驱动:YAML 新增 `telemetry:` 段(otlp_endpoint、service_name 默认 flamingos)——段缺省 = tracing 完全关闭零开销,段存在而 endpoint 缺失/非法 = 启动即败且错误一次性汇总(设计 D8),不为未建功能预埋其他字段。cmd 装配按配置构造 OTLP TracerProvider(单一构造处、三形态:关闭/OTLP/测试注入),优雅关闭路径完成导出 flush 不丢在途 span,OTLP 后端不可达不反噬数据面。observability/ 顶层目录(非 Go 模块)提供 docker compose(jaeger all-in-one,OTLP 接收)与使用说明,一条命令拉起本地 Jaeger;人工验收:Inspector(或等价客户端)发起调用,Jaeger 呈现 Agent→网关→上游全链路 span 树与属性,截图留存供发布文档使用。
**Blocked by:** 01 (标准传播全链路——telemetry 装配点与 TracerProvider 形态在其上扩展;与票 02 无依赖、可并行).
**Status:** done (2026-11-04)

- [x] config 新增 `telemetry:` 段:缺省=noop;段存在而 endpoint 缺失/非法 → 启动即败、错误一次性汇总;默认关闭契约有表驱动测试钉死
- [x] cmd 装配:OTLP TracerProvider 单一构造处;优雅关闭路径 flush 在途 span;OTLP 后端不可达时网关照常服务
- [x] observability/:docker compose(jaeger all-in-one,OTLP 接收)+ 使用说明,一条命令拉起
- [x] 人工验收完成:Jaeger 全链路可见(server/client span 树、属性、错误标注),截图留存(W4 发布文档用);验收记录回写 spec 0004 修订表——活体验证经 Jaeger 查询 API 机器确认(同 trace 三 span 树、属性齐全、优雅停机 2s 内 flush 完成),截图由用户 W4 撰写发布文档时补拍
- [x] `go test ./... -race` 全绿;`go build ./...` 成功
