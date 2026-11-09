// 全量压测场景:恒定速率三档 + 一次阶梯加载 + 缓存命中态 vs 关闭态对比,
// 相对开销臂 = 攻击器分别打网关端点与直打上游夹具端点的同分位差。全部场景
// 参数为本文件常量(报告 docs/benchmark.md 与之一致),经环境变量
// FLAMINGOS_BENCHMARK=1 显式触发,常规回归默认门控跳过(spec 0007
// Implementation Decisions:压测默认门控不进常规回归)。
package benchmark

import (
	"fmt"
	"os"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// 全量场景常量:档位、时长、样本数由恒定速率×时长决定,复现命令与报告
// 数字均以这些常量为出处。(Go 数组字面量不可为常量,速率表以包级 var
// 固定初始化,元素值逐个对应下列常量,运行期不可变。)
const (
	// 恒定三档:合理梯度覆盖轻载/中载/重载,每档 15s(样本数 750/3000/7500)。
	tier1Rate    = 50
	tier2Rate    = 200
	tier3Rate    = 500
	tierDuration = 15 * time.Second

	// 阶梯加载:速率逐级抬升观察容量拐点,每级 5s(共 25s,样本数按级累积)。
	stair1Rate       = 50
	stair2Rate       = 100
	stair3Rate       = 200
	stair4Rate       = 400
	stair5Rate       = 800
	staircaseStepDur = 5 * time.Second

	// 缓存对比档:200 rps × 15s,public 规则 ttl 5s 远大于 5ms 请求间隔,
	// 首次 miss 后全部命中(固定参数字节保证同键)。
	cacheCompareRate     = tier2Rate
	cacheCompareDuration = 15 * time.Second
	cacheRuleTTLMS       = 5000

	// 每臂正式计量前的同速率预热时长:建连与代码路径热身,预热结果不入计量。
	warmupDuration = 1 * time.Second

	// 全量门控环境变量名。
	fullGateEnv = "FLAMINGOS_BENCHMARK"
)

// 速率表:Go 数组字面量不可为常量,以包级 var 固定初始化(运行期不可变),
// 元素值逐个对应上列单个速率常量。
var (
	tierRates      = [3]int{tier1Rate, tier2Rate, tier3Rate}
	staircaseRates = [5]int{stair1Rate, stair2Rate, stair3Rate, stair4Rate, stair5Rate}
)

// TestBenchmarkFullScenarios 是唯一全量入口:一条显式命令跑完全部场景并
// 产出 docs/benchmark.md 的全部数字(故事 9:数字可重取、跨时点可对比)。
func TestBenchmarkFullScenarios(t *testing.T) {
	if os.Getenv(fullGateEnv) != "1" {
		t.Skip("全量压测门控:设置 FLAMINGOS_BENCHMARK=1 显式触发(常规回归不跑长负载)")
	}

	fixtureAddr := freeAddr(t)
	startHTTPFixture(t, fixtureAddr)
	fixtureURL := "http://" + fixtureAddr + "/mcp"

	// 两个网关形态共享同一夹具进程:缓存关闭态与缓存命中态各一实例,
	// 配置差异只在 cache 段(policy/telemetry 全缺省);监听地址与配置内
	// 地址必须同值,先取址再渲染。
	gwAddr := freeAddr(t)
	gwEndpoint := startGateway(t, gwAddr, gatewayConfigYAML(gwAddr, fixtureURL))
	cacheAddr := freeAddr(t)
	cacheGwEndpoint := startGateway(t, cacheAddr, cacheGatewayConfigYAML(cacheAddr, fixtureURL))

	// 攻击前前提复核:三端点各一例裸调用成功(Stateless 裸 POST 钉子)。
	bareCallCheck(t, gwEndpoint, gatewayToolName())
	bareCallCheck(t, cacheGwEndpoint, gatewayToolName())
	bareCallCheck(t, fixtureURL, upstreamTool)

	runConstantTiers(t, gwEndpoint, fixtureURL)
	runStaircase(t, gwEndpoint)
	runCacheCompare(t, gwEndpoint, cacheGwEndpoint)
}

// runConstantTiers 恒定三档:每档先网关臂后直连基线臂,打印同分位差
// (网关延迟 − 直连延迟,相对开销主张的执行体)。
func runConstantTiers(t *testing.T, gwEndpoint, fixtureURL string) {
	for _, rate := range tierRates {
		prefix := fmt.Sprintf("tier%d", rate)
		t.Logf("=== 恒定档 %d rps × %s ===", rate, tierDuration)
		gw := runScene(t, prefix+"-经网关", gwEndpoint, gatewayToolName(), rate, tierDuration)
		direct := runScene(t, prefix+"-直连", fixtureURL, upstreamTool, rate, tierDuration)
		logOverhead(t, gw, direct)
	}
}

// runStaircase 阶梯加载:速率逐级抬升(每级独立计量段,级间以同速率预热
// 一秒衔接,速率排程单调递增),拐点观察 = 延迟分位随档位的变化形态。
func runStaircase(t *testing.T, gwEndpoint string) {
	t.Logf("=== 阶梯加载:逐级抬升,每级 %s ===", staircaseStepDur)
	for _, rate := range staircaseRates {
		runScene(t, fmt.Sprintf("stair%d-经网关", rate), gwEndpoint, gatewayToolName(), rate, staircaseStepDur)
	}
}

// runCacheCompare 缓存对比档:同速率下缓存关闭态与命中态各打一臂,
// 同分位对照即 spec 0006 缓存收益的数字背书。
func runCacheCompare(t *testing.T, gwEndpoint, cacheGwEndpoint string) {
	t.Logf("=== 缓存对比 %d rps × %s(public 规则 ttl=%dms)===", cacheCompareRate, cacheCompareDuration, cacheRuleTTLMS)
	runScene(t, "cache-关闭态", gwEndpoint, gatewayToolName(), cacheCompareRate, cacheCompareDuration)
	runScene(t, "cache-命中态", cacheGwEndpoint, gatewayToolName(), cacheCompareRate, cacheCompareDuration)
}

// logOverhead 打印同分位差:P50/P95/P99 逐项相减(网关 − 直连),正值为
// 网关引入的额外延迟,负值说明该分位下经网关反而更快(同机回环的噪声区间)。
func logOverhead(t *testing.T, gw, direct *vegeta.Metrics) {
	t.Helper()
	t.Logf("%-28s P50差=%s P95差=%s P99差=%s",
		"相对开销(网关-直连)",
		gw.Latencies.P50-direct.Latencies.P50,
		gw.Latencies.P95-direct.Latencies.P95,
		gw.Latencies.P99-direct.Latencies.P99)
}
