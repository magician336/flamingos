// flamingos 是装配入口:解析参数 → 加载配置 → 构造全部上游客户端 →
// 聚合网关 → 装配 transport → 真实监听,收到 SIGINT/SIGTERM 后优雅退出。
// 本入口只做装配与翻译,不含业务知识(架构 §1/§5)。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/magician336/flamingos/internal/audit"
	"github.com/magician336/flamingos/internal/cache"
	"github.com/magician336/flamingos/internal/config"
	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/recorder"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
)

// 进程退出码约定:0=成功,1=配置错误,2=启动失败(上游构造、清单快照、
// 监听);CI 按字面值判定,不得漂移(字面值钉死在 main_test.go 的契约测试里)。
const (
	exitOK           = 0
	exitConfigError  = 1
	exitStartupError = 2
)

// defaultConfigPath 是 -config 未指定时的默认配置文件路径。
const defaultConfigPath = "flamingos.yaml"

// shutdownTimeout 是优雅停机的硬上限:停 server 后在途请求最多等待这么久。
const shutdownTimeout = 10 * time.Second

// startupError 标记"配置合法但装配/启动/停机阶段失败"(上游构造、聚合
// 快照、监听、停机异常),exitCode 据此与配置错误区分;文案原样透传
// 底层错误,Unwrap 保持判别能力。
type startupError struct {
	cause error
}

func (e *startupError) Error() string { return e.cause.Error() }
func (e *startupError) Unwrap() error { return e.cause }

// asStartup 把装配/启动阶段的错误包装为 *startupError。
func asStartup(err error) error { return &startupError{cause: err} }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "flamingos 启动失败:", err)
		os.Exit(exitCode(err))
	}
}

