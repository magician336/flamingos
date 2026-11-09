// cmd/flamingos 包测试:退出码约定、装配摘要渲染、config→上游构造参数
// 翻译与优雅关闭等接缝,表驱动覆盖;main 本体保持薄装配。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/config"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "成功(nil):退出码 0", err: nil, want: exitOK},
		{
			name: "配置错误:退出码 1",
			err:  errors.New("flamingos.yaml: upstreams[0]: name 不能为空"),
			want: exitConfigError,
		},
		{
			name: "启动错误(上游构造失败):退出码 2",
			err:  &startupError{cause: errors.New(`上游 "calc" 传输失败: connection refused`)},
			want: exitStartupError,
		},
		{
			name: "启动错误(嵌套包装后仍可判别):退出码 2",
			err:  fmt.Errorf("装配阶段: %w", &startupError{cause: errors.New("监听 :8080 失败")}),
			want: exitStartupError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Errorf("exitCode(%v) = %d,期望 %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestExitCodeContract 把退出码约定钉死为 spec 判据字面值:CI 按 0/1/2 判定,
// 常量改值即红,防止约定被悄悄漂移。
func TestExitCodeContract(t *testing.T) {
	if exitOK != 0 {
		t.Errorf("exitOK = %d,约定为 0(成功)", exitOK)
	}
	if exitConfigError != 1 {
		t.Errorf("exitConfigError = %d,约定为 1(配置错误)", exitConfigError)
	}
	if exitStartupError != 2 {
		t.Errorf("exitStartupError = %d,约定为 2(启动失败)", exitStartupError)
	}
}

func TestRenderSummary(t *testing.T) {
	tests := []struct {
		name string
		path string
		cfg  config.Config
		want string // 期望完整摘要输出
	}{
		{
			name: "混合双上游与显式监听地址:摘要逐行正确且无双冒号",
			path: "flamingos.yaml",
			cfg: config.Config{
				Listen: ":9090",
				Upstreams: []config.Upstream{
					{
						Name:    "calc",
						Type:    "http",
						URL:     "http://127.0.0.1:9001/mcp",
						Timeout: 10 * time.Second,
					},
					{
						Name:    "fs",
						Type:    "stdio",
						Command: "npx",
						Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
						Timeout: 30 * time.Second,
					},
				},
			},
			want: "flamingos 装配摘要\n" +
				"配置文件: flamingos.yaml\n" +
				"监听地址: :9090\n" +
				"上游清单(共 2 个):\n" +
				"  [0] calc(http)→ http://127.0.0.1:9001/mcp,超时 10s\n" +
				"  [1] fs(stdio)→ npx -y @modelcontextprotocol/server-filesystem .,超时 30s\n",
		},
		{
			name: "最小单上游默认值:监听 :8080 渲染无双冒号",
			path: "flamingos.yaml",
			cfg: config.Config{
				Listen: ":8080",
				Upstreams: []config.Upstream{
					{
						Name:    "calc",
						Type:    "http",
						URL:     "https://example.com/mcp",
						Timeout: 30 * time.Second,
					},
				},
			},
			want: "flamingos 装配摘要\n" +
				"配置文件: flamingos.yaml\n" +
				"监听地址: :8080\n" +
				"上游清单(共 1 个):\n" +
				"  [0] calc(http)→ https://example.com/mcp,超时 30s\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderSummary(tt.path, tt.cfg)
			if got != tt.want {
				t.Errorf("渲染不符:\n期望:\n%s\n实际:\n%s", tt.want, got)
			}
			// 监听地址值自带冒号前缀(如 ":9090"),分隔符若不留空格会渲染出双冒号。
			if strings.Contains(got, "::") {
				t.Errorf("摘要出现双冒号(标签分隔符需与自带冒号的值隔开):%q", got)
			}
		})
	}
}

func TestTranslateUpstreams(t *testing.T) {
	tests := []struct {
		name      string
		upstreams []config.Upstream
		want      []upstreamParams // 期望翻译产物;wantErr 非空时忽略
		wantErr   []string         // 期望错误文案包含的全部片段
	}{
		{
			name: "HTTP 条目:逐字段翻译为上游构造参数",
			upstreams: []config.Upstream{
				{
					Name:    "calc",
					Type:    config.TypeHTTP,
					URL:     "http://127.0.0.1:9001/mcp",
					Timeout: 10 * time.Second,
				},
				{
					Name:    "fs",
					Type:    config.TypeHTTP,
					URL:     "https://example.com/mcp",
					Timeout: 30 * time.Second,
				},
			},
			want: []upstreamParams{
				{name: "calc", kind: config.TypeHTTP, endpoint: "http://127.0.0.1:9001/mcp", timeout: 10 * time.Second},
				{name: "fs", kind: config.TypeHTTP, endpoint: "https://example.com/mcp", timeout: 30 * time.Second},
			},
		},
		{
			name: "stdio 条目:逐字段投影名称、命令、参数与超时",
			upstreams: []config.Upstream{
				{
					Name:    "fs",
					Type:    config.TypeStdio,
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
					Timeout: 30 * time.Second,
				},
			},
			want: []upstreamParams{
				{
					name:    "fs",
					kind:    config.TypeStdio,
					command: "npx",
					args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
					timeout: 30 * time.Second,
				},
			},
		},
		{
			name: "混合条目:同一配置内 http 与 stdio 各自按类型投影",
			upstreams: []config.Upstream{
				{
					Name:    "calc",
					Type:    config.TypeHTTP,
					URL:     "http://127.0.0.1:9001/mcp",
					Timeout: 10 * time.Second,
				},
				{
					Name:    "local",
					Type:    config.TypeStdio,
					Command: "/usr/local/bin/stdio-server",
					Timeout: 15 * time.Second,
				},
			},
			want: []upstreamParams{
				{name: "calc", kind: config.TypeHTTP, endpoint: "http://127.0.0.1:9001/mcp", timeout: 10 * time.Second},
				{name: "local", kind: config.TypeStdio, command: "/usr/local/bin/stdio-server", timeout: 15 * time.Second},
			},
		},
		{
			name: "未知 type:防御性兜底报错而非静默跳过",
			upstreams: []config.Upstream{
				{
					Name:    "calc",
					Type:    "grpc",
					URL:     "http://127.0.0.1:9001/mcp",
					Timeout: 10 * time.Second,
				},
			},
			wantErr: []string{"calc", "grpc", "无法翻译"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := translateUpstreams(tt.upstreams)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatal("期望翻译失败,实际成功")
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("错误文案应包含 %q,实际 %q", want, err.Error())
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("期望翻译成功,实际报错: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("翻译产物不符:\n期望 %+v\n实际 %+v", tt.want, got)
			}
		})
	}
}

// dialTimeout 是 cmd 测试里上游构造的超时:远大于进程内往返耗时。
const dialTimeout = 5 * time.Second

// newFakeUpstream 在 cmd 测试内起进程内假上游(go-sdk server + Stateless
// Streamable HTTP handler + httptest),注册一个 echo tool;生命周期挂靠
// t.Cleanup,返回端点地址。
func newFakeUpstream(t *testing.T) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "回显原始参数字节",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}},
		}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	t.Cleanup(ts.Close)
	return ts.URL
}

