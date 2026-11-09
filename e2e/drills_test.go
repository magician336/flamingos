// 演练自动化(spec 0007 票 02,三 family):上游 kill -9、断流重发、异常
// 注入。判据全部落在真实进程边界——MCP JSON-RPC 应答与错误文案(点名上
// 游语义)、进程存活(SIG 0 探测)、进程退出——零 import internal 的黑盒纪
// 律不变。恢复时间经轮询采样并以 t.Logf 落窗宽数据,数字的沉淀处在
// docs/drills.md(本文件只产数据,不产文档)。
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 演练轮询的两处节奏常量(单一出处):沉淀等待决定"在途"状态何时成立,
// 探针间隔决定"持续失效"与"瞬时抖动"的区分密度;两者都不进判据数值。
const (
	// inflightSettleDelay 是在途调用发起后的沉淀等待:远小于 slow 工具的
	// 3s 固定延迟,等待结束时"在途"状态成立(slow 3s >> 300ms)。
	inflightSettleDelay = 300 * time.Millisecond
	// probeRetryDelay 是"持续失效"探针的重试间隔:有限次固定间隔重试,
	// 与"瞬时抖动"区分(探针口径 3 次 × 本间隔,docs/drills.md 同源)。
	probeRetryDelay = 100 * time.Millisecond
)

// startInjectHTTPFixture 以注入形态拉起 HTTP 假上游:与默认形态同一夹具
// 二进制,argv[2]=inject 激活两种异常注入形态(panic 工具与协议外应答短
// 路器,httpserver main.go 的注入装配点)。默认形态不带该参数,tool 表与
// 票 01 完全一致(票 01 的 8 工具断言不破)。返回进程句柄:panic 场景以
// Wait 判据实证"上游进程自身崩溃"。
func startInjectHTTPFixture(t *testing.T, addr string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(httpFixtureBin, addr, "inject")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉起注入形态 HTTP 夹具失败: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if err := waitForListen(addr); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("注入形态 HTTP 夹具未在窗口内监听 %s: %v\n夹具输出:\n%s", addr, err, out.String())
	}
	return cmd
}

// startGatewayDrill 拉起网关并等监听就绪,返回结伴载体(进程句柄做 SIG 0
// 存活探测与重启恢复,输出缓冲供 waitExit 使用)与对 Agent 的 MCP 端点——
// 演练需要网关进程句柄,票 01 的 startGateway 只回端点,故另立此帮手
// (底层复用 startGatewayProcess)。
func startGatewayDrill(t *testing.T, addr, cfg string) (*procOutput, string) {
	t.Helper()
	proc := startGatewayProcess(t, cfg)
	if err := waitForListen(addr); err != nil {
		_ = proc.cmd.Process.Kill()
		_ = proc.cmd.Wait()
		t.Fatalf("网关未在窗口内监听 %s: %v\n网关输出:\n%s", addr, err, proc.out.String())
	}
	return proc, "http://" + addr + "/mcp"
}

// restartGateway 体面停掉旧网关(SIGTERM + 退出码 0)并在同一端口以同一
// 配置拉起新网关:stdio 无再拉起机制与协议外应答会话毒化两个场景共用的
// 第二恢复路径(spec 0003"stdio 上游死亡后恢复 = 重启网关"简化策略与
// 本票实测会话事实的实证手段)。旧 Agent 会话指向旧进程,由调用方重新
// 连接;端口在旧进程退出后才复用。
func restartGateway(t *testing.T, old *procOutput, addr, cfg string) (*procOutput, string) {
	t.Helper()
	if err := old.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("向旧网关发送 SIGTERM 失败: %v", err)
	}
	if code := waitExit(t, old); code != 0 {
		t.Fatalf("旧网关应优雅退出(码 0),实际 %d", code)
	}
	return startGatewayDrill(t, addr, cfg)
}

// assertAlive 以 SIG 0 探测进程存活:网关进程级崩溃路径的判据(应急修复
// 条款的生效口)——存活即"上游的异常未放大为网关不可用"。
func assertAlive(t *testing.T, cmd *exec.Cmd, label string) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("%s 进程应存活,实际 SIG 0 探测失败: %v", label, err)
	}
}

