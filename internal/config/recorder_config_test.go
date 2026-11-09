// config 的 recorder 段测试(spec 0008 票 01 配置段):段缺省(含显式
// null)= 录制关闭(Recorder 为 nil);段存在而 path 缺失 = 启动即败(中文
// 点名);与 policy.audit/cache 并存互不干扰。表驱动,与 policy.audit 段
// 测试同构。
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// recorderValidUpstream 是合法最小上游清单夹具:recorder 正例也必须整体可加载。
const recorderValidUpstream = "upstreams:\n" +
	"  - name: calc\n" +
	"    type: http\n" +
	"    url: http://127.0.0.1:9001/mcp\n"

// writeLoadForTest 把 YAML 文本落进临时目录并走真实 Load 流水线:与
// loadForTest(audit_config_test.go)同款手法独立成函数,避免跨文件共享
// 带来隐式耦合。
func writeLoadForTest(t *testing.T, yamlText string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flamingos.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	return Load(path)
}

func TestLoadRecorder(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		want       *Recorder // 期望的 cfg.Recorder(成功用例)
		wantErr    []string
		notWantErr []string
	}{
		{
			name: "recorder 段缺省:配置成立但录制关闭",
			yaml: recorderValidUpstream,
			want: nil,
		},
		{
			name: "recorder 段显式 null:与缺省同义,关闭态",
			yaml: "recorder: null\n" + recorderValidUpstream,
			want: nil,
		},
		{
			name: "recorder 完整正例:path 逐字段生效",
			yaml: "recorder:\n" +
				"  path: /var/log/flamingos-recording.jsonl\n" + recorderValidUpstream,
			want: &Recorder{Path: "/var/log/flamingos-recording.jsonl"},
		},
		{
			name: "recorder 与 policy.audit 并存:两段互不干扰,各自生效",
			yaml: "recorder:\n" +
				"  path: /tmp/recording.jsonl\n" +
				"policy:\n" +
				"  audit:\n" +
				"    path: /tmp/audit.jsonl\n" + recorderValidUpstream,
			want: &Recorder{Path: "/tmp/recording.jsonl"},
		},
		{
			name:    "recorder 段存在而 path 缺失:启动即败,中文点名 recorder",
			yaml:    "recorder: {}\n" + recorderValidUpstream,
			wantErr: []string{"recorder", "path", "录制"},
		},
		{
			name: "recorder path 为空串:与缺失同罪,启动即败",
			yaml: "recorder:\n" +
				"  path: \"\"\n" + recorderValidUpstream,
			wantErr: []string{"recorder", "path", "录制"},
		},
		{
			name: "recorder 校验错误与 policy.audit 校验错误一次性聚合",
			yaml: "recorder: {}\n" +
				"policy:\n" +
				"  audit: {}\n" + recorderValidUpstream,
			wantErr:    []string{"recorder", "policy.audit"},
			notWantErr: []string{"录制文件无法打开"}, // 配置层不碰文件系统,可写性归 recorder 包构造期
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := writeLoadForTest(t, tt.yaml)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("期望报错 %v,实际成功: %+v", tt.wantErr, cfg.Recorder)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("错误应含 %q,实际 %q", want, err.Error())
					}
				}
				for _, notWant := range tt.notWantErr {
					if strings.Contains(err.Error(), notWant) {
						t.Errorf("错误不应含 %q(config 层不碰文件系统),实际 %q", notWant, err.Error())
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("期望成功,实际报错: %v", err)
			}
			if !reflect.DeepEqual(cfg.Recorder, tt.want) {
				t.Errorf("cfg.Recorder = %+v,期望 %+v", cfg.Recorder, tt.want)
			}
			// 并存正例的 policy.audit 副产物核对(两段互不干扰的另一半)。
			if tt.name == "recorder 与 policy.audit 并存:两段互不干扰,各自生效" {
				if cfg.Policy == nil || cfg.Policy.Audit == nil || cfg.Policy.Audit.Path != "/tmp/audit.jsonl" {
					t.Errorf("并存正例中 policy.audit 应独立生效,实际 %+v", cfg.Policy)
				}
			}
		})
	}
}