// run 承载装配与服务流程;出错即返回,退出码由 exitCode 统一映射。
// 装配顺序刻意安排为:全部上游握手与聚合快照成功之后才真正监听——
// 网关不带病上线(设计 D8);收到 SIGINT/SIGTERM 后停 server、逐个
// Close 上游再退出。清理(defer)全部留在本函数注册——它们的执行时机
// 是 run 返回(serve 排空之后),assemble 只造不拆。
func run() error {
	configPath := flag.String("config", defaultConfigPath, "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	printSummary(*configPath, cfg)

	asm, err := assemble(cfg)
	if asm.provider != nil { // 停机导出的 defer 注册最早、执行最晚,在途 span 不丢
		defer shutdownTracer(asm.provider)
	}
	if asm.aud != nil {
		defer func() {
			if err := asm.aud.Close(); err != nil {
				log.Printf("警告: 关闭审计文件失败: %v", err)
			}
		}()
	}
	if asm.rec != nil {
		defer func() {
			if err := asm.rec.Close(); err != nil {
				log.Printf("警告: 关闭录制文件失败: %v", err)
			}
		}()
	}
	defer closeClients(asm.clients) // serve 返回(停机)后执行:上游在 server 之后关
	if err != nil {
		return asStartup(err)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return asStartup(fmt.Errorf("监听 %s 失败: %w", cfg.Listen, err))
	}
	fmt.Printf("flamingos 已启动:监听 %s\n", ln.Addr())
	if err := serve(ln, asm.handler, signalStop()); err != nil {
		return asStartup(err) // 监听中断/停机异常:运行期故障,不是配置错误
	}
	return nil
}

// assembled 汇总装配产物:提供能力的四件接线(可观测/策略/审计/录制)、
// 上游客户端与最终 handler。清理责任不随结构体走——shutdownTracer、审计
// Close、录制 Close、closeClients 的 defer 由 run 拿到产物后注册,时机仍
// 是 run 返回。
type assembled struct {
	provider telemetry.Provider // nil = 可观测关闭态;停机导出归 run
	tel      *telemetry.Telemetry
	pol      *policy.Policy // nil = 策略关闭态
	aud      *audit.Recorder
	rec      *recorder.Recorder // nil = 录制关闭态(spec 0008)
	clients  map[string]upstream.Client
	handler  http.Handler
}

// assemble 完成全部能力装配(spec 0004/0005):provider 先于全部 span
// 生产者建立,随后策略与审计构造、上游逐个握手聚合、transport.New 一次
// 接线。任一步失败即返回,已产出的部分产物随结构体带回——run 据非 nil
// 字段仍注册对应清理,失败路径的资源不泄漏。
func assemble(cfg config.Config) (assembled, error) {
	var a assembled

	// 可观测装配(spec 0004):provider 先于全部 span 生产者建立——serve
	// 返回(在途请求排空)与上游关闭都完成后,才做 TracerProvider 的
	// Shutdown flush(defer 在 run 注册),在途 span 不丢。
	provider, err := buildTracerProvider(cfg.Telemetry)
	if err != nil {
		return a, err
	}
	a.provider = provider
	a.tel = telemetry.New(provider) // provider 为 nil 时返回关闭态哨兵,全部挂点不装

	// 策略装配(spec 0005 票 01):判定器在 transport.New 内安装 mcp 层
	// RBAC 判定中间件与 HTTP 角色提取包装;关闭态(pol 为 nil)不装任何挂点。
	a.pol = buildPolicy(cfg.Policy)

	// 审计装配(spec 0005 票 02):审计器在 transport.New 内安装 mcp 层审计
	// 中间件,经 ctx 取角色与决策;构造期即校验审计文件可写,失败=启动失败。
	aud, err := buildAudit(cfg.Policy)
	if err != nil {
		return a, err
	}
	a.aud = aud

	// 录制装配(spec 0008 票 01):录制器在 transport.New 内安装 mcp 层录制
	// 中间件(Receiving 链末位、RBAC 判定内层),经 ctx 取角色、闭包取
	// trace_id;构造期即校验录制文件可写,失败=启动失败。
	rec, err := buildRecorder(cfg.Recorder)
	if err != nil {
		return a, err
	}
	a.rec = rec

	params, err := translateUpstreams(cfg.Upstreams)
	if err != nil {
		return a, err
	}
	a.clients, err = dialUpstreams(context.Background(), params, upstream.WithTelemetry(a.tel))
	if err != nil {
		return a, err
	}

	// 缓存装配(spec 0006 票 02/03):装饰器包装发生在 gateway.New 之前——
	// gateway 拿到的已是包装后的客户端,链上其余环节零改动;关闭态(eng
	// 为 nil)不包装任何上游,行为与未引入 cache 前逐字节一致。角色提取与
	// 命中回调经闭包在此翻译注入(cache 不 import policy/telemetry,audit
	// ctx 键先例:cmd 是唯一相遇点)。
	eng, err := buildCache(cfg.Cache, a.tel)
	if err != nil {
		return a, err
	}
	if eng != nil {
		wrapClients(a.clients, eng)
	}

	a.handler, err = transport.New(context.Background(), gateway.New(a.clients),
		transport.WithTelemetry(a.tel), transport.WithPolicy(a.pol), transport.WithAudit(a.aud),
		transport.WithRecorder(a.rec),
		transport.WithOrigin(buildOrigin(cfg.Policy)), transport.WithRateLimit(buildRateLimit(cfg.Policy)),
		transport.WithListCacheHint(listCacheHint(cfg.Cache)))
	if err != nil {
		return a, err
	}
	return a, nil
}

// exitCode 把 run 的错误映射为进程退出码(纯函数,表驱动测试见 main_test.go):
// 成功归 exitOK;配置加载错误归 exitConfigError;装配/启动阶段错误
// (上游构造、聚合快照、监听)包装为 *startupError,归 exitStartupError。
func exitCode(err error) int {
	if err == nil {
		return exitOK
	}
	var se *startupError
	if errors.As(err, &se) {
		return exitStartupError
	}
	return exitConfigError
}

// printSummary 向标准输出打印装配摘要;渲染逻辑收在纯函数 renderSummary 便于测试。
func printSummary(path string, cfg config.Config) {
	fmt.Print(renderSummary(path, cfg))
}

// renderSummary 渲染中文装配摘要(纯函数):监听地址与上游清单。
// 标签与值之间用"全角冒号 + 空格"分隔——监听地址形如 ":9090" 自带冒号前缀,
// 不留空格会渲染出"监听地址::9090"双冒号(已知瑕疵,此处修正并由测试钉死)。
func renderSummary(path string, cfg config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "flamingos 装配摘要\n")
	fmt.Fprintf(&b, "配置文件: %s\n", path)
	fmt.Fprintf(&b, "监听地址: %s\n", cfg.Listen)
	fmt.Fprintf(&b, "上游清单(共 %d 个):\n", len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		fmt.Fprintf(&b, "  [%d] %s(%s)→ %s,超时 %s\n",
			i, u.Name, u.Type, upstreamTarget(u), u.Timeout)
	}
	return b.String()
}

