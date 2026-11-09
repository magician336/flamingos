// 报告双形态测试:表格默认(人读)与 JSON(--json,机器可读)字段齐备。
package evalx

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReportJSON形态字段齐备(t *testing.T) {
	rep := &Report{
		Total:  3,
		Passed: 1,
		Failures: []Failure{
			{Seq: 2, Tool: "calc_greet", Grader: GraderResultEqual, Detail: `结果等值不符:期望 {"a":1} 实得 {"a":2}`},
			{Seq: 3, Tool: "fs_echo", Grader: GraderJSONSchema, Detail: "结果不符合 schema: required 缺 status"},
			{Seq: 0, Tool: "", Grader: GraderSequence, Detail: "回放条目数 2 多于录制 3(多调 1 条)"},
		},
	}
	rep.finishLog()
	b, err := rep.JSON()
	if err != nil {
		t.Fatalf("JSON 形态应可序列化,实际错误: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("JSON 形态应可解析,实际错误: %v", err)
	}
	if m["total"].(float64) != 3 || m["passed"].(float64) != 1 || m["failed"].(float64) != 2 {
		t.Errorf("统计字段应 total=3 passed=1 failed=2(文件级失败不计条目),实际 %v", m)
	}
	fails, ok := m["failures"].([]any)
	if !ok || len(fails) != 3 {
		t.Fatalf("failures 应为 3 条明细,实际 %v", m["failures"])
	}
	first := fails[0].(map[string]any)
	for _, key := range []string{"seq", "tool", "grader", "detail"} {
		if _, has := first[key]; !has {
			t.Errorf("失败明细应含 %q 字段,实际 %v", key, first)
		}
	}
	if first["seq"].(float64) != 2 || first["tool"] != "calc_greet" || first["grader"] != GraderResultEqual {
		t.Errorf("首条明细字段值不符,实际 %v", first)
	}
}

func TestReportJSON形态全过例(t *testing.T) {
	rep := &Report{Total: 2, Passed: 2}
	rep.finishLog()
	b, err := rep.JSON()
	if err != nil {
		t.Fatalf("全过例应可序列化: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["failed"].(float64) != 0 {
		t.Errorf("全过例 failed 应 0,实际 %v", m["failed"])
	}
	if arr, ok := m["failures"].([]any); !ok || len(arr) != 0 {
		t.Errorf("全过例 failures 应为空数组而非 null,实际 %v", m["failures"])
	}
}

func TestReport表格形态(t *testing.T) {
	rep := &Report{
		Total:  2,
		Passed: 1,
		Failures: []Failure{
			{Seq: 2, Tool: "fs_echo", Grader: GraderLatencyMax, Detail: "延迟 80ms 超上限 10ms"},
			{Seq: 0, Grader: GraderSequence, Detail: "回放条目数 2 少于录制 3(漏调 1 条)"},
		},
	}
	rep.finishLog()
	table := rep.Table()
	if !strings.Contains(table, "2 条目") || !strings.Contains(table, "1 通过") || !strings.Contains(table, "1 失败") {
		t.Errorf("统计行应含条目/通过/失败数,实际:\n%s", table)
	}
	for _, anchor := range []string{"fs_echo", GraderLatencyMax, "延迟 80ms", GraderSequence} {
		if !strings.Contains(table, anchor) {
			t.Errorf("表格应含失败要素 %q,实际:\n%s", anchor, table)
		}
	}
	allPass := (&Report{Total: 1, Passed: 1}).Table()
	if !strings.Contains(allPass, "全部断言通过") {
		t.Errorf("全过例表格应有通过结语,实际:\n%s", allPass)
	}
}

func TestReportOK判据(t *testing.T) {
	if !(&Report{Total: 2, Passed: 2}).OK() {
		t.Error("零失败应 OK")
	}
	if (&Report{Total: 1, Failures: []Failure{{Seq: 1, Grader: GraderResultEqual}}}).OK() {
		t.Error("存在失败明细不应 OK")
	}
	var nilRep *Report
	if nilRep.OK() {
		t.Error("nil 报告不应 OK")
	}
}
