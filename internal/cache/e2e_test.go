// cache 的 e2e 接缝测试(票 0006-02/spec 0006 Testing Decisions 接缝⑤):
// 计数假 upstream.Client → cache.Wrap(按命名空间逐个包装,cmd 装配同构)
// → gateway.New → transport.New → HTTP 黑盒断言,只从可观察面(上游被调
// 次数、往返内容、拒绝错误)判定装配正确性。deny 零调用是"装饰器在 RBAC
// 判定内层"的结构判据:拒绝不下探 next,根本到不了装饰器。假时钟拨过 TTL
// 的确定性沿限流假时钟先例(绝不 sleep);关闭态回归由既有全部测试零改动
// 自证,此处补一条引擎 nil 不包装的直通钉。
package cache_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/audit"
	"github.com/magician336/flamingos/internal/cache"
	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// countingUpstream 是计数假 upstream.Client:按模板应答并累计 CallTool
// 次数——缓存命中与否的外部判据只有上游被调次数。
type countingUpstream struct {
	tools []*mcp.Tool
	res   *mcp.CallToolResult
	err   error
	calls int
}

func (f *countingUpstream) ListTools(context.Context) ([]*mcp.Tool, error) { return f.tools, nil }

func (f *countingUpstream) CallTool(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	f.calls++
	return f.res, f.err
}

func (f *countingUpstream) Close() error { return nil }

// fakeClock 是可手动推进的假时钟:TTL 判定期望值全部手工可算。
type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0).UTC()}
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// roleRT 为每个出站请求注入 mcp-role 角色头——模拟在 HTTP 头自报角色的
// Agent(policy_test 先例同款形态)。
type roleRT struct {
	role string
}

func (r roleRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("mcp-role", r.role)
	return http.DefaultTransport.RoundTrip(req)
}

// echoTool 是上游注册的 tool 夹具:object schema,可带任意参数调用。
func echoTool() *mcp.Tool {
	return &mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}
}

// textResult 造一个单文本内容的成功应答。
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// textOf 提取结果中第一段文本内容。
func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<非文本内容>"
}

// connectOptions 汇总 e2e 的可选接线(零值即全部关闭,既有调用零改动):
// 票 03 的命中可观测与 hint 产出沿 cmd 装配同构在此注入。
type connectOptions struct {
	tel  *telemetry.Telemetry // 非 nil = 注入可观测接线(span 断言面)
	aud  *audit.Recorder      // 非 nil = 注入审计接线(审计行断言面)
	hint time.Duration        // >0 = 注入 tools/list hint 产出(ttlMs 断言面)
}

// connectOption 是 connectAgent 的可选接线项。
type connectOption func(*connectOptions)

func withTelemetry(tel *telemetry.Telemetry) connectOption {
	return func(o *connectOptions) { o.tel = tel }
}

func withAudit(aud *audit.Recorder) connectOption {
	return func(o *connectOptions) { o.aud = aud }
}

func withListHint(d time.Duration) connectOption {
	return func(o *connectOptions) { o.hint = d }
}

// connectAgent 完成缓存网关装配并连接 Agent 客户端:clients 按命名空间经
// 引擎逐个 Wrap 后交 gateway.New(eng 为 nil 时不包装,关闭态直通),pol
// 非 nil 时加 RBAC 接线;可选接线经 eopts 注入(可观测/审计/hint)。生命
// 周期全部挂靠 t.Cleanup。
func connectAgent(t *testing.T, eng *cache.Engine, clients map[string]upstream.Client, pol *policy.Policy, role string, eopts ...connectOption) *mcp.ClientSession {
	t.Helper()
	var co connectOptions
	for _, opt := range eopts {
		opt(&co)
	}
	wrapped := make(map[string]upstream.Client, len(clients))
	for ns, c := range clients {
		if eng != nil {
			c = eng.Wrap(c, ns)
		}
		wrapped[ns] = c
	}
	var opts []transport.Option
	if pol != nil {
		opts = append(opts, transport.WithPolicy(pol))
	}
	if co.tel != nil {
		opts = append(opts, transport.WithTelemetry(co.tel))
	}
	if co.aud != nil {
		opts = append(opts, transport.WithAudit(co.aud))
	}
	if co.hint > 0 {
		opts = append(opts, transport.WithListCacheHint(co.hint))
	}
	handler, err := transport.New(context.Background(), gateway.New(wrapped), opts...)
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ct := &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		DisableStandaloneSSE: true,
	}
	if role != "" {
		ct.HTTPClient = &http.Client{Transport: roleRT{role: role}}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e-agent", Version: "0.0.1"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callEcho 经网关调用一次 tool 并断言成功,返回结果文本。
func callEcho(t *testing.T, cs *mcp.ClientSession, tool string, args json.RawMessage) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("调用 %s 失败: %v", tool, err)
	}
	return textOf(res)
}

