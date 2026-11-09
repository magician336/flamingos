// Package transport 把网关装配为对 Agent 的 MCP 服务:go-sdk server +
// Stateless Streamable HTTP handler(仅 POST,GET/DELETE → 405)。
// SDK 机器类型(Server/StreamableHTTPHandler)不跨出本包(架构 §1/§6 D4);
// 对外只暴露标准库 http.Handler,黑盒可测。
package transport

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/magician336/flamingos/internal/audit"
	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/originrule"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/recorder"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 网关在 initialize 应答中对 Agent 自报的身份(spec 0002:Agent 端能识别
// 连接的是 flamingos;与 upstream 侧客户端自报的身份是两回事)。
const (
	gatewayImplName    = "flamingos"
	gatewayImplVersion = "0.1.0"
)

// endpointPath 是网关对 Agent 暴露的单一 MCP 端点路径(架构 §3:POST /mcp)。
// SDK 的 StreamableHTTPHandler 本身不校验路径,端点约束由本包挂载时收拢。
const endpointPath = "/mcp"

// New 构造网关侧 MCP 服务:调用 HandleToolsList 取聚合清单并完成启动时的
// 一次性快照注册(spec 0002:运行期不重列上游、不转发 tools/list_changed),
// 返回可挂到任意 http.Server 的 handler——单一端点 POST /mcp,Stateless
// 语义(GET/DELETE → 405)由 SDK 的 Streamable HTTP handler 保证。
// 上游列举失败即构造失败——网关不带病上线(设计 D8)。
//
// 可观测接线经 opts 注入(spec 0004 票 01):不传即关闭态——不装任何
// 中间件、不包装 handler,行为与未引入 telemetry 前逐字节一致。
func New(ctx context.Context, gw *gateway.Gateway, opts ...Option) (http.Handler, error) {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	tools, err := gw.HandleToolsList(ctx)
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    gatewayImplName,
		Version: gatewayImplVersion,
	}, listCacheOptions(o))
	for _, tool := range tools {
		registerTool(server, gw, tool)
	}
	if mws := receivingChain(o); len(mws) > 0 {
		server.AddReceivingMiddleware(mws...)
	}
	return httpChain(o, server), nil
}

// listCacheOptions 产出 tools/list hint 的 ServerOptions(票 0006-03,
// spec 0006 触发语义裁决:协议 hint 以产出方身份落地):listCacheTTL>0 时
// 经 SetCacheable 对携带 Cacheable 的应答统一置 TTLMs=配置值(cacheScope
// 不设,SDK resolveCacheable 后的 normalize 以协议缺省填 public——清单对
// 所有角色一致,spec 0005 既有裁决;SetCacheable 在 SDK 持锁站被调,闭包
// 只赋值不回调 server)。≤0/未传返回 nil——NewServer 拿到的与改动前逐字节
// 相同(既有 server 行为零变化)。
func listCacheOptions(o options) *mcp.ServerOptions {
	if o.listCacheTTL <= 0 {
		return nil
	}
	ttl := o.listCacheTTL
	return &mcp.ServerOptions{
		SetCacheable: func(_ context.Context, _ mcp.Request, c *mcp.Cacheable) {
			c.TTLMs = int(ttl.Milliseconds())
		},
	}
}

// receivingChain 装配 mcp 层 Receiving 链(SDK 语义:单次调用的首参最外,
// 先执行;多次调用则后装者反而包在外层——mcp/server.go addMiddleware 实测
// 核对):trace 最外;决策桥次之——ctx 只能向下传递,判定中间件 deny 路径
// 写入的决策到不了外层,桥以同一判定器预写决策(纯函数,同输入必同结果),
// 供内层审计与判定同口径;审计在 RBAC 判定之外层——先下探待决策产生再
// 落盘,allow 与 deny 都记录(RBAC 拒绝不下探 next,审计在其内层将看不
// 到拒绝);录制器在 RBAC 判定之内层(链末位)——判定放行才下探,deny
// 路径不下探故不入录制,基准表征工具处理行为而非策略配置(spec 0008)。
// 逐个 nil 剔除,关闭态零挂点。
func receivingChain(o options) []mcp.Middleware {
	var receiving []mcp.Middleware
	if mw := o.telemetry.ReceivingMiddleware(); mw != nil { // nil 接线 → nil
		receiving = append(receiving, mw)
	}
	if o.policy != nil { // 桥以判定器预写决策,判定器缺席即无决策可写
		receiving = append(receiving, decisionMiddleware(o.policy))
	}
	if mw := o.audit.ReceivingMiddleware(); mw != nil { // nil 接线 → nil
		receiving = append(receiving, mw)
	}
	if mw := o.policy.ReceivingMiddleware(); mw != nil { // nil 接线 → nil
		receiving = append(receiving, mw)
	}
	if mw := o.rec.ReceivingMiddleware(); mw != nil { // nil 接线 → nil,链末位(判定内层)
		receiving = append(receiving, mw)
	}
	return receiving
}