// TestDrillInjectOutOfProtocolReply 异常注入 family 的协议外应答场景:夹
// 具裸 HTTP 层对 calc_rawreply 一个工具名短路,返回 200 + text/html 的非
// JSON-RPC 字节流(错误 Media Type 形态),探测网关对上游应答的解析路径。
// 实测钉定的行为形态(见断言与留痕):该调用以 err 收场并点名上游;SDK
// 客户端把非法 Media Type 当作致命协议错误,网关到该上游的长会话就此关
// 闭——同一上游后续调用持续报错、不可自愈;另一上游照常;网关进程存活;
// 恢复路径 = 网关重启(与 stdio 同款第二恢复路径)。
func TestDrillInjectOutOfProtocolReply(t *testing.T) {
	fixtureAddr := freeAddr(t)
	startInjectHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	cfg := configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	)
	gw, endpoint := startGatewayDrill(t, gwAddr, cfg)
	agent := connectAgent(t, endpoint, "")

	t.Run("前置:注入工具经启动快照注册进聚合清单", func(t *testing.T) {
		list, err := agent.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("tools/list 失败: %v", err)
		}
		var names []string
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
		}
		for _, want := range []string{"calc_rawreply", "calc_panic"} {
			found := false
			for _, n := range names {
				if n == want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("注入工具 %s 应在聚合清单中(网关按启动快照注册),实际 %v", want, names)
			}
		}
	})

	var injectAt time.Time
	t.Run("注入:协议外应答下该调用以 err 收场并点名上游", func(t *testing.T) {
		injectAt = time.Now()
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_rawreply", Arguments: json.RawMessage(`{}`),
		})
		// 错误形态实测 = err(非 IsError):SDK 客户端在解析路径拒绝非法
		// Media Type,请求以传输层错误收场,业务失败通道不经过。
		if err == nil {
			t.Fatalf("协议外应答应以 err 收场,实际成功应答 IsError=%v 文本=%q", res.IsError, textOf(res))
		}
		msg := err.Error()
		for _, want := range []string{`上游 "calc" 不可用`, `unsupported content type "text/html"`} {
			if !strings.Contains(msg, want) {
				t.Errorf("错误应含 %q,实际 %q", want, msg)
			}
		}
		t.Logf("[实测] 注入调用错误文案: %q", msg)
	})

	t.Run("会话事实:同一上游后续调用持续报错,另一上游照常", func(t *testing.T) {
		// 实测:SDK 把非法 Media Type 当致命协议错误关闭整条客户端会话,
		// 网关"每上游一条长会话"构造下该上游持续报错(探针实测 3s 十次重
		// 试无自愈)。断言窗口取 3 次重试(足以区分"瞬时抖动"与"持续失
		// 效"),失败文案点名 calc。
		for i := 0; i < 3; i++ {
			_, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "calc_echo", Arguments: json.RawMessage(`{}`),
			})
			if err == nil {
				t.Fatalf("第 %d 次同上游后续调用竟成功:会话未按实测形态失效", i+1)
			}
			if !strings.Contains(err.Error(), `上游 "calc" 不可用`) {
				t.Errorf("后续调用错误应点名 calc,实际 %q", err.Error())
			}
			time.Sleep(probeRetryDelay)
		}
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: json.RawMessage(`{"after":"协议外"}`),
		})
		if err != nil {
			t.Fatalf("另一上游后续调用应照常,实际报错: %v", err)
		}
		if got := textOf(res); got != `{"after":"协议外"}` {
			t.Errorf("另一上游后续调用应正常回显,实际 %q", got)
		}
		assertAlive(t, gw.cmd, "协议外应答注入后网关")
	})

	t.Run("恢复:网关重启后同一上游调用恢复", func(t *testing.T) {
		gw, endpoint = restartGateway(t, gw, gwAddr, cfg)
		agent = connectAgent(t, endpoint, "")
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_echo", Arguments: json.RawMessage(`{"recovered":true}`),
		})
		if err != nil {
			t.Fatalf("网关重启后同一上游调用应恢复,实际报错: %v", err)
		}
		if got := textOf(res); got != `{"recovered":true}` {
			t.Errorf("恢复后应正常回显,实际 %q", got)
		}
		window := time.Since(injectAt)
		t.Logf("[实测] 恢复窗口(协议外应答注入→网关重启后首次成功调用) %s", window)
	})
}

