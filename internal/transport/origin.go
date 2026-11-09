// Origin 校验中间件(票 0005-03/spec 0005,架构 U3 裁决:HTTP 层机制留在
// transport 内部,不立新包):按 MCP Streamable HTTP 规范要求校验 Origin 头
// 防 DNS rebinding——浏览器型客户端的 Origin 不在允许列表即 403。条目语法
// 的单一出处是 originrule 叶包(2026-11-05 架构评审 C1:原 config/transport
// 双份实现退役),本包只做编译与接线;匹配是纯函数(编译后的模式表 × 请求
// 头),未配置 = 关闭态零行为(Story 11)。语法合法性由 config 校验保证
// (设计 D8),此处的解析失败分支仅为防御性兜底。
package transport

import (
	"log"
	"net/http"

	"github.com/magician336/flamingos/internal/originrule"
)

// compileOrigins 编译允许列表:解析不了的条目跳过并记启动警示——语法已由
// config 校验(启动即败),此分支仅为防御性兜底,单个坏条目不拖死网关启动
// (registerTool 同款纪律)。
func compileOrigins(allowed []string) []originrule.Pattern {
	patterns := make([]originrule.Pattern, 0, len(allowed))
	for _, raw := range allowed {
		p, err := originrule.Parse(raw)
		if err != nil {
			log.Printf("警告: 跳过 Origin 允许条目 %q:%v(配置校验应已拦截)", raw, err)
			continue
		}
		patterns = append(patterns, p)
	}
	return patterns
}

// originMatches 是匹配纯函数的入口:请求 Origin 头与编译后的模式表逐一
// 比较(每条目的匹配语义见 originrule.Pattern.Matches——scheme/host 大小
// 写不敏感、端口三形态、解析失败 fail-closed)。
func originMatches(patterns []originrule.Pattern, originHeader string) bool {
	for _, p := range patterns {
		if p.Matches(originHeader) {
			return true
		}
	}
	return false
}

// originMiddleware 返回 Origin 校验包装:请求带 Origin 头时必须在允许列表
// 内,否则 403(MCP Streamable HTTP 规范要求);无 Origin 头的请求放行
// ——非浏览器客户端不发送该头,DNS rebinding 只能借浏览器发起。中间件
// 不下探 ctx、不读 body,放行路径零拷贝。
func originMiddleware(patterns []originrule.Pattern) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" && !originMatches(patterns, origin) {
				http.Error(w, "Origin 不在允许列表:为防御 DNS rebinding,浏览器型客户端的 Origin 必须匹配 policy.origin.allowed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
