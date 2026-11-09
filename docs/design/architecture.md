# flamingos 架构设计

> 版本:v1.0(2026-11-02)· 状态:已定稿(设计会话轮 1-4;轮 1/3/4 用户逐轮确认,轮 2 仓库布局按推荐方案定)
> 本文档是开发的设计基线,定义模块边界、接口草图与仓库布局。§6 决策表为备选取舍的文档化载体(每条决策记录被否候选与理由)。
>
> 词汇约定(Ousterhout《软件设计的哲学》+ codebase-design skill):
> **模块** = 接口 + 实现;**接口** = 调用方必须知道的一切(签名、错误语义、调用约束);
> **接缝(seam)** = 不改调用处即可替换实现的位置;**深模块** = 接口小、实现厚;
> **adapter** = 在接缝处满足接口的具体实现。

---

## 1. 模块清单

每个模块回答三问:暴露什么(接口)、藏什么(实现里的知识)、哪周建。

| 模块 | 接口(暴露什么) | 隐藏的知识 | 建设 |
|---|---|---|---|
| `cmd/flamingos` | 无(可执行入口) | 只做装配:读配置、翻译并注入各模块、启动、信号处理。**刻意不含业务知识** | W1 |
| `config` | `Load(path) → Config`(1 个函数) | YAML 格式、默认值、全部校验规则与报错文案 | W1 |
| `transport` | `http.Handler` | Streamable HTTP 细节(会话头、SSE);HTTP 中间件链(Origin/审计/限流)挂这层 | W1 骨架,W2 挂中间件 |
| **`gateway`** | `HandleToolsList` / `HandleToolsCall` 两个方法 | 命名空间解析与路由、多上游扇出合并、策略挂点 | **W1 核心** |
| **`upstream`** | `Client` 接口(`ListTools`/`CallTool`/`Close`) | 连接池、重试、stdio 进程监督、协议 Pin 版本差异 | W1(两个 adapter) |
| **`recorder`** | `New(Config) → *Recorder`(构造期校验路径可写)、`ReceivingMiddleware()` | JSON-lines 记录格式(12 字段,v 版本锚点)与 kind 三态判定;并发落盘互斥(seq 分配与行序同锁);挂 Receiving 链末位(判定内层,deny 不入录制) | W4 落地(spec 0008) |
| **`evalx`** | `LoadCases` / `Replay`(本地 Caller 小接口)/ `Report` 双形态输出 | 回放引擎 + 五种确定性 grader;golden+覆写用例形态(录制文件即用例);SDK 机器类型禁入引擎(client 适配只在 cmd/evalx) | W4 落地(spec 0008) |
| 预留接缝(已清空) | — | 无未落地预留:原预留五模块 `policy`(RBAC)、`telemetry`(OTel)、`cache`、`toolpattern`(工具名通配语法)、`audit` 已各自按预定接缝实现并落地(W2/W3;cache/toolpattern 见 v1.4) | 已全部落地 |

**最大的一刀:`gateway` ↔ `upstream` 边界。** gateway 只懂 MCP 语义(命名空间、聚合、路由),upstream 只懂传输语义(把一次调用送达某进程并拿回结果)。OTel/缓存加进 gateway 时不碰 upstream;stdio 换连接策略时只动 upstream——单一知识只出现在一个模块(信息隐藏)。

**`upstream` 是真接缝**:两个 adapter(HTTP / stdio)天然存在,不是假想接缝。HTTP 与 stdio 的全部差异(连接方式、生命周期、SDK 三 bug 的绕法)封在 adapter 内部,gateway 完全无感——这是"stdio 超时可砍"能成立的结构性保证:砍 stdio = 删 `stdio.go` 并撤 cmd 装配层的少量引用(演练条目见 §8-3),gateway/transport 零改动。

## 2. 仓库布局与依赖方向

