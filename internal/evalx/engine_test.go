// Replay 引擎与五种 grader 的表驱动测试:Caller 桩零网络,五种 grader
// 通过与失败两侧 + 退出码判据(报告 OK)的映射素材。
package evalx

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/recorder"
)

// stubOut 是桩 Caller 的一次预设出参(出参三态素材)。
type stubOut struct {
	result json.RawMessage
	isErr  bool
	err    error
	sleep  time.Duration // 模拟回放耗时(延迟上限 grader 素材)
}

// stubCaller 是回放引擎的表驱动桩:按调用序吐出预设出参,记录每次收到的
// 调用(工具名/参数)供断言透传与串行顺序。
type stubCaller struct {
	outs  []stubOut
	calls []stubCall
}

type stubCall struct {
	tool string
	args json.RawMessage
}

func (s *stubCaller) Call(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, bool, error) {
	i := len(s.calls)
	s.calls = append(s.calls, stubCall{tool: tool, args: args})
	if i >= len(s.outs) {
		return nil, false, errors.New("桩出参耗尽")
	}
	out := s.outs[i]
	if out.sleep > 0 {
		time.Sleep(out.sleep)
	}
	return out.result, out.isErr, out.err
}

// mkCase 手构造一条录制条目用例(单测不经文件加载的最小夹具)。
func mkCase(seq int64, tool, kind, result, errText string) Case {
	c := Case{
		Record: recorder.Record{
			V: recorder.FormatVersion, Seq: seq, Method: methodCallTool,
			Tool: tool, Kind: kind, Error: errText,
		},
	}
	if result != "" {
		c.Result = json.RawMessage(result)
	}
	return c
}

// wantFail 是期望失败明细的一条(条目号 + grader 名;文案另行 Contains 断言)。
type wantFail struct {
	seq    int64
	grader string
	andHas string // 失败文案应含的锚点(空串不断言)
}

