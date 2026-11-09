// upstream 包测试:HTTP adapter 对照进程内假上游(go-sdk server +
// Streamable HTTP handler + httptest)验证架构 §4.1 的接口契约,
// 不依赖网络与外部进程(spec 0002 Testing Decisions)。
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testTimeout 是测试用上游超时:远大于进程内往返耗时,避免偶发超时。
const testTimeout = 5 * time.Second

// slowToolDelay 是 slow 工具的固定延迟:远超超时用例给客户端的期限,
// 保证超时必然先于应答触发;客户端断开后服务端按 ctx 取消提前返回。
const slowToolDelay = 300 * time.Millisecond

// newFakeUpstream 搭建进程内假上游并注册测试工具(echo/slow/fail),
// 返回其 HTTP 地址;生命周期挂靠 t.Cleanup。
func newFakeUpstream(t *testing.T) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "回显原始参数字节",
		InputSchema: map[string]any{"type": "object"},
	}, echoHandler)
	server.AddTool(&mcp.Tool{
		Name:        "slow",
		Description: "延迟应答,用于验证客户端超时",
		InputSchema: map[string]any{"type": "object"},
	}, slowHandler)
	server.AddTool(&mcp.Tool{
		Name:        "fail",
		Description: "正常应答但业务失败(IsError)",
		InputSchema: map[string]any{"type": "object"},
	}, failHandler)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// slowHandler 固定延迟后应答;客户端因超时断开时 request ctx 取消,
// 提前返回避免拖住 httptest 清理。
func slowHandler(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(slowToolDelay):
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "迟到的应答"}},
	}, nil
}

// failHandler 模拟"上游正常应答但业务失败":IsError=true 的应答在协议层
// 是成功响应,业务语义由调用方解读——网关必须原样透传(错误契约②)。
func failHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "上游业务失败:余额不足"}},
	}, nil
}

// echoHandler 代理式 handler:上游线路给什么参数字节就回显什么,
// 不反序列化、不重编码(与 spike/sdkapi 的 echo 同款)。
func echoHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}},
	}, nil
}

func TestNewHTTP(t *testing.T) {
	t.Run("握手成功:对可用的 HTTP 上游构造客户端并完成 initialize 握手", func(t *testing.T) {
		url := newFakeUpstream(t)

		client, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err != nil {
			t.Fatalf("期望构造成功,实际报错: %v", err)
		}
		if client == nil {
			t.Fatal("期望返回非 nil 客户端,实际为 nil")
		}
		if err := client.Close(); err != nil {
			t.Fatalf("期望关闭成功,实际报错: %v", err)
		}
	})

	t.Run("连接失败:上游连不上时构造即报携带上游名与原因的 TransportError", func(t *testing.T) {
		url := deadUpstreamURL(t)

		_, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err == nil {
			t.Fatal("期望构造失败,实际成功")
		}

		var terr *TransportError
		if !errors.As(err, &terr) {
			t.Fatalf("期望 *TransportError(errors.As 判别),实际 %T: %v", err, err)
		}
		if terr.Upstream != "calc" {
			t.Errorf("TransportError.Upstream 应点名上游 %q,实际 %q", "calc", terr.Upstream)
		}
		if terr.Cause == nil {
			t.Error("TransportError.Cause 不应为 nil(故障原因必须可诊断)")
		}
		if msg := err.Error(); !strings.Contains(msg, "calc") {
			t.Errorf("错误文案应点名上游名,实际 %q", msg)
		}
	})

	t.Run("timeout 非正:构造参数在装配瞬间被拒,给出中文报错", func(t *testing.T) {
		url := newFakeUpstream(t)

		_, err := NewHTTP(context.Background(), "calc", url, 0)
		if err == nil {
			t.Fatal("期望构造失败,实际成功")
		}
		var terr *TransportError
		if errors.Is(err, ErrClosed) || errors.As(err, &terr) {
			t.Errorf("参数错误不应冒充传输错误或 ErrClosed,实际 %v", err)
		}
		if msg := err.Error(); !strings.Contains(msg, "timeout") {
			t.Errorf("错误文案应指出 timeout 问题,实际 %q", msg)
		}
	})
}

