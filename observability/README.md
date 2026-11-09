# observability:本地 Jaeger 验收环境

flamingos 全链路 tracing 的本地验收环境(spec 0004):一条命令拉起 Jaeger
(all-in-one,内置 OTLP gRPC 接收),网关配置 `telemetry:` 段指向它,即可在
Jaeger 里看到 Agent → 网关 → 上游 的完整 span 树。

## 拉起与关闭

```sh
cd observability
docker compose up -d      # 拉起 Jaeger
docker compose down       # 用完关闭(数据随之清零,全部在容器内存)
```

- Jaeger UI:`http://localhost:16686`
- OTLP gRPC 接收端口:`localhost:4317`(明文)

## 让 flamingos 把 span 导到这里

在网关配置里加 `telemetry:` 段(段缺省 = tracing 完全关闭,零开销):

```yaml
telemetry:
  otlp_endpoint: localhost:4317   # 或 http://localhost:4317;TLS 后端用 https://host:4317
  service_name: flamingos          # 可选,缺省即 flamingos
```

网关启动后由任意 MCP 客户端发起调用(如 tools/call),span 会先在内存批量
缓存、周期性导出;网关收到 SIGINT/SIGTERM 优雅停机时,停机路径会把在途
span 全部 flush 完再退出。OTLP 后端不可达只影响导出(记日志),不影响网关
服务。

## 验收看什么

1. Jaeger UI 的 Service 列表出现配置的 `service_name`(缺省 `flamingos`)。
2. 选服务 → Find Traces:一次 tools/call 对应一棵 trace 树:
   - `tools/call` server span(网关请求,父为 Agent 带来的 trace 上下文;
     Agent 无上下文时网关自开根);
   - 其 child `tools/call` client span(网关对上游的转发,属性带上游名/
     类型/聚合 tool 名/命名空间)。
3. HTTP 上游另可在 span 属性外核对标准 `traceparent` 头已注入;stdio 上游
   走 `_meta.traceparent`(SEP-414 通道)。

## 命令行核对(不开浏览器)

```sh
curl -s 'http://localhost:16686/api/traces?service=flamingos' | \
  jq '.data[] | {traceID, spans: [.spans[] | {op: .operationName, id: .spanID,
    refs: [.references[]? | {type: .refType, to: .spanID}],
    tags: [.tags[]? | select(.key | startswith("flamingos.") or .key == "gen_ai.tool.name")]}]}'
```

同一 traceID 下应有 server 与 client 两个 `tools/call` span,且 client 的
`references`(CHILD_OF)指向 server 的 spanID;`tags` 里可见方法名、聚合
tool 名、命名空间、上游名/类型等属性。

## 边界

- 数据只在容器内存,`docker compose down` 即清零,不适合留存检索。
- Jaeger 不进 CI:全链路断言的机械等价物是进程内测试(SpanRecorder +
  OTLP 接收器),本环境只服务人工验收与联调。