// waitCallSuccess 轮询调用直到首次成功(窗口上限):恢复窗口采样的执行
// 体;每次失败携带错误文案,窗口耗尽即 Fatal(演练轮询窗口设上限,套件
// 时长可控)。
func waitCallSuccess(t *testing.T, agent *mcp.ClientSession, name string, args json.RawMessage) *mcp.CallToolResult {
	t.Helper()
	deadline := time.Now().Add(procWindow)
	for {
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err == nil {
			return res
		}
		if time.Now().After(deadline) {
			t.Fatalf("调用 %s 在窗口(%s)内未恢复,最后一次错误: %v", name, procWindow, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertRecoveredCall 恢复尾段的共同执行体:轮询到恢复后首次成功调用,
// 断言 recovered 探针参数字节级回显一致。五例恢复路径共用(HTTP 夹具重
// 启两例、panic 夹具重启、网关重启两例);两处 _pid 对照断言文案逐字不
// 同且仅两例,故保留在各调用点(见各恢复尾段)。
func assertRecoveredCall(t *testing.T, agent *mcp.ClientSession, tool string) {
	t.Helper()
	res := waitCallSuccess(t, agent, tool, json.RawMessage(`{"recovered":true}`))
	if got := textOf(res); got != `{"recovered":true}` {
		t.Errorf("恢复后应正常回显,实际 %q", got)
	}
}

// waitCallError 轮询调用直到首次报错(窗口上限):进程死亡到网关察觉之间
// 存在微小窗口,以观察到的点名不可用作为"故障已生效"判据(cmd e2e 先例)。
func waitCallError(t *testing.T, agent *mcp.ClientSession, name string, args json.RawMessage) error {
	t.Helper()
	deadline := time.Now().Add(procWindow)
	var err error
	for {
		_, err = agent.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			t.Fatalf("调用 %s 在窗口(%s)内持续成功,注入未生效", name, procWindow)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertUnavailable 断言错误点名故障上游且语义为不可用(错误契约①的
// Agent 侧形态,cmd e2e 故障隔离先例)。
func assertUnavailable(t *testing.T, err error, ns string) {
	t.Helper()
	if err == nil {
		t.Fatal("应观察到调用错误,实际 nil")
	}
	if msg := err.Error(); !strings.Contains(msg, `上游 "`+ns+`" 不可用`) {
		t.Errorf("错误应点名上游 %s 且说明不可用,实际 %q", ns, msg)
	}
}

// TestDrillInjectPanic 异常注入 family 的 panic 场景:调用 calc_panic,夹
// 具 handler 直接 panic。实测钉定的边界:SDK 生产代码零 recover 且
// tools/call 在会话读 goroutine 上执行,panic 击穿夹具进程(net/http 的
// 每连接 recover 不适用);在途调用以 err 收场并点名 calc;网关进程存活;
// 另一上游照常;恢复 = 夹具进程重启后调用恢复(与 kill -9 同语义并案)。
func TestDrillInjectPanic(t *testing.T) {
	fixtureAddr := freeAddr(t)
	fixtureCmd := startInjectHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	gw, endpoint := startGatewayDrill(t, gwAddr, configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	))
	agent := connectAgent(t, endpoint, "")

	var injectAt time.Time
	t.Run("注入:panic 调用报错且夹具进程自身崩溃", func(t *testing.T) {
		injectAt = time.Now()
		err := waitCallError(t, agent, "calc_panic", json.RawMessage(`{}`))
		assertUnavailable(t, err, "calc")
		t.Logf("[实测] panic 注入调用错误文案: %q", err.Error())

		// "上游进程自身崩溃"实证:进程 Wait 返回且为 ExitError(net/http
		// 若在此隔离 panic,进程不会退出——该判据即两者边界的钉子)。
		done := make(chan error, 1)
		go func() { done <- fixtureCmd.Wait() }()
		select {
		case err := <-done:
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("夹具进程退出形态异常(非 ExitError): %v", err)
			}
			t.Logf("[实测] 夹具进程已自身崩溃退出,退出码 %d", ee.ExitCode())
		case <-time.After(procWindow):
			t.Fatal("夹具进程应在 panic 后自身退出(实测未退出 = net/http 隔离生效,与 SDK 零 recover 事实矛盾)")
		}
	})

	t.Run("网关存活且另一上游照常", func(t *testing.T) {
		assertAlive(t, gw.cmd, "panic 注入后网关")
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: json.RawMessage(`{"after":"panic"}`),
		})
		if err != nil {
			t.Fatalf("另一上游应照常服务,实际报错: %v", err)
		}
		if got := textOf(res); got != `{"after":"panic"}` {
			t.Errorf("另一上游应正常回显,实际 %q", got)
		}
	})

	t.Run("恢复:夹具重启后调用恢复(并案 kill -9 语义)", func(t *testing.T) {
		startInjectHTTPFixture(t, fixtureAddr)
		assertRecoveredCall(t, agent, "calc_echo")
		t.Logf("[实测] 恢复窗口(panic 注入→夹具重启后首次成功调用) %s", time.Since(injectAt))
	})
}

// killUpstream 以 SIGKILL 注入进程骤死(命令级手段 = kill -9 <pid>,PID
// 经上游的 pid 工具应答取得,杀的就是当时在服务的进程)。
func killUpstream(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill -9 %d 失败: %v", pid, err)
	}
}

// pidViaTool 经网关调用 <ns>_pid 取上游进程真实 PID:演练的注入目标定位
// 手段(pid 工具自报 PID,与拉起 PID 精确对照,票 01 同款用法)。
func pidViaTool(t *testing.T, agent *mcp.ClientSession, ns string) int {
	t.Helper()
	res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name: ns + "_pid", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("经网关调用 %s_pid 失败: %v", ns, err)
	}
	pid, err := strconv.Atoi(textOf(res))
	if err != nil {
		t.Fatalf("%s_pid 应答应为十进制 PID 文本,实际 %q: %v", ns, textOf(res), err)
	}
	return pid
}

// startInFlightCall 在后台发起 slow 在途调用并等其进入在途状态:在途注入
// 三例(HTTP 在途、断流重发、stdio 在途)共用的脚手架——goroutine 发起
// 加固定沉淀等待,沉淀时长远小于 slow 的 3s 固定延迟,返回时"在途"状态
// 成立;返回错误通道供 waitInFlightError 采样。
func startInFlightCall(t *testing.T, agent *mcp.ClientSession, tool string) <-chan error {
	t.Helper()
	inflightErr := make(chan error, 1)
	go func() {
		_, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: tool, Arguments: json.RawMessage(`{}`),
		})
		inflightErr <- err
	}()
	time.Sleep(inflightSettleDelay)
	return inflightErr
}

