// Package audit 提供工具级审计(spec 0005):JSON-lines 写盘器与 mcp 层
// Receiving 审计中间件。每次 tools/call 在审计文件追加一条记录,allow 与
// deny 都记录;本包不 import policy/transport/gateway/upstream/telemetry
// (spec 0005 依赖纪律)——角色与决策经 ctx 键传递,键与决策值的翻译由
// cmd 装配层注入,本包只认不透明键值。
package audit

import (
	"fmt"
	"os"
	"sync"
)

// Config 是审计器的构造参数:config.Policy.Audit 在 cmd 装配层的投影,
// 外加 ctx 键与决策翻译(装配层注入,audit 与 policy 互不 import 的接缝)。
type Config struct {
	// Path 是 JSON-lines 审计文件的追加写入路径。
	Path string
	// RoleKey 是角色声明的 ctx 键(装配层传 policy.RoleContextKey())。
	RoleKey any
	// DecisionKey 是判定决策的 ctx 键(装配层传 policy.DecisionContextKey())。
	DecisionKey any
	// DecisionAllowed 把 ctx 中取到的决策值翻译为是否放行(装配层闭包做
	// policy.Decision 的类型断言;返回 false 按拒绝记录)。
	DecisionAllowed func(decision any) bool
}

// Recorder 是审计写盘器:持有以 append 形态打开的审计文件与构造参数
// (ctx 键与决策翻译,中间件取值用),并发追加由 mu 序列化(服务端并发
// 调用共用同一写盘器)。
type Recorder struct {
	cfg Config
	f   *os.File
	mu  sync.Mutex
}

// New 以追加形态打开审计文件并即校验可写(Story 9):打开失败返回中文
// 点名路径的错误,由装配入口判为启动失败——磁盘/权限问题不带着上线。
func New(cfg Config) (*Recorder, error) {
	f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("审计文件 %s 无法打开写入: %w", cfg.Path, err)
	}
	return &Recorder{cfg: cfg, f: f}, nil
}

// Close 同步并关闭审计文件:停机路径调用;同步失败(如磁盘满)原样上抛,
// 由调用方记日志——清理失败不改变退出语义之外的任何行为。
func (r *Recorder) Close() error {
	if r == nil || r.f == nil {
		return nil
	}
	if err := r.f.Sync(); err != nil {
		_ = r.f.Close()
		return fmt.Errorf("审计文件同步失败: %w", err)
	}
	return r.f.Close()
}
