// Package originrule 是 Origin 允许条目的语法单一出处:解析(scheme://
// host[:port],端口三形态)与请求匹配(Matches)的纯函数叶包。两个消费方
// 各取所需——config 拿 Parse 判配置合法性(启动即败),transport 编译为
// Pattern 集合做放行判定——一份语法、两个 adapter(2026-11-05 架构评审
// C1:原为 config/transport 双份实现 + 跨包一致性靠 e2e 反例,深化为单一
// 出处,等价测试退役)。本包不 import 任何业务包,依赖方向不破(设计
// D1/D7:语法知识归叶包,校验话语权仍归 config、规范化仍归消费方)。
package originrule

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Pattern 是单个编译后的允许条目:scheme 与 host 已小写规范化(比较时请求
// 侧同样小写化,防御大小写变体);端口三形态——缺省(请求必须同样无显式
// 端口,收紧形态)、"*" 通配任意端口、精确数字(1-65535)。
type Pattern struct {
	scheme string
	host   string
	port   string // "" = 条目无端口;"*" = 通配任意端口
}

// Parse 把单个允许条目解析为编译后的模式。语法:scheme://host[:port]——
// scheme 限 http/https(大小写不敏感),host 非空且不含路径片段,端口为
// 缺省、通配 * 或 1-65535 的数字。错误信息面向配置编写者(中文、点名原
// 条目):config 侧补位置前缀后原样上报,transport 侧用于防御性跳过日志。
func Parse(entry string) (Pattern, error) {
	scheme, rest, ok := strings.Cut(entry, "://")
	scheme = strings.ToLower(scheme)
	if !ok {
		return Pattern{}, fmt.Errorf("条目 %q 缺少 scheme:// 前缀(应为 scheme://host[:port],scheme 限 http/https)", entry)
	}
	if rest == "" {
		return Pattern{}, fmt.Errorf("条目 %q 缺少主机名(Origin 序列化只有 scheme://host[:port])", entry)
	}
	if scheme != "http" && scheme != "https" {
		return Pattern{}, fmt.Errorf("条目 %q 的 scheme %q 不受支持,只接受 http 或 https", entry, scheme)
	}
	host, port, err := net.SplitHostPort(rest)
	if err != nil {
		host, port = rest, "" // 无端口形式:整段是 host
	}
	if host == "" || strings.ContainsAny(host, "/?#") {
		return Pattern{}, fmt.Errorf("条目 %q 缺少主机名或含路径(Origin 序列化只有 scheme://host[:port])", entry)
	}
	if port != "" && port != "*" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return Pattern{}, fmt.Errorf("条目 %q 的端口 %q 不合法(应为 1-65535 的数字或通配 *)", entry, port)
		}
	}
	return Pattern{scheme: scheme, host: normalizeHost(host), port: port}, nil
}

// Matches 判断请求的 Origin 头是否命中该条目:解析失败的 Origin 值或缺
// host 的一律不匹配(fail-closed,javascript: 伪协议与裸文本都进不来);
// scheme 与 host 不区分大小写;条目端口通配即放行,否则按字面比较(双方
// 缺省视为相等——条目无端口而请求带显式端口不匹配)。
func (p Pattern) Matches(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if p.scheme != strings.ToLower(u.Scheme) || p.host != strings.ToLower(u.Hostname()) {
		return false
	}
	return p.port == "*" || p.port == u.Port()
}

// normalizeHost 规范化主机名:小写化并剥掉 IPv6 字面量的方括号。无端口
// 形式 net.SplitHostPort 拆不开,方括号会留在 host 里,与请求侧 url.Parse
// 取 Hostname()(恒无括号)比较时永远失配——有端口形式 SplitHostPort 已
// 剥括号,此处统一补齐无端口形式,两种写法语义一致。
func normalizeHost(host string) string {
	host = strings.ToLower(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}
