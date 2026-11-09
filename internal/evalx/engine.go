// 回放引擎:以本地 Caller 接口串行顺序回放用例(spec 0008——不并发),
// 逐条执行条目级 grader,回放完成后做文件级序列对照;零网络零 SDK 依赖,
// Caller 由调用方注入(CLI 层适配 go-sdk,单测桩驱动)。
package evalx

import (
	"context"
	"time"
)

// Replay 串行顺序回放全部用例:逐条经 Caller 发起聚合调用(参数原样),
// 出参三态判定后跑条目级 grader;全部条目回放完毕做文件级序列对照。
// 请求头不入引擎:单一头注入路径在 CLI 构造期(-header 经 RoundTripper
// 全会话生效,与录制时同口径)。
func Replay(ctx context.Context, cases []Case, caller Caller) *Report {
	rep := &Report{Total: len(cases)}
	executed := make([]execEntry, 0, len(cases))
	for i := range cases {
		c := &cases[i]
		start := time.Now()
		result, isErr, callErr := caller.Call(ctx, c.Tool, c.Args)
		latency := time.Since(start)
		executed = append(executed, execEntry{seq: c.Seq, tool: c.Tool})
		fails := gradeCase(c, result, isErr, callErr, latency)
		if len(fails) == 0 {
			rep.Passed++
		} else {
			rep.Failures = append(rep.Failures, fails...)
		}
	}
	rep.Failures = append(rep.Failures, checkSequence(cases, executed)...)
	rep.finishLog()
	return rep
}
