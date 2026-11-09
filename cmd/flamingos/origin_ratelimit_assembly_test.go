// cmd 的 origin/rate_limit 装配翻译测试(票 0005-03):config.Policy →
// transport 的两道 HTTP 层接线参数只发生在 cmd(架构 D7);段/子段缺省 =
// 关闭态(Origin 列表 nil、RateLimitConfig 零值——WithOrigin/WithRateLimit
// 对关闭态不装配任何挂点);允许列表逐条按副本移交,不与配置结构共享底层
// 切片。
package main

import (
	"testing"

	"github.com/magician336/flamingos/internal/config"
)

func TestBuildOrigin(t *testing.T) {
	t.Run("段缺省:返回 nil,Origin 校验关闭态", func(t *testing.T) {
		if got := buildOrigin(nil); got != nil {
			t.Fatalf("段缺省应返回 nil,实际 %v", got)
		}
	})

	t.Run("origin 子段缺省:同样返回 nil(各子段独立开关)", func(t *testing.T) {
		if got := buildOrigin(&config.Policy{}); got != nil {
			t.Fatalf("origin 子段缺省应返回 nil,实际 %v", got)
		}
	})

	t.Run("完整 allowed:逐条翻译且为副本,改配置切片不影响产物", func(t *testing.T) {
		allowed := []string{"http://localhost:*", "https://app.example.com"}
		got := buildOrigin(&config.Policy{Origin: &config.Origin{Allowed: allowed}})

		if len(got) != 2 || got[0] != "http://localhost:*" || got[1] != "https://app.example.com" {
			t.Fatalf("允许列表翻译失真,实际 %v", got)
		}
		allowed[0] = "http://tampered.example.com"
		if got[0] != "http://localhost:*" {
			t.Errorf("产物应与配置切片解除共享,改动配置后产物 = %v", got)
		}
	})
}

func TestBuildRateLimit(t *testing.T) {
	t.Run("段缺省:返回零值配置,限流关闭态", func(t *testing.T) {
		if got := buildRateLimit(nil); got.RPS != 0 {
			t.Fatalf("段缺省应返回零值配置,实际 %+v", got)
		}
	})

	t.Run("rate_limit 子段缺省:同样返回零值配置", func(t *testing.T) {
		if got := buildRateLimit(&config.Policy{}); got.RPS != 0 {
			t.Fatalf("rate_limit 子段缺省应返回零值配置,实际 %+v", got)
		}
	})

	t.Run("完整参数:rps 与 burst 逐项翻译,时钟不注入(产件走真实时间)", func(t *testing.T) {
		got := buildRateLimit(&config.Policy{RateLimit: &config.RateLimit{RPS: 100, Burst: 200}})
		if got.RPS != 100 || got.Burst != 200 {
			t.Fatalf("限流参数翻译失真,实际 %+v", got)
		}
		if got.Now != nil {
			t.Errorf("产件不应注入时钟,实际非 nil(测试假时钟只经测试直接构造)")
		}
	})
}
