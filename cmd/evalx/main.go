// evalx CLI(spec 0008 票 02):对运行中的网关回放录制文件,输出报告并
// 以三态退出码表达结论——一条命令完成行为基准回放,CI 门禁可按字面退出
// 码判定。go-sdk client 的适配只在本层(SDK 机器类型禁入 internal/evalx
// 引擎,D4 纪律延伸):把 CallTool 应答翻译为引擎吃的 JSON 形态,与录制
// 端同库同类型同序列化(等值比较的形态前提)。
//
// 用法:evalx -file 录制.jsonl -endpoint http://127.0.0.1:8080/mcp
//
//	[-header 名:值]... [-json]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/magician336/flamingos/internal/evalx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 退出码三态(沿网关既有 0/1/2 语义,CI 按字面值判定,不得漂移):
// 0=全部通过;1=存在断言失败;2=自身运行错误(录制文件不可读/格式不完备、
// 端点不可达、flags 缺失或非法)。
const (
	exitOK              = 0
	exitAssertionFailed = 1
	exitRuntimeError    = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options 是 flags 的解析结果(测试直接构造,不经全局 flag 状态)。
type options struct {
	file     string            // -file:录制文件路径(回放用例)
	endpoint string            // -endpoint:网关端点 URL
	headers  map[string]string // -header:可重复的请求头透传(角色头同口径)
}

// headerFlags 收集可重复的 -header(flag.Value 接线,解析在 parseHeaders)。
type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ",") }
func (h *headerFlags) Set(s string) error {
	*h = append(*h, s)
	return nil
}

// parseHeaders 解析 "名:值" 形态的请求头:按首个冒号拆分(值可含冒号),
// 同名后值覆盖前值;缺冒号或空名即错误(自身运行错误,退出码 2)。
func parseHeaders(pairs []string) (map[string]string, error) {
	headers := make(map[string]string, len(pairs))
	for _, p := range pairs {
		name, value, found := strings.Cut(p, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !found || name == "" {
			return nil, fmt.Errorf("请求头 %q 形态应为 名:值(首个冒号拆分)", p)
		}
		headers[name] = value
	}
	return headers, nil
}

// parseFlags 解析命令行 flags(注入 args 可测,不经全局 CommandLine)。
func parseFlags(args []string) (options, bool, error) {
	fs := flag.NewFlagSet("evalx", flag.ContinueOnError)
	file := fs.String("file", "", "录制文件路径(回放用例,JSON-lines)")
	endpoint := fs.String("endpoint", "", "网关端点 URL(如 http://127.0.0.1:8080/mcp)")
	jsonOut := fs.Bool("json", false, "以 JSON 形态输出报告(默认表格)")
	var hf headerFlags
	fs.Var(&hf, "header", "可重复的请求头透传,形态 名:值(角色头经此与录制时同口径)")
	if err := fs.Parse(args); err != nil {
		return options{}, false, err
	}
	headers, err := parseHeaders(hf)
	if err != nil {
		return options{}, false, err
	}
	return options{file: *file, endpoint: *endpoint, headers: headers}, *jsonOut, nil
}

// validateOptions 校验必填 flags(缺失即自身运行错误)。
func validateOptions(opts options) error {
	if opts.file == "" {
		return errors.New("必须指定 -file(录制文件路径)")
	}
	if opts.endpoint == "" {
		return errors.New("必须指定 -endpoint(网关端点 URL)")
	}
	return nil
}

// run 承载 CLI 全流程并返回进程退出码:自身运行错误(2)走 errOut 不出
// 报告;断言失败(1)与全过(0)的报告按形态走 out。
func run(args []string, out, errOut io.Writer) int {
	opts, jsonOut, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(errOut, "evalx 参数错误:", err)
		return exitRuntimeError
	}
	if err := validateOptions(opts); err != nil {
		fmt.Fprintln(errOut, "evalx 参数错误:", err)
		return exitRuntimeError
	}
	rep, err := replay(context.Background(), opts, jsonOut, out, errOut)
	if err != nil {
		fmt.Fprintln(errOut, "evalx 运行失败:", err)
		return exitRuntimeError
	}
	return exitCode(nil, rep)
}

