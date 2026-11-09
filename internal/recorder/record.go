// 录制记录形态与落盘(spec 0008 记录格式定稿):JSON-lines 一行/次实际
// 执行的 tools/call,12 字段 v/seq/time/method/tool/args/kind/result/error/
// latency_ms/trace_id/role;写盘失败记日志不反噬数据面(沿 audit 执行口径)。
package recorder

import (
	"encoding/json"
	"log"
	"time"
)

// FormatVersion 是录制格式版本(v 字段值):未来格式演进以本值为升级锚点,
// 不做跨版本迁移工具(spec Out of Scope)。
const FormatVersion = "1"

// kind 字段的三态取值(spec 0008:成功 | 业务失败 | 传输错误):业务失败
// = 上游正常应答但 IsError=true(错误语义在 result 内);传输错误 = 下探
// 报错(含上游不可用),文案入 error 字段——两者分开落位,基准能区分
// "上游不可用"与"上游报业务失败"两类回归。
const (
	KindSuccess         = "success"
	KindBusinessFailure = "business_failure"
	KindTransportError  = "transport_error"
)

// Record 是一条录制记录(单行一次调用,录制文件即回放用例,票 02 消费):
// V/Seq/Time 由 Append 以落盘时刻填写,其余字段由中间件翻译填写;Args 与
// Result 以原始 JSON 字节移交(参数不重编码,应答取 SDK 类型标准序列化
// 形态)。值语义副本移交,落盘后调用方持有的原记录不受影响。
type Record struct {
	V         string          `json:"v"`          // 格式版本(恒 FormatVersion)
	Seq       int64           `json:"seq"`        // 文件内自增序号(分配与行序同锁)
	Time      string          `json:"time"`       // 落盘时刻(RFC3339Nano,UTC)
	Method    string          `json:"method"`     // 恒 tools/call
	Tool      string          `json:"tool"`       // 聚合工具名
	Args      json.RawMessage `json:"args"`       // 参数原始字节(缺省/形状异常为 null)
	Kind      string          `json:"kind"`       // KindSuccess | KindBusinessFailure | KindTransportError
	Result    json.RawMessage `json:"result"`     // 成功与业务失败的应答内容;传输错误为 null
	Error     string          `json:"error"`      // 传输错误文案;其余形态为空串
	LatencyMS int64           `json:"latency_ms"` // 下探耗时(毫秒)
	TraceID   string          `json:"trace_id"`   // 可空(可观测关闭态留空)
	Role      string          `json:"role"`       // 可空(策略关闭态留空)
}

// Append 把一条记录追加进录制文件:v/time 在此填写,seq 分配与整行写盘同
// 一把锁——mcp 服务端并发调用共用同一写盘器,交错写入会撕碎 JSON-lines
// 行边界,seq 自增序必须与文件行序一致。序列化失败不消耗序号、整条丢弃并
// 记日志(录制文件是行为基准,序号出现空洞会让"文件内自增"失真,半行脏
// 数据比缺行更伤回放);写盘失败只记日志不上抛不 panic:录制是旁路观察,
// 故障不反噬数据面(沿 audit 执行口径,丢记录可从日志追查)。
func (r *Recorder) Append(rec Record) {
	if r == nil {
		return
	}
	rec.V = FormatVersion
	rec.Time = time.Now().UTC().Format(time.RFC3339Nano)
	r.mu.Lock()
	defer r.mu.Unlock()
	rec.Seq = r.seq + 1
	line, err := json.Marshal(rec)
	if err != nil {
		log.Printf("警告: 录制记录序列化失败(工具 %q): %v", rec.Tool, err)
		return
	}
	r.seq = rec.Seq
	if _, err := r.f.Write(append(line, '\n')); err != nil {
		log.Printf("警告: 录制记录写入失败(工具 %q): %v", rec.Tool, err)
	}
}
