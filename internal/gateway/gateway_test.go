// gateway 包测试:唯一公开接缝是 New / HandleToolsList / HandleToolsCall,
// 全部用进程内假 Client 驱动,不碰网络、不启动 httptest(spec 0002 Testing
// Decisions:gateway 测试构造假 map[string]upstream.Client 即可运行)。
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeCall 是假 Client 记录的一次 CallTool 调用:原始 tool 名 + 原始参数字节。
type fakeCall struct {
	name string
	args json.RawMessage
}

// fakeClient 是 upstream.Client 的进程内假实现:按预置清单/结果/错误应答,
// 并记录收到的调用,供断言网关"以原始名、原始参数转发"。
type fakeClient struct {
	mu      sync.Mutex
	tools   []*mcp.Tool         // ListTools 预置返回
	listErr error               // 非 nil 时 ListTools 返回它
	callRes *mcp.CallToolResult // CallTool 预置返回(与 callErr 互斥使用)
	callErr error               // 非 nil 时 CallTool 返回它
	calls   []fakeCall          // CallTool 实际收到的调用记录
}

func (f *fakeClient) ListTools(context.Context) ([]*mcp.Tool, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *fakeClient) CallTool(_ context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{name: name, args: append(json.RawMessage(nil), args...)})
	f.mu.Unlock()
	if f.callErr != nil {
		return nil, f.callErr
	}
	return f.callRes, nil
}

func (f *fakeClient) Close() error { return nil }

// receivedCalls 返回调用记录的副本,锁内读取保证 -race 干净。
func (f *fakeClient) receivedCalls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// toolNames 提取聚合清单的全部名字,按出现序,用于失败信息展示与断言。
func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// textOf 提取结果中第一段文本内容,便于断言透传内容。
func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<非文本内容>"
}