// httpChain 装配 HTTP 层中间件链(装配链:Origin → 限流 → … → SDK
// handler):mux 以单一端点挂 SDK 的 Stateless Streamable HTTP handler,
// 角色提取紧贴 SDK handler,telemetry 包装再内一层;票 0005-03 两道准入
// 中间件装在链最外层——被 403(Origin 不匹配)与 429(限流)的请求不
// 产生网关请求 span,攻击与过载流量不进 trace;内层既有链相对顺序不动,
// 关闭态零改动。
func httpChain(o options, server *mcp.Server) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(endpointPath, mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	var handler http.Handler = mux
	if o.policy != nil { // 角色提取紧贴 SDK handler(装配链:… → 角色提取 → SDK handler)
		handler = roleMiddleware(o.policy)(handler)
	}
	if wrap := o.telemetry.HTTPMiddleware(); wrap != nil { // nil 接线 → nil,关闭态零包装
		handler = wrap(handler)
	}
	if o.rateLimit != nil {
		handler = rateLimitMiddleware(o.rateLimit)(handler)
	}
	if o.origin != nil {
		handler = originMiddleware(o.origin)(handler)
	}
	return handler
}

// Option 是 transport 的装配选项:可观测等横切能力以构造参数注入,
// 不传任何选项 = 全部关闭态(架构 §8-1:装配行级接线,知识在能力包)。
type Option func(*options)

// options 承载全部装配参数;零值即关闭态。
type options struct {
	telemetry    *telemetry.Telemetry // nil = 可观测关闭态
	policy       *policy.Policy       // nil = 策略关闭态(全放行)
	audit        *audit.Recorder      // nil = 审计关闭态(零挂点)
	rec          *recorder.Recorder   // nil = 录制关闭态(零挂点,spec 0008)
	origin       []originrule.Pattern // nil = Origin 校验关闭态(放行)
	rateLimit    *rateLimiter         // nil = 限流关闭态
	listCacheTTL time.Duration        // ≤0 = tools/list hint 不宣告(缺省)
}

// WithTelemetry 注入可观测接线:入站 HTTP 头提取包装与网关请求 span
// 中间件在 New 内安装;t 为 nil(含 telemetry.New(nil))时不装任何
// 挂点——关闭态零开销(spec 0004 Story 13/20)。
func WithTelemetry(t *telemetry.Telemetry) Option {
	return func(o *options) { o.telemetry = t }
}

// WithPolicy 注入策略接线(spec 0005 票 01):HTTP 角色头提取包装与 mcp 层
// RBAC 判定中间件在 New 内安装;p 为 nil 时策略关闭态——不装任何挂点,
// 行为与未引入 policy 前逐字节一致(关闭态全放行,Story 11)。
func WithPolicy(p *policy.Policy) Option {
	return func(o *options) { o.policy = p }
}

// WithAudit 注入审计接线(spec 0005 票 02):mcp 层审计中间件在 New 内
// 安装,挂在 RBAC 判定之外层(trace 之内);a 为 nil 时审计关闭态——不装
// 任何挂点,行为与未引入 audit 前逐字节一致(Story 11)。
func WithAudit(a *audit.Recorder) Option {
	return func(o *options) { o.audit = a }
}

// WithRecorder 注入录制接线(spec 0008 票 01):mcp 层录制中间件在 New 内
// 安装,挂在 RBAC 判定之内层(Receiving 链末位)——判定放行才下探,deny
// 不入录制;r 为 nil 时录制关闭态——不装任何挂点,行为与未引入 recorder
// 前逐字节一致。
func WithRecorder(r *recorder.Recorder) Option {
	return func(o *options) { o.rec = r }
}

// WithOrigin 注入 Origin 校验接线(spec 0005 票 03):允许列表在 New 内
// 编译为匹配器并安装 HTTP 层校验中间件(链最外层);allowed 为空即关闭态
// ——不装任何挂点,任意 Origin 放行(Story 11)。条目语法已由 config 校验
// 保证(设计 D8),编译期对坏条目仅记警示并跳过。
func WithOrigin(allowed []string) Option {
	return func(o *options) {
		if len(allowed) == 0 {
			return
		}
		o.origin = compileOrigins(allowed)
	}
}

// WithListCacheHint 注入 tools/list 应答的缓存 hint 产出(spec 0006 票 03):
// ttl>0 时构造 ServerOptions.SetCacheable,对携带 Cacheable 的应答(tools/
// list 等 list/read 结果)统一置 TTLMs=该值,协议缺省 cacheScope=public;
// ttl≤0 即关闭态——不构造任何 ServerOptions,既有 server 行为逐字节不变
// (Agent 侧 prompt cache 依赖清单在该 TTL 内稳定,Story 6/7)。
func WithListCacheHint(ttl time.Duration) Option {
	return func(o *options) {
		if ttl <= 0 {
			return
		}
		o.listCacheTTL = ttl
	}
}

