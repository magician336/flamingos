// 回放报告双形态(spec 0008 CLI 契约):表格默认(人读)、JSON(--json,
// 机器可读:条目数/通过数/失败明细含条目号与 grader 名)。两形态同源,
// 由 Report 单一构造出处派生。
package evalx

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Failure 是一条失败明细:条目号(seq;文件级序列失败为 0)+ 聚合工具名
// + grader 名 + 失败文案。
type Failure struct {
	Seq    int64  `json:"seq"`
	Tool   string `json:"tool"`
	Grader string `json:"grader"`
	Detail string `json:"detail"`
}

// Report 是一次回放的完整结果与双形态报告的单一出处。
type Report struct {
	Total    int       `json:"total"`    // 回放条目数
	Passed   int       `json:"passed"`   // 通过条目数
	Failed   int       `json:"failed"`   // 失败条目数(含失败明细的条目;文件级序列失败不计条目)
	Failures []Failure `json:"failures"` // 失败明细(条目级 + 文件级序列一条)
}

// OK 判定全部通过(退出码判据的引擎侧素材:全过 = 0,存在断言失败 = 1)。
func (r *Report) OK() bool {
	return r != nil && len(r.Failures) == 0
}

// finishLog 在回放收尾时填写派生字段(失败条目数按 seq 去重:同条目多
// grader 并败只计一条;文件级序列失败 seq=0 不计条目)。
func (r *Report) finishLog() {
	seen := map[int64]bool{}
	for _, f := range r.Failures {
		if f.Seq > 0 && !seen[f.Seq] {
			seen[f.Seq] = true
			r.Failed++
		}
	}
	if r.Failures == nil {
		r.Failures = []Failure{}
	}
}

// WriteTable 输出表格形态(默认,人读):统计行 + 失败明细行。
func (r *Report) WriteTable(w io.Writer) error {
	head := fmt.Sprintf("evalx 回放报告:%d 条目,%d 通过,%d 失败(存在断言失败时退出码 1,全部通过 0)\n",
		r.Total, r.Passed, r.Failed)
	if _, err := io.WriteString(w, head); err != nil {
		return err
	}
	if len(r.Failures) == 0 {
		_, err := io.WriteString(w, "全部断言通过。\n")
		return err
	}
	if _, err := io.WriteString(w, fmt.Sprintf("%-6s %-24s %-14s %s\n", "seq", "工具", "grader", "明细")); err != nil {
		return err
	}
	for _, f := range r.Failures {
		seq := fmt.Sprintf("%d", f.Seq)
		if f.Seq == 0 {
			seq = "-" // 文件级序列失败不属于任何单条条目
		}
		if _, err := io.WriteString(w, fmt.Sprintf("%-6s %-24s %-14s %s\n", seq, f.Tool, f.Grader, f.Detail)); err != nil {
			return err
		}
	}
	return nil
}

// Table 返回表格形态字符串(测试与展示便利封装,行为同 WriteTable)。
func (r *Report) Table() string {
	var sb strings.Builder
	if err := r.WriteTable(&sb); err != nil {
		return fmt.Sprintf("表格渲染失败: %v", err)
	}
	return sb.String()
}

// WriteJSON 输出 JSON 形态(--json,机器可读:条目数/通过数/失败明细含
// 条目号与 grader 名),缩进两格便于人工核读日志。
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// JSON 返回 JSON 形态字节(测试便利封装,行为同 WriteJSON)。
func (r *Report) JSON() ([]byte, error) {
	var sb strings.Builder
	if err := r.WriteJSON(&sb); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}