func TestHandleToolsList(t *testing.T) {
	t.Run("聚合成功:多上游清单合并为带命名空间前缀的单份清单,描述与 schema 原样保留", func(t *testing.T) {
		calc := &fakeClient{tools: []*mcp.Tool{
			{Name: "add", Description: "加法", InputSchema: map[string]any{"type": "object"}},
			{Name: "mul", Description: "乘法", InputSchema: map[string]any{"type": "object"}},
		}}
		fs := &fakeClient{tools: []*mcp.Tool{
			{Name: "read", Description: "读文件", InputSchema: map[string]any{"type": "object"}},
		}}
		g := New(map[string]upstream.Client{"calc": calc, "fs": fs})

		tools, err := g.HandleToolsList(context.Background())
		if err != nil {
			t.Fatalf("期望聚合成功,实际报错: %v", err)
		}

		got := toolNames(tools)
		// 命名空间序输出保证清单稳定(map 迭代随机,排序消除抖动)
		want := []string{"calc_add", "calc_mul", "fs_read"}
		if len(got) != len(want) {
			t.Fatalf("聚合清单应含 %d 个 tool,实际 %d 个: %v", len(want), len(got), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("聚合清单第 %d 项应为 %q,实际 %q(全部: %v)", i, want[i], got[i], got)
			}
		}
		add := tools[0]
		if add.Description != "加法" {
			t.Errorf("tool 描述应原样保留,实际 %q", add.Description)
		}
		schema, ok := add.InputSchema.(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Errorf("输入 schema 应原样保留,实际 %#v", add.InputSchema)
		}
		// 网关不得原地修改上游返回的 tool 对象:加前缀必须产出副本
		if calc.tools[0].Name != "add" {
			t.Errorf("上游原始 tool 名被原地改写:期望 add,实际 %q", calc.tools[0].Name)
		}
	})

	t.Run("原始名含下划线:聚合名仍只加一个命名空间前缀(按第一个 _ 拆分才不歧义)", func(t *testing.T) {
		db := &fakeClient{tools: []*mcp.Tool{
			{Name: "query_user", InputSchema: map[string]any{"type": "object"}},
		}}
		g := New(map[string]upstream.Client{"db": db})

		tools, err := g.HandleToolsList(context.Background())
		if err != nil {
			t.Fatalf("期望聚合成功,实际报错: %v", err)
		}
		if got := toolNames(tools); len(got) != 1 || got[0] != "db_query_user" {
			t.Errorf("聚合名应为 db_query_user(原始名的下划线原样保留),实际 %v", got)
		}
	})

	t.Run("任一上游失败即整体失败:错误点名故障上游,不产出半份清单", func(t *testing.T) {
		dead := &fakeClient{listErr: &upstream.TransportError{
			Upstream: "calc",
			Cause:    errors.New("connection refused"),
		}}
		alive := &fakeClient{tools: []*mcp.Tool{{Name: "read"}}}
		g := New(map[string]upstream.Client{"calc": dead, "fs": alive})

		_, err := g.HandleToolsList(context.Background())
		if err == nil {
			t.Fatal("期望整体失败,实际成功")
		}
		if msg := err.Error(); !strings.Contains(msg, "calc") || !strings.Contains(msg, "不可用") {
			t.Errorf("错误应点名上游 calc 并说明不可用,实际 %q", msg)
		}
		var uerr *UpstreamUnavailableError
		if !errors.As(err, &uerr) {
			t.Fatalf("期望 errors.As 判别 *UpstreamUnavailableError,实际 %T: %v", err, err)
		}
		if uerr.Upstream != "calc" {
			t.Errorf("UpstreamUnavailableError.Upstream 应为 calc,实际 %q", uerr.Upstream)
		}
		if uerr.Cause == nil || !strings.Contains(uerr.Cause.Error(), "connection refused") {
			t.Errorf("传输层根因应保留可诊断,实际 %#v", uerr.Cause)
		}
	})

	t.Run("上游已关闭:ErrClosed 经 errors.Is 可判别,且错误点名上游", func(t *testing.T) {
		closed := &fakeClient{listErr: upstream.ErrClosed}
		g := New(map[string]upstream.Client{"calc": closed})

		_, err := g.HandleToolsList(context.Background())
		if err == nil {
			t.Fatal("期望报错,实际成功")
		}
		if !errors.Is(err, upstream.ErrClosed) {
			t.Errorf("应可经 errors.Is 判别 ErrClosed,实际 %v", err)
		}
		if msg := err.Error(); !strings.Contains(msg, "calc") {
			t.Errorf("多上游扇出场景错误应点名上游,实际 %q", msg)
		}
	})

	t.Run("非法聚合名跳过并警示:单个坏 tool 不拖死整体,警示点名上游与原始名", func(t *testing.T) {
		broken := &fakeClient{tools: []*mcp.Tool{
			{Name: "read", InputSchema: map[string]any{"type": "object"}}, // 合法:应保留
			{Name: "wri/te"}, // 含 /:R3,MCP tool 名规范不允许
			{Name: strings.Repeat("x", maxToolNameLen-len("fs_")+1)}, // 超长
			{Name: ""}, // 原始名为空 → 聚合名不可能非空
		}}
		g := New(map[string]upstream.Client{"fs": broken})

		var warning bytes.Buffer
		log.SetOutput(&warning)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })

		tools, err := g.HandleToolsList(context.Background())
		if err != nil {
			t.Fatalf("单个坏 tool 不应拖死聚合,实际报错: %v", err)
		}
		if got := toolNames(tools); len(got) != 1 || got[0] != "fs_read" {
			t.Errorf("清单应只保留合法 tool fs_read,实际 %v", got)
		}
		warn := warning.String()
		for _, want := range []string{"fs", "wri/te", "警告"} {
			if !strings.Contains(warn, want) {
				t.Errorf("警示应包含 %q,实际 %q", want, warn)
			}
		}
	})
}

// fanoutTimeout 是并发扇出测试的上限:实现若退化为串行,先进入的上游会
// 阻塞到该期限并以 ctx 错误返回,测试随之失败而非挂死。
const fanoutTimeout = 5 * time.Second

// barrierClient 阻塞在 ListTools 内直到测试放行:进入即向 entered 发信号,
// 测试收齐全部上游的信号后关闭 release。若网关串行逐个列举,先进入者
// 永远等不到同伴,ctx 到期返回错误——据此判别"未并发扇出"。
type barrierClient struct {
	tool    *mcp.Tool
	entered chan struct{} // 进入 ListTools 的信号(容量 1)
	release chan struct{} // 测试收齐信号后关闭以放行
}

func (b *barrierClient) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return []*mcp.Tool{b.tool}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("未并发扇出:上游在同伴到齐前被挂起至超时: %w", ctx.Err())
	}
}

func (b *barrierClient) CallTool(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, errors.New("本用例不发起调用")
}

func (b *barrierClient) Close() error { return nil }

func TestHandleToolsListFanout(t *testing.T) {
	t.Run("并发扇出:全部上游同时列举而非逐个排队,且结果正确合并", func(t *testing.T) {
		const upstreams = 3
		release := make(chan struct{})
		clients := make(map[string]upstream.Client, upstreams)
		for i := range upstreams {
			clients[fmt.Sprintf("ns-%d", i)] = &barrierClient{
				tool:    &mcp.Tool{Name: fmt.Sprintf("tool%d", i)},
				entered: make(chan struct{}, 1),
				release: release,
			}
		}
		g := New(clients)

		// 收齐全部上游的进入信号后放行:串行实现下这里永远等不齐,
		// 上游将在 fanoutTimeout 后以 ctx 错误使 HandleToolsList 失败。
		go func() {
			for _, c := range clients {
				<-c.(*barrierClient).entered
			}
			close(release)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), fanoutTimeout)
		defer cancel()
		tools, err := g.HandleToolsList(ctx)
		if err != nil {
			t.Fatalf("期望并发列举成功,实际报错: %v", err)
		}
		if got := toolNames(tools); len(got) != upstreams {
			t.Errorf("并发扇出后应合并 %d 个 tool,实际 %v", upstreams, got)
		}
	})
}

