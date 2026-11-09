// e2e evalx 门禁 family(spec 0008 票 03):录制→回放→确定性判定的门禁
// 自举三例,随常规 go test ./... 运行(无环境变量门控)。全部判据落在真实
// 进程边界——网关/夹具/evalx 三个真实二进制、进程退出码、stdout 的 JSON
// 报告、磁盘上的录制文件——零 import internal 的黑盒纪律不变(harness 复
// 用既有帮手,零新基建;cmd/evalx 二进制由 TestMain 构建清单加入)。录制
// 态配置:临时 YAML 含 recorder 段,cache 段缺省 = 缓存关闭(spec 0008:
// 门禁场景缓存关闭)。等待窗口全部沿用 harness 既有上限(procWindow,经
// waitExit/waitForListen 同源),不新增轮询节奏。
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordingConfigYAML 在 configYAML 基础上追加 recorder 段(录制态配置):
// path 指向临时目录内的录制文件;cache 段缺省 = 缓存关闭,回放语义不受
// 命中态污染(spec 0008 缓存交互边界的门禁基线)。
func recordingConfigYAML(recPath, listen string, upstreams ...string) string {
	return configYAML(listen, upstreams...) + "\nrecorder:\n  path: " + recPath + "\n"
}

// runEvalx 拉起 cmd/evalx 真实二进制并阻塞至退出,返回退出码:stdout 与
// stderr 分离捕获(JSON 报告判据在 stdout,自身运行错误文案判据在 stderr,
// CLI 契约的两条输出通道分开断言);退出等待沿用 waitExit 的窗口上限。
func runEvalx(t *testing.T, stdout, stderr *strings.Builder, args ...string) int {
	t.Helper()
	cmd := exec.Command(evalxBin, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉起 evalx 失败: %v", err)
	}
	return waitExit(t, &procOutput{cmd: cmd, out: stderr})
}

