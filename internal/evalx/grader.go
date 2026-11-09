// 五种确定性 grader(spec 0008 断言面):结果等值/错误形态/json-schema/
// 序列匹配/延迟上限。全部确定性判定,零 LLM;失败明细携带条目号与 grader
// 名(报告机器可读的素材),文案点名期望与实际。
package evalx

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/magician336/flamingos/internal/recorder"
)

// upstreamNameRe 从传输错误文案提取点名上游(`上游 "ns"` 形态):spec 0002
// 透传语义的契约锚点(与 internal/gateway/errors.go 的错误文案同形,e2e
// 既有断言先例同款)。
var upstreamNameRe = regexp.MustCompile(`上游 "([^"]+)"`)

// jsonEqual 判定两段 JSON 字节是否语义等值:Unmarshal 为 any 后 DeepEqual——
// 对象键序无关、数组有序(spec 定稿口径);数字按 JSON 数值语义(1 与 1.0
// 等值)。任一侧非法 JSON 即不等值。
func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// truncateJSON 截断 JSON 字节供失败文案引用(防超长应答撑爆报告)。
func truncateJSON(b json.RawMessage) string {
	const max = 200
	s := string(b)
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// gradeCase 对一条回放执行条目级 grader(①等值 ②错误形态 ③schema ⑤延迟),
// 返回失败明细(零失败 = 该条目通过)。④序列匹配是文件级,见 checkSequence。
// 各 grader 一个子函数(单一职责,早返回),聚合序即追加序:②→①→③→⑤。
func gradeCase(c *Case, result json.RawMessage, isErr bool, callErr error, latency time.Duration) []Failure {
	actual := kindOf(callErr, isErr)
	fails := gradeErrorShape(c, actual, callErr)
	fails = append(fails, gradeResultEqual(c, result)...)
	fails = append(fails, gradeSchema(c, result)...)
	fails = append(fails, gradeLatencyMax(c, latency)...)
	return fails
}

// newFailure 是条目级失败明细的单一构造处(条目号、聚合工具名、grader 名、
// 点名期望与实际的文案)。
func newFailure(c *Case, grader, format string, args ...any) Failure {
	return Failure{Seq: c.Seq, Tool: c.Tool, Grader: grader, Detail: fmt.Sprintf(format, args...)}
}

// gradeErrorShape 是②错误形态:kind 与期望一致;传输错误文案须含上游点名
// (spec 0002 透传语义——录制文案里的点名上游应在回放文案中重现)。
func gradeErrorShape(c *Case, actual string, callErr error) []Failure {
	if actual != c.Kind {
		if callErr != nil {
			return []Failure{newFailure(c, GraderErrorShape, "错误形态不符:期望 %s 实得 %s(传输错误文案 %q)", c.Kind, actual, callErr)}
		}
		return []Failure{newFailure(c, GraderErrorShape, "错误形态不符:期望 %s 实得 %s", c.Kind, actual)}
	}
	if actual != recorder.KindTransportError {
		return nil
	}
	want := upstreamNameRe.FindStringSubmatch(c.Error)
	got := upstreamNameRe.FindStringSubmatch(callErr.Error())
	if !namesUpstream(got) || (want != nil && !strings.Contains(callErr.Error(), `上游 "`+want[1]+`"`)) {
		return []Failure{newFailure(c, GraderErrorShape, "传输错误文案未点名上游:期望点名 %q(录制文案 %q),实际 %q",
			upstreamName(want), c.Error, callErr)}
	}
	return nil
}

// gradeResultEqual 是①结果等值:JSON 语义比较(键序无关、数组有序),适用
// 录制为成功与业务失败形态(spec 定稿);传输错误条目无 result,不适用。
func gradeResultEqual(c *Case, result json.RawMessage) []Failure {
	if c.Kind != recorder.KindSuccess && c.Kind != recorder.KindBusinessFailure {
		return nil
	}
	if jsonEqual(c.Result, result) {
		return nil
	}
	return []Failure{newFailure(c, GraderResultEqual, "结果等值不符:期望 %s 实得 %s", truncateJSON(c.Result), truncateJSON(result))}
}

// gradeSchema 是③json-schema:条目可选覆写字段,对回放 result 校验(缺省
// 跳过);LoadCases 预解析缓存优先,手构造 Case 的直接调用方(单测)现解析
// 兜底。
func gradeSchema(c *Case, result json.RawMessage) []Failure {
	if c.Schema == nil {
		return nil
	}
	resolved := c.resolved
	if resolved == nil {
		var s jsonschema.Schema
		if err := json.Unmarshal(c.Schema, &s); err == nil {
			resolved, _ = s.Resolve(nil)
		}
	}
	if resolved == nil {
		return []Failure{newFailure(c, GraderJSONSchema, "schema 覆写无法解析为可用 schema: %s", truncateJSON(c.Schema))}
	}
	var instance any
	if err := json.Unmarshal(result, &instance); err != nil {
		return []Failure{newFailure(c, GraderJSONSchema, "回放结果不是合法 JSON 无法校验: %v", err)}
	}
	if err := resolved.Validate(instance); err != nil {
		return []Failure{newFailure(c, GraderJSONSchema, "结果不符合 schema: %v", err)}
	}
	return nil
}

// gradeLatencyMax 是⑤延迟上限:条目可选覆写字段,超限即败,缺省(0)不断言
// (spec 定稿;上限语义 = 不允许超过,等于上限不算超限)。
func gradeLatencyMax(c *Case, latency time.Duration) []Failure {
	if c.LatencyLimitMS <= 0 || latency <= time.Duration(c.LatencyLimitMS)*time.Millisecond {
		return nil
	}
	return []Failure{newFailure(c, GraderLatencyMax, "延迟 %dms 超上限 %dms", latency.Milliseconds(), c.LatencyLimitMS)}
}

// namesUpstream 判定点名提取结果是否成立(文案含 `上游 "ns"` 形态)。
func namesUpstream(submatch []string) bool {
	return len(submatch) >= 2 && submatch[1] != ""
}

// upstreamName 取点名上游名(无点名返回占位说明)。
func upstreamName(submatch []string) string {
	if namesUpstream(submatch) {
		return submatch[1]
	}
	return "(无)"
}

// execEntry 是回放执行台账的一条:引擎实际发起的一次调用(串行单发引擎
// 下逐条填写,序列对照的输入)。
type execEntry struct {
	seq  int64
	tool string
}

// checkSequence 文件级序列对照(④):回放执行台账与录制条目序列逐位对齐
// (条目数与聚合名序列),错序/漏调/多调即败。串行单发引擎的台账天然与
// 用例序同构,本对照是管线完整性断言:用例管线任何丢行/重复行与引擎未来
// 的跳条/重试/并发改动都会被它抓住(失败面语义由表驱动测试钉住)。
func checkSequence(cases []Case, executed []execEntry) []Failure {
	if len(executed) != len(cases) {
		verb := "多"
		if len(executed) < len(cases) {
			verb = "漏"
		}
		return []Failure{{
			Grader: GraderSequence,
			Detail: fmt.Sprintf("回放条目数 %d %s于录制 %d(%s调 %d 条)", len(executed), verb, len(cases), verb, absInt(len(cases)-len(executed))),
		}}
	}
	for i := range cases {
		if executed[i].tool != cases[i].Tool {
			return []Failure{{
				Grader: GraderSequence,
				Detail: fmt.Sprintf("回放序列错位:第 %d 位期望 %q 实得 %q(错序)", i+1, cases[i].Tool, executed[i].tool),
			}}
		}
	}
	return nil
}

// absInt 是 int 绝对值(避免 math.Abs 的浮点往返)。
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
