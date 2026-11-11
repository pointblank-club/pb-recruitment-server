package middleware

import (
	"app/internal/common"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	defaultTabSwitchBurst      = 20.0
	defaultTabSwitchRefillRate = 5.0 // tokens per second
	tabSwitchBucketTTL         = 10 * time.Minute
	tabSwitchMaxBuckets        = 10000
)

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

type userRateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*tokenBucket
	burst      float64
	refillRate float64
	maxBuckets int
	nowFn      func() time.Time
}

func newUserRateLimiter(burst, refillRate float64, nowFn func() time.Time) *userRateLimiter {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &userRateLimiter{
		buckets:    make(map[string]*tokenBucket),
		burst:      burst,
		refillRate: refillRate,
		maxBuckets: tabSwitchMaxBuckets,
		nowFn:      nowFn,
	}
}

func (l *userRateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFn()

	b, ok := l.buckets[key]
	if !ok {
		maxBuckets := l.maxBuckets
		if maxBuckets <= 0 {
			maxBuckets = tabSwitchMaxBuckets
		}
		if len(l.buckets) >= maxBuckets {
			for k, existing := range l.buckets {
				if now.Sub(existing.lastSeen) > tabSwitchBucketTTL {
					delete(l.buckets, k)
				}
			}
			if len(l.buckets) >= maxBuckets {
				// Evict oldest seen entry only when admitting a new key into a full table.
				var oldestKey string
				var oldestTime time.Time
				for k, existing := range l.buckets {
					if oldestKey == "" || existing.lastSeen.Before(oldestTime) {
						oldestKey = k
						oldestTime = existing.lastSeen
					}
				}
				if oldestKey != "" {
					delete(l.buckets, oldestKey)
				}
			}
		}

		l.buckets[key] = &tokenBucket{
			tokens:     l.burst - 1,
			lastRefill: now,
			lastSeen:   now,
		}
		return true
	}

	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.refillRate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.lastRefill = now
	}
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens -= 1
	return true
}

// TabSwitchRateLimit bounds per-user POST /contests/:id/tab-switch ingestion traffic.
func TabSwitchRateLimit() echo.MiddlewareFunc {
	return newTabSwitchRateLimitWithLimiter(newUserRateLimiter(defaultTabSwitchBurst, defaultTabSwitchRefillRate, nil))
}

func newTabSwitchRateLimitWithLimiter(limiter *userRateLimiter) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key, _ := c.Get(common.AUTH_USER_ID).(string)
			if key == "" {
				key = c.RealIP()
			}
			if !limiter.allow(key) {
				return c.JSON(http.StatusTooManyRequests, map[string]string{
					"error": "too many tab switch requests",
				})
			}
			return next(c)
		}
	}
}
