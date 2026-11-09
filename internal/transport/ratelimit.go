// 令牌桶限流中间件(票 0005-03/spec 0005,架构 U3 裁决:HTTP 层机制留在
// transport 内部,不立新包):全局单桶,rps+burst 两参数;时间来源经构造
// 注入(测试注入假时钟,Story 20,绝不 sleep);refill 惰性发生在每次取
// 令牌时(不起后台 goroutine);桶空返回 429 + Retry-After(按补充速率
// 向上取整秒,Story 6)。参数语法由 config 校验保证(设计 D8)。
package transport

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiter 是全局令牌桶:并发请求共用,状态由 mu 保护;tokens 上限
// burst,初始满桶;last 是上次 refill 的时点,惰性推进。
type rateLimiter struct {
	mu     sync.Mutex
	rps    float64 // 每秒补充令牌数
	burst  float64 // 桶容量
	tokens float64 // 当前令牌数
	last   time.Time
	now    func() time.Time // 时间来源(构造注入,nil 已在构造处落 time.Now)
}

// newRateLimiter 构造满桶的限流器;Now 为 nil 落真实时钟(产件形态),
// 测试经 RateLimitConfig.Now 注入假时钟。
func newRateLimiter(cfg RateLimitConfig) *rateLimiter {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{
		rps:    cfg.RPS,
		burst:  float64(cfg.Burst),
		tokens: float64(cfg.Burst), // 启动即满桶:重启不清空攒下的“欠账”,也不预罚
		last:   now(),
		now:    now,
	}
}

// take 惰性补充并尝试取 1 个令牌:放行返回 0;桶空返回建议等待时长
// (补足 1 个令牌所需时间,向上取整秒,≥1)——调用方把它写进 Retry-After。
func (l *rateLimiter) take() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if elapsed := l.now().Sub(l.last); elapsed > 0 {
		l.tokens = math.Min(l.burst, l.tokens+l.rps*elapsed.Seconds())
		l.last = l.now()
	}
	if l.tokens >= 1 {
		l.tokens--
		return 0
	}
	deficit := 1 - l.tokens // 补足一个可消费令牌的缺口
	return time.Duration(math.Ceil(deficit/l.rps)) * time.Second
}

// rateLimitMiddleware 返回限流包装:每请求取 1 个令牌,取到即下探;取不到
// 写 Retry-After(整数秒)并以 429 拒绝——客户端据此退避而非无限重试。
// 中间件不下探 ctx、不读 body,放行路径零拷贝。
func rateLimitMiddleware(l *rateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if wait := l.take(); wait > 0 {
				w.Header().Set("Retry-After", strconv.FormatInt(int64(wait/time.Second), 10))
				http.Error(w, "请求过于频繁:已触发全局限流,请按 Retry-After 退避重试", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
