// 审计记录形态与落盘(spec 0005 审计记录决策):JSON-lines 一条/次
// tools/call,字段 ts/role/tool/decision/error;写盘失败记日志不反噬数据面。
package audit

import (
	"encoding/json"
	"log"
	"time"
)

// decision 字段的两个取值:判定放行/策略拒绝。
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Record 是一条审计记录(Story 8:谁在何时调了什么、放没放行、结果如何)。
// ts 由 Append 以落盘时刻填写,调用方不填;值语义副本移交,落盘后调用方
// 持有的原记录不受影响。
type Record struct {
	TS       string `json:"ts"`       // 落盘时刻(RFC3339Nano,UTC)
	Role     string `json:"role"`     // 判定所用角色声明
	Tool     string `json:"tool"`     // 聚合工具名
	Decision string `json:"decision"` // DecisionAllow | DecisionDeny
	Error    bool   `json:"error"`    // 调用结果是否出错(含策略拒绝)
}

// Append 把一条记录追加进审计文件:ts 以此刻填写,整行加互斥锁序列化——
// mcp 服务端并发调用共用同一写盘器,交错写入会撕碎 JSON-lines 行边界。
// 写盘失败只记日志、不上抛不 panic:审计是旁路观察,故障不反噬数据面
// (spec 未明说,执行口径:被审计的调用照常完成,丢记录可从日志追查)。
func (r *Recorder) Append(rec Record) {
	if r == nil {
		return
	}
	rec.TS = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(rec)
	if err != nil {
		log.Printf("警告: 审计记录序列化失败(角色 %q 工具 %q): %v", rec.Role, rec.Tool, err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Write(append(line, '\n')); err != nil {
		log.Printf("警告: 审计记录写入失败(角色 %q 工具 %q): %v", rec.Role, rec.Tool, err)
	}
}
