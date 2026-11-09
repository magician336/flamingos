// originrule 的语法单一测试面(2026-11-05 架构评审 C1):Parse 合法性表
// 与 Matches 匹配表在此归并——原 config 侧 13 例语法用例与 transport 侧
// 散落在中间件表里的匹配语义由此收口,两个消费方只测各自的管线(config
// 测"Parse 失败→中文点名",transport 测"编译→403 接线")。语义冻结自
// 提取前的实现:端口三形态、大小写不敏感、fail-closed;唯一行为变化是
// 无端口 IPv6 条目的方括号规范化(原实现方括号残留导致该形态永不匹配,
// 无测试覆盖的潜在不一致,提取时一并修复并有专例钉住)。
package originrule

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		entry   string
		wantErr string // 空串 = 合法;非空 = 错误信息应包含的片段
	}{
		// ---- 合法:端口三形态 ----
		{name: "通配端口:合法", entry: "http://localhost:*"},
		{name: "精确端口", entry: "http://localhost:3000"},
		{name: "无端口形式", entry: "https://console.example.com"},

		// ---- 合法:大小写与 IPv6 规范化 ----
		{name: "scheme 与 host 大小写不敏感", entry: "HTTP://LOCALHOST:*"},
		{name: "IPv6 有端口:方括号由 SplitHostPort 剥除", entry: "http://[::1]:3000"},
		{name: "IPv6 无端口:方括号由 normalizeHost 剥除(提取时修复的不一致)", entry: "http://[::1]"},

		// ---- 非法:逐条件点名 ----
		{name: "缺 scheme:// 前缀:裸主机名", entry: "localhost:*", wantErr: "缺少 scheme:// 前缀"},
		{name: "scheme 非 http(s):ftp", entry: "ftp://example.com", wantErr: "只接受 http 或 https"},
		{name: "缺主机名:仅 scheme,rest 为空走缺主机名分支", entry: "http://", wantErr: "缺少主机名"},
		{name: "带路径无端口:整段是 host,含路径即拒", entry: "http://localhost/app/", wantErr: "缺少主机名或含路径"},
		{name: "带路径带端口:路径字符落进端口段,走端口不合法分支", entry: "http://localhost:3000/app/", wantErr: "不合法"},
		{name: "端口非数字:8O80", entry: "http://localhost:8O80", wantErr: "不合法"},
		{name: "端口越界:65536", entry: "http://localhost:65536", wantErr: "不合法"},
		{name: "端口为零", entry: "http://localhost:0", wantErr: "不合法"},
		{name: "错误信息点名原条目", entry: "ftp://a.example.com", wantErr: "ftp://a.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse(tt.entry)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("%q 应合法,实际报错: %v", tt.entry, err)
				}
				if strings.ContainsAny(p.host, "[]") {
					t.Errorf("host 应完成 IPv6 括号规范化,实际 %q", p.host)
				}
				return
			}
			if err == nil {
				t.Fatalf("%q 应被拒,实际解析为 %+v", tt.entry, p)
			}
			if !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), tt.entry) {
				t.Errorf("错误信息应含 %q 与原条目 %q,实际 %v", tt.wantErr, tt.entry, err)
			}
		})
	}
}

func TestMatches(t *testing.T) {
	must := func(entry string) Pattern {
		t.Helper()
		p, err := Parse(entry)
		if err != nil {
			t.Fatalf("夹具条目 %q 应合法: %v", entry, err)
		}
		return p
	}
	tests := []struct {
		name    string
		entry   string
		origin  string
		matches bool
	}{
		// ---- 端口三形态 ----
		{name: "通配端口:任意端口命中", entry: "http://localhost:*", origin: "http://localhost:3000", matches: true},
		{name: "通配端口:请求无显式端口也命中", entry: "http://localhost:*", origin: "http://localhost", matches: true},
		{name: "通配端口:scheme 不同不命中", entry: "http://localhost:*", origin: "https://localhost:3000"},
		{name: "通配端口:host 不同不命中", entry: "http://localhost:*", origin: "http://evil.example.com:3000"},
		{name: "通配端口:host 前缀混淆不命中", entry: "http://localhost:*", origin: "http://localhosty:3000"},
		{name: "精确端口:相等命中", entry: "http://localhost:3000", origin: "http://localhost:3000", matches: true},
		{name: "精确端口:不同不命中", entry: "http://localhost:3000", origin: "http://localhost:3001"},
		{name: "无端口条目:请求无端口命中(收紧形态)", entry: "https://console.example.com", origin: "https://console.example.com", matches: true},
		{name: "无端口条目:请求带端口不命中", entry: "https://console.example.com", origin: "https://console.example.com:8443"},

		// ---- 大小写与 IPv6 ----
		{name: "scheme 与 host 大小写不敏感", entry: "http://localhost:*", origin: "HTTP://LOCALHOST:3000", matches: true},
		{name: "IPv6 有端口:精确命中", entry: "http://[::1]:3000", origin: "http://[::1]:3000", matches: true},
		{name: "IPv6 有端口:端口不同不命中", entry: "http://[::1]:3000", origin: "http://[::1]:4000"},
		{name: "IPv6 无端口条目:无括号规范化后命中(提取时修复)", entry: "http://[::1]", origin: "http://[::1]", matches: true},

		// ---- fail-closed:解析不了的 Origin 一律不命中 ----
		{name: "裸文本 Origin 不命中", entry: "http://localhost:*", origin: "not a url"},
		{name: "javascript 伪协议不命中", entry: "http://localhost:*", origin: "javascript:alert(1)"},
		{name: "无 host 的 Origin 不命中", entry: "http://localhost:*", origin: "http://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := must(tt.entry).Matches(tt.origin)
			if got != tt.matches {
				t.Fatalf("条目 %q × Origin %q:期望命中=%v,实际 %v", tt.entry, tt.origin, tt.matches, got)
			}
		})
	}
}
