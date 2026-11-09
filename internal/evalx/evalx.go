// Package evalx 是录制文件的行为基准消费端(spec 0008):回放引擎、五种
// 确定性 grader 与双形态报告。引擎只依赖录制记录格式与本包的本地 Caller
// 小接口(SDK 机器类型禁入,D4 纪律延伸)——go-sdk client 的适配只存在于
// cmd/evalx CLI 层,引擎可桩驱动单测零网络。录制文件即回放用例(golden +
// 覆写):录到的应答就是期望,schema 与延迟上限等可选字段经显式手编覆写,
// 无第二套用例格式。
package evalx

import (
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/magician336/flamingos/internal/recorder"
)

// methodCallTool 是回放用例唯一的合法 JSON-RPC method:录制端(recorder
// 中间件)同名常量的镜像——两包互不 import 依赖方向单向(evalx → recorder
// 仅取 Record 形态),一致性由 e2e 钉住(沿 recorder 各以本包单一出处对齐
// MCP method 名的先例口径)。
const methodCallTool = "tools/call"

// grader 名(report 失败明细的 grader 字段取值,JSON 报告机器可读)。
const (
	GraderResultEqual = "result_equal" // ①结果等值:JSON 语义比较
	GraderErrorShape  = "error_shape"  // ②错误形态:kind 一致 + 传输错误点名上游
	GraderJSONSchema  = "json_schema"  // ③json-schema:覆写 schema 对 result 校验
	GraderSequence    = "sequence"     // ④序列匹配:文件级条目数与聚合名序列
	GraderLatencyMax  = "latency_max"  // ⑤延迟上限:覆写阈值,超限即败
)

// Caller 是回放引擎对"执行一次聚合调用"的本地小接口(单方法):入参聚合
// 工具名与参数原始字节;出参应答 JSON 与错误二元组,三态判定在引擎内
// (与录制端同构单一出处,见 kindOf):err 非 nil = 传输错误(文案须点名
// 上游)、isError 置位 = 业务失败(应答 JSON 含错误语义)、其余成功。
// 请求头不入接口:单一头注入路径在 CLI 构造期(-header 经 RoundTripper
// 全会话生效,与录制时同口径)。结构化类型无 import 即满足 D4:本接口零
// SDK 机器类型,实现方在 CLI 层适配(把 go-sdk 应答翻译为引擎吃的 JSON
// 形态)。
type Caller interface {
	Call(ctx context.Context, tool string, args json.RawMessage) (result json.RawMessage, isError bool, err error)
}

// Case 是一条回放用例:录制条目本体(12 字段,格式单一出处 recorder.Record)
// 加手编覆写字段——golden + 覆写成立的关键:覆写直接写进录制行,单文件自足。
type Case struct {
	recorder.Record
	// Schema 是对回放 result 的 json-schema 校验(条目可选,手编覆写):
	// LoadCases 预解析进 resolved,解析失败 = 文件错误(CLI 退出码 2)。
	Schema json.RawMessage `json:"schema,omitempty"`
	// LatencyLimitMS 是单条目延迟上限(毫秒,条目可选,手编覆写):
	// 0 = 缺省不断言(spec:超限即败,缺省不断言)。
	LatencyLimitMS int64 `json:"latency_limit_ms,omitempty"`

	// resolved 是 Schema 的预解析缓存(LoadCases 填写;直接手构造 Case 的
	// 调用方如单测由引擎在回放时现解析)。
	resolved *jsonschema.Resolved
}

// kindOf 把 Caller 出参判定为三态 kind(与录制端 recorder 中间件的判定
// 同构,引擎侧单一出处):err 非 nil = 传输错误、isError 置位 = 业务失败、
// 其余成功。
func kindOf(err error, isError bool) string {
	switch {
	case err != nil:
		return recorder.KindTransportError
	case isError:
		return recorder.KindBusinessFailure
	default:
		return recorder.KindSuccess
	}
}
