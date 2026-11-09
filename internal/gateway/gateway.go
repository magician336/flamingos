// Package gateway 实现 N 个上游的聚合与路由:一次 HandleToolsList 并发拉取
// 全部上游清单、合并为带"命名空间_"前缀的聚合清单;一次 HandleToolsCall
// 按第一个下划线拆出命名空间、查表选中上游、以原始 tool 名与原始参数转发。
//
// 命名空间规则单一出处 = 架构 §4.2 R1-R3:命名空间名仅 [a-zA-Z0-9-] 且不含
// 下划线(由 config 层校验保证,本包不重复校验);tool 全名 = 命名空间 + "_"
// + 原始名;解析按第一个下划线拆分,永无歧义。依赖方向 gateway → upstream。
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// namespaceSep 是命名空间与原始 tool 名的分隔符(架构 §4.2 R2/R3)。
const namespaceSep = "_"

// Gateway 聚合 clients 中的全部上游并在调用时路由;构造后只读,
// 多 goroutine 并发使用安全。
type Gateway struct {
	clients map[string]upstream.Client // 键 = 命名空间
}

// New 以"命名空间 → 上游客户端"映射构造网关;测试可注入任意假实现。
// 映射被防御性拷贝:构造完成后调用方改动原映射不影响网关。
func New(clients map[string]upstream.Client) *Gateway {
	copied := make(map[string]upstream.Client, len(clients))
	for ns, client := range clients {
		copied[ns] = client
	}
	return &Gateway{clients: copied}
}

// HandleToolsList 并发拉取全部上游的 tool 清单并合并:每个 tool 名加上
// "命名空间_" 前缀(多上游同名原始 tool 不冲突),描述与输入 schema
// 原样保留(浅拷贝改名,不原地修改上游对象)。任一上游失败即整体失败,
// 错误点名上游(设计 D8:不带病聚合);上游数量增多时启动延迟不线性叠加。
func (g *Gateway) HandleToolsList(ctx context.Context) ([]*mcp.Tool, error) {
	names := g.sortedNamespaces()
	results, errs := fanout(ctx, g.clients, names)
	for i, err := range errs {
		if err != nil {
			return nil, upstreamFailure("列举清单", names[i], err)
		}
	}
	var aggregated []*mcp.Tool
	for i, ns := range names {
		aggregated = append(aggregated, prefixTools(ns, results[i])...)
	}
	return aggregated, nil
}

// fanout 并发调用全部上游的 ListTools:每个结果与错误按下标收位,
// 完成顺序不影响返回顺序(确定性);任一失败由调用方在 names 序上
// 取首个失败,同样与完成时序无关。
func fanout(ctx context.Context, clients map[string]upstream.Client, names []string) ([][]*mcp.Tool, []error) {
	results := make([][]*mcp.Tool, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, ns := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = clients[ns].ListTools(ctx)
		}()
	}
	wg.Wait()
	return results, errs
}

// HandleToolsCall 路由一次 tool 调用:按第一个下划线拆出命名空间与原始
// tool 名(架构 §4.2 R2,R1 保证拆分永无歧义),查表选中上游,以原始名与
// 原始参数字节转发;上游结果原样返回(含 IsError=true 的业务失败应答,
// 错误契约②,不吞不改)。名字无下划线、原始名为空或前缀无匹配时,
// 返回明确的中文错误且不触碰任何上游。
func (g *Gateway) HandleToolsCall(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	ns, orig, found := strings.Cut(name, namespaceSep)
	if !found {
		return nil, fmt.Errorf("tool 名 %q 缺少命名空间前缀:应为 命名空间_原始名(按第一个下划线 %q 拆分)", name, namespaceSep)
	}
	if orig == "" {
		return nil, fmt.Errorf("tool 名 %q 的原始名为空:应为 命名空间_原始名", name)
	}
	client, ok := g.clients[ns]
	if !ok {
		return nil, fmt.Errorf("tool 名 %q 的命名空间 %q 无匹配上游", name, ns)
	}
	res, err := client.CallTool(ctx, orig, args)
	if err != nil {
		return nil, callFailure(ns, err)
	}
	return res, nil
}

// sortedNamespaces 返回按字典序排列的命名空间列表:map 迭代随机,
// 排序使聚合清单与错误选取都有确定顺序。
func (g *Gateway) sortedNamespaces() []string {
	names := make([]string, 0, len(g.clients))
	for ns := range g.clients {
		names = append(names, ns)
	}
	sort.Strings(names)
	return names
}

// callFailure 把上游调用失败翻译为网关错误契约:ErrClosed 原样上抛;
// TransportError 映射为点名上游的 *UpstreamUnavailableError;其余错误
// 以 %w 包装保持判别能力。
func callFailure(ns string, err error) error {
	if errors.Is(err, upstream.ErrClosed) {
		return err
	}
	return upstreamFailure("调用", ns, err)
}

// prefixTools 为单个上游的 tool 清单加命名空间前缀;非法聚合名跳过并警示,
// 单个坏 tool 不拖死整体(SDK 对非法名只打日志不拒绝,网关自守)。
func prefixTools(ns string, tools []*mcp.Tool) []*mcp.Tool {
	prefixed := make([]*mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		full := ns + namespaceSep + tool.Name
		if err := validateAggregatedName(tool.Name, full); err != nil {
			logSkipped(ns, tool.Name, full, err)
			continue
		}
		renamed := *tool // 浅拷贝:不原地修改上游返回的对象
		renamed.Name = full
		prefixed = append(prefixed, &renamed)
	}
	return prefixed
}