// upstreamTarget 返回上游的可读目标:http 上游为目标 URL,
// stdio 上游为"命令 + 参数"拼接。
func upstreamTarget(u config.Upstream) string {
	if u.Type == config.TypeHTTP {
		return u.URL
	}
	target := u.Command
	for _, arg := range u.Args {
		target += " " + arg
	}
	return target
}

// upstreamParams 是单个上游的构造参数:config.Upstream 在模块世界的
// 投影,除公共的名称/超时外,按传输类型携带各自的连接参数(http 为目标
// URL,stdio 为命令与参数)。config.Upstream → 构造参数的翻译只发生在
// cmd(架构 §5/D7),upstream 包不认识配置结构。
type upstreamParams struct {
	name     string        // 命名空间名
	kind     string        // 传输类型(config.TypeHTTP | config.TypeStdio)
	endpoint string        // http 上游的目标 URL
	command  string        // stdio 上游的启动命令
	args     []string      // stdio 上游的命令行参数
	timeout  time.Duration // 该上游的调用超时
}

// translateUpstreams 把配置上游条目翻译为构造参数:http 条目投影目标
// URL,stdio 条目投影命令与参数(spec 0003 装配翻译),两类条目在同一
// 配置内混用互不影响。config.Load 已保证 type 白名单与必填字段,此处的
// 未知 type 分支仅为防御性兜底。type 级联与 dialOne 刻意各持一份:
// 翻译是纯函数(可表驱动直测翻译产物),拨号是副作用,合并成单一
// 分派点会把两者搅在一起,伤这条可测性边界。
func translateUpstreams(upstreams []config.Upstream) ([]upstreamParams, error) {
	params := make([]upstreamParams, 0, len(upstreams))
	for _, u := range upstreams {
		switch u.Type {
		case config.TypeHTTP:
			params = append(params, upstreamParams{
				name: u.Name, kind: u.Type, endpoint: u.URL, timeout: u.Timeout,
			})
		case config.TypeStdio:
			params = append(params, upstreamParams{
				name: u.Name, kind: u.Type, command: u.Command, args: u.Args, timeout: u.Timeout,
			})
		default:
			return nil, fmt.Errorf("上游 %q 的 type %q 无法翻译(配置校验应已拦截)", u.Name, u.Type)
		}
	}
	return params, nil
}

// dialUpstreams 逐个构造上游客户端并完成 initialize 握手(架构 §3:Agent
// 侧 Stateless 与 upstream 侧长连接并存):连不上的上游在装配瞬间报错
// 而非首个请求时。任一失败即整体失败,并先关闭本次已建成的客户端,
// 不留孤儿连接。构造参数按类型选择 adapter:http 走 NewHTTP,stdio 走
// NewStdio(spec 0003 装配翻译)。opts 透传给 adapter(如可观测出站
// 接线;不传即关闭态)。
func dialUpstreams(ctx context.Context, params []upstreamParams, opts ...upstream.Option) (map[string]upstream.Client, error) {
	clients := make(map[string]upstream.Client, len(params))
	for _, p := range params {
		client, err := dialOne(ctx, p, opts)
		if err != nil {
			closeClients(clients)
			return nil, err
		}
		clients[p.name] = client
	}
	return clients, nil
}