// evalxReportShape 是 evalx --json 报告的黑盒镜像:字段名对齐 CLI 报告契
// 约(条目数/通过数/失败明细含条目号与 grader 名),e2e 零 import internal
// 的纪律下自持,一致性由本 family 的断言本身钉住。
type evalxReportShape struct {
	Total    int `json:"total"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Failures []struct {
		Seq    int64  `json:"seq"`
		Tool   string `json:"tool"`
		Grader string `json:"grader"`
		Detail string `json:"detail"`
	} `json:"failures"`
}

// parseEvalxReport 解析 evalx 的 --json 报告:不是合法 JSON 即 Fatal(报告
// 形态本身就是 CLI 契约的一部分)。
func parseEvalxReport(t *testing.T, out string) evalxReportShape {
	t.Helper()
	var rep evalxReportShape
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("--json 报告不是合法 JSON: %v\n原始输出:\n%s", err, out)
	}
	return rep
}

// TestEvalxRecordReplayAllPass 门禁正例:录制态配置拉起真网关与夹具,经网
// 关产生确定性调用完成采集,cmd/evalx 对同一网关回放同一录制文件,断言退
// 出码 0 且 JSON 报告全过——"基准全过路径成立"的判据(spec 0008 门禁自
// 举:每轮 go test 即兑现)。采集面覆盖成功/业务失败两形态与参数回显/动态
// 但进程内恒定(PID)两类应答,采集调用全为确定性应答,回放与录制在同一
// 对进程间进行,应答逐条相同。
//
// 采集面限于 http 上游的实测依据(SEP-2575 不对称,本票边界实测):录制
// 落盘的是网关 Receiving 链内的 handler 应答对象(SDK 注记之前),回放端
// 看到的是网关发往客户端的线路应答(新协议下被网关注记 _meta serverInfo,
// go-sdk v1.8.0 server.go annotateServerInfo)。http 上游夹具以新协议与网
// 关对话、应答已自带夹具 serverInfo 注记,网关见键已占不再注记,两侧形
// 态一致;stdio 上游客户端钉 2025-11-25 旧协议、上游不注记,录制形态无
// _meta 而回放形态含网关注记,result_equal 必然误报——stdio 条目不入采集
// 面(stdio 转发行为由既有 fullchain e2e 覆盖,边界话术见票 03 定档留痕)。
func TestEvalxRecordReplayAllPass(t *testing.T) {
	recPath := filepath.Join(t.TempDir(), "recording.jsonl")
	fixtureAddr := freeAddr(t)
	startHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	endpoint := startGateway(t, gwAddr, recordingConfigYAML(recPath, gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
	))
	agent := connectAgent(t, endpoint, "")

	// 采集:录制中间件在应答返回调用方之前同步落盘(下探后 Append 再返
	// 回),调用返回即录制行已就位,无需轮询。
	calls := []struct{ tool, args string }{
		{"calc_echo", `{"msg":"你好,flamingos","n":1}`},
		{"calc_fail", `{}`}, // 业务失败形态:gateway 原样透传 IsError 应答
		{"calc_pid", `{}`},  // 同夹具进程内恒定,回放等值判据的确定性输入
	}
	for _, c := range calls {
		if _, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: c.tool, Arguments: json.RawMessage(c.args),
		}); err != nil {
			t.Fatalf("采集调用 %s 失败: %v", c.tool, err)
		}
	}

	// 采集完备性前置判据:录制文件恰与调用数同行数(黑盒直读磁盘文件)。
	data, err := os.ReadFile(recPath)
	if err != nil {
		t.Fatalf("读录制文件失败: %v", err)
	}
	if lines := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1; lines != len(calls) {
		t.Fatalf("录制文件应恰 %d 行(一次调用一行),实际 %d 行\n文件内容:\n%s", len(calls), lines, data)
	}

	// 回放:对同一网关回放同一录制文件,断言退出码 0 且 JSON 报告全过。
	var stdout, stderr strings.Builder
	code := runEvalx(t, &stdout, &stderr, "-file", recPath, "-endpoint", endpoint, "-json")
	if code != 0 {
		t.Fatalf("回放退出码应为 0,实际 %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	rep := parseEvalxReport(t, stdout.String())
	if rep.Total != len(calls) || rep.Passed != len(calls) || rep.Failed != 0 || len(rep.Failures) != 0 {
		t.Fatalf("JSON 报告应全过(total=passed=%d, failed=0, failures 空),实际 %+v\n原始输出:\n%s",
			len(calls), rep, stdout.String())
	}
}

// TestEvalxInjectReplyDrift 门禁负例(防 grader 永真的硬性组成):同一录制
// 文件对"差异世界"回放必报失败且退出码 1,失败明细点名条目与 grader——
// grader 永真(怎么跑都绿)被结构排除。差异由夹具既有 inject 形态注入
// (默认形态与既有逐字节一致,注入只在既有 tool 表上追加,0007 票 02 纪
// 律):采集态网关对接 inject 形态夹具,rawreply 经协议外应答短路器落
// transport_error 且文案点名 calc(0007 演练实测形态);回放态网关对接默
// 认形态夹具(新进程新端口),默认 tool 表无 rawreply,该调用在网关启动
// 快照层面以 unknown tool 错误收场(go-sdk JSON-RPC 错误,不含上游点名)——
// 与录制文案的 calc 点名锚点不符,error_shape 必败;正例条目 calc_echo 在
// 同一回放中照常通过,证明失败是差异检出而非全局误报。
func TestEvalxInjectReplyDrift(t *testing.T) {
	recPath := filepath.Join(t.TempDir(), "recording.jsonl")

	// 采集态:inject 形态夹具 + 录制网关。echo 先采(注入生效前,基线含
	// 一条应通过的条目);rawreply 的注入必报错(drills 实测),录制是旁路
	// 观察照落 transport_error 一行,采集端不判失败。
	fixtureAddr := freeAddr(t)
	startInjectHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	endpoint := startGateway(t, gwAddr, recordingConfigYAML(recPath, gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
	))
	agent := connectAgent(t, endpoint, "")
	if _, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "calc_echo", Arguments: json.RawMessage(`{"msg":"注入前基线"}`),
	}); err != nil {
		t.Fatalf("采集调用 calc_echo 失败: %v", err)
	}
	_, _ = agent.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "calc_rawreply", Arguments: json.RawMessage(`{}`),
	})
	data, err := os.ReadFile(recPath)
	if err != nil {
		t.Fatalf("读录制文件失败: %v", err)
	}
	if lines := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1; lines != 2 {
		t.Fatalf("录制文件应恰 2 行(echo + rawreply 的 transport_error),实际 %d 行\n文件内容:\n%s", lines, data)
	}

	// 回放态:同一录制文件对默认形态世界回放——差异检出判据。
	fixture2Addr := freeAddr(t)
	startHTTPFixture(t, fixture2Addr)
	gwAddr2 := freeAddr(t)
	endpoint2 := startGateway(t, gwAddr2, configYAML(gwAddr2,
		httpUpstreamYAML("calc", "http://"+fixture2Addr+"/mcp"),
	))
	var stdout, stderr strings.Builder
	code := runEvalx(t, &stdout, &stderr, "-file", recPath, "-endpoint", endpoint2, "-json")
	if code != 1 {
		t.Fatalf("差异回放退出码应为 1,实际 %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	rep := parseEvalxReport(t, stdout.String())
	if rep.Total != 2 || rep.Passed != 1 || rep.Failed != 1 || len(rep.Failures) != 1 {
		t.Fatalf("JSON 报告应为 total=2/passed=1/failed=1/失败明细恰一条,实际 %+v\n原始输出:\n%s", rep, stdout.String())
	}
	f := rep.Failures[0]
	if f.Seq != 2 || f.Tool != "calc_rawreply" || f.Grader != "error_shape" {
		t.Fatalf("失败明细应点名条目(seq 2/tool calc_rawreply)与 grader(error_shape),实际 %+v", f)
	}
	if !strings.Contains(f.Detail, `期望点名 "calc"`) {
		t.Errorf("失败明细应点名录制文案中的上游锚点,实际 %q", f.Detail)
	}
}

// TestEvalxExitCodeUnreachable CLI 契约例:不可达端点回放得退出码 2——环境
// 错误与行为回归(退出码 1)严格分界(CI 按字面退出码区分处置)。用例文件
// 用单行手编录制条目(录制格式即用例格式,手编覆写是 golden+覆写设计的支
// 持形态),文件合法可读,故退出码 2 只能来自端点不可达,契约分界不混;
// dead 端口占位后立即关闭(exitcode_test 的必然失败地址同款,不碰运气选
// 端口)。连接在加载用例之后进行(cmd/evalx replay 主流程次序),报告不产
// 出——环境错误不产生也不误判为行为结论。
func TestEvalxExitCodeUnreachable(t *testing.T) {
	recPath := filepath.Join(t.TempDir(), "recording.jsonl")
	line := `{"v":"1","seq":1,"time":"2026-11-08T00:00:00Z","method":"tools/call","tool":"calc_echo","args":{"msg":"契约例"},"kind":"success","result":{"content":[{"type":"text","text":"契约例"}]},"error":"","latency_ms":1,"trace_id":"","role":""}`
	if err := os.WriteFile(recPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("写临时录制文件失败: %v", err)
	}
	dead := freeAddr(t)
	var stdout, stderr strings.Builder
	code := runEvalx(t, &stdout, &stderr, "-file", recPath, "-endpoint", "http://"+dead+"/mcp", "-json")
	if code != 2 {
		t.Fatalf("不可达端点退出码应为 2,实际 %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "不可达") || !strings.Contains(msg, dead) {
		t.Errorf("运行错误文案应点名端点不可达且带地址,实际 %q", msg)
	}
	if out := strings.TrimSpace(stdout.String()); out != "" {
		t.Errorf("环境错误不应产出报告(--json stdout 应为空),实际 %q", out)
	}
}
