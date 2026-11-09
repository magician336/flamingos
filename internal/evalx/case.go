// 用例加载:录制文件即回放用例(golden + 覆写)的读取与校验——回放用例
// 要求格式完备可回放,任何一行格式不完备 = 文件错误(CLI 判自身运行错误,
// 退出码 2),与断言失败(退出码 1)严格分开。
package evalx

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/magician336/flamingos/internal/recorder"
)

// LoadCases 读录制文件并解析为回放用例:逐行 JSON 解析、格式校验(格式
// 版本、method、kind 三态、tool 非空)与手编覆写字段(schema/延迟上限)
// 一并在此;schema 预解析进 resolved 供 grader 复用。返回的错误是自身运行
// 错误(文件不可读、格式不完备),CLI 据此映射退出码 2。
func LoadCases(path string) ([]Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("录制文件 %s 无法读取: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // 只读句柄,关闭失败无资源泄漏后果

	var cases []Case
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue // 容忍尾随空行(编辑器手编常见形态)
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("录制文件 %s 第 %d 行不是合法 JSON: %w", path, lineNo, err)
		}
		if err := validateCase(&c); err != nil {
			return nil, fmt.Errorf("录制文件 %s 第 %d 行: %w", path, lineNo, err)
		}
		if c.Schema != nil {
			var s jsonschema.Schema
			if err := json.Unmarshal(c.Schema, &s); err != nil {
				return nil, fmt.Errorf("录制文件 %s 第 %d 行的 schema 覆写无法解析: %w", path, lineNo, err)
			}
			resolved, err := s.Resolve(nil)
			if err != nil {
				return nil, fmt.Errorf("录制文件 %s 第 %d 行的 schema 覆写无法定稿: %w", path, lineNo, err)
			}
			c.resolved = resolved
		}
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("录制文件 %s 读取失败: %w", path, err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("录制文件 %s 不含任何录制条目:行为基准为空", path)
	}
	return cases, nil
}

// validateCase 校验单条用例的可回放性:格式版本恒定、method 恒 tools/call、
// kind 三态、tool 非空(录制端 params 形状异常的空 tool 行是观察记录而非
// 可回放用例,消费端拒绝并点名)。
func validateCase(c *Case) error {
	if c.V != recorder.FormatVersion {
		return fmt.Errorf("格式版本不符:期望 %q 实际 %q(录制文件不做跨版本迁移)", recorder.FormatVersion, c.V)
	}
	if c.Method != methodCallTool {
		return fmt.Errorf("method 应恒为 %s,实际 %q", methodCallTool, c.Method)
	}
	switch c.Kind {
	case recorder.KindSuccess, recorder.KindBusinessFailure, recorder.KindTransportError:
	default:
		return fmt.Errorf("kind 应为 %s|%s|%s 三态之一,实际 %q",
			recorder.KindSuccess, recorder.KindBusinessFailure, recorder.KindTransportError, c.Kind)
	}
	if c.Tool == "" {
		return fmt.Errorf("tool 为空:该行是录制端的形状异常观察记录,不构成可回放用例")
	}
	return nil
}
