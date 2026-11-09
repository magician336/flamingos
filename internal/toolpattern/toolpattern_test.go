// toolpattern 的语法单一测试面(spec 0006 票 01):Parse 合法性表与
// Matches 匹配表在此归并——原 policy TestDecide 中的通配三形态语法用例
// 由此收口,policy 只保留判定管线用例(deny 优先、fail-close)。语义冻结
// 自提取前的实现:三形态判定与 policy 规则匹配现行为逐字节等价,语法判
// 定(星号个数与位置)与错误文案冻结自 config.checkPattern 现校验——
// config 未来切换消费时补位置前缀后原样上报,文案零变化。
package toolpattern

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr string // 空串 = 合法;非空 = 错误信息应包含的片段
	}{
		// ---- 合法:三形态 ----
		{name: "全量通配", pattern: "*"},
		{name: "前缀通配", pattern: "ns_*"},
		{name: "前缀通配:工具名数字后缀风格", pattern: "calc_*"},
		{name: "精确名", pattern: "calc_add"},
		{name: "精确名:含数字", pattern: "fs_echo2"},

		// ---- 非法:逐条件点名 ----
		{name: "空串", pattern: "", wantErr: "模式不能为空"},
		{name: "中间星:ns_*_x", pattern: "ns_*_x", wantErr: "不合法"},
		{name: "首星:*ns(非单独出现)", pattern: "*ns", wantErr: "不合法"},
		{name: "双星:**", pattern: "**", wantErr: "不合法"},
		{name: "双尾星:calc_**", pattern: "calc_**", wantErr: "不合法"},
		{name: "错误信息点名原模式", pattern: "ns_*_x", wantErr: "ns_*_x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse(tt.pattern)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("%q 应合法,实际报错: %v", tt.pattern, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%q 应被拒,实际解析为 %+v", tt.pattern, p)
			}
			if !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), tt.pattern) {
				t.Errorf("错误信息应含 %q 与原模式 %q,实际 %v", tt.wantErr, tt.pattern, err)
			}
		})
	}
}

func TestMatches(t *testing.T) {
	must := func(pattern string) Pattern {
		t.Helper()
		p, err := Parse(pattern)
		if err != nil {
			t.Fatalf("夹具模式 %q 应合法: %v", pattern, err)
		}
		return p
	}
	tests := []struct {
		name    string
		pattern string
		tool    string
		matches bool
	}{
		// ---- 全量通配 ----
		{name: "全量:常规工具名命中", pattern: "*", tool: "calc_add", matches: true},
		{name: "全量:任意形状命中", pattern: "*", tool: "fs/rm/x", matches: true},
		{name: "全量:空工具名也命中(恒真)", pattern: "*", tool: "", matches: true},

		// ---- 前缀通配 ----
		{name: "前缀:命中", pattern: "calc_*", tool: "calc_multiply", matches: true},
		{name: "前缀边界:恰好等于前缀字面量命中", pattern: "calc_*", tool: "calc_", matches: true},
		{name: "前缀边界:不以该前缀开头不命中", pattern: "calc_*", tool: "calcx_add", matches: false},
		{name: "前缀边界:短于前缀字面量不命中", pattern: "calc_*", tool: "calc", matches: false},

		// ---- 精确 ----
		{name: "精确:点名命中", pattern: "fs_echo", tool: "fs_echo", matches: true},
		{name: "精确边界:同名加尾缀不命中(不前缀误伤)", pattern: "fs_echo", tool: "fs_echo2", matches: false},
		{name: "精确边界:空工具名不命中", pattern: "fs_echo", tool: "", matches: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := must(tt.pattern).Matches(tt.tool)
			if got != tt.matches {
				t.Fatalf("模式 %q × 工具名 %q:期望命中=%v,实际 %v", tt.pattern, tt.tool, tt.matches, got)
			}
		})
	}
}