func TestReplay表驱动(t *testing.T) {
	ctx := context.Background()
	transportErr := errors.New(`上游 "calc" 不可用: dial refused`)
	transportErrNoName := errors.New("dial refused: connection reset")

	tests := []struct {
		name       string
		cases      []Case
		outs       []stubOut
		wantPasses int
		wantFails  []wantFail
	}{
		{
			name:       "全过:success 对 success 等值",
			cases:      []Case{mkCase(1, "calc_greet", recorder.KindSuccess, `{"content":[{"text":"hi"}]}`, "")},
			outs:       []stubOut{{result: json.RawMessage(`{"content":[{"text":"hi"}]}`)}},
			wantPasses: 1,
		},
		{
			name:       "全过:对象键序无关、数组有序",
			cases:      []Case{mkCase(1, "calc_greet", recorder.KindSuccess, `{"a":1,"b":[1,2],"c":{"x":true}}`, "")},
			outs:       []stubOut{{result: json.RawMessage(`{"c":{"x":true},"b":[1,2],"a":1}`)}},
			wantPasses: 1,
		},
		{
			name:      "等值败:数组有序被打破",
			cases:     []Case{mkCase(1, "calc_greet", recorder.KindSuccess, `{"b":[1,2]}`, "")},
			outs:      []stubOut{{result: json.RawMessage(`{"b":[2,1]}`)}},
			wantFails: []wantFail{{1, GraderResultEqual, ""}},
		},
		{
			name:      "等值败:值漂移(注入差异形态,票 03 负例的素材)",
			cases:     []Case{mkCase(1, "calc_greet", recorder.KindSuccess, `{"text":"hi ada"}`, "")},
			outs:      []stubOut{{result: json.RawMessage(`{"text":"hi INJECTED"}`)}},
			wantFails: []wantFail{{1, GraderResultEqual, "INJECTED"}},
		},
		{
			name:  "错误形态:kind 漂移 success→transport_error 双报(error_shape+result_equal)",
			cases: []Case{mkCase(1, "calc_greet", recorder.KindSuccess, `{"text":"hi"}`, "")},
			outs:  []stubOut{{err: transportErr}},
			wantFails: []wantFail{
				{1, GraderErrorShape, `期望 success`},
				{1, GraderResultEqual, ""},
			},
		},
		{
			name:       "错误形态过:transport_error 对 transport_error 且点名同一上游",
			cases:      []Case{mkCase(1, "calc_greet", recorder.KindTransportError, "", `上游 "calc" 不可用: 录制态故障`)},
			outs:       []stubOut{{err: transportErr}},
			wantPasses: 1,
		},
		{
			name:      "错误形态败:传输错误文案不点名上游",
			cases:     []Case{mkCase(1, "calc_greet", recorder.KindTransportError, "", `上游 "calc" 不可用: 录制态故障`)},
			outs:      []stubOut{{err: transportErrNoName}},
			wantFails: []wantFail{{1, GraderErrorShape, `点名`}},
		},
		{
			name:  "错误形态败:transport_error 回放成功(kind 收敛即回归)",
			cases: []Case{mkCase(1, "calc_greet", recorder.KindTransportError, "", `上游 "calc" 不可用: 录制态故障`)},
			outs:  []stubOut{{result: json.RawMessage(`{"text":"hi"}`)}},
			wantFails: []wantFail{
				{1, GraderErrorShape, `期望 transport_error`},
			},
		},
		{
			name:       "业务失败形态过:IsError 置位且 result 等值",
			cases:      []Case{mkCase(1, "calc_greet", recorder.KindBusinessFailure, `{"content":[{"text":"除零"}],"isError":true}`, "")},
			outs:       []stubOut{{result: json.RawMessage(`{"isError":true,"content":[{"text":"除零"}]}`), isErr: true}},
			wantPasses: 1,
		},
		{
			name:  "业务失败形态败:回放成功(IsError 丢失,kind 与等值双报)",
			cases: []Case{mkCase(1, "calc_greet", recorder.KindBusinessFailure, `{"content":[{"text":"除零"}],"isError":true}`, "")},
			outs:  []stubOut{{result: json.RawMessage(`{"content":[{"text":"除零"}]}`)}},
			wantFails: []wantFail{
				{1, GraderErrorShape, `期望 business_failure`},
				{1, GraderResultEqual, ""},
			},
		},
		{
			name: "schema 过:覆写 schema 对回放 result 校验通过(等值同时成立)",
			cases: func() []Case {
				c := mkCase(1, "calc_greet", recorder.KindSuccess, `{"content":[{"text":"hi"}]}`, "")
				c.Schema = json.RawMessage(`{"type":"object","required":["content"]}`)
				return []Case{c}
			}(),
			outs:       []stubOut{{result: json.RawMessage(`{"content":[{"text":"hi"}]}`)}},
			wantPasses: 1,
		},
		{
			name: "schema 败:回放 result 缺 schema 必需键(等值成立,单报 schema)",
			cases: func() []Case {
				c := mkCase(1, "calc_greet", recorder.KindSuccess, `{"content":[]}`, "")
				c.Schema = json.RawMessage(`{"type":"object","required":["content","status"]}`)
				return []Case{c}
			}(),
			outs:      []stubOut{{result: json.RawMessage(`{"content":[]}`)}},
			wantFails: []wantFail{{1, GraderJSONSchema, "status"}},
		},
		{
			name:      "schema 败:手构造 Case 未预解析时引擎现解析并照样校验(等值成立)",
			cases:     mustResolveCase(t, mkCase(1, "t", recorder.KindSuccess, `{"a":"类型漂移"}`, "")),
			outs:      []stubOut{{result: json.RawMessage(`{"a":"类型漂移"}`)}},
			wantFails: []wantFail{{1, GraderJSONSchema, ""}},
		},
		{
			name: "延迟过:limit 500ms 实际毫秒级",
			cases: func() []Case {
				c := mkCase(1, "calc_greet", recorder.KindSuccess, `{}`, "")
				c.LatencyLimitMS = 500
				return []Case{c}
			}(),
			outs:       []stubOut{{result: json.RawMessage(`{}`), sleep: 2 * time.Millisecond}},
			wantPasses: 1,
		},
		{
			name: "延迟败:超限即败",
			cases: func() []Case {
				c := mkCase(1, "calc_greet", recorder.KindSuccess, `{}`, "")
				c.LatencyLimitMS = 1
				return []Case{c}
			}(),
			outs:      []stubOut{{result: json.RawMessage(`{}`), sleep: 30 * time.Millisecond}},
			wantFails: []wantFail{{1, GraderLatencyMax, ""}},
		},
		{
			name: "缺省不断言:无覆写字段的条目零附加 grader(等值之外零失败)",
			cases: []Case{
				mkCase(1, "calc_greet", recorder.KindSuccess, `{"a":1}`, ""),
				mkCase(2, "fs_echo", recorder.KindSuccess, `{"a":2}`, ""),
			},
			outs: []stubOut{
				{result: json.RawMessage(`{"a":1}`)},
				{result: json.RawMessage(`{"a":2}`)},
			},
			wantPasses: 2,
		},
		{
			name: "多败并存:同条目等值与延迟各自点名",
			cases: func() []Case {
				c := mkCase(1, "calc_greet", recorder.KindSuccess, `{"a":1}`, "")
				c.LatencyLimitMS = 1
				return []Case{c}
			}(),
			outs: []stubOut{{result: json.RawMessage(`{"a":2}`), sleep: 30 * time.Millisecond}},
			wantFails: []wantFail{
				{1, GraderResultEqual, ""},
				{1, GraderLatencyMax, ""},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubCaller{outs: tc.outs}
			rep := Replay(ctx, tc.cases, stub)
			if rep.Total != len(tc.cases) {
				t.Errorf("报告条目数应 %d,实际 %d", len(tc.cases), rep.Total)
			}
			if rep.Passed != tc.wantPasses {
				t.Errorf("通过数应 %d,实际 %d(失败明细 %v)", tc.wantPasses, rep.Passed, rep.Failures)
			}
			if ok := rep.OK(); ok != (len(tc.wantFails) == 0) {
				t.Errorf("OK() 应为 %v,实际 %v", len(tc.wantFails) == 0, ok)
			}
			got := map[string]bool{} // seq:grader 集合
			for _, f := range rep.Failures {
				key := fmtFailKey(f.Seq, f.Grader)
				if got[key] {
					t.Errorf("失败明细重复: %s", key)
				}
				got[key] = true
				if f.Tool == "" {
					t.Errorf("失败明细应携带聚合工具名(seq=%d grader=%s)", f.Seq, f.Grader)
				}
			}
			for _, wf := range tc.wantFails {
				key := fmtFailKey(wf.seq, wf.grader)
				if !got[key] {
					t.Errorf("缺期望失败: %s(实际 %v)", key, rep.Failures)
					continue
				}
				if wf.andHas != "" {
					detail := failDetailOf(rep, wf.seq, wf.grader)
					if !strings.Contains(detail, wf.andHas) {
						t.Errorf("失败文案应含 %q,实际 %q", wf.andHas, detail)
					}
				}
			}
			if len(got) != len(tc.wantFails) {
				t.Errorf("失败条数应 %d,实际 %d: %v", len(tc.wantFails), len(got), rep.Failures)
			}
		})
	}
}