func TestHandleToolsCall(t *testing.T) {
	t.Run("路由转发:按第一个下划线拆出命名空间,以原始名与原始参数字节调用,结果原样返回", func(t *testing.T) {
		calc := &fakeClient{
			tools:   []*mcp.Tool{{Name: "add"}},
			callRes: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "42"}}},
		}
		fs := &fakeClient{tools: []*mcp.Tool{{Name: "read"}}}
		g := New(map[string]upstream.Client{"calc": calc, "fs": fs})

		// 参数字节刻意含中文与紧凑分隔:被重编码则回环失真
		args := json.RawMessage(`{"a":1,"msg":"你好"}`)
		res, err := g.HandleToolsCall(context.Background(), "calc_add", args)
		if err != nil {
			t.Fatalf("期望转发成功,实际报错: %v", err)
		}

		calls := calc.receivedCalls()
		if len(calls) != 1 {
			t.Fatalf("calc 应被调用恰好 1 次,实际 %d 次", len(calls))
		}
		if calls[0].name != "add" {
			t.Errorf("上游应收到的原始名(前缀已拆掉),期望 add,实际 %q", calls[0].name)
		}
		if string(calls[0].args) != string(args) {
			t.Errorf("参数应字节级透传:\n期望 %q\n实际 %q", string(args), string(calls[0].args))
		}
		if got := textOf(res); got != "42" {
			t.Errorf("上游结果应原样返回,实际 %q", got)
		}
		if len(fs.receivedCalls()) != 0 {
			t.Errorf("前缀指向 calc,fs 不应被调用,实际收到 %v", fs.receivedCalls())
		}
	})

	t.Run("原始名含下划线:第一个下划线后才全是原始名,不二次拆分", func(t *testing.T) {
		db := &fakeClient{
			tools:   []*mcp.Tool{{Name: "query_user"}},
			callRes: &mcp.CallToolResult{},
		}
		g := New(map[string]upstream.Client{"db": db})

		_, err := g.HandleToolsCall(context.Background(), "db_query_user", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("期望转发成功,实际报错: %v", err)
		}
		calls := db.receivedCalls()
		if len(calls) != 1 || calls[0].name != "query_user" {
			t.Errorf("上游应收到完整原始名 query_user,实际 %#v", calls)
		}
	})

	t.Run("非法调用名:表驱动", func(t *testing.T) {
		calc := &fakeClient{tools: []*mcp.Tool{{Name: "add"}}}
		g := New(map[string]upstream.Client{"calc": calc})
		tests := []struct {
			name    string   // 用例名(中文,描述用户可见行为)
			tool    string   // 传给 HandleToolsCall 的 tool 名
			wantErr []string // 错误文案应包含的子串
		}{
			{
				name:    "名字无下划线:说明命名空间前缀规则",
				tool:    "add",
				wantErr: []string{"add", "命名空间", "下划线"},
			},
			{
				name:    "前缀无匹配:点名未知的命名空间",
				tool:    "nosuch_add",
				wantErr: []string{"nosuch", "无匹配上游"},
			},
			{
				name:    "空前缀:空命名空间同样无匹配",
				tool:    "_add",
				wantErr: []string{"无匹配上游"},
			},
			{
				name:    "空原始名:说明完整名形状,不误路由到上游",
				tool:    "calc_",
				wantErr: []string{"calc_", "原始名为空"},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := g.HandleToolsCall(context.Background(), tt.tool, json.RawMessage(`{}`))
				if err == nil {
					t.Fatalf("期望报错,实际成功")
				}
				for _, substr := range tt.wantErr {
					if !strings.Contains(err.Error(), substr) {
						t.Errorf("错误信息缺少 %q:实际 %q", substr, err.Error())
					}
				}
				if calls := calc.receivedCalls(); len(calls) != 0 {
					t.Errorf("非法名不得触碰任何上游,实际 calc 收到 %v", calls)
				}
			})
		}
	})

	t.Run("上游不可用:TransportError 映射为点名上游的不可用错误,根因可穿透判别", func(t *testing.T) {
		dead := &fakeClient{
			tools:   []*mcp.Tool{{Name: "add"}},
			callErr: &upstream.TransportError{Upstream: "calc", Cause: errors.New("connection reset")},
		}
		g := New(map[string]upstream.Client{"calc": dead})

		_, err := g.HandleToolsCall(context.Background(), "calc_add", json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("期望报错,实际成功")
		}
		var uerr *UpstreamUnavailableError
		if !errors.As(err, &uerr) {
			t.Fatalf("期望 errors.As 判别 *UpstreamUnavailableError,实际 %T: %v", err, err)
		}
		if uerr.Upstream != "calc" {
			t.Errorf("Upstream 应点名 calc,实际 %q", uerr.Upstream)
		}
		if msg := err.Error(); !strings.Contains(msg, "calc") || !strings.Contains(msg, "不可用") {
			t.Errorf("错误文案应点名上游并说明不可用,实际 %q", msg)
		}
	})

	t.Run("上游超时:不可用文案单列超时,且 errors.Is 可穿透判别 DeadlineExceeded", func(t *testing.T) {
		slow := &fakeClient{
			tools:   []*mcp.Tool{{Name: "add"}},
			callErr: &upstream.TransportError{Upstream: "calc", Cause: context.DeadlineExceeded},
		}
		g := New(map[string]upstream.Client{"calc": slow})

		_, err := g.HandleToolsCall(context.Background(), "calc_add", json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("期望报错,实际成功")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("超时应可经 errors.Is 穿透判别,实际 %v", err)
		}
		if msg := err.Error(); !strings.Contains(msg, "calc") || !strings.Contains(msg, "超时") {
			t.Errorf("错误文案应点名上游并单列超时,实际 %q", msg)
		}
	})

	t.Run("业务失败透传:IsError 应答是成功调用的结果,原样返回不报错", func(t *testing.T) {
		failing := &fakeClient{
			tools: []*mcp.Tool{{Name: "charge"}},
			callRes: &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "余额不足"}},
			},
		}
		g := New(map[string]upstream.Client{"calc": failing})

		res, err := g.HandleToolsCall(context.Background(), "calc_charge", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("IsError 是成功应答,不应报错,实际: %v", err)
		}
		if !res.IsError {
			t.Error("res.IsError 应原样保留为 true")
		}
		if got := textOf(res); got != "余额不足" {
			t.Errorf("业务失败内容应原样保留,实际 %q", got)
		}
	})

	t.Run("上游已关闭:ErrClosed 原样上抛,可经 errors.Is 判别", func(t *testing.T) {
		closed := &fakeClient{
			tools:   []*mcp.Tool{{Name: "add"}},
			callErr: upstream.ErrClosed,
		}
		g := New(map[string]upstream.Client{"calc": closed})

		_, err := g.HandleToolsCall(context.Background(), "calc_add", json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("期望报错,实际成功")
		}
		if !errors.Is(err, upstream.ErrClosed) {
			t.Errorf("应原样上抛 ErrClosed(errors.Is 判别),实际 %T: %v", err, err)
		}
		if msg := err.Error(); !strings.Contains(msg, "已关闭") {
			t.Errorf("ErrClosed 文案应保留上游包原样语义,实际 %q", msg)
		}
	})
}

