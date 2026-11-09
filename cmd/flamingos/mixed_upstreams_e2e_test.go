// cmd/flamingos 的装配级测试基础设施与混合上游端到端测试:stdio 夹具
// (internal/upstream/testdata/stdioserver)的一次性构建 helper 在此小规模
// 复制——夹具归 upstream 包所有,不为测试改生产接缝,也不跨包引用其
// 测试内部(spec 0003 Testing Decisions:装配层沿 cmd 既有进程行为接缝)。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/config"
	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioServerBin 是 TestMain 构建出的夹具二进制路径;空串表示构建失败,
// 此时套件已在 TestMain 报错退出。
var stdioServerBin string

func TestMain(m *testing.M) {
	bin, err := buildStdioServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "构建 stdio 夹具失败: %v\n", err)
		os.Exit(1)
	}
	stdioServerBin = bin
	code := m.Run()
	os.RemoveAll(filepath.Dir(bin))
	os.Exit(code)
}

// buildStdioServer 用 go build 把 upstream 包的 stdio 夹具一次性构建为
// 临时二进制,本包测试复用(spec 0003 用户故事 17);与 upstream 包的
// 同名 helper 同款逻辑,构建目标换成自本包出发的相对路径。go 可执行文件
// 优先取 PATH,PATH 无 go 时回退测试二进制自带的 GOROOT(本机 go 不在
// PATH)。
func buildStdioServer() (string, error) {
	dir, err := os.MkdirTemp("", "flamingos-cmd-stdio-fixture-")
	if err != nil {
		return "", fmt.Errorf("创建临时目录: %w", err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	cmd := exec.Command(goBin, "build", "-o", filepath.Join(dir, "stdioserver"),
		"../../internal/upstream/testdata/stdioserver")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("%s: %w\n%s", cmd.String(), err, out)
	}
	return filepath.Join(dir, "stdioserver"), nil
}

// fixturePids 扫描 /proc 进程表,返回 argv[0] 恰为夹具二进制路径的存活
// 进程 PID:"无孤儿"核对直读真实进程表,不依赖已被撤销的客户端句柄。
// 夹具二进制建在一次性临时目录,路径全局唯一,不会误伤无关进程;/proc
// 不可读时返回 nil(本仓目标平台为 Linux,与 processAlive 同款边界)。
func fixturePids(binPath string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // /proc 下的非 PID 条目(如 /proc/self)
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue // 进程恰好在此刻退出
		}
		if argv0, _, _ := strings.Cut(string(data), "\x00"); argv0 == binPath {
			pids = append(pids, pid)
		}
	}
	return pids
}

