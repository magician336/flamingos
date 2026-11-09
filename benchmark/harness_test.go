// 压测 harness:真实二进制拉起(网关 + HTTP 假上游)、监听等待、vegeta 靶体
// 与攻击执行。负载靶 = 夹具 echo 工具(回显参数字节,零延迟);裸 POST 靶体
// 钉住 SDK serveStateless 前提:每请求临时会话,tools/call 无需先 initialize,
// Content-Type 必须 application/json,Accept 必须同时含 application/json 与
// text/event-stream(go-sdk v1.8.0 mcp/streamable.go serveStateless 源码核查,
// 烟测一例裸调用实证)。
package benchmark

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// 压测负载与拓扑常量:报告 docs/benchmark.md 与这些常量一致(复现命令的
// 参数出处,spec 0007 Implementation Decisions)。
const (
	// upstreamName 是 http 夹具上游的命名空间名,聚合工具名 = 命名空间_原名
	// (路由按第一个 _ 拆分,聚合名即网关侧 tools/call 的 name)。
	upstreamName = "bench"
	// upstreamTool 是夹具内的原始工具名:echo 回显参数字节,零延迟压测负载。
	upstreamTool = "echo"
	// echoArguments 是固定参数字节:缓存键含 args 摘要,固定字节保证缓存
	// 场景在首次 miss 后全部命中(spec 0006 缓存收益的数字背书前提)。
	echoArguments = `{"msg":"flamingos-benchmark-payload"}`
	// attackerTimeout 是攻击器单请求超时(请求发出到应答读完):高于上游
	// 5s 超时,让上游侧超时以错误应答形态暴露而非攻击器静默掐断。
	attackerTimeout = 10 * time.Second
)

// gatewayToolName 返回网关侧聚合工具名(命名空间_原名)。
func gatewayToolName() string { return upstreamName + "_" + upstreamTool }

// bareCallTarget 构造vegeta 静态靶:裸 POST /mcp tools/call 固定 JSON-RPC 体
// (id 固定 1,Stateless 每请求独立临时会话,无需 initialize)。头按 SDK
// 事实配置,缺一即 415/400。
func bareCallTarget(endpoint, tool string) *vegeta.Target {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		tool, echoArguments)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	return &vegeta.Target{Method: http.MethodPost, URL: endpoint, Body: []byte(body), Header: h}
}

// bareCallCheck 发一例裸调用并断言 200 与 echo 正确应答:全量压测开跑前的
// Stateless 裸 POST 前提实证(前提不成立则整场数字无效,直接 Fatal)。应答
// 媒体类型兼容两种形态:SDK 缺省 SSE 帧(取 data 行拼接)或整包 JSON-RPC。
func bareCallCheck(t *testing.T, endpoint, tool string) {
	t.Helper()
	tr := &http.Transport{Proxy: nil} // 回环流量禁走系统代理(本机有 Clash 环境变量)
	client := &http.Client{Transport: tr, Timeout: attackerTimeout}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bareCallTarget(endpoint, tool).Body))
	if err != nil {
		t.Fatalf("构造裸调用请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("裸调用 %s 失败: %v", endpoint, err)
	}
	defer resp.Body.Close()
	payload := readPayload(t, resp)
	var rpc struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &rpc); err != nil {
		t.Fatalf("裸调用应答不是合法 JSON-RPC(%s): %v", resp.Header.Get("Content-Type"), err)
	}
	if rpc.Error != nil {
		t.Fatalf("裸调用应答携带 JSON-RPC 错误: %s", rpc.Error.Message)
	}
	if len(rpc.Result.Content) == 0 || rpc.Result.Content[0].Text != echoArguments {
		t.Fatalf("裸调用 echo 应答回显参数字节 %q,实际 %+v", echoArguments, rpc.Result.Content)
	}
}

// readPayload 读出应答体并钉住状态码:text/event-stream 时按 SSE 帧提取
// data 行拼接(SDK 缺省应答形态),否则整包即 JSON-RPC 文本。
func readPayload(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("裸调用状态码应为 200,实际 %d,体: %s", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, _ := io.ReadAll(resp.Body)
		return body
	}
	var data []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			data = append(data, strings.TrimSpace(line))
		}
	}
	if len(data) == 0 {
		t.Fatal("SSE 应答不含 data 帧")
	}
	return []byte(strings.Join(data, "\n"))
}

// procWindow 是单条轮询判据的窗口上限:监听等待不超过它。与 e2e 包同名
// 常量各包自持、值一致(e2e/benchmark 互不依赖,共享需先立架构布局内共
// 享包),改动须两处同步。
const procWindow = 10 * time.Second