// deadUpstreamURL 返回必然连接失败的地址:起真实 httptest server 记下
// 端口后立刻关闭,不碰运气选未监听端口(与 upstream 包测试同款)。
func deadUpstreamURL() string {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close()
	return url
}

func TestDialUpstreams(t *testing.T) {
	t.Run("全部构造成功:返回以命名空间为键的客户端映射且可用", func(t *testing.T) {
		url := newFakeUpstream(t)

		clients, err := dialUpstreams(context.Background(), []upstreamParams{
			{name: "calc", kind: config.TypeHTTP, endpoint: url, timeout: dialTimeout},
			{name: "fs", kind: config.TypeHTTP, endpoint: url, timeout: dialTimeout},
		})
		if err != nil {
			t.Fatalf("期望构造成功,实际报错: %v", err)
		}
		defer closeClients(clients)
		if len(clients) != 2 {
			t.Fatalf("应构造 2 个客户端,实际 %d", len(clients))
		}
		for _, ns := range []string{"calc", "fs"} {
			if clients[ns] == nil {
				t.Errorf("命名空间 %q 的客户端不应为 nil", ns)
			}
		}
	})

	t.Run("单个上游连不上:构造即败并点名该上游,不带病上线", func(t *testing.T) {
		live := newFakeUpstream(t)

		_, err := dialUpstreams(context.Background(), []upstreamParams{
			{name: "calc", kind: config.TypeHTTP, endpoint: live, timeout: dialTimeout},
			{name: "dead", kind: config.TypeHTTP, endpoint: deadUpstreamURL(), timeout: dialTimeout},
		})
		if err == nil {
			t.Fatal("期望构造失败,实际成功")
		}
		var terr *upstream.TransportError
		if !errors.As(err, &terr) || terr.Upstream != "dead" {
			t.Errorf("错误应点名连不上的上游 dead(TransportError),实际 %v", err)
		}
	})

	t.Run("混合配置全部拨通:http 假上游与 stdio 夹具上游各自构造对应客户端", func(t *testing.T) {
		url := newFakeUpstream(t)

		clients, err := dialUpstreams(context.Background(), []upstreamParams{
			{name: "calc", kind: config.TypeHTTP, endpoint: url, timeout: dialTimeout},
			{name: "fs", kind: config.TypeStdio, command: stdioServerBin, timeout: dialTimeout},
		})
		if err != nil {
			t.Fatalf("期望混合配置拨通,实际报错: %v", err)
		}
		defer closeClients(clients)
		if len(clients) != 2 {
			t.Fatalf("应构造 2 个客户端,实际 %d", len(clients))
		}
		// 可用性各验证一次:http 客户端经进程内假上游,stdio 客户端经真实
		// 子进程返回夹具的四个原始 tool。
		calcTools, err := clients["calc"].ListTools(context.Background())
		if err != nil {
			t.Errorf("calc 客户端应可用,实际列举报错: %v", err)
		} else if len(calcTools) != 1 || calcTools[0].Name != "echo" {
			t.Errorf("calc 应列出假上游的原始 tool echo,实际 %v", toolNames(calcTools))
		}
		fsTools, err := clients["fs"].ListTools(context.Background())
		if err != nil {
			t.Errorf("fs 客户端应可用,实际列举报错: %v", err)
		} else if len(fsTools) != 4 {
			t.Errorf("fs 应列出夹具的四个原始 tool,实际 %v", toolNames(fsTools))
		}
	})

	t.Run("任一失败全撤不留孤儿:stdio 拉起失败时已拨通的子进程被关闭", func(t *testing.T) {
		live := newFakeUpstream(t)
		const ghost = "flamingos-no-such-command"

		_, err := dialUpstreams(context.Background(), []upstreamParams{
			{name: "calc", kind: config.TypeHTTP, endpoint: live, timeout: dialTimeout},
			{name: "fs", kind: config.TypeStdio, command: stdioServerBin, timeout: dialTimeout},
			{name: "ghost", kind: config.TypeStdio, command: ghost, timeout: dialTimeout},
		})
		if err == nil {
			t.Fatal("期望构造失败,实际成功")
		}
		var terr *upstream.TransportError
		if !errors.As(err, &terr) || terr.Upstream != "ghost" {
			t.Errorf("错误应点名拉不起来的上游 ghost(TransportError),实际 %v", err)
		}
		if msg := err.Error(); !strings.Contains(msg, ghost) {
			t.Errorf("错误文案应点名失败的命令 %q,实际 %q", ghost, msg)
		}

		// 全撤的判据是真实进程表:已拨通的 fs 子进程必须已被关闭回收,
		// 不留孤儿(Close 经 SDK 的 cmd.Wait 同步回收,轮询仅作冗余核对)。
		deadline := time.Now().Add(2 * time.Second)
		for len(fixturePids(stdioServerBin)) > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if pids := fixturePids(stdioServerBin); len(pids) > 0 {
			t.Errorf("失败全撤后不应残留夹具子进程,实际存活 PID %v", pids)
		}
	})
}