```
flamingos/
├── cmd/
│   ├── flamingos/
│   │   └── main.go        # 只做装配
│   └── evalx/             # W4 落地(spec 0008):evalx CLI 二进制(SDK client 适配只在 cmd 层)
├── internal/
│   ├── config/            # W1
│   ├── transport/         # W1:HTTP server + HTTP 中间件链
│   ├── gateway/           # W1:命名空间聚合 + 路由(mcp 层)
│   ├── upstream/          # W1:接口 + http.go / stdio.go
│   ├── policy/            # W2 建
│   ├── telemetry/         # W2 建
│   ├── toolpattern/       # W3 落地(spec 0006):工具名通配语法叶包
│   ├── cache/             # W3 落地(spec 0006):缓存引擎 + upstream.Client 装饰器
│   ├── evalx/             # W4 落地(spec 0008):回放引擎 + 五种确定性 grader + 双形态报告
│   └── recorder/          # W4 落地(spec 0008):录制中间件 + JSONL 记录格式(Receiving 链末位)
├── e2e/                   # W3 落地(spec 0007):基础 e2e+故障演练,黑盒故在 internal 外
├── benchmark/             # W3 落地(spec 0007):vegeta 压测场景
└── examples/              # W4 建(Eino 示例)
```

**纪律(编译器守门):**

1. 依赖单向:`cmd → transport → gateway → upstream`,禁止反向(Go 循环依赖直接编译报错)。
2. `config` 包只被 `cmd` import;cmd 读配置后以构造参数注入各模块(依赖注入)。模块自己不读任何全局配置。
3. W2-W4 功能包不预建空目录——目录随模块一起诞生,结构永远与已实现功能同步。

**否决候选:按技术层分包**(server/middleware/service/client/model)。三条理由:W2 加 RBAC 被动改 3 包(本方案 1 处);`middleware/` 混放 HTTP 层与 mcp 层两套不同抽象;`model/` 共享类型包 = 信息泄漏温床。完整对比见设计会话记录,结论见 §6 D1。

## 3. 数据流(一次 tools/call)

```
Agent(如 Inspector)
 └─POST /mcp→ transport ──[W2: Origin校验→审计→限流]
      └─→ gateway.HandleToolsCall("github_create_issue")
           ├─[W2: ReceivingMW = trace提取 + RBAC判定]   ← 接缝已留,W1 为空操作
           ├─ 解析命名空间 github + 原名 create_issue → 选出 upstream "github"
           ├─[W3: 查缓存 ttlMs/cacheScope]              ← 接缝已留
           ├─→ upstream.Client.CallTool("create_issue", args)
           │      └─ HTTP adapter / stdio adapter(实现自选,gateway 无感)
           └─[W2/W4: SendingMW = span补全 + trace录制]  ← 接缝已留
```

Agent 侧为 Streamable HTTP(Stateless,2026-07-28 语义):网关本身无会话状态,每次请求独立——因此 server 侧无状态存储,而 upstream.Client 可长期持有(HTTP 连接池 / stdio 进程)。

> W2 策略层落地后的挂点现状(spec 0005,2026-11-05):HTTP 层实际链为 Origin 校验 → 限流 → 角色提取 → trace → SDK handler(准入装在可观测之外,403/429 不产生 span);mcp 层实际链为 trace → 决策桥 → 审计 → RBAC 判定(审计装在判定外层,deny 也落盘)。与上图 W2 占位标注的差异以实现为准,依据见 docs/specs/0005-policy-rbac-audit-ratelimit.md。

> W3 缓存层落地后的挂点现状(spec 0006,2026-11-08):缓存不在 gateway 内查找,实落为 cmd 装配层的 upstream.Client 装饰器(internal/cache,挂在命名空间解析与路由之后、传输之前,单一引擎全网关共享);缓存键 = 聚合名 + args 摘要(private 规则另拼角色),协议 hint(ttlMs/cacheScope)由网关自身 tools/list 应答产出(SDK SetCacheable)。与上图 W3 占位标注的差异以实现为准,依据见 docs/specs/0006-cache-tool-results.md。

> W4 录制器落地后的挂点现状(spec 0008,2026-11-08):上图 W4 占位"trace 录制"不在 SendingMW,实落为 mcp 层 Receiving 链末位(internal/recorder 的录制中间件,挂在 RBAC 判定内层——deny 不下探故不入录制;链序现状 trace → 决策桥 → 审计 → RBAC 判定 → 录制)。只录实际执行的 tools/call,JSON-lines 落盘(录制态经配置顶层 recorder 段声明,段缺省 = 关闭零挂点);录制文件的消费端为 internal/evalx(回放 + 五种确定性 grader),cmd/evalx 为 CLI 入口,e2e evalx family 自 spec 0008 起兑现"门禁自举"。与上图 W4 占位标注的差异以实现为准,依据见 docs/specs/0008-evalx-record-replay-grader.md。

