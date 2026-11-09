// LoadCases 测试:录制文件即回放用例的消费端解析——格式校验(版本/方法/
// kind 三态/tool 非空)与手编覆写字段(schema/延迟上限)的解析生效。
package evalx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/magician336/flamingos/internal/recorder"
)

// writeLines 把若干录制行写进临时 JSONL 文件,返回路径(测试夹具)。
func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recording.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("写夹具失败: %v", err)
	}
	return path
}

// successLine 是一条标准成功录制行(键序刻意打乱:等值与解析均键序无关)。
const successLine = `{"v":"1","seq":1,"time":"2026-11-08T00:00:00Z","method":"tools/call",` +
	`"tool":"calc_greet","args":{"name":"ada"},"kind":"success",` +
	`"result":{"content":[{"type":"text","text":"hi ada"}],"isError":false},` +
	`"error":"","latency_ms":3,"trace_id":"abc","role":"admin"}`

// transportLine 是一条标准传输错误录制行(result 为 null,文案点名上游)。
const transportLine = `{"v":"1","seq":2,"time":"2026-11-08T00:00:01Z","method":"tools/call",` +
	`"tool":"calc_greet","args":{"name":"bob"},"kind":"transport_error",` +
	`"result":null,"error":"上游 \"calc\" 不可用: dial refused","latency_ms":0,` +
	`"trace_id":"","role":""}`

func TestLoadCases解析标准录制文件(t *testing.T) {
	path := writeLines(t, successLine, transportLine)
	cases, err := LoadCases(path)
	if err != nil {
		t.Fatalf("合法录制文件应加载成功,实际错误: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("应解析出 2 条用例,实际 %d 条", len(cases))
	}
	c := cases[0]
	if c.V != recorder.FormatVersion || c.Seq != 1 || c.Method != "tools/call" || c.Tool != "calc_greet" {
		t.Errorf("条目头字段不符: v=%q seq=%d method=%q tool=%q", c.V, c.Seq, c.Method, c.Tool)
	}
	if string(c.Args) != `{"name":"ada"}` {
		t.Errorf("args 应原样保留原始字节,实际 %s", c.Args)
	}
	if c.Kind != recorder.KindSuccess {
		t.Errorf("kind 应为 success,实际 %q", c.Kind)
	}
	if c.Role != "admin" || c.TraceID != "abc" {
		t.Errorf("role/trace_id 应冗余保留,实际 role=%q trace_id=%q", c.Role, c.TraceID)
	}
	if c.LatencyLimitMS != 0 || c.Schema != nil {
		t.Errorf("未覆写条目的可选字段应为零值,实际 schema=%s limit=%d", c.Schema, c.LatencyLimitMS)
	}
	te := cases[1]
	if te.Kind != recorder.KindTransportError || te.Error == "" || string(te.Result) != "null" {
		t.Errorf("传输错误条目应 kind=transport_error 且 error 非空 result 为 JSON null,实际 kind=%q error=%q result=%s",
			te.Kind, te.Error, te.Result)
	}
}

func TestLoadCases解析手编覆写字段(t *testing.T) {
	// golden + 覆写:同一录制行手编追加 schema 与延迟上限字段,单文件自足。
	overwritten := `{"v":"1","seq":1,"time":"2026-11-08T00:00:00Z","method":"tools/call",` +
		`"tool":"calc_greet","args":{"name":"ada"},"kind":"success",` +
		`"result":{"content":[{"type":"text","text":"hi"}],"isError":false},` +
		`"latency_ms":3,"latency_limit_ms":250,` +
		`"schema":{"type":"object","required":["content"],"properties":{"content":{"type":"array"}}}}`
	path := writeLines(t, overwritten)
	cases, err := LoadCases(path)
	if err != nil {
		t.Fatalf("覆写行应加载成功,实际错误: %v", err)
	}
	c := cases[0]
	if c.LatencyLimitMS != 250 {
		t.Errorf("latency_limit_ms 覆写应生效,实际 %d", c.LatencyLimitMS)
	}
	if c.Schema == nil {
		t.Fatal("schema 覆写应生效并解析")
	}
	if c.resolved == nil {
		t.Error("LoadCases 应预解析 schema 供 grader 复用")
	}
	var want map[string]any
	if err := json.Unmarshal(c.Schema, &want); err != nil || want["type"] != "object" {
		t.Errorf("schema 原始字节应原样保留,实际 %s (err=%v)", c.Schema, err)
	}
}

func TestLoadCases错误面(t *testing.T) {
	t.Run("文件不可读:错误点名路径", func(t *testing.T) {
		_, err := LoadCases(filepath.Join(t.TempDir(), "不存在.jsonl"))
		if err == nil || !strings.Contains(err.Error(), "不存在.jsonl") {
			t.Errorf("错误应点名路径,实际 %v", err)
		}
	})
	t.Run("空文件:无基准即错误", func(t *testing.T) {
		path := writeLines(t)
		if _, err := LoadCases(path); err == nil {
			t.Error("空文件应报错误而非零用例")
		}
	})
	t.Run("坏 JSON 行:错误点名行号", func(t *testing.T) {
		path := writeLines(t, successLine, `{"v":"1","seq":2,`)
		_, err := LoadCases(path)
		if err == nil || !strings.Contains(err.Error(), "2") {
			t.Errorf("错误应点名行号 2,实际 %v", err)
		}
	})
	t.Run("格式版本不符", func(t *testing.T) {
		path := writeLines(t, strings.Replace(successLine, `"v":"1"`, `"v":"999"`, 1))
		_, err := LoadCases(path)
		if err == nil {
			t.Error("版本不符应报错误")
		}
	})
	t.Run("method 非 tools/call", func(t *testing.T) {
		path := writeLines(t, strings.Replace(successLine, `tools/call`, `resources/list`, 1))
		_, err := LoadCases(path)
		if err == nil {
			t.Error("非 tools/call 条目应报错误")
		}
	})
	t.Run("kind 非法", func(t *testing.T) {
		path := writeLines(t, strings.Replace(successLine, `"kind":"success"`, `"kind":"崩溃"`, 1))
		_, err := LoadCases(path)
		if err == nil {
			t.Error("非法 kind 应报错误")
		}
	})
	t.Run("tool 为空:不可回放条目即文件错误", func(t *testing.T) {
		path := writeLines(t, strings.Replace(successLine, `"tool":"calc_greet"`, `"tool":""`, 1))
		_, err := LoadCases(path)
		if err == nil {
			t.Error("tool 为空的条目应报错误")
		}
	})
	t.Run("手编 schema 坏:文件错误", func(t *testing.T) {
		// jsonschema-go 对未知 type 词表值宽容(校验时自然败),未定义 $ref
		// 在 Resolve 即报错——坏 schema 用后者钉住。
		path := writeLines(t, strings.Replace(successLine, `"latency_ms":3`, `"schema":{"$ref":"#/definitions/缺失"},"latency_ms":3`, 1))
		_, err := LoadCases(path)
		if err == nil {
			t.Error("无法解析的 schema 应报文件错误")
		}
	})
}