// waitInFlightError 等待在途调用返回错误(窗口上限):断流后 EOF/RST 的
// 传播速度不可假设,窗口取 2×procWindow;在途调用自身还有 5s 上游超时兜
// 底,窗口只可能早于它触发。
func waitInFlightError(t *testing.T, errCh <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("%s 在途调用竟成功返回", label)
		}
		return err
	case <-time.After(2 * procWindow):
		t.Fatalf("%s 在途调用在窗口内未返回(断流未被网关察觉)", label)
		return nil // 不可达:Fatalf 已 Goexit
	}
}

// TestDrillKillHTTPUpstreamIdle kill -9 family:HTTP 夹具空载骤死。注入目
// 标经 calc_pid 与拉起 PID 精确对照;断言故障生效(点名 calc 不可用)、网
// 关存活、健康上游不受影响;恢复路径 = 夹具进程同端口重启后调用恢复,同
// 一 Agent 会话与网关会话直接续用(SDK 客户端会话容忍度的实测点)。
func TestDrillKillHTTPUpstreamIdle(t *testing.T) {
	fixtureAddr := freeAddr(t)
	fixturePid := startHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	gw, endpoint := startGatewayDrill(t, gwAddr, configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	))
	agent := connectAgent(t, endpoint, "")

	if got := pidViaTool(t, agent, "calc"); got != fixturePid {
		t.Fatalf("calc_pid 应答 %d 应与拉起 PID %d 精确对照", got, fixturePid)
	}

	injectAt := time.Now()
	killUpstream(t, fixturePid)

	err := waitCallError(t, agent, "calc_echo", json.RawMessage(`{}`))
	assertUnavailable(t, err, "calc")
	t.Logf("[实测] kill -9 HTTP 空载故障生效错误文案: %q", err.Error())

	assertAlive(t, gw.cmd, "kill -9 HTTP 空载后网关")
	res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "fs_echo", Arguments: json.RawMessage(`{"alive":true}`),
	})
	if err != nil {
		t.Fatalf("另一上游应照常服务,实际报错: %v", err)
	}
	if got := textOf(res); got != `{"alive":true}` {
		t.Errorf("另一上游应正常回显,实际 %q", got)
	}

	// 恢复:夹具同端口重启,新进程接管;同一 Agent 会话不重连直接续用。
	startHTTPFixture(t, fixtureAddr)
	assertRecoveredCall(t, agent, "calc_echo")
	// _pid 对照断言文案逐字保留(旧进程换代判据),仅两例故不入 helper。
	if got := pidViaTool(t, agent, "calc"); got == fixturePid {
		t.Errorf("恢复后 calc_pid 应答 %d 不应仍是旧进程(应为重启后的新进程)", got)
	}
	t.Logf("[实测] 恢复窗口(kill -9 HTTP 空载→夹具重启后首次成功调用) %s", time.Since(injectAt))
}

