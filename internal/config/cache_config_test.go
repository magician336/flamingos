package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// config 的 cache 段测试(票 0006-02/spec 0006 配置段):段缺省(含显式
// null)= 缓存关闭(Cache 为 nil);规则存在就要能用——tools 死规则、通配
// 语法(判定单一出处 toolpattern)、ttl_ms≤0、scope 白名单外、max_entries
// 显式 ≤0 或非整数均启动即败(中文点名位置),多问题一次性聚合(D8);
// max_entries 缺省(含显式 null)= DefaultCacheMaxEntries 兜底。
func TestLoadCache(t *testing.T) {
	// validUpstream 是合法最小上游清单夹具:cache 正例也必须整体可加载。
	const validUpstream = "upstreams:\n" +
		"  - name: calc\n" +
		"    type: http\n" +
		"    url: http://127.0.0.1:9001/mcp\n"

	tests := []struct {
		name       string
		yaml       string
		want       *Cache // 期望的 cfg.Cache(成功用例)
		wantErr    []string
		notWantErr []string
	}{
		{
			name: "段缺省:缓存关闭,Cache 为 nil 指针",
			yaml: validUpstream,
			want: nil,
		},
		{
			name: "段显式 null:与缺省同义,关闭态",
			yaml: "cache:\n" + validUpstream,
			want: nil,
		},
		{
			name: "段存在而 rules 缺省:零规则零行为,合法",
			yaml: "cache:\n" +
				"  max_entries: 64\n" + validUpstream,
			want: &Cache{MaxEntries: 64},
		},
		{
			name: "完整正例:规则表与容量逐字段生效,scope 两取值都合法",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"calc_*\", \"fs_echo\"]\n" +
				"      ttl_ms: 30000\n" +
				"      scope: public\n" +
				"    - tools: [\"priv_*\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: private\n" +
				"  max_entries: 2048\n" + validUpstream,
			want: &Cache{
				Rules: []CacheRule{
					{Tools: []string{"calc_*", "fs_echo"}, TTLMS: 30000, Scope: "public"},
					{Tools: []string{"priv_*"}, TTLMS: 1000, Scope: "private"},
				},
				MaxEntries: 2048,
			},
		},
		{
			name: "max_entries 缺省:兜底 DefaultCacheMaxEntries",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"*\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: public\n" + validUpstream,
			want: &Cache{
				Rules:      []CacheRule{{Tools: []string{"*"}, TTLMS: 1000, Scope: "public"}},
				MaxEntries: DefaultCacheMaxEntries,
			},
		},
		{
			name: "max_entries 显式 null:与缺省同义,兜底默认容量",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"*\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: public\n" +
				"  max_entries:\n" + validUpstream,
			want: &Cache{
				Rules:      []CacheRule{{Tools: []string{"*"}, TTLMS: 1000, Scope: "public"}},
				MaxEntries: DefaultCacheMaxEntries,
			},
		},
		{
			name: "ttl_ms 为 0:永不生效的规则是误导状态,启动即败并点名规则位置",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"*\"]\n" +
				"      ttl_ms: 0\n" +
				"      scope: public\n" + validUpstream,
			wantErr: []string{"cache.rules[0]", "ttl_ms"},
		},
		{
			name: "ttl_ms 负数:同样拒绝",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"*\"]\n" +
				"      ttl_ms: -5\n" +
				"      scope: public\n" + validUpstream,
			wantErr: []string{"cache.rules[0]", "ttl_ms"},
		},
		{
			name: "scope 白名单外:启动即败并点名取值",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"*\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: internal\n" + validUpstream,
			wantErr: []string{"cache.rules[0]", "scope", "internal"},
		},
		{
			name: "通配反例:中间含星号的模式拒绝(语法判定经 toolpattern)",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"calc_*_add\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: public\n" + validUpstream,
			wantErr: []string{"cache.rules[0].tools[0]", "calc_*_add"},
		},
		{
			name: "通配反例:空字符串模式拒绝",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: public\n" + validUpstream,
			wantErr: []string{"cache.rules[0].tools[0]", "不能为空"},
		},
		{
			name: "tools 为空表:永不命中的死规则,启动即败",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - ttl_ms: 1000\n" +
				"      scope: public\n" + validUpstream,
			wantErr: []string{"cache.rules[0]", "tools"},
		},
		{
			name:    "max_entries 显式 0:不做静默修正,启动即败",
			yaml:    "cache:\n  max_entries: 0\n" + validUpstream,
			wantErr: []string{"cache", "max_entries"},
		},
		{
			name:    "max_entries 负数:同样拒绝",
			yaml:    "cache:\n  max_entries: -8\n" + validUpstream,
			wantErr: []string{"cache", "max_entries"},
		},
		{
			name:       "max_entries 非整数:拒绝且不泄漏解析库原文",
			yaml:       "cache:\n  max_entries: many\n" + validUpstream,
			wantErr:    []string{"cache", "max_entries", "many"},
			notWantErr: []string{"cannot unmarshal", "rawCache"},
		},
		{
			name: "list_ttl_ms 正数:tools/list hint 毫秒数逐字段生效",
			yaml: "cache:\n" +
				"  list_ttl_ms: 5000\n" +
				"  rules:\n" +
				"    - tools: [\"*\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: public\n" + validUpstream,
			want: &Cache{
				ListTTLMS:  5000,
				Rules:      []CacheRule{{Tools: []string{"*"}, TTLMS: 1000, Scope: "public"}},
				MaxEntries: DefaultCacheMaxEntries,
			},
		},
		{
			name: "list_ttl_ms 缺省:0 = 不宣告(缺省 0 与显式 0 同值无歧义)",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"*\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: public\n" + validUpstream,
			want: &Cache{
				ListTTLMS:  0,
				Rules:      []CacheRule{{Tools: []string{"*"}, TTLMS: 1000, Scope: "public"}},
				MaxEntries: DefaultCacheMaxEntries,
			},
		},
		{
			name: "list_ttl_ms 显式 null:与 0 同义,不宣告",
			yaml: "cache:\n" +
				"  list_ttl_ms:\n" +
				"  rules:\n" +
				"    - tools: [\"*\"]\n" +
				"      ttl_ms: 1000\n" +
				"      scope: public\n" + validUpstream,
			want: &Cache{
				ListTTLMS:  0,
				Rules:      []CacheRule{{Tools: []string{"*"}, TTLMS: 1000, Scope: "public"}},
				MaxEntries: DefaultCacheMaxEntries,
			},
		},
		{
			name:    "list_ttl_ms 负数:负的 TTL 无法宣告,启动即败并点名",
			yaml:    "cache:\n  list_ttl_ms: -500\n" + validUpstream,
			wantErr: []string{"cache", "list_ttl_ms", "-500"},
		},
		{
			name: "聚合断言:rules 问题与 list_ttl_ms 负数一次性全部列出(D8)",
			yaml: "cache:\n" +
				"  list_ttl_ms: -1\n" +
				"  rules:\n" +
				"    - ttl_ms: 0\n" +
				"      scope: public\n" + validUpstream,
			wantErr: []string{
				"共 3 处问题",
				"list_ttl_ms",
				"rules[0]", "ttl_ms",
				"rules[0]", "tools",
			},
		},
		{
			name: "聚合断言:坏模式 + ttl_ms 为 0 + scope 非法一次性全部列出(D8)",
			yaml: "cache:\n" +
				"  rules:\n" +
				"    - tools: [\"calc_*_x\"]\n" +
				"      ttl_ms: 0\n" +
				"      scope: wrong\n" + validUpstream,
			wantErr: []string{
				"共 3 处问题",
				"tools[0]", "calc_*_x",
				"ttl_ms",
				"scope",
			},
		},
		{
			name: "cache 段未知字段:键名 typo 报错,不泄漏解析库原文与内部类型名",
			yaml: "cache:\n" +
				"  rulz:\n" +
				"    - tools: [\"*\"]\n" + validUpstream,
			wantErr:    []string{"字段", "rulz", "无法识别"},
			notWantErr: []string{"not found in type", "rawCache"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "flamingos.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatalf("写夹具失败: %v", err)
			}

			got, err := Load(path)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("期望报错,实际成功返回:%+v", got)
				}
				for _, substr := range tt.wantErr {
					if !strings.Contains(err.Error(), substr) {
						t.Errorf("错误信息缺少 %q:实际 %q", substr, err.Error())
					}
				}
				for _, substr := range tt.notWantErr {
					if strings.Contains(err.Error(), substr) {
						t.Errorf("错误信息泄漏 %q:实际 %q", substr, err.Error())
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("期望成功,实际报错: %v", err)
			}
			if !reflect.DeepEqual(got.Cache, tt.want) {
				t.Errorf("Cache 不符:\n期望 %+v\n实际 %+v", tt.want, got.Cache)
			}
			// 关闭态契约的回归钉子:cache 缺省时其余字段不受影响。
			if got.Cache == nil && len(got.Upstreams) != 1 {
				t.Errorf("段缺省时上游清单应不受影响,实际 %+v", got.Upstreams)
			}
		})
	}
}
