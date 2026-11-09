// cache 包的引擎与装饰器测试(spec 0006 Testing Decisions 接缝①③):存储
// 引擎以假时钟表驱动(绝不 sleep,限流假时钟先例),只断言外部行为(命中/
// 未命中、逐出后的可见性、计数快照数字),不断言内部 map 结构;装饰器以计
// 数假 upstream.Client 驱动,只断言上游被调次数与往返内容。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeClock 是可手动推进的假时钟:测试按离散步骤推进,TTL 判定期望值全部
// 手工可算(独立真值:写入时点 + TTL)。
type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0).UTC()}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// mustNew 构造测试用引擎:夹具本应合法,构造失败直接 Fatal。
func mustNew(t *testing.T, opts Options) *Engine {
	t.Helper()
	e, err := New(opts)
	if err != nil {
		t.Fatalf("构造缓存引擎失败: %v", err)
	}
	return e
}

// textResult 造一个单文本内容的成功应答。
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// textOf 提取结果中第一段文本内容,便于断言往返内容。
func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<非文本内容>"
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr string // 空串 = 合法;非空 = 错误信息应包含的片段
	}{
		{
			name:    "max_entries 为 0:容量上限形同虚设,拒绝",
			opts:    Options{Rules: []Rule{{Tools: []string{"*"}, TTL: time.Second, Scope: ScopePublic}}},
			wantErr: "max_entries",
		},
		{
			name:    "max_entries 负数:同样拒绝",
			opts:    Options{Rules: []Rule{{Tools: []string{"*"}, TTL: time.Second, Scope: ScopePublic}}, MaxEntries: -3},
			wantErr: "max_entries",
		},
		{
			name:    "规则模式非法:错误点名原模式",
			opts:    Options{Rules: []Rule{{Tools: []string{"calc_*_x"}, TTL: time.Second, Scope: ScopePublic}}, MaxEntries: 4},
			wantErr: "calc_*_x",
		},
		{
			name:    "ttl 非正:永不生效的规则是误导状态,拒绝",
			opts:    Options{Rules: []Rule{{Tools: []string{"*"}, TTL: 0, Scope: ScopePublic}}, MaxEntries: 4},
			wantErr: "ttl",
		},
		{
			name:    "scope 白名单外:拒绝",
			opts:    Options{Rules: []Rule{{Tools: []string{"*"}, TTL: time.Second, Scope: "internal"}}, MaxEntries: 4},
			wantErr: "scope",
		},
		{
			name:    "规则未声明任何模式:永不命中的死规则,拒绝",
			opts:    Options{Rules: []Rule{{TTL: time.Second, Scope: ScopePublic}}, MaxEntries: 4},
			wantErr: "tools",
		},
		{
			name: "正例:规则与容量齐备(Now 缺省落真实时钟,产件形态)",
			opts: Options{Rules: []Rule{{Tools: []string{"calc_*"}, TTL: time.Second, Scope: ScopePublic}}, MaxEntries: 4},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := New(tt.opts)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("应合法,实际报错: %v", err)
				}
				if e == nil {
					t.Fatal("合法构造应返回引擎,实际 nil")
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错含 %q,实际构造成功", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("错误信息应含 %q,实际 %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestEngineStorage(t *testing.T) {
	t.Run("写入后命中:同键 get 返回写入内容并计一次命中", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 4, Now: clock.Now})
		e.put("k", textResult("v"), time.Minute)

		got, ok := e.get("k")
		if !ok {
			t.Fatal("刚写入的键应命中,实际 miss")
		}
		if textOf(got) != "v" {
			t.Errorf("命中内容失真,实际 %q", textOf(got))
		}
		if st := e.Stats(); st.Hits != 1 || st.Misses != 0 {
			t.Errorf("计数快照 = %+v,期望 Hits 1 / Misses 0", st)
		}
	})

	t.Run("未写入键:miss", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 4, Now: clock.Now})

		if _, ok := e.get("absent"); ok {
			t.Fatal("未写入键应 miss,实际命中")
		}
		if st := e.Stats(); st.Hits != 0 || st.Misses != 1 {
			t.Errorf("计数快照 = %+v,期望 Hits 0 / Misses 1", st)
		}
	})

	t.Run("异参异键:互不串扰", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 4, Now: clock.Now})
		e.put("calc_add\x00a", textResult("sum"), time.Minute)
		e.put("calc_add\x00b", textResult("diff"), time.Minute)

		if got, ok := e.get("calc_add\x00a"); !ok || textOf(got) != "sum" {
			t.Errorf("键 a 应命中 sum,实际 ok=%v text=%q", ok, textOf(got))
		}
		if got, ok := e.get("calc_add\x00b"); !ok || textOf(got) != "diff" {
			t.Errorf("键 b 应命中 diff,实际 ok=%v text=%q", ok, textOf(got))
		}
	})

	t.Run("TTL 惰性过期:拨过 TTL 后 miss,条目被清除(再查仍 miss)", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 4, Now: clock.Now})
		e.put("k", textResult("v"), 50*time.Millisecond)
		clock.Advance(51 * time.Millisecond)

		for i := range 2 {
			if _, ok := e.get("k"); ok {
				t.Fatalf("第 %d 次查询:过期条目应 miss,实际命中", i+1)
			}
		}
		if st := e.Stats(); st.Misses != 2 {
			t.Errorf("过期查询应各计一次 miss,实际 %+v", st)
		}
	})

	t.Run("TTL 边界:恰好到期算过期,到期前一瞬仍命中", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 4, Now: clock.Now})
		e.put("k", textResult("v"), 50*time.Millisecond)

		clock.Advance(49 * time.Millisecond)
		if _, ok := e.get("k"); !ok {
			t.Fatal("到期前一瞬应命中,实际 miss")
		}
		clock.Advance(1 * time.Millisecond) // 恰好 50ms:存活区间 [写入, 写入+TTL)
		if _, ok := e.get("k"); ok {
			t.Fatal("恰好到期应视为过期,实际命中")
		}
	})

	t.Run("容量超限:无过期可清时按最早插入 FIFO 逐出", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 2, Now: clock.Now})
		e.put("a", textResult("1"), time.Hour)
		e.put("b", textResult("2"), time.Hour)
		e.put("c", textResult("3"), time.Hour) // 超限:无过期,逐出最早插入的 a

		if _, ok := e.get("a"); ok {
			t.Error("最早插入的 a 应被 FIFO 逐出,实际命中")
		}
		if _, ok := e.get("b"); !ok {
			t.Error("b 应保留,实际 miss")
		}
		if _, ok := e.get("c"); !ok {
			t.Error("最新插入的 c 应保留,实际 miss")
		}
	})

	t.Run("容量超限:先清一轮已过期,未过期条目免于逐出", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 2, Now: clock.Now})
		e.put("a", textResult("1"), 10*time.Millisecond)
		clock.Advance(20 * time.Millisecond) // a 已过期
		e.put("b", textResult("2"), time.Hour)
		e.put("c", textResult("3"), time.Hour) // 超限:先清过期的 a,b/c 都保留

		if _, ok := e.get("a"); ok {
			t.Error("过期的 a 应被清掉,实际命中")
		}
		if _, ok := e.get("b"); !ok {
			t.Error("先清过期后 b 不应被逐出,实际 miss")
		}
		if _, ok := e.get("c"); !ok {
			t.Error("先清过期后 c 不应被逐出,实际 miss")
		}
	})

	t.Run("同键覆写:更新内容但 FIFO 位次不刷新(FIFO 非 LRU 的分界)", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 2, Now: clock.Now})
		e.put("a", textResult("1"), time.Hour)
		e.put("b", textResult("2"), time.Hour)
		e.put("a", textResult("1x"), time.Hour) // 覆写:内容更新,插入位次仍最早

		if got, ok := e.get("a"); !ok || textOf(got) != "1x" {
			t.Errorf("覆写应更新内容,实际 ok=%v", ok)
		}
		e.put("c", textResult("3"), time.Hour) // 超限:逐出位次最早的 a(覆写未刷新位次)

		if _, ok := e.get("a"); ok {
			t.Error("a 位次最早应被 FIFO 逐出(覆写不刷新位次),实际命中")
		}
		if _, ok := e.get("b"); !ok {
			t.Error("b 位次次早应保留,实际 miss")
		}
		if _, ok := e.get("c"); !ok {
			t.Error("c 最新插入应保留,实际 miss")
		}
	})

	t.Run("计数快照:hits/misses 全程累计正确", func(t *testing.T) {
		clock := newFakeClock()
		e := mustNew(t, Options{MaxEntries: 4, Now: clock.Now})

		_, _ = e.get("x")                      // miss 1:未写入
		e.put("k", textResult("v"), time.Hour) // put 不计数
		_, _ = e.get("k")                      // hit 1
		_, _ = e.get("y")                      // miss 2
		clock.Advance(2 * time.Hour)           // 拨过 TTL
		_, _ = e.get("k")                      // miss 3:过期

		if st := e.Stats(); st.Hits != 1 || st.Misses != 3 {
			t.Errorf("计数快照 = %+v,期望 Hits 1 / Misses 3", st)
		}
	})
}

