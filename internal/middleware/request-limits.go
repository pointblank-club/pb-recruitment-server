package middleware

import (
	"app/internal/common"
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"
	mdw "github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"
)

// RequestLimiter shares budgets across API processes and restarts.
type RequestLimiter interface {
	Allow(context.Context, string, int, int) (int, error)
}

type postgresRequestLimiter struct {
	db            *sql.DB
	cleanupMinute atomic.Int64
}

func NewRequestLimiter(db *sql.DB) RequestLimiter { return &postgresRequestLimiter{db: db} }

func (l *postgresRequestLimiter) Allow(ctx context.Context, key string, limit, seconds int) (int, error) {
	if l.db == nil {
		return 0, errors.New("request limiter database unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	// Keep one row per budget; sample the database clock after the conflicting row is locked.
	const query = `INSERT INTO request_rate_limits (key, hits, resets_at)
 VALUES ($1,1,clock_timestamp()+make_interval(secs=>$3))
 ON CONFLICT (key) DO UPDATE SET
 hits=CASE WHEN request_rate_limits.resets_at<=clock_timestamp() THEN 1 ELSE request_rate_limits.hits+1 END,
 resets_at=CASE WHEN request_rate_limits.resets_at<=clock_timestamp() THEN clock_timestamp()+make_interval(secs=>$3) ELSE request_rate_limits.resets_at END
 WHERE request_rate_limits.resets_at<=clock_timestamp() OR request_rate_limits.hits<$2
 RETURNING 1`
	var admitted int
	err := l.db.QueryRowContext(ctx, query, key, limit, seconds).Scan(&admitted)
	if errors.Is(err, sql.ErrNoRows) {
		var retry int
		err = l.db.QueryRowContext(ctx, `SELECT GREATEST(1,ceil(extract(epoch FROM resets_at-clock_timestamp())))::int FROM request_rate_limits WHERE key=$1`, key).Scan(&retry)
		if errors.Is(err, sql.ErrNoRows) {
			return 1, nil
		}
		return retry, err
	}
	if err != nil {
		return 0, err
	}
	minute := time.Now().Unix() / 60
	previous := l.cleanupMinute.Load()
	if previous != minute && l.cleanupMinute.CompareAndSwap(previous, minute) {
		if _, err = l.db.ExecContext(ctx, `DELETE FROM request_rate_limits WHERE resets_at<clock_timestamp()-interval '1 day'`); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

func LimitRequests(limiter RequestLimiter, budget string, limit, seconds int) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			uid, _ := c.Get(common.AUTH_USER_ID).(string)
			key := "ip:" + c.RealIP()
			if uid != "" {
				key = "user:" + uid
			}
			if budget == "signup-fleet" {
				key = "fleet"
			}
			if limiter == nil {
				c.Response().Header().Set(echo.HeaderRetryAfter, "5")
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "request admission unavailable"})
			}
			retry, err := limiter.Allow(c.Request().Context(), budget+":"+key, limit, seconds)
			if err != nil {
				log.Errorf("request admission %s unavailable: %v", budget, err)
				c.Response().Header().Set(echo.HeaderRetryAfter, "5")
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "request admission unavailable"})
			}
			if retry > 0 {
				c.Response().Header().Set(echo.HeaderRetryAfter, strconv.Itoa(retry))
				return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "too many requests; wait before retrying"})
			}
			return next(c)
		}
	}
}

// SubmissionBurstLimit rejects repeated clicks before they contend on shared SQL budgets.
func SubmissionBurstLimit() echo.MiddlewareFunc {
	return newSubmissionBurstLimitWithLimiter(newUserRateLimiter(4, 1, nil))
}

func newSubmissionBurstLimitWithLimiter(limiter *userRateLimiter) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			uid, _ := c.Get(common.AUTH_USER_ID).(string)
			if uid == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			}
			// shortcut: this fast guard is process-local; the SQL budget remains the shared authority.
			if !limiter.allow(uid) {
				c.Response().Header().Set(echo.HeaderRetryAfter, "1")
				return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "too many submission requests; wait before retrying"})
			}
			return next(c)
		}
	}
}