// TestReplay引擎机制测试:headers 透传、串行顺序、参数原样、序列对照。
func TestReplay引擎机制(t *testing.T) {
	ctx := context.Background()
	cases := []Case{
		mkCase(1, "calc_greet", recorder.KindSuccess, `{}`, ""),
		mkCase(2, "fs_echo", recorder.KindSuccess, `{}`, ""),
	}
	stub := &stubCaller{outs: []stubOut{{result: json.RawMessage(`{}`)}, {result: json.RawMessage(`{}`)}}}
	rep := Replay(ctx, cases, stub)
	if !rep.OK() {
		t.Fatalf("双条全对应通过,实际 %v", rep.Failures)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("应发起 2 次调用,实际 %d", len(stub.calls))
	}
	// 串行顺序:发起序 = 用例序;参数原样透传(请求头不入引擎,单一头
	// 注入路径在 CLI 构造期,经 HTTP 层实测钉在 cmd/evalx)。
	for i, want := range cases {
		got := stub.calls[i]
		if got.tool != want.Tool {
			t.Errorf("第 %d 次调用应发起 %q,实际 %q", i+1, want.Tool, got.tool)
		}
		if want.Args != nil && string(got.args) != string(want.Args) {
			t.Errorf("第 %d 次调用参数应原样透传,实际 %s", i+1, got.args)
		}
	}
}

// TestReplay序列对照两侧:文件级 grader 的通过/错序/漏调/多调四形态
// (纯函数直测——串行单发引擎的执行台账天然对齐用例序,失败面由本测试
// 钉住语义,引擎演进为跳条/重试/并发时被它抓住)。
func TestReplay序列对照两侧(t *testing.T) {
	cases := []Case{
		mkCase(1, "calc_greet", recorder.KindSuccess, `{}`, ""),
		mkCase(2, "fs_echo", recorder.KindSuccess, `{}`, ""),
	}
	t.Run("对齐:过", func(t *testing.T) {
		fails := checkSequence(cases, []execEntry{{seq: 1, tool: "calc_greet"}, {seq: 2, tool: "fs_echo"}})
		if len(fails) != 0 {
			t.Errorf("对齐序列应通过,实际 %v", fails)
		}
	})
	t.Run("错序:败", func(t *testing.T) {
		fails := checkSequence(cases, []execEntry{{seq: 1, tool: "fs_echo"}, {seq: 2, tool: "calc_greet"}})
		if len(fails) != 1 || fails[0].Grader != GraderSequence {
			t.Errorf("错序应报 sequence 失败,实际 %v", fails)
		}
	})
	t.Run("漏调:败", func(t *testing.T) {
		fails := checkSequence(cases, []execEntry{{seq: 1, tool: "calc_greet"}})
		if len(fails) != 1 || fails[0].Grader != GraderSequence || !strings.Contains(fails[0].Detail, "漏") {
			t.Errorf("漏调应报 sequence 失败并点名,实际 %v", fails)
		}
	})
	t.Run("多调:败", func(t *testing.T) {
		fails := checkSequence(cases, []execEntry{
			{seq: 1, tool: "calc_greet"}, {seq: 2, tool: "fs_echo"}, {seq: 0, tool: "calc_greet"},
		})
		if len(fails) != 1 || fails[0].Grader != GraderSequence || !strings.Contains(fails[0].Detail, "多") {
			t.Errorf("多调应报 sequence 失败并点名,实际 %v", fails)
		}
	})
}

// mustResolveCase 给用例挂上已解析 schema(手构造路径的 schema 败例素材)。
func mustResolveCase(t *testing.T, c Case) []Case {
	t.Helper()
	c.Schema = json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"}},"required":["a"],"additionalProperties":false}`)
	return []Case{c}
}

func fmtFailKey(seq int64, grader string) string {
	return strconv.FormatInt(seq, 10) + ":" + grader
}

func failDetailOf(rep *Report, seq int64, grader string) string {
	for _, f := range rep.Failures {
		if f.Seq == seq && f.Grader == grader {
			return f.Detail
		}
	}
	return ""
}