// roleCtxKey 是 private 键控测试的 ctx 角色键:cache 不认识任何具体键——
// 键与提取闭包都由构造方持有(模拟 cmd 装配把 policy.RoleContextKey 翻译进
// RoleOf 闭包的形态),本包只消费闭包返回值。
type roleCtxKey struct{}

// asRole 把角色声明放入测试 ctx,模拟上游携带角色的请求。
func asRole(role string) context.Context {
	return context.WithValue(context.Background(), roleCtxKey{}, role)
}

// noRole 是恒报取不到角色的 RoleOf 闭包(宁 miss 不可错 hit 的定档测试用)。
func noRole(context.Context) (string, bool) { return "", false }

// roleOfTestCtx 是测试级 RoleOf 闭包:从测试 ctx 提取角色,缺失报 false
// (与 cmd 装配翻译 policy.RoleFromContext 的兜底口径一致)。
func roleOfTestCtx(ctx context.Context) (string, bool) {
	if role, _ := ctx.Value(roleCtxKey{}).(string); role != "" {
		return role, true
	}
	return "", false
}

// fakeUpstream 是计数假 upstream.Client:按模板应答,记录每次收到的原始
// tool 名与参数字节,供断言转发路径与缓存键的参数区分度。
type fakeUpstream struct {
	tools  []*mcp.Tool         // ListTools 预置返回
	res    *mcp.CallToolResult // 每次 CallTool 的应答模板
	err    error               // 非 nil 时每次 CallTool 返回它
	calls  int
	names  []string
	args   []json.RawMessage
	closes int
}

