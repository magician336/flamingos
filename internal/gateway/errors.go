// gateway 的错误语义(错误契约,票 02):upstream.TransportError 映射为
// 点名上游的"不可用"错误;ErrClosed 原样可判别;本文件同时承载聚合 tool
// 名的自校验规则(SDK 对非法名只打日志不拒绝,网关自守)。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/magician336/flamingos/internal/upstream"
)

// UpstreamUnavailableError 表示某上游在传输层不可用(连不上、超时、断开),
// 是错误契约①的网关侧形态:调用方经 errors.As 点名上游说"不可用",
// 绝不冒充 tool 本身的业务错误(业务失败走 IsError 原样透传)。
type UpstreamUnavailableError struct {
	Upstream string // 故障上游名(即命名空间)
	Cause    error  // 传输层根因,来自 upstream.TransportError.Cause
}

// Error 输出点名上游的中文文案;超时单列文案便于部署者一眼定位。
func (e *UpstreamUnavailableError) Error() string {
	if errors.Is(e.Cause, context.DeadlineExceeded) {
		return fmt.Sprintf("上游 %q 不可用(调用超时)", e.Upstream)
	}
	return fmt.Sprintf("上游 %q 不可用: %v", e.Upstream, e.Cause)
}

// Unwrap 暴露传输层根因,使 errors.Is/As(如超时判别)可穿透。
func (e *UpstreamUnavailableError) Unwrap() error {
	return e.Cause
}

// upstreamFailure 把上游返回的错误包装为点名该上游的中文错误:
// TransportError → *UpstreamUnavailableError(错误契约①);其余错误
// (含 ErrClosed)以 %w 包装,保持 errors.Is 判别能力。
func upstreamFailure(action, ns string, err error) error {
	var terr *upstream.TransportError
	if errors.As(err, &terr) {
		return &UpstreamUnavailableError{Upstream: ns, Cause: terr.Cause}
	}
	return fmt.Errorf("上游 %q %s失败: %w", ns, action, err)
}

// maxToolNameLen 是聚合后完整 tool 名的长度上限,与 SDK validateToolName
// 的上限一致(mcp/tool.go)。
const maxToolNameLen = 128

// validateAggregatedName 校验聚合前后的 tool 名组合:原始名为空时完整名
// ("命名空间_")字面合法、却会在路由拆分后得到空原始名而无法转发,
// 必须单独拦截;完整名再按 SDK 同款规则校验。
func validateAggregatedName(orig, full string) error {
	if orig == "" {
		return errors.New("原始 tool 名为空")
	}
	return validateToolName(full)
}

// validateToolName 校验聚合后的完整 tool 名:非空、≤128、字符集
// [a-zA-Z0-9_-.],规则与 SDK 的 validateToolName 一致(含点号);凡 SDK
// 会拒绝的名字,网关提前拦下。
func validateToolName(name string) error {
	if name == "" {
		return errors.New("tool 名不能为空")
	}
	if len(name) > maxToolNameLen {
		return fmt.Errorf("tool 名超过 %d 字符上限(实际 %d)", maxToolNameLen, len(name))
	}
	for _, r := range name {
		if !validToolNameRune(r) {
			return fmt.Errorf("tool 名含非法字符 %q", string(r))
		}
	}
	return nil
}

// validToolNameRune 判断 r 是否属于 tool 名允许字符集(镜像 SDK 规则)。
func validToolNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
	case r >= 'A' && r <= 'Z':
	case r >= '0' && r <= '9':
	case r == '_' || r == '-' || r == '.':
	default:
		return false
	}
	return true
}

// logSkipped 输出"跳过非法 tool"的启动警示:点名上游与原始名,部署者
// 一眼定位是谁的 tool 没被聚合。
func logSkipped(ns, orig, full string, reason error) {
	log.Printf("警告: 跳过上游 %q 的 tool %q:聚合名 %q 非法(%v)", ns, orig, full, reason)
}