// TestDrillKillHTTPUpstreamInFlight kill -9 family:HTTP 夹具在途骤死
// (slow 固定延迟 3s 进行中注入)。在途调用以 err 收场并点名 calc;网关
// 存活;健康上游不受影响;恢复同空载例(夹具重启)。
func TestDrillKillHTTPUpstreamInFlight(t *testing.T) {
	fixtureAddr := freeAddr(t)
	fixturePid := startHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	gw, endpoint := startGatewayDrill(t, gwAddr, configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	))
	agent := connectAgent(t, endpoint, "")

	inflightErr := startInFlightCall(t, agent, "calc_slow")

	injectAt := time.Now()
	killUpstream(t, fixturePid)

	err := waitInFlightError(t, inflightErr, "calc_slow")
	assertUnavailable(t, err, "calc")
	t.Logf("[实测] kill -9 HTTP 在途断流错误文案: %q", err.Error())

	assertAlive(t, gw.cmd, "kill -9 HTTP 在途后网关")
	res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "fs_echo", Arguments: json.RawMessage(`{"alive":true}`),
	})
	if err != nil {
		t.Fatalf("另一上游应照常服务,实际报错: %v", err)
	}
	if got := textOf(res); got != `{"alive":true}` {
		t.Errorf("另一上游应正常回显,实际 %q", got)
	}

	startHTTPFixture(t, fixtureAddr)
	assertRecoveredCall(t, agent, "calc_echo")
	t.Logf("[实测] 恢复窗口(kill -9 HTTP 在途→夹具重启后首次成功调用) %s", time.Since(injectAt))
}

// TestDrillResendAfterStreamBreak 断流重发 family:slow 在途时杀上游,在
// 途调用失败,夹具重启(上游恢复)后,同一 Agent 会话(自始至终未重连)
// 重发同一调用成功。网关侧无自动重试(spec 0007 Out of Scope),本场景记
// 录的是客户端重发语义。
func TestDrillResendAfterStreamBreak(t *testing.T) {
	fixtureAddr := freeAddr(t)
	fixturePid := startHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	gw, endpoint := startGatewayDrill(t, gwAddr, configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	))
	agent := connectAgent(t, endpoint, "")

	inflightErr := startInFlightCall(t, agent, "calc_slow")

	injectAt := time.Now()
	killUpstream(t, fixturePid)

	err := waitInFlightError(t, inflightErr, "calc_slow")
	assertUnavailable(t, err, "calc")
	assertAlive(t, gw.cmd, "断流重发注入后网关")

	// 上游恢复:夹具同端口重启。同一 Agent 会话(agent 变量未动)重发同
	// 一调用(slow,空参数)。
	startHTTPFixture(t, fixtureAddr)
	resendAt := time.Now()
	res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "calc_slow", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("同一会话重发同一调用应成功,实际报错: %v", err)
	}
	if got := textOf(res); got != "迟到的应答" {
		t.Errorf("重发应答应为 slow 固定文案,实际 %q", got)
	}
	t.Logf("[实测] 恢复窗口(断流注入→同一会话重发成功) %s(其中重发调用自身含 slow 固定延迟 %s,注入→重发发起 %s)",
		time.Since(injectAt), time.Since(resendAt), time.Since(injectAt)-time.Since(resendAt))
}