func (f *fakeUpstream) ListTools(context.Context) ([]*mcp.Tool, error) { return f.tools, nil }

func (f *fakeUpstream) CallTool(_ context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	f.calls++
	f.names = append(f.names, name)
	f.args = append(f.args, args)
	return f.res, f.err
}

func (f *fakeUpstream) Close() error {
	f.closes++
	return nil
}

func TestWrapCallTool(t *testing.T) {
	argsA := json.RawMessage(`{"q":"你好"}`)
	argsB := json.RawMessage(`{"q":"世界"}`)

	newCached := func(t *testing.T, clock *fakeClock, up *fakeUpstream, ns string, rules ...Rule) upstream.Client {
		t.Helper()
		e := mustNew(t, Options{Rules: rules, MaxEntries: 8, Now: clock.Now})
		return e.Wrap(up, ns)
	}

	t.Run("同参二调命中:首调 miss 上游 1 次,二调 hit 上游仍 1 次且内容一致", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		c := newCached(t, clock, up, "calc",
			Rule{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic})

		r1, err := c.CallTool(context.Background(), "greet", argsA)
		if err != nil {
			t.Fatalf("首调应成功,实际报错: %v", err)
		}
		r2, err := c.CallTool(context.Background(), "greet", argsA)
		if err != nil {
			t.Fatalf("二调应成功,实际报错: %v", err)
		}
		if up.calls != 1 {
			t.Errorf("同参二调上游应恰被调 1 次,实际 %d", up.calls)
		}
		if textOf(r1) != "pong" || textOf(r2) != "pong" {
			t.Errorf("两次往返内容应一致为 pong,实际 %q / %q", textOf(r1), textOf(r2))
		}
		if up.names[0] != "greet" {
			t.Errorf("上游应收到原始名 greet,实际 %q", up.names[0])
		}
		if string(up.args[0]) != string(argsA) {
			t.Errorf("上游应收到的参数字节失真,实际 %q", up.args[0])
		}
		if st := mustEngine(t, c).Stats(); st.Hits != 1 || st.Misses != 1 {
			t.Errorf("计数快照 = %+v,期望 Hits 1 / Misses 1", st)
		}
	})

	t.Run("异参各自 miss:不同参数不错误命中", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		c := newCached(t, clock, up, "calc",
			Rule{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic})

		_, _ = c.CallTool(context.Background(), "greet", argsA)
		_, _ = c.CallTool(context.Background(), "greet", argsB) // 异参:miss
		_, _ = c.CallTool(context.Background(), "greet", argsA) // 同参 A:命中
		if up.calls != 2 {
			t.Errorf("异参应各自穿透(A/B/A 序列上游恰 2 次),实际 %d", up.calls)
		}
	})

	t.Run("假时钟拨过 TTL:重调 miss 再达上游", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		c := newCached(t, clock, up, "calc",
			Rule{Tools: []string{"calc_*"}, TTL: 50 * time.Millisecond, Scope: ScopePublic})

		_, _ = c.CallTool(context.Background(), "greet", argsA)
		_, _ = c.CallTool(context.Background(), "greet", argsA)
		clock.Advance(51 * time.Millisecond)
		_, _ = c.CallTool(context.Background(), "greet", argsA)
		if up.calls != 2 {
			t.Errorf("拨过 TTL 后重调应再达上游(恰 2 次),实际 %d", up.calls)
		}
	})

	t.Run("err 非 nil 不写入:重复调用每次都穿透", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("ignored"), err: errors.New("boom")}
		c := newCached(t, clock, up, "calc",
			Rule{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic})

		for range 2 {
			if _, err := c.CallTool(context.Background(), "greet", argsA); err == nil {
				t.Fatal("上游报错应原样上抛,实际成功")
			}
		}
		if up.calls != 2 {
			t.Errorf("传输失败不缓存:上游应被调 2 次,实际 %d", up.calls)
		}
		if st := mustEngine(t, c).Stats(); st.Hits != 0 {
			t.Errorf("失败结果不入缓存,实际快照 %+v", st)
		}
	})

	t.Run("IsError=true 不写入:业务失败重复调用每次都穿透", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "业务失败"}}}}
		c := newCached(t, clock, up, "calc",
			Rule{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic})

		for range 2 {
			res, err := c.CallTool(context.Background(), "greet", argsA)
			if err != nil || !res.IsError {
				t.Fatalf("IsError 应答应原样透传(err=%v IsError=%v)", err, res.IsError)
			}
		}
		if up.calls != 2 {
			t.Errorf("业务失败不缓存:上游应被调 2 次,实际 %d", up.calls)
		}
	})

	t.Run("无命中规则:不查不写,每次穿透", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		c := newCached(t, clock, up, "fs",
			Rule{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic})

		for range 2 {
			_, _ = c.CallTool(context.Background(), "echo", argsA)
		}
		if up.calls != 2 {
			t.Errorf("规则未命中(聚合名 fs_echo ≠ calc_*)应每次穿透,实际 %d", up.calls)
		}
		if st := mustEngine(t, c).Stats(); st.Hits != 0 || st.Misses != 0 {
			t.Errorf("未查缓存不应计数,实际快照 %+v", st)
		}
	})

	t.Run("private 未注入 RoleOf:引擎无角色知识,规则命中等同未配置,查/写都跳过", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		c := newCached(t, clock, up, "priv",
			Rule{Tools: []string{"priv_*"}, TTL: time.Minute, Scope: ScopePrivate})

		for range 2 {
			_, _ = c.CallTool(context.Background(), "secret", argsA)
		}
		if up.calls != 2 {
			t.Errorf("private 规则命中但无角色知识时应按不缓存处理,上游应被调 2 次,实际 %d", up.calls)
		}
		if st := mustEngine(t, c).Stats(); st.Hits != 0 || st.Misses != 0 {
			t.Errorf("跳过路径未查缓存不应计数,实际快照 %+v", st)
		}
	})

	t.Run("private 键含角色:同角色二调 hit(上游恰 1 次),跨角色 miss(各上游 1 次)", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		e := mustNew(t, Options{
			Rules:      []Rule{{Tools: []string{"priv_*"}, TTL: time.Minute, Scope: ScopePrivate}},
			MaxEntries: 8,
			Now:        clock.Now,
			RoleOf:     roleOfTestCtx, // 模拟 cmd 装配闭包:ctx 携带的测试级角色键 → 角色
		})
		c := e.Wrap(up, "priv")

		// alice 同参二调:首调 miss 写入,二调 hit——上游恰 1 次。
		r1, err := c.CallTool(asRole("alice"), "secret", argsA)
		if err != nil {
			t.Fatalf("alice 首调应成功,实际报错: %v", err)
		}
		r2, err := c.CallTool(asRole("alice"), "secret", argsA)
		if err != nil {
			t.Fatalf("alice 二调应成功,实际报错: %v", err)
		}
		if up.calls != 1 {
			t.Fatalf("alice 同参二调上游应恰被调 1 次,实际 %d", up.calls)
		}
		if textOf(r1) != "pong" || textOf(r2) != "pong" {
			t.Errorf("两次往返内容应一致为 pong,实际 %q / %q", textOf(r1), textOf(r2))
		}

		// bob 同参首调:键含角色,与 alice 的条目隔离——miss 再达上游(恰 2 次)。
		r3, err := c.CallTool(asRole("bob"), "secret", argsA)
		if err != nil {
			t.Fatalf("bob 首调应成功,实际报错: %v", err)
		}
		if up.calls != 2 {
			t.Fatalf("bob 同参调用应 miss 再达上游(恰 2 次),实际 %d", up.calls)
		}
		if textOf(r3) != "pong" {
			t.Errorf("bob 往返内容失真,实际 %q", textOf(r3))
		}

		// alice 再调:仍命中自己那份(bob 的写入不串 alice 的键域)。
		if _, err := c.CallTool(asRole("alice"), "secret", argsA); err != nil {
			t.Fatalf("alice 再调应成功,实际报错: %v", err)
		}
		if up.calls != 2 {
			t.Errorf("bob 的写入不应影响 alice 命中(上游应仍恰 2 次),实际 %d", up.calls)
		}
		if st := mustEngine(t, c).Stats(); st.Hits != 2 || st.Misses != 2 {
			t.Errorf("计数快照 = %+v,期望 Hits 2 / Misses 2", st)
		}
	})

	t.Run("private 同角色异参:不错误命中", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		e := mustNew(t, Options{
			Rules:      []Rule{{Tools: []string{"priv_*"}, TTL: time.Minute, Scope: ScopePrivate}},
			MaxEntries: 8,
			Now:        clock.Now,
			RoleOf:     roleOfTestCtx, // 固定 ctx 角色由 asRole 携带
		})
		c := e.Wrap(up, "priv")

		_, _ = c.CallTool(asRole("alice"), "secret", argsA)
		_, _ = c.CallTool(asRole("alice"), "secret", argsB) // 异参:miss
		_, _ = c.CallTool(asRole("alice"), "secret", argsA) // 同参:命中
		if up.calls != 2 {
			t.Errorf("private 键的角色前缀不应影响参数区分度(A/B/A 序列上游恰 2 次),实际 %d", up.calls)
		}
	})

	t.Run("private 取不到角色:查/写都跳过(宁 miss 不可错 hit)", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		e := mustNew(t, Options{
			Rules:      []Rule{{Tools: []string{"priv_*"}, TTL: time.Minute, Scope: ScopePrivate}},
			MaxEntries: 8,
			Now:        clock.Now,
			RoleOf:     noRole,
		})
		c := e.Wrap(up, "priv")

		for range 2 {
			if _, err := c.CallTool(context.Background(), "secret", argsA); err != nil {
				t.Fatalf("取不到角色的调用应照常穿透成功,实际报错: %v", err)
			}
		}
		if up.calls != 2 {
			t.Errorf("取不到角色时 private 查/写都应跳过(上游 2 次),实际 %d", up.calls)
		}
		if st := mustEngine(t, c).Stats(); st.Hits != 0 || st.Misses != 0 {
			t.Errorf("跳过路径未查缓存不应计数,实际快照 %+v", st)
		}
	})

	t.Run("public 路径不咨询 RoleOf:角色缺失时 public 照常缓存", func(t *testing.T) {
		// 定档钉死:public 键不含角色,结果对所有调用者一致,RoleOf 的有无
		// 与返回值都不影响 public 查/写(配 cache 未配 policy 的部署 public
		// 缓存不静默失效)。
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		e := mustNew(t, Options{
			Rules:      []Rule{{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic}},
			MaxEntries: 8,
			Now:        clock.Now,
			RoleOf:     noRole,
		})
		c := e.Wrap(up, "calc")

		for range 2 {
			_, _ = c.CallTool(context.Background(), "greet", argsA)
		}
		if up.calls != 1 {
			t.Errorf("public 路径与角色无关,同参二调应命中缓存(上游恰 1 次),实际 %d", up.calls)
		}
	})

	t.Run("OnHit 回调:命中 true/未命中 false 各恰一次,规则未命中与角色跳过不回调", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		var calls []bool
		e := mustNew(t, Options{
			Rules: []Rule{
				{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic},
				{Tools: []string{"priv_*"}, TTL: time.Minute, Scope: ScopePrivate},
			},
			MaxEntries: 8,
			Now:        clock.Now,
			RoleOf:     noRole,
			OnHit:      func(_ context.Context, hit bool) { calls = append(calls, hit) },
		})
		c := e.Wrap(up, "calc")
		p := e.Wrap(up, "priv") // 同一引擎的另一命名空间:命中 priv_* 规则
		u := e.Wrap(up, "fs")   // 同一引擎的未覆盖命名空间:fs_* 无任何规则

		_, _ = c.CallTool(context.Background(), "greet", argsA)  // miss → false
		_, _ = c.CallTool(context.Background(), "greet", argsA)  // hit → true
		_, _ = u.CallTool(context.Background(), "echo", argsA)   // 规则未命中:不回调
		_, _ = p.CallTool(context.Background(), "secret", argsA) // private 无角色跳过:不回调

		if len(calls) != 2 || calls[0] || !calls[1] {
			t.Errorf("回调序列应为 [false true](未查缓存的调用不回调),实际 %v", calls)
		}
	})

	t.Run("OnHit 为 nil:命中路径安全无副作用", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		e := mustNew(t, Options{
			Rules:      []Rule{{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic}},
			MaxEntries: 8,
			Now:        clock.Now,
		})
		c := e.Wrap(up, "calc")

		for range 2 {
			if _, err := c.CallTool(context.Background(), "greet", argsA); err != nil {
				t.Fatalf("未注入 OnHit 时调用应照常工作,实际报错: %v", err)
			}
		}
		if up.calls != 1 {
			t.Errorf("同参二调应照常命中(上游恰 1 次),实际 %d", up.calls)
		}
	})

	t.Run("首条命中生效:前条 private 压过后条 public", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{res: textResult("pong")}
		c := newCached(t, clock, up, "calc",
			Rule{Tools: []string{"calc_add"}, TTL: time.Minute, Scope: ScopePrivate},
			Rule{Tools: []string{"calc_*"}, TTL: time.Minute, Scope: ScopePublic})

		for range 2 {
			_, _ = c.CallTool(context.Background(), "add", argsA) // 首条命中:private,不缓存
		}
		for range 2 {
			_, _ = c.CallTool(context.Background(), "mul", argsA) // 首条未命中,次条 public:缓存
		}
		if up.calls != 3 {
			t.Errorf("add 两次穿透 + mul 二调一 hit = 上游恰 3 次,实际 %d", up.calls)
		}
	})

	t.Run("ListTools 与 Close 原样透传", func(t *testing.T) {
		clock := newFakeClock()
		up := &fakeUpstream{tools: []*mcp.Tool{{Name: "greet"}}}
		c := newCached(t, clock, up, "calc",
			Rule{Tools: []string{"*"}, TTL: time.Minute, Scope: ScopePublic})

		tools, err := c.ListTools(context.Background())
		if err != nil || len(tools) != 1 || tools[0].Name != "greet" {
			t.Errorf("ListTools 应透传上游清单,实际 err=%v tools=%v", err, tools)
		}
		if err := c.Close(); err != nil {
			t.Fatalf("Close 应透传,实际报错: %v", err)
		}
		if up.closes != 1 {
			t.Errorf("Close 应送达内层客户端恰 1 次,实际 %d", up.closes)
		}
	})
}

// mustEngine 从装饰器取回其引擎(测试辅助:装饰器与引擎同包,直取断言
// 计数快照)。
func mustEngine(t *testing.T, c upstream.Client) *Engine {
	t.Helper()
	cc, ok := c.(*cachedClient)
	if !ok {
		t.Fatalf("装饰器应实现为 *cachedClient,实际 %T", c)
	}
	return cc.eng
}