func TestServe(t *testing.T) {
	t.Run("优雅关闭:stop 关闭后在途请求完成、serve 正常返回", func(t *testing.T) {
		// handler 记录入场、延迟应答:关闭信号到来时请求仍在途,
		// 优雅关闭应等它完成而不是掐断。
		requestStarted := make(chan struct{})
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(requestStarted)
			select {
			case <-r.Context().Done():
			case <-time.After(200 * time.Millisecond):
			}
			fmt.Fprint(w, "done")
		})
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		stop := make(chan struct{})
		served := make(chan error, 1)
		go func() { served <- serve(ln, handler, stop) }()

		type result struct {
			body string
			err  error
		}
		response := make(chan result, 1)
		go func() {
			res, err := http.Post("http://"+ln.Addr().String(), "text/plain", strings.NewReader("ping"))
			if err != nil {
				response <- result{err: err}
				return
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			response <- result{body: string(body), err: err}
		}()

		<-requestStarted // 确保请求已在途再触发关闭
		close(stop)

		if err := <-served; err != nil {
			t.Fatalf("优雅关闭后 serve 应返回 nil,实际 %v", err)
		}
		got := <-response
		if got.err != nil {
			t.Fatalf("在途请求应被等完,实际客户端报错: %v", got.err)
		}
		if got.body != "done" {
			t.Errorf("在途请求应完整应答,实际 %q", got.body)
		}
	})

	t.Run("监听意外中断:serve 上抛 Accept 错误而非静默退出", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		stop := make(chan struct{})
		served := make(chan error, 1)
		go func() { served <- serve(ln, http.NotFoundHandler(), stop) }()

		if err := ln.Close(); err != nil { // 模拟监听层意外故障
			t.Fatalf("关闭 listener 失败: %v", err)
		}
		select {
		case err := <-served:
			if err == nil {
				t.Fatal("监听意外中断应上抛错误,实际 nil")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("serve 未在期限内置错退出,疑似阻塞")
		}
	})
}

// TestSignalStop 以真实信号验证信号源接线:向自身发 SIGTERM,signalStop
// 返回的通道应关闭(NotifyContext 已接管信号,进程不会因此终止)。
func TestSignalStop(t *testing.T) {
	stop := signalStop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("发送 SIGTERM 失败: %v", err)
	}
	select {
	case <-stop:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM 后信号源通道未关闭,接线疑似失效")
	}
}
