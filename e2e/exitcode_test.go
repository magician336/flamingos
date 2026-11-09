// 判据③:退出码三态契约(spec 0007 用户故事 20)。0 成功/1 配置错误/
// 2 启动失败在真实二进制上复核,字面值沿 main 包既有契约测试,不新增
// 语义;断言手段是进程退出码本身(exec.ExitError.ExitCode),无任何代理
// 判据。
package e2e

import (
	"syscall"
	"testing"
)

func TestExitCodeContract(t *testing.T) {
	t.Run("合法配置+SIGTERM 优雅退出 = 0", func(t *testing.T) {
		gwAddr := freeAddr(t)
		// 上游用 stdio 夹具:拨测(拉起子进程+握手)必然成功,网关得以
		// 真正走到监听;SIGTERM 后优雅停机,子进程随上游关闭退出。
		proc := startGatewayProcess(t, configYAML(gwAddr,
			stdioUpstreamYAML("fs", stdioFixtureBin),
		))
		if err := waitForListen(gwAddr); err != nil {
			_ = proc.cmd.Process.Kill()
			_ = proc.cmd.Wait()
			t.Fatalf("网关未在窗口内监听 %s: %v\n网关输出:\n%s", gwAddr, err, proc.out.String())
		}
		if err := proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("发送 SIGTERM 失败: %v", err)
		}
		if code := waitExit(t, proc); code != 0 {
			t.Fatalf("优雅退出码应为 0,实际 %d\n网关输出:\n%s", code, proc.out.String())
		}
	})

	t.Run("坏 YAML = 1", func(t *testing.T) {
		// 坏缩进沿 config 包既有用例同款形态:解析即败,属配置错误而非
		// 启动失败——判据钉住两类错误的分界。
		code := runGatewayUntilExit(t, "listen: 127.0.0.1:0\nupstreams:\n  - name: calc\n   type: http\n")
		if code != 1 {
			t.Fatalf("坏 YAML 退出码应为 1,实际 %d", code)
		}
	})

	t.Run("合法配置但上游不可达 = 2", func(t *testing.T) {
		// dead 端口占位后立即关闭:必然拨测失败的地址(cmd 测试
		// deadUpstreamURL 同款,不碰运气选未监听端口)。配置合法、装配期
		// 拨测失败——启动失败语义,不是配置错误。
		dead := freeAddr(t)
		code := runGatewayUntilExit(t, configYAML("127.0.0.1:0",
			httpUpstreamYAML("calc", "http://"+dead+"/mcp"),
		))
		if code != 2 {
			t.Fatalf("上游不可达退出码应为 2,实际 %d", code)
		}
	})
}
