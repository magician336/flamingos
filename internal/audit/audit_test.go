// audit 包测试(spec 0005 接缝③):写盘器写 t.TempDir 断言 JSON-lines
// 字段与追加顺序,启动不可写路径报错中文点名;测试零残留(Story 21)。
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNewFailFastOnUnwritablePath 钉死启动即校验可写(Story 9):路径所在
// 目录不存在,构造即报错并中文点名路径——磁盘/权限问题在启动瞬间暴露,
// 而非等到第一条审计记录静默丢失。
func TestNewFailFastOnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "audit.jsonl")
	rec, err := New(Config{Path: path})
	if err == nil {
		_ = rec.Close()
		t.Fatal("不可写路径应构造即败,实际成功")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("错误应点名路径 %q,实际 %q", path, err.Error())
	}
	if !strings.Contains(err.Error(), "审计") {
		t.Errorf("错误应中文点名审计文件,实际 %q", err.Error())
	}
}

// TestAppendWritesJSONLinesInOrder 钉死 JSON-lines 记录形态(Story 8):
// 每次追加一行独立 JSON 对象,字段恰为 ts/role/tool/decision/error,
// 多条记录按追加顺序落盘,ts 是可解析的时刻。
func TestAppendWritesJSONLinesInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	rec, err := New(Config{Path: path})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	before := time.Now()
	rec.Append(Record{Role: "viewer", Tool: "calc_greet", Decision: DecisionAllow, Error: false})
	rec.Append(Record{Role: "admin", Tool: "fs_rm", Decision: DecisionDeny, Error: true})
	if err := rec.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读审计文件失败: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("应恰两行 JSON-lines,实际 %d 行: %q", len(lines), string(raw))
	}
	var got []Record
	for i, line := range lines {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON 对象: %v(%q)", i+1, err, line)
		}
		if ts, err := time.Parse(time.RFC3339Nano, rec.TS); err != nil || ts.Before(before) {
			t.Errorf("第 %d 行 ts %q 应为 append 时刻的 RFC3339 时间", i+1, rec.TS)
		}
		got = append(got, rec)
	}
	// ts 由 Append 落盘时刻填写,逐字段断言其余字段与追加顺序。
	want := []Record{
		{Role: "viewer", Tool: "calc_greet", Decision: DecisionAllow, Error: false},
		{Role: "admin", Tool: "fs_rm", Decision: DecisionDeny, Error: true},
	}
	for i, w := range want {
		w.TS = got[i].TS // ts 已单独断言
		if got[i] != w {
			t.Errorf("第 %d 行记录 = %+v,期望 %+v", i+1, got[i], w)
		}
	}
}