// gatewayURL 以真实装配路径(翻译 → 拨测 → gateway → transport)把混合
// 上游配置装配为网关,起 httptest 服务并返回端点;上游客户端与测试服务
// 的生命周期挂靠 t.Cleanup。装配即端到端的"被测对象":不注入假客户端。
func gatewayURL(t *testing.T, upstreams []config.Upstream) string {
	t.Helper()
	params, err := translateUpstreams(upstreams)
	if err != nil {
		t.Fatalf("翻译上游配置失败: %v", err)
	}
	clients, err := dialUpstreams(context.Background(), params)
	if err != nil {
		t.Fatalf("拨测上游失败: %v", err)
	}
	t.Cleanup(func() { closeClients(clients) })

	handler, err := transport.New(context.Background(), gateway.New(clients))
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// newAgent 以 go-sdk 客户端连接网关端点并完成 initialize 握手;连接的
// 关闭挂靠 t.Cleanup。
func newAgent(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e-agent", Version: "0.0.1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: endpoint,
			// 无状态网关不维持常驻 SSE 流(与 transport 包测试同款配置)
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// textOf 提取结果中第一段文本内容,便于断言转发内容。
func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<非文本内容>"
}

// toolNames 提取清单的全部名字,用于失败信息展示与顺序断言。
func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestMixedUpstreamsEndToEnd 是混合上游端到端验收(spec 0003 装配与
// 端到端):http 假上游(httptest + go-sdk server)+ stdio 夹具上游经
// 真实装配路径接进网关,Agent 视角走完 initialize → tools/list →
// tools/call,并验证 stdio 子进程死亡后的故障隔离。
func TestMixedUpstreamsEndToEnd(t *testing.T) {
	httpURL := newFakeUpstream(t)
	endpoint := gatewayURL(t, []config.Upstream{
		{Name: "calc", Type: config.TypeHTTP, URL: httpURL, Timeout: dialTimeout},
		{Name: "fs", Type: config.TypeStdio, Command: stdioServerBin, Timeout: dialTimeout},
	})
	agent := newAgent(t, endpoint)

	t.Run("initialize:握手完成并自报 flamingos", func(t *testing.T) {
		initRes := agent.InitializeResult()
		if initRes == nil {
			t.Fatal("连接网关应完成 initialize 握手,InitializeResult 不应为 nil")
		}
		if initRes.ServerInfo == nil || initRes.ServerInfo.Name != "flamingos" {
			t.Errorf("网关应自报 flamingos,实际 %+v", initRes.ServerInfo)
		}
	})

	t.Run("tools/list:两命名空间聚合,描述与 schema 原样", func(t *testing.T) {
		list, err := agent.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("tools/list 失败: %v", err)
		}
		// 命名空间按字典序(calc < fs)由网关保证;上游内部顺序跟随上游
		// 自身清单——go-sdk server 的 tools/list 按名字字典序返回,故 fs
		// 侧为 echo/fail/pid/slow(http 假上游只有 echo,夹具注册四种 tool)。
		wantNames := []string{"calc_echo", "fs_echo", "fs_fail", "fs_pid", "fs_slow"}
		var gotNames []string
		for _, tool := range list.Tools {
			gotNames = append(gotNames, tool.Name)
		}
		if !reflect.DeepEqual(gotNames, wantNames) {
			t.Fatalf("聚合清单不符:\n期望 %v\n实际 %v", wantNames, gotNames)
		}
		byName := make(map[string]*mcp.Tool, len(list.Tools))
		for _, tool := range list.Tools {
			byName[tool.Name] = tool
		}
		if d := byName["calc_echo"].Description; d != "回显原始参数字节" {
			t.Errorf("calc_echo 描述应原样保留,实际 %q", d)
		}
		if d := byName["fs_echo"].Description; d != "回显收到的原始参数字节" {
			t.Errorf("fs_echo 描述应原样保留,实际 %q", d)
		}
		schema, ok := byName["fs_echo"].InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("fs_echo schema 应为线路解码后的 map,实际 %T", byName["fs_echo"].InputSchema)
		}
		if schema["type"] != "object" {
			t.Errorf("fs_echo schema.type 应原样保留为 object,实际 %v", schema["type"])
		}
	})

	t.Run("tools/call 双通道:经网关往返与直连上游一致", func(t *testing.T) {
		// 字面量刻意含中文与紧凑分隔:任何一层重编码都会使回显失真。
		echoArgs := json.RawMessage(`{"msg":"你好,flamingos","n":1}`)

		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_echo", Arguments: echoArgs,
		})
		if err != nil {
			t.Fatalf("经网关调用 calc_echo 失败: %v", err)
		}
		direct := directHTTPCall(t, httpURL, "echo", echoArgs)
		if got := textOf(res); got != direct || got != string(echoArgs) {
			t.Errorf("calc_echo 经网关 = %q,直连 = %q,期望一致且为原始参数字节 %q", got, direct, string(echoArgs))
		}

		res, err = agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: echoArgs,
		})
		if err != nil {
			t.Fatalf("经网关调用 fs_echo 失败: %v", err)
		}
		direct = directStdioCall(t, "echo", echoArgs)
		if got := textOf(res); got != direct || got != string(echoArgs) {
			t.Errorf("fs_echo 经网关 = %q,直连 = %q,期望一致且为原始参数字节 %q", got, direct, string(echoArgs))
		}
	})

	t.Run("业务失败透传:fs_fail 的 IsError=true 应答原样透传,不吞不改不冒错", func(t *testing.T) {
		// 错误契约②的 e2e 闭环:stdio 上游的业务失败是"协议层成功响应 +
		// IsError=true",经网关往返必须同样以结果返回(err 为 nil),此前
		// 双通道只走了 echo 的成功路径。
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_fail", Arguments: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("业务失败应作为结果透传而非报错,实际报错: %v", err)
		}
		if !res.IsError {
			t.Error("res.IsError 应原样保留为 true,实际 false")
		}
		direct := directStdioCall(t, "fail", json.RawMessage(`{}`))
		if got := textOf(res); got != direct || got != "上游业务失败:余额不足" {
			t.Errorf("业务失败内容应原样透传且与直连一致:\n直连 %q\n经网关 %q", direct, got)
		}
	})

	t.Run("故障隔离:杀掉 stdio 子进程后指向它的调用点名不可用,http 上游照常", func(t *testing.T) {
		// 经网关调 fs_pid 拿子进程 PID:夹具的 pid 工具就是为此准备的。
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_pid", Arguments: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("经网关调用 fs_pid 失败: %v", err)
		}
		pid, err := strconv.Atoi(textOf(res))
		if err != nil {
			t.Fatalf("fs_pid 应答应为十进制 PID 文本,实际 %q: %v", textOf(res), err)
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatalf("杀掉 stdio 子进程(PID %d)失败: %v", pid, err)
		}

		// 杀掉到会话读循环察觉 EOF 之间存在微小窗口:轮询直到调用报错,
		// 以观察到的"点名不可用"作为子进程死亡已生效的判据。
		deadline := time.Now().Add(10 * time.Second)
		var callErr error
		for {
			_, callErr = agent.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "fs_echo", Arguments: json.RawMessage(`{}`),
			})
			if callErr != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("杀掉 stdio 子进程后调用仍持续成功,疑似未杀掉")
			}
			time.Sleep(20 * time.Millisecond)
		}
		msg := callErr.Error()
		for _, want := range []string{"fs", "不可用"} {
			if !strings.Contains(msg, want) {
				t.Errorf("错误应点名故障上游与不可用语义(含 %q),实际 %q", want, msg)
			}
		}

		res, err = agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_echo", Arguments: json.RawMessage(`{"alive":true}`),
		})
		if err != nil {
			t.Fatalf("另一上游应照常服务,实际报错: %v", err)
		}
		if got := textOf(res); got != `{"alive":true}` {
			t.Errorf("calc_echo 应正常回显,实际 %q", got)
		}
	})
}

// directHTTPCall 直连 http 假上游调用原始 tool 名,返回结果文本——
// "与直连一致"的独立参照,不经网关链路。
func directHTTPCall(t *testing.T, endpoint, tool string, args json.RawMessage) string {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "direct-client", Version: "0.0.1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:             endpoint,
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("直连上游 %s 失败: %v", endpoint, err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("直连调用 %s 失败: %v", tool, err)
	}
	return textOf(res)
}

// directStdioCall 直连 stdio 夹具调用原始 tool 名,返回结果文本——
// stdio 通道的独立参照:不经网关,也不复用被测装配路径拉起的子进程。
func directStdioCall(t *testing.T, tool string, args json.RawMessage) string {
	t.Helper()
	client, err := upstream.NewStdio(context.Background(), "direct", stdioServerBin, nil, dialTimeout)
	if err != nil {
		t.Fatalf("直连 stdio 夹具失败: %v", err)
	}
	defer func() { _ = client.Close() }()
	res, err := client.CallTool(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("直连调用 %s 失败: %v", tool, err)
	}
	return textOf(res)
}