## 4. 核心接口草图

> 签名级草图;`mcp.*` 类型名已经 go-sdk v1.8.0 spike 编译+运行验证(2026-11-02,证据见 docs/design/spike-go-sdk.md)。SDK 机器类型(client/session/transport)严禁跨出 adapter。
>
> **adapter 实现注意(spike 实测)**:①转发参数一律 `json.RawMessage`(裸 `[]byte` 会被编成 base64);②注册上游 tool 前兜底空 schema(`{"type":"object"}`),否则 SDK panic;③注册前自校验 tool 名(SDK 对非法名只打日志不拒绝);④stdio adapter 连接用 `ClientSessionOptions{ProtocolVersion: "2025-11-25"}`;⑤HTTP adapter 用 `StreamableClientTransport{DisableStandaloneSSE: true}`。

### 4.1 upstream(项目最重要的一份接口)

```go
// internal/upstream/upstream.go
package upstream

// Client 是"一个可调用的上游 MCP server"。
// 实现:http.go(HTTP 上游)、stdio.go(stdio 子进程,简化版)。
// 契约:构造时建立底层连接/进程;并发安全;Close 后再调用返回 ErrClosed。
type Client interface {
    // ListTools 返回该上游全部 tool 的原始描述——名字未加命名空间前缀。
    ListTools(ctx context.Context) ([]mcp.Tool, error)

    // CallTool 用"原始 tool 名"调用;args 为上游期望的原始参数。
    CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)

    // Close 释放连接/子进程。
    Close() error
}

// 错误契约(接口的一部分,调用方必须知道):
// ① 连不上/断开/超时 → 包装为 *TransportError{Upstream, Cause}
//    (gateway 据此向 Agent 报"上游不可用",不冒充 tool 本身的错)
// ② 上游正常应答但业务失败 → 协议错误原样透传,不吞不改

// cmd 装配时调用:配置 → map[命名空间]Client
func NewClients(cfg []Config) (map[string]Client, error)
```

### 4.2 gateway

```go
// internal/gateway/gateway.go
func New(clients map[string]upstream.Client) *Gateway   // 依赖注入,测试可塞假实现

// 并发拉取所有上游 ListTools → 合并 → 加 "命名空间_" 前缀
func (g *Gateway) HandleToolsList(ctx context.Context) ([]mcp.Tool, error)

// 拆前缀 → 选 upstream → CallTool(原名);前缀无匹配 → 明确报错
func (g *Gateway) HandleToolsCall(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)
```

**命名空间规则(单一出处=本节):**

- R1:命名空间名禁止含下划线(config 校验强制,字符集 `[a-zA-Z0-9-]+`);
- R2:tool 全名 = `命名空间 + "_" + 原始名`,解析按**第一个**下划线拆分——R1 保证永无歧义;
- R3:MCP tool 名规范不允许 `/`,故不用斜杠做分隔符。

### 4.3 config

```go
// internal/config/config.go
type Config struct {
    Listen    string      `yaml:"listen"`     // 默认 ":8080"
    Upstreams []Upstream  `yaml:"upstreams"`
}

type Upstream struct {
    Name    string        `yaml:"name"`     // R1:禁下划线、不重复
    Type    string        `yaml:"type"`     // "http" | "stdio"
    URL     string        `yaml:"url"`      // type=http 必填,合法 http(s) URL
    Command string        `yaml:"command"`  // type=stdio 必填
    Args    []string      `yaml:"args"`
    Timeout time.Duration `yaml:"timeout"`  // 缺省 30s
}

// Load:读文件 → 解析 → 校验 → 返回可完全信任的 Config。
// 校验失败一次性汇总全部问题(非只报第一个),例:
//   flamingos.yaml: upstreams[1]: type=stdio 缺少 command
func Load(path string) (Config, error)
```

配置文件示例:

```yaml
# flamingos.yaml
listen: ":8080"

upstreams:
  - name: calc            # 命名空间:禁止下划线(路由按第一个 _ 拆分)
    type: http
    url: http://127.0.0.1:9001/mcp
    timeout: 10s

  - name: fs
    type: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "."]
```

