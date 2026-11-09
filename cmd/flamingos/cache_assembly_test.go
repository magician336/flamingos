// cmd 的 cache 装配翻译测试(票 0006-02/spec 0006):config.Cache → 缓存
// 引擎的翻译只发生在 cmd(架构 D7);段缺省或 rules 为空 = 关闭态 nil 哨兵
// (装配处不包装任何上游,行为与未引入 cache 前逐字节一致);包装按命名
// 空间逐个进行、单一引擎共享——聚合名命中行为经计数假上游黑盒断言。
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/cache"
	"github.com/magician336/flamingos/internal/config"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// cacheFakeUpstream 是计数假 upstream.Client:固定成功应答,累计 CallTool
// 次数——命中行为的外部判据只有"上游被调次数"。
type cacheFakeUpstream struct {
	calls int
}

func (f *cacheFakeUpstream) ListTools(context.Context) ([]*mcp.Tool, error) {
	return []*mcp.Tool{{Name: "greet", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (f *cacheFakeUpstream) CallTool(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	f.calls++
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
}

func (f *cacheFakeUpstream) Close() error { return nil }

// callTwice 同参调用两次并断言往返成功(计数由调用方读假上游字段)。
func callTwice(t *testing.T, c upstream.Client, name string, args json.RawMessage) {
	t.Helper()
	for range 2 {
		res, err := c.CallTool(context.Background(), name, args)
		if err != nil {
			t.Fatalf("经装饰器调用 %s 应成功,实际报错: %v", name, err)
		}
		if got := textOf(res); got != "pong" {
			t.Errorf("往返内容失真,实际 %q", got)
		}
	}
}

func TestBuildCache(t *testing.T) {
	t.Run("段缺省:关闭态,nil,nil", func(t *testing.T) {
		eng, err := buildCache(nil, nil)
		if err != nil {
			t.Fatalf("段缺省不应报错,实际: %v", err)
		}
		if eng != nil {
			t.Fatal("段缺省应返回 nil 哨兵(不包装任何上游),实际非 nil")
		}
	})

	t.Run("rules 为空:零规则零行为,同样 nil", func(t *testing.T) {
		eng, err := buildCache(&config.Cache{MaxEntries: 64}, nil)
		if err != nil {
			t.Fatalf("rules 为空不应报错,实际: %v", err)
		}
		if eng != nil {
			t.Fatal("rules 为空应返回 nil 哨兵,实际非 nil")
		}
	})

	t.Run("完整段:规则翻译生效——public 命中缓存,private 无角色不缓存", func(t *testing.T) {
		eng, err := buildCache(&config.Cache{
			Rules: []config.CacheRule{
				{Tools: []string{"calc_*"}, TTLMS: 100, Scope: config.ScopePublic},
				{Tools: []string{"priv_*"}, TTLMS: 100, Scope: config.ScopePrivate},
			},
			MaxEntries: config.DefaultCacheMaxEntries,
		}, nil)
		if err != nil {
			t.Fatalf("合法配置不应报错,实际: %v", err)
		}
		if eng == nil {
			t.Fatal("cache 段存在且含规则时应构造引擎,实际 nil")
		}

		// public 规则按翻译后的模式生效:同参二调上游恰 1 次(命中)。
		// public 键不含角色,RoleOf 的有无与返回值都不影响 public 查/写。
		calc := &cacheFakeUpstream{}
		c := eng.Wrap(calc, "calc")
		callTwice(t, c, "greet", json.RawMessage(`{"q":1}`))
		if calc.calls != 1 {
			t.Errorf("翻译后的 public 规则应命中(上游恰 1 次),实际 %d", calc.calls)
		}

		// private 规则:翻译出的 RoleOf 从 ctx 取角色,本测试 ctx 无角色
		// (无 roleMiddleware 写入)——查/写都跳过,宁 miss 不可错 hit。
		priv := &cacheFakeUpstream{}
		p := eng.Wrap(priv, "priv")
		callTwice(t, p, "secret", json.RawMessage(`{"q":1}`))
		if priv.calls != 2 {
			t.Errorf("private 取不到角色应按不缓存处理(上游 2 次),实际 %d", priv.calls)
		}
	})

	t.Run("RoleOf 闭包翻译 policy 角色访问器:同角色 hit、跨角色 miss", func(t *testing.T) {
		eng, err := buildCache(&config.Cache{
			Rules:      []config.CacheRule{{Tools: []string{"priv_*"}, TTLMS: 100, Scope: config.ScopePrivate}},
			MaxEntries: config.DefaultCacheMaxEntries,
		}, nil)
		if err != nil {
			t.Fatalf("合法配置不应报错,实际: %v", err)
		}
		up := &cacheFakeUpstream{}
		c := eng.Wrap(up, "priv")

		args := json.RawMessage(`{"q":1}`)
		// alice 同参二调:角色经 policy.WithRole 写入 ctx,闭包提取——hit。
		_, _ = c.CallTool(policy.WithRole(context.Background(), "alice"), "secret", args)
		_, _ = c.CallTool(policy.WithRole(context.Background(), "alice"), "secret", args)
		if up.calls != 1 {
			t.Fatalf("alice 同参二调应命中缓存(上游恰 1 次),实际 %d", up.calls)
		}
		// bob 同参:键含角色,与 alice 隔离——miss(恰 2 次)。
		_, _ = c.CallTool(policy.WithRole(context.Background(), "bob"), "secret", args)
		if up.calls != 2 {
			t.Errorf("bob 同参调用应 miss 再达上游(恰 2 次),实际 %d", up.calls)
		}
	})

	t.Run("非法模式:防御性报错上抛(config 已校验,不可达路径的兜底)", func(t *testing.T) {
		_, err := buildCache(&config.Cache{
			Rules:      []config.CacheRule{{Tools: []string{"calc_*_x"}, TTLMS: 100, Scope: config.ScopePublic}},
			MaxEntries: config.DefaultCacheMaxEntries,
		}, nil)
		if err == nil {
			t.Fatal("非法模式应报错,实际构造成功")
		}
		if !strings.Contains(err.Error(), "calc_*_x") {
			t.Errorf("错误应点名原模式,实际 %q", err.Error())
		}
	})

	t.Run("OnHit 闭包接到 telemetry:命中与未命中各回调一次,关闭态不注入", func(t *testing.T) {
		// 可观测接线:SpanRecorder 断言命中属性落在 ctx 当前 span 上(真值
		// 形态与产件装配一致——网关请求 span 由链最外层开出,此处以测试
		// span 模拟其 ctx 传递)。
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

		eng, err := buildCache(&config.Cache{
			Rules:      []config.CacheRule{{Tools: []string{"calc_*"}, TTLMS: 100, Scope: config.ScopePublic}},
			MaxEntries: config.DefaultCacheMaxEntries,
		}, telemetry.New(tp))
		if err != nil {
			t.Fatalf("合法配置不应报错,实际: %v", err)
		}
		c := eng.Wrap(&cacheFakeUpstream{}, "calc")

		tr := tp.Tracer("cmd-test")
		// 每次调用各持一个 span(产件形态:一次请求 = 一个网关请求 span;
		// 同 span 重复写同键会被 OTel 的同键覆盖语义折叠,产件路径不发生)。
		ctx1, span1 := tr.Start(context.Background(), "tools/call")
		_, _ = c.CallTool(ctx1, "greet", json.RawMessage(`{"q":1}`)) // 首调 miss → false
		span1.End()
		ctx2, span2 := tr.Start(context.Background(), "tools/call")
		_, _ = c.CallTool(ctx2, "greet", json.RawMessage(`{"q":1}`)) // 二调 hit → true
		span2.End()

		var hits []bool
		for _, s := range sr.Ended() {
			for _, kv := range s.Attributes() {
				if kv.Key == attribute.Key("flamingos.cache.hit") {
					hits = append(hits, kv.Value.AsBool())
				}
			}
		}
		if len(hits) != 2 || hits[0] || !hits[1] {
			t.Errorf("命中属性序列应为 [false true],实际 %v", hits)
		}

		// 关闭态:telemetry.New(nil) 的 nil 哨兵不注入 OnHit——调用照常工作。
		eng2, err := buildCache(&config.Cache{
			Rules:      []config.CacheRule{{Tools: []string{"calc_*"}, TTLMS: 100, Scope: config.ScopePublic}},
			MaxEntries: config.DefaultCacheMaxEntries,
		}, nil)
		if err != nil {
			t.Fatalf("合法配置不应报错,实际: %v", err)
		}
		up := &cacheFakeUpstream{}
		callTwice(t, eng2.Wrap(up, "calc"), "greet", json.RawMessage(`{"q":1}`))
		if up.calls != 1 {
			t.Errorf("nil 接线不应影响缓存行为(上游恰 1 次),实际 %d", up.calls)
		}
	})

	t.Run("listCacheHint 翻译:毫秒换算时长,段缺省为 0(不宣告)", func(t *testing.T) {
		if got := listCacheHint(nil); got != 0 {
			t.Errorf("段缺省应翻译为 0,实际 %v", got)
		}
		if got := listCacheHint(&config.Cache{ListTTLMS: 0}); got != 0 {
			t.Errorf("显式 0 应翻译为 0,实际 %v", got)
		}
		if got := listCacheHint(&config.Cache{ListTTLMS: 2500}); got != 2500*time.Millisecond {
			t.Errorf("毫秒声明单位应换算为时长,实际 %v", got)
		}
	})
}

func TestWrapClients(t *testing.T) {
	t.Run("按命名空间逐个包装:聚合名规则只命中对应命名空间,引擎共享", func(t *testing.T) {
		eng, err := cache.New(cache.Options{
			Rules:      []cache.Rule{{Tools: []string{"a_*"}, TTL: time.Minute, Scope: cache.ScopePublic}},
			MaxEntries: 8,
		})
		if err != nil {
			t.Fatalf("构造引擎失败: %v", err)
		}
		a, b := &cacheFakeUpstream{}, &cacheFakeUpstream{}
		clients := map[string]upstream.Client{"a": a, "b": b}

		wrapClients(clients, eng)

		args := json.RawMessage(`{}`)
		for range 2 { // ns a:a_greet 命中 a_*,同参二调恰穿透 1 次
			if _, err := clients["a"].CallTool(context.Background(), "greet", args); err != nil {
				t.Fatalf("调用应成功,实际报错: %v", err)
			}
		}
		for range 2 { // ns b:b_greet 不命中,两次都穿透
			if _, err := clients["b"].CallTool(context.Background(), "greet", args); err != nil {
				t.Fatalf("调用应成功,实际报错: %v", err)
			}
		}
		if a.calls != 1 {
			t.Errorf("a 命名空间应命中缓存(上游恰 1 次),实际 %d", a.calls)
		}
		if b.calls != 2 {
			t.Errorf("b 命名空间不命中应两次穿透(上游 2 次),实际 %d", b.calls)
		}
	})
}