// TestDrillKillStdioUpstreamIdle kill -9 family:stdio 夹具子进程空载骤死。
// 实证 spec 0003 简化策略:子进程无再拉起机制,该上游持续不可用;恢复路
// 径 = 网关重启(同端口同配置),新网关拉起新子进程。网关存活;HTTP 上游
// 不受影响。
func TestDrillKillStdioUpstreamIdle(t *testing.T) {
	fixtureAddr := freeAddr(t)
	startHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	cfg := configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	)
	gw, endpoint := startGatewayDrill(t, gwAddr, cfg)
	agent := connectAgent(t, endpoint, "")

	childPid := pidViaTool(t, agent, "fs")
	injectAt := time.Now()
	killUpstream(t, childPid)

	err := waitCallError(t, agent, "fs_echo", json.RawMessage(`{}`))
	assertUnavailable(t, err, "fs")
	t.Logf("[实测] kill -9 stdio 空载故障生效错误文案: %q", err.Error())

	assertAlive(t, gw.cmd, "kill -9 stdio 空载后网关")
	res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "calc_echo", Arguments: json.RawMessage(`{"alive":true}`),
	})
	if err != nil {
		t.Fatalf("HTTP 上游应照常服务,实际报错: %v", err)
	}
	if got := textOf(res); got != `{"alive":true}` {
		t.Errorf("HTTP 上游应正常回显,实际 %q", got)
	}

	// 无再拉起实证:子进程死亡后网关不重拉(spec 0003 简化策略),有限窗
	// 口内重试全部报错(与"瞬时抖动"区分)。
	for i := 0; i < 3; i++ {
		if _, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: json.RawMessage(`{}`),
		}); err == nil {
			t.Fatalf("第 %d 次 fs_echo 竟成功:stdio 子进程不应被再拉起", i+1)
		}
		time.Sleep(probeRetryDelay)
	}

	// 恢复:网关重启(同端口同配置),新网关拉起新 stdio 子进程,调用恢复。
	gw, endpoint = restartGateway(t, gw, gwAddr, cfg)
	agent = connectAgent(t, endpoint, "")
	assertRecoveredCall(t, agent, "fs_echo")
	// _pid 对照断言文案逐字保留(旧子进程换代判据),仅两例故不入 helper。
	if got := pidViaTool(t, agent, "fs"); got == childPid {
		t.Errorf("重启后 fs_pid 应答 %d 不应仍是旧子进程(应为新网关拉起的新进程)", got)
	}
	t.Logf("[实测] 恢复窗口(kill -9 stdio 空载→网关重启后首次成功调用) %s", time.Since(injectAt))
}

// TestDrillKillStdioUpstreamInFlight kill -9 family:stdio 夹具子进程在途
// 骤死(fs_slow 固定延迟 3s 进行中注入)。在途调用以 err 收场并点名 fs;
// 恢复同空载例(网关重启)。
func TestDrillKillStdioUpstreamInFlight(t *testing.T) {
	fixtureAddr := freeAddr(t)
	startHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	cfg := configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	)
	gw, endpoint := startGatewayDrill(t, gwAddr, cfg)
	agent := connectAgent(t, endpoint, "")

	childPid := pidViaTool(t, agent, "fs")

	inflightErr := startInFlightCall(t, agent, "fs_slow")

	injectAt := time.Now()
	killUpstream(t, childPid)

	err := waitInFlightError(t, inflightErr, "fs_slow")
	assertUnavailable(t, err, "fs")
	t.Logf("[实测] kill -9 stdio 在途断流错误文案: %q", err.Error())

	assertAlive(t, gw.cmd, "kill -9 stdio 在途后网关")
	res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "calc_echo", Arguments: json.RawMessage(`{"alive":true}`),
	})
	if err != nil {
		t.Fatalf("HTTP 上游应照常服务,实际报错: %v", err)
	}
	if got := textOf(res); got != `{"alive":true}` {
		t.Errorf("HTTP 上游应正常回显,实际 %q", got)
	}

	gw, endpoint = restartGateway(t, gw, gwAddr, cfg)
	agent = connectAgent(t, endpoint, "")
	assertRecoveredCall(t, agent, "fs_echo")
	t.Logf("[实测] 恢复窗口(kill -9 stdio 在途→网关重启后首次成功调用) %s", time.Since(injectAt))
}
