// transport 的令牌桶限流测试(票 0005-03/spec 0005):全局单桶(rps+burst),
// 时钟经 WithRateLimit 的 RateLimitConfig.Now 注入(spec 0005 Story 20:
// 表驱动连续消费/按速率恢复/burst 上限,绝不 sleep);桶空返回 429 +
// Retry-After(按补充速率向上取整秒)。黑盒只从 transport.New 产出的
// http.Handler 接缝断言:裸 POST 以状态码判定——放行 case 的 SDK 层应答
// 不属本票,断言"非 429"即穿透本层。
package transport_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/transport"
)

// fakeClock 是可手动推进的假时钟:测试按离散步骤推进,refill 惰性发生在
// 下一次请求,期望值全部手工可算(独立真值:速率×时长)。
type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0).UTC()}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// rlStep 描述脚本的一步:先推进时钟(0 = 不推进),再发一个请求;
// wantLimited = true 时断言 429 且 Retry-After 头逐字相等。
type rlStep struct {
	advance        time.Duration
	wantLimited    bool
	wantRetryAfter string // 仅 wantLimited 时断言;空串表示不断言该头
}

// postNoOrigin 向 /mcp 发一个无 Origin 头的 POST(Origin 未配置,放行),
// 返回状态码与 Retry-After 头。
func postNoOrigin(t *testing.T, ts *httptest.Server) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, res.Header.Get("Retry-After")
}

func TestTransportRateLimit(t *testing.T) {
	tests := []struct {
		name  string
		rps   float64
		burst int
		steps []rlStep
	}{
		{
			name:  "连续消费:burst 内全放行,桶空即 429 且 Retry-After 按速率取整秒",
			rps:   1,
			burst: 3,
			steps: []rlStep{
				{wantLimited: false},
				{wantLimited: false},
				{wantLimited: false},
				{wantLimited: true, wantRetryAfter: "1"},
			},
		},
		{
			name:  "按速率恢复:rps=2 推进 500ms 恰好补 1 个令牌,下一发放行",
			rps:   2,
			burst: 1,
			steps: []rlStep{
				{wantLimited: false},
				{wantLimited: true, wantRetryAfter: "1"},
				{advance: 500 * time.Millisecond, wantLimited: false},
			},
		},
		{
			name:  "部分恢复:低速桶补偿不足 1 个令牌仍 429,Retry-After 随恢复缩短",
			rps:   0.5,
			burst: 1,
			steps: []rlStep{
				{wantLimited: false},                           // 初始满桶,消费 1
				{wantLimited: true, wantRetryAfter: "2"},       // 桶空:补 1 个需 2s
				{advance: 1 * time.Second},                     // 补 0.5 个
				{wantLimited: true, wantRetryAfter: "1"},       // 差 0.5 个需 1s
				{advance: 1 * time.Second, wantLimited: false}, // 再补 0.5,凑满 1 个
			},
		},
		{
			name:  "burst 上限:长时间闲置补充封顶容量,不会攒出超发",
			rps:   100,
			burst: 2,
			steps: []rlStep{
				{wantLimited: false},
				{wantLimited: false},
				{wantLimited: true, wantRetryAfter: "1"},
				{advance: 10 * time.Second, wantLimited: false}, // 补 1000 封顶 2
				{wantLimited: false},
				{wantLimited: true, wantRetryAfter: "1"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newFakeClock()
			handler, err := transport.New(context.Background(), gateway.New(policyTools()),
				transport.WithRateLimit(transport.RateLimitConfig{RPS: tt.rps, Burst: tt.burst, Now: clock.Now}))
			if err != nil {
				t.Fatalf("构造 transport 失败: %v", err)
			}
			ts := httptest.NewServer(handler)
			t.Cleanup(ts.Close)

			for i, step := range tt.steps {
				clock.Advance(step.advance)
				status, retryAfter := postNoOrigin(t, ts)
				if step.wantLimited {
					if status != http.StatusTooManyRequests {
						t.Fatalf("第 %d 步应被限流(429),实际状态码 %d", i+1, status)
					}
					if step.wantRetryAfter != "" && retryAfter != step.wantRetryAfter {
						t.Errorf("第 %d 步 Retry-After = %q,期望 %q", i+1, retryAfter, step.wantRetryAfter)
					}
					return
				}
				if status == http.StatusTooManyRequests {
					t.Fatalf("第 %d 步应放行(桶内有令牌),实际 429", i+1)
				}
			}
		})
	}
}