// exitCode 把运行结论映射为进程退出码(纯函数,表驱动测试):err 非 nil
// = 自身运行错误 2;其余按报告 OK 判(全过 0 / 存在断言失败 1)。
func exitCode(err error, rep *evalx.Report) int {
	if err != nil {
		return exitRuntimeError
	}
	if rep.OK() {
		return exitOK
	}
	return exitAssertionFailed
}

// replay 执行回放主流程:加载用例(格式不完备即错误)→ 连接端点(不可达
// 即错误)→ 串行回放 → 按形态输出报告。输出通道统一经注入:报告走 out,
// 关闭会话失败等告警走 errOut(不自写 os.Stderr)。返回 nil 报告 + 非 nil
// 错误 = 自身运行错误(退出码 2);返回报告 = 断言结论(退出码 0/1)。
func replay(ctx context.Context, opts options, jsonOut bool, out, errOut io.Writer) (*evalx.Report, error) {
	cases, err := evalx.LoadCases(opts.file)
	if err != nil {
		return nil, err
	}
	caller, err := connectCaller(ctx, opts.endpoint, opts.headers)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := caller.Close(); cerr != nil {
			fmt.Fprintln(errOut, "警告: 关闭网关会话失败:", cerr)
		}
	}()
	rep := evalx.Replay(ctx, cases, caller)
	if jsonOut {
		err = rep.WriteJSON(out)
	} else {
		err = rep.WriteTable(out)
	}
	if err != nil {
		return nil, fmt.Errorf("报告写出失败: %w", err)
	}
	return rep, nil
}

// headerTransport 在默认传输上注入构造期固定的请求头(--header 全会话生效:
// 角色头经此与录制时同口径,网关策略与 private 缓存语义一致)。头注入的
// 单一路径在 CLI 构造期(连接时一次性装配,SDK CallTool 无 per-call header
// 旋钮,HTTPClient 是唯一注入面);RoundTripper 契约要求不改传入请求,故
// 在 req.Clone 副本上设头后交底层传输。
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 {
		req = req.Clone(req.Context())
		for k, v := range t.headers {
			req.Header.Set(k, v)
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// sdkCaller 是引擎 Caller 的 go-sdk 适配:出参三态翻译(应答 JSON + IsError
// + err)与录制端判定同构。应答序列化与录制端同库同类型(json.Marshal
// *mcp.CallToolResult),等值比较双方形态一致。
type sdkCaller struct {
	session *mcp.ClientSession
}

// connectCaller 建立网关会话并包装为引擎 Caller;headers 在构造期一次性
// 装配进传输(单一头注入路径,全会话生效);连接失败即"端点不可达"
// (自身运行错误,退出码 2),错误点名端点。
func connectCaller(ctx context.Context, endpoint string, headers map[string]string) (*sdkCaller, error) {
	client := &http.Client{Transport: &headerTransport{headers: headers}}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "evalx", Version: "0.1.0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           client,
			DisableStandaloneSSE: true, // 无状态网关不维持常驻 SSE 流(e2e 同款配置)
		}, nil)
	if err != nil {
		return nil, fmt.Errorf("网关端点 %s 不可达: %w", endpoint, err)
	}
	return &sdkCaller{session: session}, nil
}

// Close 关闭网关会话(优雅退出)。
func (c *sdkCaller) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	return c.session.Close()
}

// Call 实现引擎 Caller:参数原始字节解码为 SDK Arguments(参数不重编码的
// 前提是往返保持等值——JSON 往返数值形态由引擎的语义等值比较兜住),应答
// 经标准序列化回译为引擎吃的 JSON 形态。
func (c *sdkCaller) Call(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, bool, error) {
	var arguments any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return nil, false, fmt.Errorf("录制参数不是合法 JSON(工具 %q): %w", tool, err)
		}
	}
	res, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		return nil, false, err
	}
	encoded, merr := json.Marshal(res)
	if merr != nil {
		return nil, false, fmt.Errorf("应答序列化失败(工具 %q): %w", tool, merr)
	}
	return encoded, res.IsError, nil
}
