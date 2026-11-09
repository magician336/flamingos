// 烟测:常规回归门控内随 go test ./... 运行(spec 0007 Testing Decisions),
// 钉住两件事——①Stateless 裸 POST tools/call 无需 initialize 的前提实证
// (前提不成立则全量靶体需改 initialize+call 配对并在报告方法节记录);
// ②攻击器-网关-夹具三进程 harness 正确性(低速率短时攻击零错误响应)。
package benchmark

import (
	"testing"
	"time"
)

// 烟测常量:总时长远低于全量,常规回归时长不受影响。
const (
	smokeRate     = 10 // rps
	smokeDuration = 2 * time.Second
)

// TestBareCallSmoke 单例裸调用:200 + echo 字节级往返一致,即裸 POST 前提
// 的实证;每例拓扑独立拉起,判据失败即前提不成立。
func TestBareCallSmoke(t *testing.T) {
	fixtureAddr := freeAddr(t)
	startHTTPFixture(t, fixtureAddr)
	fixtureURL := "http://" + fixtureAddr + "/mcp"

	gwAddr := freeAddr(t)
	gwEndpoint := startGateway(t, gwAddr, gatewayConfigYAML(gwAddr, fixtureURL))

	t.Run("经网关聚合名", func(t *testing.T) {
		bareCallCheck(t, gwEndpoint, gatewayToolName())
	})
	t.Run("直连上游原名", func(t *testing.T) {
		bareCallCheck(t, fixtureURL, upstreamTool)
	})
}

// TestSmokeAttack 短时低速率攻击:恒定 10 rps × 2s 打网关,断言零错误响应
// (vegeta Metrics.Errors 为空)且全部样本状态码 200、样本数与排程速率同
// 量级。这是全量场景的机械判据在常规回归里的常驻形态。
func TestSmokeAttack(t *testing.T) {
	fixtureAddr := freeAddr(t)
	startHTTPFixture(t, fixtureAddr)
	fixtureURL := "http://" + fixtureAddr + "/mcp"

	gwAddr := freeAddr(t)
	gwEndpoint := startGateway(t, gwAddr, gatewayConfigYAML(gwAddr, fixtureURL))

	m := runScene(t, "smoke", gwEndpoint, gatewayToolName(), smokeRate, smokeDuration)
	if m.Requests == 0 {
		t.Fatal("攻击未产生任何样本")
	}
	if len(m.Errors) != 0 {
		t.Fatalf("烟测应零错误响应,实际错误: %v", m.Errors)
	}
	if uint64(m.StatusCodes["200"]) != m.Requests {
		t.Fatalf("全部样本应为 200,状态码分布: %v", m.StatusCodes)
	}
	// 排程速率 10 rps × 2s ≈ 20 样本;Actual throughput 不作判据(环境依赖),
	// 只核样本数下限,保证排程未被提前截断。
	if m.Requests < uint64(smokeRate*int(smokeDuration.Seconds()))-1 {
		t.Fatalf("样本数应与排程同量级(≈%d),实际 %d", smokeRate*int(smokeDuration.Seconds()), m.Requests)
	}
}
