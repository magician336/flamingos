// stdio adapter 测试:对照真实子进程夹具(testdata/stdioserver,TestMain
// 一次性构建为临时二进制)验证 Client 接口契约在 stdio 传输上的第二个
// 实现,与 HTTP adapter 的测试同构(spec 0003 Testing Decisions:测真实
// stdio 路径而非模拟管道)。
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioServerBin 是 TestMain 构建出的夹具二进制路径;空串表示构建失败,
// 此时套件已在 TestMain 报错退出。
var stdioServerBin string

// wantPinnedVersion 是协议 Pin 的期望值:刻意写字面量而非引用实现的
// pinnedProtocolVersion 常量——期望值必须来自独立出处(spec 0003 用户
// 故事 14),Pin 常量的任何改动都会先红在这里。
const wantPinnedVersion = "2025-11-25"

// stdioSlowToolDelay 镜像夹具包内 slow 工具的延迟:夹具是独立 main 包,
// 常量无法跨包共享;两处若改动必须同步(改动夹具行为属新决定,应过评审)。
const stdioSlowToolDelay = 3 * time.Second

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

// buildStdioServer 用 go build 把 testdata/stdioserver 一次性构建为临时
// 二进制,全部测试复用(spec 0003 用户故事 17)。go 可执行文件优先取
// PATH,PATH 无 go 时回退测试二进制自带的 GOROOT(本机 go 不在 PATH)。
func buildStdioServer() (string, error) {
	dir, err := os.MkdirTemp("", "flamingos-stdio-fixture-")
	if err != nil {
		return "", fmt.Errorf("创建临时目录: %w", err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	cmd := exec.Command(goBin, "build", "-o", filepath.Join(dir, "stdioserver"), "./testdata/stdioserver")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("%s: %w\n%s", cmd.String(), err, out)
	}
	return filepath.Join(dir, "stdioserver"), nil
}

// newStdioClient 构造指向夹具二进制的 stdio 客户端,失败即 t.Fatal;
// 生命周期由调用方 Close。
func newStdioClient(t *testing.T, name string, timeout time.Duration) Client {
	t.Helper()
	client, err := NewStdio(context.Background(), name, stdioServerBin, nil, timeout)
	if err != nil {
		t.Fatalf("构造 stdio 客户端失败: %v", err)
	}
	return client
}

func TestNewStdio(t *testing.T) {
	t.Run("握手成功:拉起子进程完成 initialize 握手,协商版本固定为 2025-11-25", func(t *testing.T) {
		client, err := NewStdio(context.Background(), "fs", stdioServerBin, nil, testTimeout)
		if err != nil {
			t.Fatalf("期望构造成功,实际报错: %v", err)
		}
		if client == nil {
			t.Fatal("期望返回非 nil 客户端,实际为 nil")
		}
		defer func() {
			if err := client.Close(); err != nil {
				t.Errorf("关闭客户端失败: %v", err)
			}
		}()

		// 同包直读内部会话的协商结果(接口不加断言专用的方法):
		// Pin 旋钮是否生效只有协商结果能作证(spec 0003 用户故事 11)。
		sc, ok := client.(*stdioClient)
		if !ok {
			t.Fatalf("期望 *stdioClient 实现,实际 %T", client)
		}
		initRes := sc.session.InitializeResult()
		if initRes == nil {
			t.Fatal("握手成功后 InitializeResult 不应为 nil")
		}
		if got := initRes.ProtocolVersion; got != wantPinnedVersion {
			t.Errorf("协商版本应固定为 %s,实际 %s(Pin 失效会触发 go-sdk #1332 的握手卡死)", wantPinnedVersion, got)
		}
	})

	t.Run("命令不存在:构造即报 TransportError,点名上游与命令", func(t *testing.T) {
		const ghost = "flamingos-no-such-command"

		_, err := NewStdio(context.Background(), "fs", ghost, nil, testTimeout)
		if err == nil {
			t.Fatal("期望构造失败,实际成功")
		}

		var terr *TransportError
		if !errors.As(err, &terr) {
			t.Fatalf("期望 *TransportError(errors.As 判别),实际 %T: %v", err, err)
		}
		if terr.Upstream != "fs" {
			t.Errorf("TransportError.Upstream 应点名上游 %q,实际 %q", "fs", terr.Upstream)
		}
		if terr.Cause == nil {
			t.Error("TransportError.Cause 不应为 nil(故障原因必须可诊断)")
		}
		if msg := err.Error(); !strings.Contains(msg, "fs") || !strings.Contains(msg, ghost) {
			t.Errorf("错误文案应点名上游与命令,实际 %q", msg)
		}
	})

	t.Run("命令不可执行:无执行权限的文件同样启动即败,报错点名命令", func(t *testing.T) {
		script := filepath.Join(t.TempDir(), "not-executable")
		if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatalf("写夹具脚本失败: %v", err)
		}

		_, err := NewStdio(context.Background(), "fs", script, nil, testTimeout)
		if err == nil {
			t.Fatal("期望构造失败,实际成功")
		}

		var terr *TransportError
		if !errors.As(err, &terr) {
			t.Fatalf("期望 *TransportError,实际 %T: %v", err, err)
		}
		if msg := err.Error(); !strings.Contains(msg, script) {
			t.Errorf("错误文案应点名命令路径,实际 %q", msg)
		}
	})

	t.Run("timeout 非正:构造参数在装配瞬间被拒,给出中文报错", func(t *testing.T) {
		_, err := NewStdio(context.Background(), "fs", stdioServerBin, nil, 0)
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

func TestStdioListTools(t *testing.T) {
	t.Run("原始清单:返回上游全部 tool 的名字与描述,未加命名空间前缀", func(t *testing.T) {
		client := newStdioClient(t, "fs", testTimeout)
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
		for _, name := range []string{"echo", "slow", "fail", "pid"} {
			if _, ok := byName[name]; !ok {
				t.Errorf("清单应含上游原始 tool %q(不加前缀),实际清单 %v", name, toolNames(tools))
			}
		}
		echo := byName["echo"]
		if echo == nil {
			t.Fatal("清单缺少 echo,无法继续断言描述")
		}
		if echo.Description != "回显收到的原始参数字节" {
			t.Errorf("tool 描述应原样保留,实际 %q", echo.Description)
		}
	})
}

func TestStdioCallTool(t *testing.T) {
	t.Run("往返成功:以原始 tool 名与原始参数字节经子进程往返,结果原样返回", func(t *testing.T) {
		client := newStdioClient(t, "fs", testTimeout)
		defer func() {
			if err := client.Close(); err != nil {
				t.Errorf("关闭客户端失败: %v", err)
			}
		}()

		// 字面量刻意选中文 + 紧凑分隔:若被重编码(键序重排、加空格)
		// 或被编成 base64(裸 []byte 的坑,spike 实测),回显必然不同。
		args := json.RawMessage(`{"msg":"你好,stdio","n":1}`)
		res, err := client.CallTool(context.Background(), "echo", args)
		if err != nil {
			t.Fatalf("期望调用成功,实际报错: %v", err)
		}
		if got := textOf(res); got != string(args) {
			t.Errorf("参数应经子进程字节级透传:\n期望 %q\n实际 %q", string(args), got)
		}
	})

	t.Run("超时生效:慢工具被该上游 timeout 打断,报携上游名的 TransportError", func(t *testing.T) {
		// 客户端期限 50ms,夹具 slow 工具延迟 3s:超时必然先发生。
		client := newStdioClient(t, "slowpoke", 50*time.Millisecond)
		defer func() {
			if err := client.Close(); err != nil {
				t.Errorf("关闭客户端失败: %v", err)
			}
		}()

		start := time.Now()
		_, err := client.CallTool(context.Background(), "slow", json.RawMessage(`{}`))
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
		// 期限是硬上限:必须在 slow 工具应答(3s)之前返回。
		if elapsed > stdioSlowToolDelay {
			t.Errorf("超时应在该上游 timeout 附近生效,实际耗时 %s(超过 slow 工具延迟 %s)", elapsed, stdioSlowToolDelay)
		}
	})

	t.Run("业务失败透传:上游 IsError 应答原样返回,不吞不改、不冒充传输错误", func(t *testing.T) {
		client := newStdioClient(t, "fs", testTimeout)
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

func TestStdioClose(t *testing.T) {
	t.Run("Close 后调用:ListTools 与 CallTool 均返回 errors.Is 可判别的 ErrClosed", func(t *testing.T) {
		client := newStdioClient(t, "fs", testTimeout)
		if err := client.Close(); err != nil {
			t.Fatalf("关闭客户端失败: %v", err)
		}

		if _, err := client.ListTools(context.Background()); !errors.Is(err, ErrClosed) {
			t.Errorf("Close 后 ListTools 应返回 ErrClosed,实际 %v", err)
		}
		_, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
		if !errors.Is(err, ErrClosed) {
			t.Errorf("Close 后 CallTool 应返回 ErrClosed,实际 %v", err)
		}
	})

	t.Run("Close 幂等:重复关闭不报错", func(t *testing.T) {
		client := newStdioClient(t, "fs", testTimeout)
		if err := client.Close(); err != nil {
			t.Fatalf("首次关闭失败: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Errorf("重复关闭应返回 nil,实际 %v", err)
		}
	})

	t.Run("无孤儿:Close 返回后子进程确实退出", func(t *testing.T) {
		client := newStdioClient(t, "fs", testTimeout)

		// 经 pid 工具拿到子进程自身 PID(后续装配票的故障隔离同样依赖它)。
		res, err := client.CallTool(context.Background(), "pid", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("调用 pid 工具失败: %v", err)
		}
		pid, err := strconv.Atoi(textOf(res))
		if err != nil {
			t.Fatalf("pid 应答应为十进制 PID 文本,实际 %q: %v", textOf(res), err)
		}

		if err := client.Close(); err != nil {
			t.Fatalf("关闭客户端失败: %v", err)
		}
		// SDK 的 Close 沿"关 stdin → 等 → SIGTERM → SIGKILL"链阻塞至进程
		// 退出;轮询只作冗余核对(进程回收有短暂窗口),上限 2s。
		deadline := time.Now().Add(2 * time.Second)
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if processAlive(pid) {
			t.Errorf("Close 返回后子进程(PID %d)应已退出,仍存活", pid)
		}
	})
}

// processAlive 探测 PID 对应进程是否仍存活:信号 0 不产生实际效果,
// 仅校验进程存在(Linux 语义);进程已被回收时返回 false。
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func TestStdioConcurrentUse(t *testing.T) {
	t.Run("并发调用:同一客户端多 goroutine 混合 ListTools/CallTool,-race 下无竞态且全部成功", func(t *testing.T) {
		client := newStdioClient(t, "fs", testTimeout)
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
