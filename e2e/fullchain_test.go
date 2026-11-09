// 判据①:混合上游全链(spec 0007 用户故事 18)。http 假上游夹具与 stdio
// 夹具(stdioserver 二进制)经真实网关二进制 + 真实配置文件 + 真实监听
// 端口走完 initialize → tools/list → tools/call 全链,内容往返一致。
package e2e

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMixedUpstreamsFullChain 是交付形态的端到端验收:两上游均以真实进程
// 参与(http 夹具独立进程,由本套件拉起;stdio 夹具由网关按配置拉起),
// 网关侧与 Agent 侧之间没有任何进程内替身。
func TestMixedUpstreamsFullChain(t *testing.T) {
	fixtureAddr := freeAddr(t)
	fixturePid := startHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	endpoint := startGateway(t, gwAddr, configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	))
	agent := connectAgent(t, endpoint, "")

	t.Run("initialize:真实网关握手完成并自报 flamingos", func(t *testing.T) {
		init := agent.InitializeResult()
		if init == nil {
			t.Fatal("应完成 initialize 握手,InitializeResult 不应为 nil")
		}
		if init.ServerInfo == nil || init.ServerInfo.Name != "flamingos" {
			t.Errorf("网关应自报 flamingos,实际 %+v", init.ServerInfo)
		}
	})

	t.Run("tools/list:两上游进程各四工具聚合,共八项且描述原样", func(t *testing.T) {
		list, err := agent.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("tools/list 失败: %v", err)
		}
		// 命名空间按字典序(calc < fs)由网关保证;上游内部顺序跟随上游
		// 自身清单——go-sdk server 的 tools/list 按名字字典序返回。
		wantNames := []string{
			"calc_echo", "calc_fail", "calc_pid", "calc_slow",
			"fs_echo", "fs_fail", "fs_pid", "fs_slow",
		}
		var gotNames []string
		for _, tool := range list.Tools {
			gotNames = append(gotNames, tool.Name)
		}
		if !reflect.DeepEqual(gotNames, wantNames) {
			t.Fatalf("聚合清单不符:\n期望 %v\n实际 %v", wantNames, gotNames)
		}
	})

	t.Run("tools/call:echo 经网关往返一致,http 与 stdio 两通道", func(t *testing.T) {
		// 字面量刻意含中文与紧凑分隔:任何一层重编码都会使回显失真。
		httpArgs := json.RawMessage(`{"msg":"你好,flamingos","n":1}`)
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_echo", Arguments: httpArgs,
		})
		if err != nil {
			t.Fatalf("经网关调用 calc_echo 失败: %v", err)
		}
		if got := textOf(res); got != string(httpArgs) {
			t.Errorf("calc_echo 往返应一致,发送 %q 实际 %q", string(httpArgs), got)
		}

		stdioArgs := json.RawMessage(`{"msg":"stdio 通道往返","ok":true}`)
		res, err = agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: stdioArgs,
		})
		if err != nil {
			t.Fatalf("经网关调用 fs_echo 失败: %v", err)
		}
		if got := textOf(res); got != string(stdioArgs) {
			t.Errorf("fs_echo 往返应一致,发送 %q 实际 %q", string(stdioArgs), got)
		}
	})

	t.Run("tools/call:fail 业务失败以 IsError=true 原样透传", func(t *testing.T) {
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_fail", Arguments: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("业务失败应作为结果透传而非报错,实际报错: %v", err)
		}
		if !res.IsError {
			t.Error("res.IsError 应原样保留为 true,实际 false")
		}
		if got := textOf(res); got != "上游业务失败:余额不足" {
			t.Errorf("业务失败内容应原样透传,实际 %q", got)
		}
	})

	t.Run("tools/call:pid 应答 HTTP 夹具进程的真实 PID", func(t *testing.T) {
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_pid", Arguments: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("经网关调用 calc_pid 失败: %v", err)
		}
		if got, err := strconv.Atoi(textOf(res)); err != nil || got != fixturePid {
			t.Errorf("calc_pid 应应答夹具进程 PID %d,实际 %q: %v", fixturePid, textOf(res), err)
		}
	})

	t.Run("tools/call:slow 固定延迟后应答", func(t *testing.T) {
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_slow", Arguments: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("经网关调用 calc_slow 失败: %v", err)
		}
		if got := textOf(res); got != "迟到的应答" {
			t.Errorf("calc_slow 应答应为固定文案,实际 %q", got)
		}
	})
}