// dialOne 按上游类型构造对应 adapter;类型非法是翻译层缺陷的信号,
// 防御性报错而非静默跳过(translateUpstreams 应已保证类型白名单)。
// 与 translateUpstreams 的 type 级联刻意不合并:此处是副作用(拨号),
// 彼处是纯函数(表驱动直测);新增类型漏改任一侧都会在此的防御分支
// 显式报错,而非静默错。
func dialOne(ctx context.Context, p upstreamParams, opts []upstream.Option) (upstream.Client, error) {
	switch p.kind {
	case config.TypeHTTP:
		return upstream.NewHTTP(ctx, p.name, p.endpoint, p.timeout, opts...)
	case config.TypeStdio:
		return upstream.NewStdio(ctx, p.name, p.command, p.args, p.timeout, opts...)
	default:
		return nil, fmt.Errorf("上游 %q 的类型 %q 无法拨测(翻译层应已投影为已知类型)", p.name, p.kind)
	}
}

// closeClients 逐个关闭上游客户端:优雅关闭路径上一个上游的关闭失败不应
// 阻断其余上游的清理,也不改写进程退出语义,故仅记录;按命名空间排序
// 保证清理顺序确定、日志可读。
func closeClients(clients map[string]upstream.Client) {
	names := make([]string, 0, len(clients))
	for name := range clients {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := clients[name].Close(); err != nil {
			log.Printf("警告: 关闭上游 %q 失败: %v", name, err)
		}
	}
}

// buildTracerProvider 是 TracerProvider 的单一构造处(spec 0004 Story 23,
// 三形态):telemetry 段缺省 → nil(关闭态,零挂点);段存在 → telemetry
// 包的 OTLP 导出形态。测试注入形态不经此函数——测试直接把含 SpanRecorder
// 的 provider 交给 telemetry.New。OTLP provider 的构造知识(导出器、
// endpoint 规范化、资源、采样)全部收在 telemetry 包,此处只做
// config→构造参数的翻译;关闭态必须返回无类型 nil,telemetry.New 靠
// nil 接口值识别哨兵。
func buildTracerProvider(cfg *config.Telemetry) (telemetry.Provider, error) {
	if cfg == nil {
		return nil, nil
	}
	return telemetry.NewOTLPTracerProvider(context.Background(), telemetry.OTLPConfig{
		Endpoint:    cfg.OTLPEndpoint,
		ServiceName: cfg.ServiceName,
	})
}

// shutdownTracer 在停机路径完成 TracerProvider 的停机导出:
// BatchSpanProcessor 的 Shutdown 先清空在途 span 再关闭导出器(spec 0004
// Story 18)。预算沿用既有 shutdownTimeout 量级;导出失败(如 OTLP 后端
// 不可达)只记日志——可观测链路的故障不反噬退出路径。
func shutdownTracer(tp telemetry.Provider) {
	if tp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := tp.Shutdown(ctx); err != nil {
		log.Printf("警告: 停机导出 span 未完成: %v", err)
	}
}

// buildPolicy 是 RBAC 判定器的单一构造处(与 buildTracerProvider 同位):
// config.Policy → policy.Config 的翻译只发生在 cmd(架构 D7)。段缺省或
// rbac 子段缺省时返回 nil(关闭态哨兵,装配处不装任何挂点,Story 11);
// 规则表逐角色以副本移交,不与配置结构共享底层切片。
func buildPolicy(cfg *config.Policy) *policy.Policy {
	if cfg == nil || cfg.RBAC == nil {
		return nil
	}
	rbac := cfg.RBAC
	roles := make(map[string]policy.RoleRules, len(rbac.Roles))
	for name, rules := range rbac.Roles {
		roles[name] = policy.RoleRules{
			Allow: append([]string(nil), rules.Allow...),
			Deny:  append([]string(nil), rules.Deny...),
		}
	}
	return policy.New(policy.Config{
		RoleHeader:  rbac.RoleHeader,
		DefaultRole: rbac.DefaultRole,
		Roles:       roles,
	})
}