// freeAddr 以占位监听取得内核分配的空闲端口后立即关闭,返回可写进配置的
// "127.0.0.1:port"。"占位-关闭"窗口内端口被抢走是理论竞态,真被占用时
// 进程起不来,判据以监听超时暴露而非静默错端口。
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

// startHTTPFixture 拉起 HTTP 假上游进程并等其真正监听;杀进程与收尸挂靠
// t.Cleanup。
func startHTTPFixture(t *testing.T, addr string) {
	t.Helper()
	cmd := exec.Command(httpFixtureBin, addr)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉起 HTTP 夹具失败: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // 同时收掉 exec 内部输出拷贝 goroutine,避免与读 out 竞争
	})
	if err := waitForListen(addr); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("HTTP 夹具未在窗口内监听 %s: %v\n夹具输出:\n%s", addr, err, out.String())
	}
}

// startGateway 写临时配置并拉起网关真实二进制,等监听就绪后返回 MCP 端点;
// 兜底清理发 SIGTERM。启动即成功同时证明配置形态合法(规则非法网关启动即败)。
func startGateway(t *testing.T, addr, cfg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flamingos.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	cmd := exec.Command(gatewayBin, "-config", path)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉起网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM) })
	if err := waitForListen(addr); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("网关未在窗口内监听 %s: %v\n网关输出:\n%s", addr, err, out.String())
	}
	return "http://" + addr + "/mcp"
}

// gatewayConfigYAML 渲染缓存关闭态配置:listen + upstreams 段,telemetry/
// policy/cache 全缺省(策略、审计、限流、追踪、缓存全关闭态,spec 0007
// Implementation Decisions 的压测配置形态)。
func gatewayConfigYAML(listen, upstreamURL string) string {
	return fmt.Sprintf("listen: %s\nupstreams:\n  - name: %s\n    type: http\n    url: %s\n    timeout: 5s\n",
		listen, upstreamName, upstreamURL)
}

// cacheGatewayConfigYAML 渲染缓存命中态配置:关闭态形态加 cache 段 public
// 规则(聚合名通配、ttl 与 scope 为压测常量;scope=public 不咨询角色,
// policy 段缺省时可用)。
func cacheGatewayConfigYAML(listen, upstreamURL string) string {
	return fmt.Sprintf("listen: %s\nupstreams:\n  - name: %s\n    type: http\n    url: %s\n    timeout: 5s\ncache:\n  rules:\n    - tools:\n        - '*'\n      ttl_ms: %d\n      scope: public\n",
		listen, upstreamName, upstreamURL, cacheRuleTTLMS)
}

// runScene 执行单场景:同速率预热一秒(建连与代码路径热身,预热结果不入
// 计量)后按 ConstantPacer 恒定速率攻击 dur 时长,返回关闭后的 Metrics
// (分位数已算好)。恒定速率是方法学核心:攻击器按 ticker 排程发请求、不等
// 上一应答,避免 Coordinated Omission 污染 P99。本机系统代理环境变量对
// vegeta 默认 transport 生效,攻击器显式禁代理保回环拓扑纯净。
// 注意 v12.13 的 Attacker 单次使用:Attack 结束时 Stop 关闭攻击器级
// stopch,预热与计量两臂必须各自新建 Attacker,复用会得到零样本。
func runScene(t *testing.T, name, endpoint, tool string, rate int, dur time.Duration) *vegeta.Metrics {
	t.Helper()
	targeter := vegeta.NewStaticTargeter(*bareCallTarget(endpoint, tool))
	pacer := vegeta.ConstantPacer{Freq: rate, Per: time.Second}
	for range vegeta.NewAttacker(vegeta.Proxy(nil), vegeta.Timeout(attackerTimeout)).
		Attack(targeter, pacer, warmupDuration, name+"-warmup") {
	}
	m := &vegeta.Metrics{}
	for res := range vegeta.NewAttacker(vegeta.Proxy(nil), vegeta.Timeout(attackerTimeout)).
		Attack(targeter, pacer, dur, name) {
		m.Add(res)
	}
	m.Close()
	logMetrics(t, name, m)
	return m
}

// logMetrics 打印单场景计量行:场景名、样本数、实际吞吐与 P50/P95/P99。
// 全量输出经 go test -v 捕获,是 docs/benchmark.md 数字表的直接出处。
func logMetrics(t *testing.T, name string, m *vegeta.Metrics) {
	t.Helper()
	t.Logf("%-28s 样本=%-6d 吞吐=%8.1f rps  P50=%-10s P95=%-10s P99=%-10s 错误=%d 状态码=%v",
		name, m.Requests, m.Throughput, m.Latencies.P50, m.Latencies.P95, m.Latencies.P99,
		len(m.Errors), m.StatusCodes)
}
