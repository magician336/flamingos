# examples:Eino 接入 flamingos 网关

第三方 Agent 框架(CloudWeGo Eino)消费网关聚合工具的最小示例。本目录是
独立 Go module:依赖面(eino、eino-ext officialmcp、go-sdk)隔离在示例
侧,主模块的 go.mod 与 `go build ./...` / `go test ./...` 机械判据不受
本目录影响。

## 内容

- `main.go`(命令 `examples`,即本 module 根包):经 eino-ext officialmcp
  的 `GetTools` 把网关聚合工具桥接为 Eino 工具接口(`tool.BaseTool`),打印
  桥接清单并直调其中一个工具、输出应答。无 LLM key 依赖,接入验证不依赖
  任何厂商账号。与官方示例的差异点(transport 形态、无模型段)在包注释注明。

## 运行

1. 起一个网关(最小配置,单 http 上游;写为 `flamingos.yaml`):

   ```yaml
   listen: 127.0.0.1:8080
   upstreams:
     - name: calc
       type: http
       url: http://127.0.0.1:9001/mcp
       timeout: 5s
   ```

   上游可以是任意 MCP server(含仓库内 e2e 夹具
   `internal/upstream/testdata/httpserver`,其 `echo` 工具回显原始参数字
   节)。启动(仓库根):

   ```sh
   go run github.com/magician336/flamingos/cmd/flamingos -config flamingos.yaml
   ```

2. 跑示例(在 examples 目录内):

   ```sh
   go run . -endpoint http://127.0.0.1:8080/mcp \
     -tool calc_echo -args '{"text":"hello from eino"}'
   ```

   端点也可经环境变量 `FLAMINGOS_MCP_ENDPOINT` 注入;`-tool` 缺省取聚合清
   单首项。聚合工具命名形如 `<上游名>_<工具名>`。

   预期输出(节选;应答为 officialmcp 透出的 CallToolResult JSON 信封,
   上游工具原文在 content[0].text):

   ```text
   桥接到 4 个网关聚合工具:
     - calc_echo: 回显收到的原始参数字节
     ...
   直调 calc_echo 的应答:
   {"_meta":...,"content":[{"type":"text","text":"{\"text\":\"hello from eino\"}"}],...}
   ```

## 从直调到 agent

示例中 `invoke` 所走的 `tool.InvokableTool.InvokableRun` 与 ReAct agent 里
模型调用的接口是同一个:接入 ReAct 只需把 `GetTools` 产物传入
`react.NewAgent` 的 `AgentConfig.ToolsConfig`,工具调用由模型决策触发。
官方示例出处:eino-ext 仓库 `components/tool/mcp/officialmcp/examples/mcp.go`
(v0.1.1,含模型段的厂商接线)。