// buildAudit 是审计器的单一构造处(与 buildPolicy 同位):config.Policy →
// audit.Config 的翻译只发生在 cmd(架构 D7)。段缺省或 audit 子段缺省时
// 返回 (nil, nil)(关闭态哨兵,装配处不装任何挂点,Story 11);ctx 键经
// policy 的访问器注入、决策翻译以 policy.Decision 类型断言闭包注入——
// audit 与 policy 互不 import,两包知识只在此处相遇。路径可写性由 audit
// 包构造期校验,失败原样上抛(run 判为启动失败,Story 9)。
func buildAudit(cfg *config.Policy) (*audit.Recorder, error) {
	if cfg == nil || cfg.Audit == nil {
		return nil, nil
	}
	return audit.New(audit.Config{
		Path:        cfg.Audit.Path,
		RoleKey:     policy.RoleContextKey(),
		DecisionKey: policy.DecisionContextKey(),
		DecisionAllowed: func(v any) bool {
			d, ok := v.(policy.Decision)
			return ok && d.Allowed
		},
	})
}

// buildRecorder 是录制器的单一构造处(与 buildAudit 同位,spec 0008 票 01):
// config.Recorder → recorder.Config 的翻译只发生在 cmd(架构 D7)。段缺省
// 时返回 (nil, nil)(关闭态哨兵,装配处不装任何挂点);两路访问器闭包在此
// 注入——角色经 policy 的 ctx 键访问器、trace_id 经 telemetry 只读访问器,
// recorder 与 policy/telemetry 互不 import,三包知识只在此处相遇。路径可写
// 性由 recorder 包构造期校验,失败原样上抛(run 判为启动失败)。
func buildRecorder(cfg *config.Recorder) (*recorder.Recorder, error) {
	if cfg == nil {
		return nil, nil
	}
	return recorder.New(recorder.Config{
		Path:      cfg.Path,
		RoleKey:   policy.RoleContextKey(),
		TraceIDOf: telemetry.TraceIDFromContext,
	})
}

// buildOrigin 是 Origin 允许列表的单一构造处(与 buildPolicy 同位,票
// 0005-03):config.Policy → transport 允许列表的翻译只发生在 cmd(架构
// D7)。段缺省或 origin 子段缺省时返回 nil(关闭态哨兵,WithOrigin 不装配
// 任何挂点,Story 11);列表逐条以副本移交,不与配置结构共享底层切片。
func buildOrigin(cfg *config.Policy) []string {
	if cfg == nil || cfg.Origin == nil {
		return nil
	}
	return append([]string(nil), cfg.Origin.Allowed...)
}

// buildRateLimit 是令牌桶参数的单一构造处(与 buildOrigin 同位):段缺省
// 或 rate_limit 子段缺省时返回零值配置(RPS=0 是 WithRateLimit 认定的关闭
// 态,不装配任何挂点);时钟不在此注入——产件走真实时间,假时钟只由测试
// 直接构造时使用(Story 20)。
func buildRateLimit(cfg *config.Policy) transport.RateLimitConfig {
	if cfg == nil || cfg.RateLimit == nil {
		return transport.RateLimitConfig{}
	}
	return transport.RateLimitConfig{
		RPS:   cfg.RateLimit.RPS,
		Burst: cfg.RateLimit.Burst,
	}
}