W2 的 `policy:`/`ratelimit:`、W3 的 `cache:` 段随模块落地时增补——不为未建功能预埋死字段。

## 5. 装配(文件世界 → 模块世界的翻译,只发生在 cmd)

```go
// cmd/flamingos/main.go 装配示意
cfg     := must(config.Load(*configPath))                  // 文件世界 → 结构
clients := must(upstream.NewClients(translate(cfg)...))    // 结构 → 模块,cmd 独占翻译
gw      := gateway.New(clients)
srv     := transport.New(cfg.Listen, gw)
```

任何模块不 import config;改 YAML 字段名的波及面 = config + cmd 两处。测试收益:gateway 测试构造假 `map[string]upstream.Client` 即可运行,全仓库无一测试需要配置文件。

## 6. 设计决策记录

| # | 决策 | 被否候选 | 理由 |
|---|---|---|---|
| D1 | 按模块分包,目录=模块清单 | 按技术层分包 | 变化放大:层式加 RBAC 动 3 包;`model/` 共享包信息泄漏;目录即架构图 |
| D2 | upstream 接口 typed(ListTools/CallTool) | 通用 `Forward(method, params)` | 防协议知识漏进 gateway;W3 缓存 key、W4 录制直接用类型;不支持的方法不出现=显式声明 |
| D3 | CallTool 收原始名,拆前缀归 gateway | upstream 收全名自拆 | 路由知识不下沉传输层;adapter 不知道"命名空间"存在 |
| D4 | SDK 协议数据类型可入签名,SDK 机器类型禁出 adapter | 全部自定义包装 / 全部裸用 | 包装协议类型是仪式主义;机器类型泄漏才致命(锁死 SDK) |
| D5 | 命名空间禁下划线 + 按第一个 `_` 拆 | 双下划线、斜杠 | 解析零歧义;`/` 违反 MCP tool 名规范(§4.2 R1-R3) |
| D6 | 单 upstreams 列表 + type 判别 | 分 http/stdio 两个列表 | 概念不劈两处,公共字段不重复;校验代价花在 config 本职上 |
| D7 | config 只被 cmd import,构造注入 | 各模块自读配置 | 可测试性;改配置格式波及面恒为 2 处 |
| D8 | 启动时一次性全量校验、即败 | 逐字段懒校验 | "把错误设计掉":下游构造函数零防御代码 |
| D9 | stdio 是 upstream 包内一个 adapter 文件 | 独立顶层包 | 同一接口的两个实现;超时砍的现行代价见 §8-3(upstream 内删单文件,cmd 装配层另有引用) |
| D10 | W2-W4 功能包不预建空目录 | 一次建全 | 空目录无代码即无设计;结构与已实现功能同步 |

## 7. 悬而未决项(带决定时间点,不拍脑袋)

| # | 问题 | 候选 | 决定时间 |
|---|---|---|---|
| U1 | ReceivingMW/SendingMW 挂点位置 | (a) go-sdk server 自带 middleware/hook 机制 (b) gateway 自定义 hook 链 | **定案 (2026-11-02 spike)**:选 (a)。SDK 提供 `AddReceivingMiddleware/AddSendingMiddleware`(`Middleware func(MethodHandler) MethodHandler`),与 §3 数据流概念一一对应,详见 spike 报告 |
| U2 | go-sdk 版本 Pin 与实际 API 形状 | — | **定案 (2026-11-02 spike)**:Pin v1.8.0;§4 草图类型名经编译+运行验证一致;证据与行为级发现见 docs/design/spike-go-sdk.md |
| U3 | Origin 校验/令牌桶限流的代码位置 | transport 内部文件 vs 独立小包 | **定案 (2026-11-05,spec 0005)**:选 transport 内部文件(`origin.go`/`ratelimit.go` 两道 HTTP 层中间件)。纯 HTTP 机制不服务其他层,几十行立一包是过度切分;实现与证据见 docs/specs/0005-policy-rbac-audit-ratelimit.md |
| U4 | audit 的包归属 | 独立 `audit/`(自有输出格式知识) vs transport 内 | **定案 (2026-11-05,spec 0005)**:选独立 `audit/`。JSON-lines 输出格式知识自有,与 policy 互不 import(ctx 键与决策翻译只在 cmd 装配层相遇);实现与证据见 docs/specs/0005-policy-rbac-audit-ratelimit.md |