// deadUpstreamURL 返回一个必然连接失败的地址:起真实 httptest server
// 记下端口后立刻关闭,避免碰运气选未监听端口。
func deadUpstreamURL(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close()
	return url
}

func TestListTools(t *testing.T) {
	t.Run("原始清单:返回上游 tool 的名字、描述与 schema,未加命名空间前缀", func(t *testing.T) {
		url := newFakeUpstream(t)
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		defer func() {
			if err := client.Close(); err != nil {
				t.Errorf("关闭客户端失败: %v", err)
			}
		}()

		tools, err := client.ListTools(context.Background())
		if err != nil {
			t.Fatalf("期望列举成功,实际报错: %v", err)
		}
		byName := make(map[string]*mcp.Tool, len(tools))
		for _, tool := range tools {
			byName[tool.Name] = tool
		}
		for _, name := range []string{"echo", "slow", "fail"} {
			if _, ok := byName[name]; !ok {
				t.Errorf("清单应含上游原始 tool %q(不加前缀),实际清单 %v", name, toolNames(tools))
			}
		}
		echo := byName["echo"]
		if echo == nil {
			t.Fatal("清单缺少 echo,无法继续断言描述与 schema")
		}
		if echo.Description != "回显原始参数字节" {
			t.Errorf("tool 描述应原样保留,实际 %q", echo.Description)
		}
		schema, ok := echo.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("schema 应为线路解码后的 map,实际 %T: %+v", echo.InputSchema, echo.InputSchema)
		}
		if schema["type"] != "object" {
			t.Errorf("schema.type 应为 object,实际 %v", schema["type"])
		}
	})
}

