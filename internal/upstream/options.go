// upstream 的装配选项:可观测等横切能力以构造选项注入,不传任何选项 =
// 关闭态(不装中间件、不注 HTTPClient),既有调用方行为不变
// (spec 0004 Story 13/20:adapter 只留装配行,知识在 telemetry 包)。
package upstream

import (
	"github.com/magician336/flamingos/internal/telemetry"
)

// Option 是 adapter 构造函数的装配选项。
type Option func(*clientOptions)

// clientOptions 承载全部装配参数;零值即关闭态。
type clientOptions struct {
	telemetry *telemetry.Telemetry // nil = 可观测关闭态
}

// WithTelemetry 注入可观测出站接线:该上游的 client 会话挂 Sending
// 中间件(child client span,上游名/类型经构造注入),HTTP adapter 另
// 注入标准头 RoundTripper;t 为 nil(含 telemetry.New(nil))时不装任何
// 挂点——关闭态零开销。Client 接口与 gateway 不因此改动(架构 §8-1)。
func WithTelemetry(t *telemetry.Telemetry) Option {
	return func(o *clientOptions) { o.telemetry = t }
}

// apply 解析全部构造选项;无选项时返回零值(关闭态)。
func apply(opts []Option) clientOptions {
	o := clientOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