## 8. 结构验收标准(设计对不对,写码时用这三条检验)

1. **W2 diff 测试**:加 RBAC 那天,diff 应仅出现在 `policy/` 新包 + 装配行;若动了 gateway/upstream/config 三处,边界切错,回来改本文档。
2. **删除测试**:删 upstream 包 → 连接管理/进程监督的复杂度散落到每个调用点 → 它在挣自己的饭钱;gateway 同理。
3. **砍 stdio 演练**:stdio 已按 spec 0003 落地,"其余零改动"不再成立——upstream 包内仍是删 `stdio.go` 单文件(共享会话基座在 `session.go`,http adapter 继续复用),但 cmd 装配层需同步撤对 `NewStdio`/`TypeStdio` 的引用(`dialOne`/`translateUpstreams`/`upstreamTarget` 的 stdio 分支)及 stdio 相关测试,config 的 type 白名单去掉 stdio 条目;gateway/transport 仍零改动。

## 9. 与里程碑任务的映射

| 设计产物 | 任务 | 时长 |
|---|---|---|
| §2 目录骨架 + §4.3 config | 脚手架 + config | 3h |
| §4.1 upstream + §4.2 gateway | HTTP 上游 + 聚合 + 转发跑通 | 8h |
| §4.1 stdio adapter | stdio 简化版(时间盒,超时即砍) | 6h |

---

## 修订历史

| 日期 | 版本 | 变更 |
|---|---|---|
| 2026-11-02 | v1.0 | 设计会话轮 1-4 定稿:模块清单、仓库布局(D1)、upstream/gateway/config 接口草图、决策 D1-D10、悬而未决 U1-U4 |
| 2026-11-02 | v1.1 | go-sdk spike 收口:Pin v1.8.0;U1 定案选 (a) SDK 内建 middleware;U2 定案;§4.1 补 adapter 实现注意(5 条实测);证据见 docs/design/spike-go-sdk.md |
| 2026-11-05 | v1.2 | spec 0005 策略层落地回填:U3 定案(Origin/限流=transport 内部文件)、U4 定案(audit 独立包);§3 补 W2 挂点现状 |
| 2026-11-05 | v1.3 | 架构评审深化(C1/C2):origin 条目语法收进 originrule 叶包(原 config/transport 双份实现与跨包一致性 e2e 退役,一份语法两个消费方);两条中间件链的顺序约束升为逐对定点单测(receivingChain 探针法 + HTTP 准入对),W3 插路只跑定点 |
| 2026-11-08 | v1.4 | spec 0006 缓存层落地回填:缓存=cache 包 upstream.Client 装饰器而非 gateway 内挂点(cmd 装配层包装,§3 补 W3 挂点现状);工具名通配语法诞生 toolpattern 叶包(policy/cache/config 三消费方共用一份语法),§1 模块清单与 §2 布局树同步 |
| 2026-11-08 | v1.5 | spec 0007 运行质量实证落地回填:§2 布局树 e2e/ 与 benchmark/ 两行由"W3 建"改已落地口径(顶层黑盒套件,零 import internal 自足纪律;运行期零挂点变化——internal/ 与 cmd/ 生产文件零改动,依据 docs/specs/0007-benchmark-drills-e2e.md) |
| 2026-11-08 | v1.6 | spec 0008 evalx 最小闭环落地回填:§1 模块清单补 recorder/evalx 两行(recorder 撤出预留接缝行——已落地模块不再列预留);§2 布局树补 internal/evalx 与 cmd/evalx 条目(cmd/ 收拢为嵌套块)、recorder 行改已落地口径;§3 W4 占位"trace 录制"改实落口径(Receiving 链末位而非 SendingMW,补挂点现状注,沿"以实现为准"先例);依据 docs/specs/0008-evalx-record-replay-grader.md |
| 2026-11-08 | v1.7 | spec 0008 双轴评审收口:§1"预留接缝"行整行清空改写——原列 policy/telemetry/cache/toolpattern/audit 五模块在 v1.6 时点均已落地(W2/W3 及 v1.4),预留行只留真预留(现状为零),与 v1.6"已落地模块不再列预留"的口径对齐;依据 docs/specs/0008-evalx-record-replay-grader.md 修订表 |