func TestNew(t *testing.T) {
	t.Run("防御性拷贝:构造后调用方改动原映射不影响网关", func(t *testing.T) {
		calc := &fakeClient{tools: []*mcp.Tool{{Name: "add"}}}
		clients := map[string]upstream.Client{"calc": calc}
		g := New(clients)

		// 构造后向原映射塞入新上游并删掉 calc:网关视角应无变化
		clients["ghost"] = &fakeClient{tools: []*mcp.Tool{{Name: "boom"}}}
		delete(clients, "calc")

		tools, err := g.HandleToolsList(context.Background())
		if err != nil {
			t.Fatalf("期望聚合成功,实际报错: %v", err)
		}
		if got := toolNames(tools); len(got) != 1 || got[0] != "calc_add" {
			t.Errorf("网关应只见到构造时的 calc,实际 %v", got)
		}
		if _, err := g.HandleToolsCall(context.Background(), "ghost_boom", json.RawMessage(`{}`)); err == nil {
			t.Error("构造后注入的 ghost 不应可路由")
		}
	})

	t.Run("空映射:网关零上游,聚合返回空清单而非报错", func(t *testing.T) {
		g := New(nil)
		tools, err := g.HandleToolsList(context.Background())
		if err != nil {
			t.Fatalf("零上游应返回空清单而非报错,实际: %v", err)
		}
		if len(tools) != 0 {
			t.Errorf("期望空清单,实际 %v", toolNames(tools))
		}
	})
}