// SignupCapacity allows a campus burst to wait without unbounded Firebase or SQL work.
// shortcut: slots are process-local; share worker admission before adding API replicas.
func SignupCapacity() echo.MiddlewareFunc {
	return newSignupCapacity(8, 100, 20*time.Second, 20*time.Second)
}

func newSignupCapacity(activeLimit, totalLimit int, wait, work time.Duration) echo.MiddlewareFunc {
	active, admitted := make(chan struct{}, activeLimit), make(chan struct{}, totalLimit)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			cancelled := func() error {
				return c.JSON(http.StatusRequestTimeout, map[string]string{"error": "signup request cancelled"})
			}
			if ctx.Err() != nil {
				return cancelled()
			}
			busy := func() error {
				c.Response().Header().Set(echo.HeaderRetryAfter, "1")
				return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "signup capacity busy; wait before retrying"})
			}
			select {
			case admitted <- struct{}{}:
				defer func() { <-admitted }()
			default:
				return busy()
			}
			deadline := time.Now().Add(wait)
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case active <- struct{}{}:
				defer func() { <-active }()
			case <-ctx.Done():
				return cancelled()
			case <-timer.C:
				return busy()
			}
			if ctx.Err() != nil {
				return cancelled()
			}
			if !time.Now().Before(deadline) {
				return busy()
			}
			workCtx, cancelWork := context.WithTimeout(ctx, work)
			defer cancelWork()
			c.SetRequest(c.Request().WithContext(workCtx))
			return next(c)
		}
	}
}

// AddRequestLimits rejects ingress bursts without making authentication depend on PostgreSQL.
func AddRequestLimits(e *echo.Echo) {
	e.Use(newIngressRateLimitWithLimiter(newUserRateLimiter(60000, 1000, nil)))
}

func newIngressRateLimitWithLimiter(limiter *userRateLimiter) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Path() == "/health" || c.Request().Method == http.MethodOptions || c.Path() == "/internal/judge0/callback/:execution_id" {
				return next(c)
			}
			// shortcut: ingress buckets are process-local; enforce a fleet-wide ingress ceiling at the proxy when scaling.
			if !limiter.allow(c.RealIP()) {
				c.Response().Header().Set(echo.HeaderRetryAfter, "1")
				return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "too many requests; wait before retrying"})
			}
			return next(c)
		}
	}
}

func ConfigureRequestBounds(e *echo.Echo) {
	e.Server.ReadHeaderTimeout = 5 * time.Second
	e.Server.ReadTimeout = 30 * time.Second
	e.Server.WriteTimeout = 90 * time.Second
	e.Server.IdleTimeout = 60 * time.Second
	e.Server.MaxHeaderBytes = 32 << 10
	e.IPExtractor = echo.ExtractIPDirect()
	if raw := os.Getenv("TRUSTED_PROXY_CIDRS"); raw != "" {
		options := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustPrivateNet(false), echo.TrustLinkLocal(false)}
		for _, value := range strings.Split(raw, ",") {
			_, network, err := net.ParseCIDR(strings.TrimSpace(value))
			if err != nil {
				panic("invalid TRUSTED_PROXY_CIDRS")
			}
			options = append(options, echo.TrustIPRange(network))
		}
		e.IPExtractor = echo.ExtractIPFromXFFHeader(options...)
	}
	defaultLimit := mdw.BodyLimit("1M")
	adminLimit := mdw.BodyLimit("8M")
	callbackLimit := mdw.BodyLimit("12M")
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		ordinary, admin, callback := defaultLimit(next), adminLimit(next), callbackLimit(next)
		return func(c echo.Context) error {
			if strings.HasPrefix(c.Path(), "/admin/") {
				return admin(c)
			}
			if c.Path() == "/internal/judge0/callback/:execution_id" {
				return callback(c)
			}
			return ordinary(c)
		}
	})
}

func LimitReads(limiter RequestLimiter) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		public := LimitRequests(limiter, "read", 6000, 60)(next)
		participant := LimitRequests(limiter, "read", 180, 60)(next)
		return func(c echo.Context) error {
			if uid, _ := c.Get(common.AUTH_USER_ID).(string); uid != "" {
				return participant(c)
			}
			return public(c)
		}
	}
}
