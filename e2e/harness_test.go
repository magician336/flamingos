// e2e 套件的进程级 harness:真实二进制的拉起、监听等待与收尸。全部判据
// 建立在"进程真正在服务"的黑盒观察上——端口可拨号、进程已退出且退出码
// 可读——不注入任何进程内替身(spec 0007 Testing Decisions:真实进程边界)。
package e2e

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// procWindow 是单条轮询判据的窗口上限:进程拉起与退出的等待都不超过它
// (spec 0007 Testing Decisions:演练轮询窗口设上限,套件时长可控)。
// benchmark 包有同名常量:各包自持、值一致(e2e/benchmark 互不依赖,
// 共享需先立架构布局内共享包),改动须两处同步。
const procWindow = 10 * time.Second

// procOutput 是"进程句柄 + 输出缓冲"的结伴载体:两者同生共死——输出缓
// 冲只能在显式 Kill+Wait 之后读取,否则与 exec 的输出拷贝 goroutine 竞争
// (-race 纪律)。结伴不变量收进一个类型,函数签名不再让二者成对穿行。
type procOutput struct {
	cmd *exec.Cmd        // 进程句柄:存活探测、信号与 Wait
	out *strings.Builder // 合并输出缓冲:失败诊断与退出码判据的旁证
}

// freeAddr 以占位监听取得内核分配的空闲端口后立即关闭,返回可写进配置的
// "127.0.0.1:port"。"占位-关闭"窗口内端口被其他进程抢走是理论竞态,真被
// 占用时网关/夹具起不来,判据以监听超时暴露而非静默错端口。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位分配空闲端口失败: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// waitForListen 轮询直到 addr 可拨号或窗口耗尽:进程"真正在服务"的唯一
// 黑盒判据是端口可连。
func waitForListen(addr string) error {
	deadline := time.Now().Add(procWindow)
	for {
		c, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %s 监听超时(窗口 %s)", addr, procWindow)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// configYAML 渲染最小合法网关配置:listen + upstreams 段,telemetry/
// policy/cache 全部缺省(关闭态,spec 0007 Implementation Decisions 的
// 配置形态)。stdio 上游 command 为一次性临时目录下的夹具二进制绝对路径,
// 不含空白与 YAML 特殊字符,无需引号。
func configYAML(listen string, upstreams ...string) string {
	return fmt.Sprintf("listen: %s\nupstreams:\n%s", listen, strings.Join(upstreams, ""))
}

// httpUpstreamYAML 渲染单个 http 类型上游条目;url 带显式 /mcp 路径,
// 与配置文档样例同形(upstream 客户端原样使用该 endpoint)。
func httpUpstreamYAML(name, url string) string {
	return fmt.Sprintf("  - name: %s\n    type: http\n    url: %s\n    timeout: 5s\n", name, url)
}

// stdioUpstreamYAML 渲染单个 stdio 类型上游条目。
func stdioUpstreamYAML(name, command string) string {
	return fmt.Sprintf("  - name: %s\n    type: stdio\n    command: %s\n    timeout: 5s\n", name, command)
}

// writeConfig 把配置内容写入临时文件,返回文件路径——真实交付形态的
// 第二要素:网关经 -config 读文件,不经任何进程内配置通道。
func writeConfig(t *testing.T, cfg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flamingos.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return path
}

// startHTTPFixture 拉起 HTTP 假上游进程并等其真正监听,返回进程 PID
// (pid 工具的应答可与之精确对照)。杀进程与收尸挂靠 t.Cleanup。
func startHTTPFixture(t *testing.T, addr string) int {
	t.Helper()
	cmd := exec.Command(httpFixtureBin, addr)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉起 HTTP 夹具失败: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // 同时收掉 exec 内部的输出拷贝 goroutine,避免与读 out 竞争
	})
	if err := waitForListen(addr); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("HTTP 夹具未在窗口内监听 %s: %v\n夹具输出:\n%s", addr, err, out.String())
	}
	return cmd.Process.Pid
}

// startGatewayProcess 写临时配置并拉起网关真实二进制,返回结伴载体,供调
// 用方等待监听或断言退出码。兜底清理只发 SIGTERM(退出码判据归调用方);
// 显式 Kill+Wait 后才读 out,避免与 exec 的输出拷贝 goroutine 竞争(-race
// 纪律)。
func startGatewayProcess(t *testing.T, cfg string) *procOutput {
	t.Helper()
	cmd := exec.Command(gatewayBin, "-config", writeConfig(t, cfg))
	proc := &procOutput{cmd: cmd, out: &strings.Builder{}}
	cmd.Stdout, cmd.Stderr = proc.out, proc.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉起网关失败: %v", err)
	}
	t.Cleanup(func() { _ = proc.cmd.Process.Signal(syscall.SIGTERM) })
	return proc
}

// startGateway 拉起网关并等监听就绪,返回对 Agent 的 MCP 端点;停机归
// startGatewayProcess 的兜底清理(LIFO:后注册的网关先于先注册的夹具停,
// 网关持有的上游连接先于夹具进程关闭)。
func startGateway(t *testing.T, addr, cfg string) string {
	t.Helper()
	proc := startGatewayProcess(t, cfg)
	if err := waitForListen(addr); err != nil {
		_ = proc.cmd.Process.Kill()
		_ = proc.cmd.Wait()
		t.Fatalf("网关未在窗口内监听 %s: %v\n网关输出:\n%s", addr, err, proc.out.String())
	}
	return "http://" + addr + "/mcp"
}

// waitExit 等待进程退出(窗口上限)并返回退出码;超时先补刀再 Fatal。
func waitExit(t *testing.T, proc *procOutput) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- proc.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			t.Fatalf("等待进程退出失败: %v", err)
		}
		return 0
	case <-time.After(procWindow):
		_ = proc.cmd.Process.Kill()
		<-done
		t.Fatalf("进程未在窗口内退出(%s)\n输出:\n%s", procWindow, proc.out.String())
	}
	return -1 // 不可达:Fatalf 已 Goexit
}

// runGatewayUntilExit 拉起网关并阻塞至进程退出,返回退出码——退出码三态
// 判据的执行体:退出的进程不会监听,故不等待端口。
func runGatewayUntilExit(t *testing.T, cfg string) int {
	t.Helper()
	return waitExit(t, startGatewayProcess(t, cfg))
}

// connectAgent 以 go-sdk 客户端连接网关端点并完成握手;连接关闭挂靠
// t.Cleanup。protocolVersion 为空串即 SDK 默认(最新 2026-07-28),否则
// 经公开旋钮 ClientSessionOptions.ProtocolVersion 显式指定请求版本
// (go-sdk v1.8.0 mcp/client.go:250)。
func connectAgent(t *testing.T, endpoint, protocolVersion string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e-agent", Version: "0.0.1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: endpoint,
			// 无状态网关不维持常驻 SSE 流(cmd e2e 同款配置)
			DisableStandaloneSSE: true,
		}, &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion})
	if err != nil {
		t.Fatalf("连接网关 %s 失败: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// textOf 提取结果中第一段文本内容,便于断言转发内容(cmd e2e 同款)。
func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<非文本内容>"
}