// toolNames 提取清单的全部名字,用于失败信息展示。
func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestCallTool(t *testing.T) {
	t.Run("往返成功:以原始 tool 名与原始参数字节调用,结果原样返回", func(t *testing.T) {
		url := newFakeUpstream(t)
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		defer func() {
			if err := client.Close(); err != nil {
				t.Errorf("关闭客户端失败: %v", err)
			}
		}()

		// 字面量刻意选中文 + 紧凑分隔:若被重编码(键序重排、加空格)
		// 或被编成 base64(裸 []byte 的坑,spike 实测),回显必然不同。
		args := json.RawMessage(`{"msg":"你好,flamingos","n":1}`)
		res, err := client.CallTool(context.Background(), "echo", args)
		if err != nil {
			t.Fatalf("期望调用成功,实际报错: %v", err)
		}
		if got := textOf(res); got != string(args) {
			t.Errorf("参数应字节级透传:\n期望 %q\n实际 %q", string(args), got)
		}
	})

	t.Run("超时生效:慢上游在该上游的 timeout 期限内被打断,报携上游名的 TransportError", func(t *testing.T) {
		url := newFakeUpstream(t)
		// 客户端期限 50ms,slow 工具延迟 300ms:超时必然先发生。
		client, err := NewHTTP(context.Background(), "slowpoke", url, 50*time.Millisecond)
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		defer func() {
			if err := client.Close(); err != nil {
				t.Errorf("关闭客户端失败: %v", err)
			}
		}()

		start := time.Now()
		_, err = client.CallTool(context.Background(), "slow", json.RawMessage(`{}`))
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("期望超时报错,实际成功")
		}

		var terr *TransportError
		if !errors.As(err, &terr) {
			t.Fatalf("期望 *TransportError,实际 %T: %v", err, err)
		}
		if terr.Upstream != "slowpoke" {
			t.Errorf("TransportError.Upstream 应点名上游 %q,实际 %q", "slowpoke", terr.Upstream)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("超时应可经 errors.Is 判别(Unwrap 穿透),实际 %v", err)
		}
		if msg := err.Error(); !strings.Contains(msg, "slowpoke") || !strings.Contains(msg, "传输失败") {
			t.Errorf("错误文案应点名上游并说明传输失败(超时单列文案归 Agent 侧,超时判别见上),实际 %q", msg)
		}
		// 期限是硬上限:容许少量调度误差,但必须在 slow 工具应答之前返回。
		if elapsed > slowToolDelay {
			t.Errorf("超时应在该上游 timeout 附近生效,实际耗时 %s(超过 slow 工具延迟 %s)", elapsed, slowToolDelay)
		}

		// 单次调用超时只影响该调用:同一会话的后续调用应仍可用,
		// 挂死上游不拖死指向同一上游的其他请求(故障不放大)。
		res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"after":"timeout"}`))
		if err != nil {
			t.Fatalf("超时后会话应仍可用,实际后续调用报错: %v", err)
		}
		if got := textOf(res); got != `{"after":"timeout"}` {
			t.Errorf("超时后的调用应正常往返,实际回显 %q", got)
		}
	})

	t.Run("业务失败透传:上游 IsError 应答原样返回,不吞不改、不冒充传输错误", func(t *testing.T) {
		url := newFakeUpstream(t)
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		defer func() {
			if err := client.Close(); err != nil {
				t.Errorf("关闭客户端失败: %v", err)
			}
		}()

		res, err := client.CallTool(context.Background(), "fail", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("业务失败应作为结果透传而非报错,实际报错: %v", err)
		}
		if !res.IsError {
			t.Error("res.IsError 应原样保留为 true,实际 false")
		}
		if got := textOf(res); got != "上游业务失败:余额不足" {
			t.Errorf("业务失败内容应原样保留,实际 %q", got)
		}
	})
}

// textOf 提取结果中第一段文本内容,便于断言回显内容。
func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<非文本内容>"
}

func TestClose(t *testing.T) {
	t.Run("Close 后调用:ListTools 与 CallTool 均返回 errors.Is 可判别的 ErrClosed", func(t *testing.T) {
		url := newFakeUpstream(t)
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Fatalf("关闭客户端失败: %v", err)
		}

		if _, err := client.ListTools(context.Background()); !errors.Is(err, ErrClosed) {
			t.Errorf("Close 后 ListTools 应返回 ErrClosed,实际 %v", err)
		}
		_, err = client.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
		if !errors.Is(err, ErrClosed) {
			t.Errorf("Close 后 CallTool 应返回 ErrClosed,实际 %v", err)
		}
	})

	t.Run("Close 幂等:重复关闭不报错", func(t *testing.T) {
		url := newFakeUpstream(t)
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Fatalf("首次关闭失败: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Errorf("重复关闭应返回 nil,实际 %v", err)
		}
	})
}

func TestConcurrentUse(t *testing.T) {
	t.Run("并发调用:同一客户端多 goroutine 混合 ListTools/CallTool,-race 下无竞态且全部成功", func(t *testing.T) {
		url := newFakeUpstream(t)
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		defer func() {
			if err := client.Close(); err != nil {
				t.Errorf("关闭客户端失败: %v", err)
			}
		}()

		// goroutine 数刻意大于典型 GOMAXPROCS,放大 -race 检测窗口。
		const goroutines = 16
		var wg sync.WaitGroup
		errs := make(chan error, goroutines*2)
		for i := range goroutines {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, err := client.ListTools(context.Background()); err != nil {
					errs <- fmt.Errorf("goroutine %d ListTools: %w", i, err)
				}
			}()
			go func() {
				defer wg.Done()
				res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"i":1}`))
				if err != nil {
					errs <- fmt.Errorf("goroutine %d CallTool: %w", i, err)
					return
				}
				if got := textOf(res); got != `{"i":1}` {
					errs <- fmt.Errorf("goroutine %d CallTool 回显失真: %q", i, got)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
	})
}
