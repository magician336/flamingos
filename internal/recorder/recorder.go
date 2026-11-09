// Package recorder 提供行为基准录制(spec 0008):JSON-lines 写盘器与 mcp
// 层 Receiving 录制中间件。每次实际执行的 tools/call 在录制文件追加一条
// 记录;中间件挂 Receiving 链末位(RBAC 判定内层)——deny 路径不下探
// next,故 deny 不入录制,基准表征工具处理行为而非策略配置。本包不 import
// policy/transport/gateway/upstream/telemetry(沿 audit 依赖纪律):角色经
// ctx 键传递、trace_id 经只读访问器闭包注入,键与闭包由 cmd 装配层注入,
// 本包只认不透明键值与闭包;格式知识自有(audit 同款 U4 裁决,不为两份
// JSONL 落盘抽共享包)。
package recorder

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// Config 是录制器的构造参数:config.Recorder 在 cmd 装配层的投影,外加
// ctx 键与 trace_id 访问器(装配层注入,recorder 与 policy/telemetry 互不
// import 的接缝)。
type Config struct {
	// Path 是 JSON-lines 录制文件的追加写入路径。
	Path string
	// RoleKey 是角色声明的 ctx 键(装配层传 policy.RoleContextKey())。
	RoleKey any
	// TraceIDOf 从 ctx 取当前请求 span 的 trace_id(装配层注入 telemetry
	// 的只读访问器闭包);nil 时记录留空——可观测关闭态(无 span)由访问器
	// 返回空串兜住,不报错。
	TraceIDOf func(ctx context.Context) string
}

// Recorder 是录制写盘器:持有以 append 形态打开的录制文件与构造参数,
// 并发追加由 mu 序列化(服务端并发调用共用同一写盘器),seq 分配与写盘
// 同锁保证自增序与文件行序一致。
type Recorder struct {
	cfg Config
	f   *os.File
	mu  sync.Mutex
	seq int64
}

// New 以追加形态打开录制文件并即校验可写(沿 audit 先例):打开失败返回
// 中文点名路径的错误,由装配入口判为启动失败——磁盘/权限问题不带着上线。
func New(cfg Config) (*Recorder, error) {
	f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("录制文件 %s 无法打开写入: %w", cfg.Path, err)
	}
	return &Recorder{cfg: cfg, f: f}, nil
}

// Close 同步并关闭录制文件(沿 audit 先例):停机路径调用;同步失败(如
// 磁盘满)原样上抛,由调用方记日志——清理失败不改变退出语义之外的任何行为。
func (r *Recorder) Close() error {
	if r == nil || r.f == nil {
		return nil
	}
	if err := r.f.Sync(); err != nil {
		_ = r.f.Close()
		return fmt.Errorf("录制文件同步失败: %w", err)
	}
	return r.f.Close()
}