// WithRateLimit 注入限流接线(spec 0005 票 03):全局单桶在 New 内构造并
// 安装 HTTP 层限流中间件(链最外层);RPS≤0 即关闭态——不装任何挂点。
// Now 是时间来源(测试注入假时钟以获得确定性,Story 20),nil 落 time.Now。
func WithRateLimit(cfg RateLimitConfig) Option {
	return func(o *options) {
		if cfg.RPS <= 0 {
			return
		}
		o.rateLimit = newRateLimiter(cfg)
	}
}

// RateLimitConfig 是令牌桶的构造参数:config.Policy.RateLimit 在 cmd 装配层
// 的投影;零值(RPS=0)即关闭态。构造知识在 transport 包内(U3 裁决:
// 限流是 HTTP 层机制不立新包),cmd 只翻译数字。
type RateLimitConfig struct {
	RPS   float64          // 每秒补充令牌数(必须 > 0,config 已校验)
	Burst int              // 桶容量(瞬时突发上限,必须 ≥ 1,config 已校验)
	Now   func() time.Time // 时间来源;nil = time.Now(测试注入假时钟)
}

// roleMiddleware 返回角色声明头提取包装(spec 0005 装配链 HTTP 层的小中间件,
// 本包内自持):从配置的头名读取 Agent 自报的角色,未带头落默认角色,经
// policy 的专用 ctx 键交给 mcp 层判定。头名与默认角色知识的单一出处是
// config,经 Policy 只读透出——本包不含策略判定知识,只做 HTTP 头到 ctx
// 的搬运。
func roleMiddleware(p *policy.Policy) func(http.Handler) http.Handler {
	header, defaultRole := p.RoleHeader(), p.DefaultRole()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := r.Header.Get(header)
			if role == "" {
				role = defaultRole
			}
			next.ServeHTTP(w, r.WithContext(policy.WithRole(r.Context(), role)))
		})
	}
}

// decisionMiddleware 返回决策桥中间件(mcp 层,本包内自持):SDK 的中间件
// 链上 ctx 只能向下传递——判定中间件 allow 路径写入的决策只对其内层可见,
// deny 路径不下探 next、写入随 return 丢弃,挂在判定之外的审计永远取不到。
// 桥在判定之外层以 policy.DecideContext 预写决策后下探——判定胶水(默认
// 角色兜底 → 取工具名 → Decide)的单一出处在本包,桥的预写决策与判定中间
// 件的实际判定必然同输入同结果;判定与拒绝语义的权威仍是判定中间件本身,
// 桥不重复拒绝、不改错误。仅对 tools/call 预写,其余方法原样下探。
func decisionMiddleware(p *policy.Policy) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}
			return next(p.DecideContext(ctx, req), method, req)
		}
	}
}

// methodCallTool 是桥与转发共用认定的 tools/call 字面量;SDK 未导出该常量,
// transport/policy/audit 各以本包单一出处对齐 MCP method 名,一致性由 e2e
// 钉住。
const methodCallTool = "tools/call"

// registerTool 把单个聚合 tool 注册进网关侧 server,转发 handler 统一走
// HandleToolsCall。schema 形状非法的 tool 会被 SDK 的 AddTool 直接 panic
// 拒绝(mcp/server.go:缺 schema 或 type≠object 均如此),此处提前拦下、
// 跳过并输出启动警示——单个坏 tool 不拖死整个网关启动(与 gateway 侧
// 非法名自守同一纪律)。
func registerTool(server *mcp.Server, gw *gateway.Gateway, tool *mcp.Tool) {
	if tool.InputSchema != nil && !isObjectSchema(tool.InputSchema) {
		log.Printf("警告: 跳过 tool %q:输入 schema 不是 object(上游应发布 object schema)", tool.Name)
		return
	}
	server.AddTool(withSchemaFallback(tool), forwardHandler(gw, tool.Name))
}

// isObjectSchema 判断 schema 是否为 SDK 可接受的 object schema:线路解码后
// 的形状是 map[string]any 且 type=object(spike §3-4 实测);其余形状一律
// 视为非法交由调用方跳过。
func isObjectSchema(schema any) bool {
	m, ok := schema.(map[string]any)
	return ok && m["type"] == "object"
}

// withSchemaFallback 返回可安全注册的 tool:上游缺 InputSchema 时兜底为
// 空 object schema——缺 schema 的 AddTool 直接 panic(spike §3 实测),
// 以副本兜底,不原地修改聚合清单里的对象。
func withSchemaFallback(tool *mcp.Tool) *mcp.Tool {
	if tool.InputSchema != nil {
		return tool
	}
	fallback := *tool
	fallback.InputSchema = map[string]any{"type": "object"}
	return &fallback
}

// forwardHandler 返回统一走 HandleToolsCall 的转发 handler:以注册时的
// 聚合名(闭包捕获)路由,参数取 req.Params.Arguments 的原始字节透传,
// 不反序列化、不重编码(spike §3:裸 []byte 会被编成 base64)。
func forwardHandler(gw *gateway.Gateway, name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return gw.HandleToolsCall(ctx, name, req.Params.Arguments)
	}
}