// buildCache 是缓存引擎的单一构造处(与 buildPolicy 同位,票 0006-02;
// 票 03 增两条构造注入缝):config.Cache → cache.Options 的翻译只发生在
// cmd(架构 D7)。段缺省或 rules 为空时返回 (nil, nil)(关闭态哨兵,装配
// 处不包装任何上游);规则表逐条以副本移交(不与配置结构共享底层切片),
// ttl_ms 按声明单位毫秒换算为时长。规则语法已由 config 校验保证(设计
// D8),cache.New 的防御性校验失败在此上抛(run 判为启动失败)。时钟不在
// 此注入——产件走真实时间(Now 为 nil 落 time.Now),假时钟只由测试直接
// 构造时使用(Story 20)。两条注入缝的翻译(audit ctx 键先例):
//   - RoleOf:闭包经 policy 的角色访问器从 ctx 取声明——未带角色头的请求
//     已被 transport 的 roleMiddleware 落默认角色(装配恒有角色);缺失
//     (policy 关闭未装提取层)返回 false,cache 按"private 查/写都跳过"
//     处理(宁 miss 不可错 hit,cache.Options 定档注释)。
//   - OnHit:可观测接线非 nil 时闭包接到 telemetry.MarkCacheHit——命中
//     属性落在 ctx 当前请求 span(链最外层 Receiving 中间件开出)上;
//     关闭态不注入(nil 回调,零开销)。
func buildCache(cfg *config.Cache, tel *telemetry.Telemetry) (*cache.Engine, error) {
	if cfg == nil || len(cfg.Rules) == 0 {
		return nil, nil
	}
	rules := make([]cache.Rule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		rules = append(rules, cache.Rule{
			Tools: append([]string(nil), r.Tools...),
			TTL:   time.Duration(r.TTLMS) * time.Millisecond,
			Scope: r.Scope,
		})
	}
	opts := cache.Options{
		Rules:      rules,
		MaxEntries: cfg.MaxEntries,
		RoleOf: func(ctx context.Context) (string, bool) {
			if role := policy.RoleFromContext(ctx); role != "" {
				return role, true
			}
			return "", false
		},
	}
	if tel != nil {
		opts.OnHit = func(ctx context.Context, hit bool) { tel.MarkCacheHit(ctx, hit) }
	}
	return cache.New(opts)
}

// listCacheHint 是 tools/list hint 时长的单一翻译处(票 0006-03):config
// 的声明单位毫秒换算为时长;cache 段缺省(nil)= 0,WithListCacheHint 按
// ≤0 不安装(关闭态零行为,既有 server 行为逐字节不变)。
func listCacheHint(cfg *config.Cache) time.Duration {
	if cfg == nil {
		return 0
	}
	return time.Duration(cfg.ListTTLMS) * time.Millisecond
}

// wrapClients 按命名空间逐个包装上游客户端并原地替换:单一引擎实例在全部
// 命名空间间共享(命中计数全网关一份),装饰器捕获各自的 ns,聚合工具名 =
// ns + "_" + 原始名(架构 §4.2 R2)。必须发生在 gateway.New 之前——gateway
// 构造时防御性拷贝映射,此后替换不再可见。
func wrapClients(clients map[string]upstream.Client, eng *cache.Engine) {
	for ns, client := range clients {
		clients[ns] = eng.Wrap(client, ns)
	}
}

// serve 在 ln 上启动 HTTP 服务并阻塞,直到 stop 关闭或服务意外中断:
// 前者走优雅关闭——停止接收新请求、等待在途请求完成(上限 shutdownTimeout),
// 正常返回 nil;后者(Accept 层错误)原样上抛。信号源由调用方注入。
func serve(ln net.Listener, handler http.Handler, stop <-chan struct{}) error {
	srv := &http.Server{Handler: handler}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case <-stop:
	case err := <-served: // 监听意外中断:不等信号,立即上抛
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("停机未在 %s 内完成: %w", shutdownTimeout, err)
	}
	// Shutdown 之后 Serve 以 ErrServerClosed 返回,视为正常关停;其余错误上抛。
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// signalStop 返回收到 SIGINT/SIGTERM 即关闭的通道,即优雅关闭的信号源。
// NotifyContext 的 cancel 不调用:进程随即退出,无需解除信号监听。
func signalStop() <-chan struct{} {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx.Done()
}