func TestCacheEndToEnd(t *testing.T) {
	t.Run("同参两次调用上游恰一次;异参各自 miss;拨过 TTL 重调 miss;未覆盖命名空间不受影响", func(t *testing.T) {
		clock := newFakeClock()
		calc := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("calc-pong")}
		fs := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("fs-pong")}
		eng, err := cache.New(cache.Options{
			Rules:      []cache.Rule{{Tools: []string{"calc_*"}, TTL: 50 * time.Millisecond, Scope: cache.ScopePublic}},
			MaxEntries: 8,
			Now:        clock.Now,
		})
		if err != nil {
			t.Fatalf("构造引擎失败: %v", err)
		}
		cs := connectAgent(t, eng, map[string]upstream.Client{"calc": calc, "fs": fs}, nil, "")

		args := json.RawMessage(`{"q":"你好"}`)
		// ① 同参两次:首调 miss,二调 hit——上游恰 1 次,内容一致。
		if got := callEcho(t, cs, "calc_echo", args); got != "calc-pong" {
			t.Errorf("首调内容失真,实际 %q", got)
		}
		if got := callEcho(t, cs, "calc_echo", args); got != "calc-pong" {
			t.Errorf("二调(命中)内容失真,实际 %q", got)
		}
		if calc.calls != 1 {
			t.Fatalf("同参两次调用上游应恰被调 1 次,实际 %d", calc.calls)
		}

		// ② 异参:字节级不同即 miss,各自独立缓存。
		callEcho(t, cs, "calc_echo", json.RawMessage(`{"q":"世界"}`))
		if calc.calls != 2 {
			t.Fatalf("异参应 miss 再达上游(恰 2 次),实际 %d", calc.calls)
		}

		// ③ 假时钟拨过 TTL(50ms):重调 miss 再达上游。
		clock.Advance(60 * time.Millisecond)
		callEcho(t, cs, "calc_echo", args)
		if calc.calls != 3 {
			t.Fatalf("拨过 TTL 后重调应 miss(恰 3 次),实际 %d", calc.calls)
		}

		// ④ fs 无规则命中(fs_echo ≠ calc_*):同参两次各穿透,互不串扰。
		fsArgs := json.RawMessage(`{"q":"你好"}`)
		callEcho(t, cs, "fs_echo", fsArgs)
		callEcho(t, cs, "fs_echo", fsArgs)
		if fs.calls != 2 {
			t.Errorf("未覆盖命名空间应每次穿透(恰 2 次),实际 %d", fs.calls)
		}
	})

	t.Run("err 非 nil 不写入:传输失败重复调用每次都穿透", func(t *testing.T) {
		clock := newFakeClock()
		calc := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("ignored"), err: errors.New("boom")}
		eng, err := cache.New(cache.Options{
			Rules:      []cache.Rule{{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: cache.ScopePublic}},
			MaxEntries: 8,
			Now:        clock.Now,
		})
		if err != nil {
			t.Fatalf("构造引擎失败: %v", err)
		}
		cs := connectAgent(t, eng, map[string]upstream.Client{"calc": calc}, nil, "")

		for range 2 {
			if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "calc_echo", Arguments: json.RawMessage(`{}`),
			}); err == nil {
				t.Fatal("上游传输失败应报错,实际成功")
			}
		}
		if calc.calls != 2 {
			t.Errorf("err≠nil 不缓存:上游应被调 2 次,实际 %d", calc.calls)
		}
	})

	t.Run("IsError=true 不写入:业务失败重复调用每次都穿透且内容原样", func(t *testing.T) {
		clock := newFakeClock()
		calc := &countingUpstream{tools: []*mcp.Tool{echoTool()},
			res: &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "上游业务失败:余额不足"}}}}
		eng, err := cache.New(cache.Options{
			Rules:      []cache.Rule{{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: cache.ScopePublic}},
			MaxEntries: 8,
			Now:        clock.Now,
		})
		if err != nil {
			t.Fatalf("构造引擎失败: %v", err)
		}
		cs := connectAgent(t, eng, map[string]upstream.Client{"calc": calc}, nil, "")

		for range 2 {
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "calc_echo", Arguments: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatalf("IsError 应答应作为结果透传(err 为 nil),实际报错: %v", err)
			}
			if !res.IsError || textOf(res) != "上游业务失败:余额不足" {
				t.Errorf("业务失败内容应原样透传,实际 IsError=%v text=%q", res.IsError, textOf(res))
			}
		}
		if calc.calls != 2 {
			t.Errorf("IsError=true 不缓存:上游应被调 2 次,实际 %d", calc.calls)
		}
	})

	t.Run("deny 调用上游零调用:装饰器在 RBAC 判定内层的结构判据", func(t *testing.T) {
		clock := newFakeClock()
		calc := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("calc-pong")}
		fs := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("fs-pong")}
		eng, err := cache.New(cache.Options{
			Rules:      []cache.Rule{{Tools: []string{"*"}, TTL: time.Minute, Scope: cache.ScopePublic}},
			MaxEntries: 8,
			Now:        clock.Now,
		})
		if err != nil {
			t.Fatalf("构造引擎失败: %v", err)
		}
		// viewer 只放行 calc_*:fs_echo 在 mcp 层被 RBAC 拒绝——拒绝不下探
		// next,包装在 fs 客户端上的装饰器根本到不了,上游零调用。
		pol := policy.New(policy.Config{
			RoleHeader:  "mcp-role",
			DefaultRole: "viewer",
			Roles:       map[string]policy.RoleRules{"viewer": {Allow: []string{"calc_*"}}},
		})
		cs := connectAgent(t, eng, map[string]upstream.Client{"calc": calc, "fs": fs}, pol, "viewer")

		_, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: json.RawMessage(`{}`),
		})
		var wire *jsonrpc.Error
		if !errors.As(err, &wire) {
			t.Fatalf("deny 应为 JSON-RPC 结构化错误,实际 %T: %v", err, err)
		}
		if wire.Code != policy.CodePolicyDenied {
			t.Errorf("错误码 = %d,期望 %d", wire.Code, policy.CodePolicyDenied)
		}
		if fs.calls != 0 {
			t.Errorf("被拒调用不应触及上游(装饰器在判定内层),实际 %d 次", fs.calls)
		}

		// 对照:放行路径在缓存规则下照常工作,同一装配内 calc 命中缓存。
		args := json.RawMessage(`{}`)
		callEcho(t, cs, "calc_echo", args)
		callEcho(t, cs, "calc_echo", args)
		if calc.calls != 1 {
			t.Errorf("放行路径同参二调应命中缓存(上游恰 1 次),实际 %d", calc.calls)
		}
	})

	t.Run("关闭态:引擎 nil 不包装,每次调用都达上游(逐字节现状)", func(t *testing.T) {
		calc := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("pong")}
		cs := connectAgent(t, nil, map[string]upstream.Client{"calc": calc}, nil, "")

		args := json.RawMessage(`{}`)
		callEcho(t, cs, "calc_echo", args)
		callEcho(t, cs, "calc_echo", args)
		if calc.calls != 2 {
			t.Errorf("关闭态不应有缓存行为(上游 2 次),实际 %d", calc.calls)
		}
	})

	t.Run("private 键控:同角色二调 hit(上游 1 次),跨角色 miss(各上游 1 次)", func(t *testing.T) {
		clock := newFakeClock()
		calc := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("priv-pong")}
		// RoleOf 按 cmd 装配同构翻译 policy 角色访问器(cache 包本身不
		// import policy——编译器守门是包级 import,外部测试包不受约束)。
		eng, err := cache.New(cache.Options{
			Rules:      []cache.Rule{{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: cache.ScopePrivate}},
			MaxEntries: 8,
			Now:        clock.Now,
			RoleOf:     policyRoleOf,
		})
		if err != nil {
			t.Fatalf("构造引擎失败: %v", err)
		}
		// 三角色都放行 calc_*(未知角色 fail-close 拒绝,故 alice/bob 必须
		// 在角色表内);role 头经 roleMiddleware 落 ctx(默认角色兜底口径,
		// 与 RBAC 判定同源)。
		pol := policy.New(policy.Config{
			RoleHeader:  "mcp-role",
			DefaultRole: "viewer",
			Roles: map[string]policy.RoleRules{
				"viewer": {Allow: []string{"calc_*"}},
				"alice":  {Allow: []string{"calc_*"}},
				"bob":    {Allow: []string{"calc_*"}},
			},
		})
		args := json.RawMessage(`{}`)

		alice := connectAgent(t, eng, map[string]upstream.Client{"calc": calc}, pol, "alice")
		if got := callEcho(t, alice, "calc_echo", args); got != "priv-pong" {
			t.Errorf("alice 首调内容失真,实际 %q", got)
		}
		if got := callEcho(t, alice, "calc_echo", args); got != "priv-pong" {
			t.Errorf("alice 二调(命中)内容失真,实际 %q", got)
		}
		if calc.calls != 1 {
			t.Fatalf("alice 同参二调上游应恰被调 1 次,实际 %d", calc.calls)
		}

		// bob 同参数:键含角色,与 alice 的条目隔离——miss 再达上游。
		bob := connectAgent(t, eng, map[string]upstream.Client{"calc": calc}, pol, "bob")
		if got := callEcho(t, bob, "calc_echo", args); got != "priv-pong" {
			t.Errorf("bob 首调内容失真,实际 %q", got)
		}
		if calc.calls != 2 {
			t.Errorf("bob 同参调用应 miss 再达上游(恰 2 次),实际 %d", calc.calls)
		}
	})

	t.Run("hint 产出:list_ttl_ms>0 时 tools/list 应答携带 ttlMs;缺省 0 不宣告", func(t *testing.T) {
		calc := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("pong")}
		eng, err := cache.New(cache.Options{
			Rules:      []cache.Rule{{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: cache.ScopePublic}},
			MaxEntries: 8,
			Now:        newFakeClock().Now,
		})
		if err != nil {
			t.Fatalf("构造引擎失败: %v", err)
		}

		// 开启形态:应答 TTL hint = 配置毫秒数,cacheScope 走 SDK normalize
		// 协议缺省 public(清单对所有角色一致,spec 0005 既有裁决)。
		cs := connectAgent(t, eng, map[string]upstream.Client{"calc": calc}, nil, "", withListHint(1500*time.Millisecond))
		list, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("列举 tool 失败: %v", err)
		}
		if got := list.GetTTLMs(); got != 1500 {
			t.Errorf("应答 TTL hint = %d ms,期望 1500", got)
		}
		if got := list.GetCacheScope(); got != "public" {
			t.Errorf("cacheScope = %q,期望 public", got)
		}

		// 关闭形态:缺省 0 不宣告(GetTTLMs==0,协议立即过期语义)。
		cs0 := connectAgent(t, eng, map[string]upstream.Client{"calc": calc}, nil, "")
		list0, err := cs0.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("缺省形态列举 tool 失败: %v", err)
		}
		if got := list0.GetTTLMs(); got != 0 {
			t.Errorf("缺省应不宣告 TTL hint(GetTTLMs==0),实际 %d", got)
		}
	})

	t.Run("命中可观测:命中请求 span 带命中属性、未命中 false;审计照常落 allow 无错误", func(t *testing.T) {
		// 可观测与审计接线按 cmd 装配同构注入;OnHit 闭包同构接到
		// telemetry.MarkCacheHit(cache 不 import telemetry)。
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
		tel := telemetry.New(tp)

		audPath := filepath.Join(t.TempDir(), "audit.jsonl")
		aud, err := audit.New(audit.Config{
			Path:        audPath,
			RoleKey:     policy.RoleContextKey(),
			DecisionKey: policy.DecisionContextKey(),
			DecisionAllowed: func(v any) bool {
				d, ok := v.(policy.Decision)
				return ok && d.Allowed
			},
		})
		if err != nil {
			t.Fatalf("构造审计器失败: %v", err)
		}

		calc := &countingUpstream{tools: []*mcp.Tool{echoTool()}, res: textResult("pong")}
		eng, err := cache.New(cache.Options{
			Rules:      []cache.Rule{{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: cache.ScopePublic}},
			MaxEntries: 8,
			Now:        newFakeClock().Now,
			OnHit:      tel.MarkCacheHit, // cmd 装配闭包的同构形态
		})
		if err != nil {
			t.Fatalf("构造引擎失败: %v", err)
		}
		cs := connectAgent(t, eng, map[string]upstream.Client{"calc": calc}, nil, "",
			withTelemetry(tel), withAudit(aud))

		args := json.RawMessage(`{}`)
		callEcho(t, cs, "calc_echo", args) // miss → span 属性 false
		callEcho(t, cs, "calc_echo", args) // hit → span 属性 true
		if calc.calls != 1 {
			t.Fatalf("同参二调上游应恰被调 1 次(二调命中),实际 %d", calc.calls)
		}

		// span 断言:两次 tools/call 网关请求 span 各带一个 bool 属性,
		// 序列 [false true](两态都写——属性存在性标记"经过缓存层")。
		var hits []bool
		for _, span := range sr.Ended() {
			if span.Name() != "tools/call" || span.SpanKind() != trace.SpanKindServer {
				continue
			}
			for _, kv := range span.Attributes() {
				if kv.Key == attribute.Key("flamingos.cache.hit") {
					hits = append(hits, kv.Value.AsBool())
				}
			}
		}
		if len(hits) != 2 || hits[0] || !hits[1] {
			t.Errorf("命中属性序列应为 [false true],实际 %v", hits)
		}

		// 审计断言:命中调用的审计行照常落(审计在装饰器外层,行为与结果
		// 来源无关)——两行、判定 allow、无错误;Role 为空(RBAC 关闭,ctx
		// 无角色声明,决策缺席按 allow 记录)。
		lines := auditLines(t, audPath)
		if len(lines) != 2 {
			t.Fatalf("两次调用应各落一条审计行,实际 %d 条", len(lines))
		}
		for i, rec := range lines {
			if rec.Tool != "calc_echo" || rec.Decision != audit.DecisionAllow || rec.Error {
				t.Errorf("第 %d 条审计行 = %+v,期望 tool=calc_echo decision=%s error=false",
					i+1, rec, audit.DecisionAllow)
			}
		}
	})
}

// policyRoleOf 是 cmd 装配同构的角色提取闭包:经 policy 的角色访问器从
// ctx 取声明,缺失报 false(cache 侧按"private 查/写都跳过"处理)。
func policyRoleOf(ctx context.Context) (string, bool) {
	if role := policy.RoleFromContext(ctx); role != "" {
		return role, true
	}
	return "", false
}

// auditLines 读取 JSON-lines 审计文件并逐行反序列化(transport audit_test
// 先例同形;空尾行跳过)。
func auditLines(t *testing.T, path string) []audit.Record {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取审计文件失败: %v", err)
	}
	var lines []audit.Record
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec audit.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("审计行反序列化失败(%q): %v", line, err)
		}
		lines = append(lines, rec)
	}
	return lines
}
