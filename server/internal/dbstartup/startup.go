// startup.go 提供数据库启动期公用的「连接/重试」工具：迁移器与 API server 共享同一套
// 由环境变量与容器入口点写下的重试预算（MULTICA_INTERNAL_DATABASE_STARTUP_STARTED_AT_UNIX
// 记录整个容器重试窗口的起点），并以带抖动、指数退避、统一判据（什么错误可重试）的
// Retry 循环驱动连接直到数据库就绪。
package dbstartup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StartupTimeoutEnv = "MULTICA_DATABASE_STARTUP_TIMEOUT"
	ConnectTimeoutEnv = "MULTICA_DATABASE_CONNECT_TIMEOUT"
	// startupStartedAtEnv 由容器入口点设置，让迁移器与 API server 共用一份重试预算。
	// 它刻意不是面向用户的设置。
	startupStartedAtEnv = "MULTICA_INTERNAL_DATABASE_STARTUP_STARTED_AT_UNIX"

	DefaultStartupTimeout = 3 * time.Minute
	DefaultConnectTimeout = 5 * time.Second
	defaultInitialBackoff = time.Second
	defaultMaxBackoff     = 30 * time.Second
)

// keywordConnectTimeoutPattern 用来在 DSN 里识别 keyword/value 形式的 connect_timeout。
var keywordConnectTimeoutPattern = regexp.MustCompile(`(?:^|\s)connect_timeout\s*=`)

// Settings 界定数据库启动重试的总预算与单次连接尝试的超时。
type Settings struct {
	StartupTimeout time.Duration
	ConnectTimeout time.Duration

	startupDeadline time.Time // 启动截止时刻（预算终点）
	now             func() time.Time
}

// SettingsFromEnv 读取迁移器与 API server 共用的启动设置。启动超时为零保留了
// 「显式快速失败」选项。
func SettingsFromEnv() Settings {
	return settingsFromEnv(time.Now)
}

func settingsFromEnv(now func() time.Time) Settings {
	startupTimeout := envDuration(StartupTimeoutEnv, DefaultStartupTimeout, true)
	settings := Settings{
		StartupTimeout: startupTimeout,
		ConnectTimeout: envDuration(ConnectTimeoutEnv, DefaultConnectTimeout, false),
		now:            now,
	}
	if startupTimeout <= 0 {
		return settings
	}

	startedAt := now()
	if raw := os.Getenv(startupStartedAtEnv); raw != "" {
		unixSeconds, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && unixSeconds > 0 {
			startedAt = time.Unix(unixSeconds, 0)
		} else {
			if err == nil {
				err = errors.New("timestamp must be positive")
			}
			slog.Warn("invalid internal database startup timestamp, starting a new retry budget",
				"name", startupStartedAtEnv,
				"value", raw,
				"error", err,
			)
		}
	}
	settings.startupDeadline = startedAt.Add(startupTimeout)
	return settings
}

// ParsePoolConfig 把 connectTimeout 作为「兼容回退」应用：原生 pgx 的 connect_timeout
// 优先级更高，因为 pgx 会用同一个值同时配置「整条连接的超时」与 DialFunc。
func ParsePoolConfig(databaseURL string, connectTimeout time.Duration) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if pgxConnectTimeoutConfigured(databaseURL, cfg.ConnConfig.ConnectTimeout) {
		return cfg, nil
	}
	if connectTimeout <= 0 {
		connectTimeout = DefaultConnectTimeout
	}
	cfg.ConnConfig.ConnectTimeout = connectTimeout
	dialer := &net.Dialer{Timeout: connectTimeout}
	cfg.ConnConfig.DialFunc = dialer.DialContext
	return cfg, nil
}

// pgxConnectTimeoutConfigured 报告 DSN/环境里是否已经显式配置了 pgx 的 connect_timeout
// （解析值非零、PGCONNECT_TIMEOUT 已设、URL query 里有 connect_timeout、或 keyword
// 形式里出现它）。
func pgxConnectTimeoutConfigured(databaseURL string, parsedTimeout time.Duration) bool {
	if parsedTimeout != 0 || os.Getenv("PGCONNECT_TIMEOUT") != "" {
		return true
	}

	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		parsedURL, err := url.Parse(databaseURL)
		if err == nil {
			query := parsedURL.Query()
			_, hasConnectTimeout := query["connect_timeout"]
			return hasConnectTimeout
		}
	}

	return keywordConnectTimeoutPattern.MatchString(databaseURL)
}

// NewPool 用共享的连接超时建一个启动用连接池。
func NewPool(ctx context.Context, databaseURL string, connectTimeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := ParsePoolConfig(databaseURL, connectTimeout)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// RetryEvent 描述进入下一次退避前的那次失败尝试。
type RetryEvent struct {
	Attempt int
	Delay   time.Duration
	Err     error
}

// RetryOptions 控制一个有界的指数退避循环。Jitter 与 Sleep 可注入，测试因此能在不
// 真正等待的情况下覆盖完整时序。
type RetryOptions struct {
	Timeout        time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Jitter         func(time.Duration) time.Duration
	Sleep          func(context.Context, time.Duration) error
	ShouldRetry    func(error) bool
	// AllowOperationPastTimeout 让耗时的操作继续跑在父 context 上，同时超时仍约束
	// 后续的重试与退避。
	AllowOperationPastTimeout bool
	OnRetry                   func(RetryEvent)
}

// RetryOptions 返回「按调用时刻剩余启动预算」计算的生产默认值。因此各阶段共享同一个
// 墙钟截止点——阶段之间成功做完的工作同样消耗预算。预算耗尽后，后面的阶段只做一次
// 快速失败尝试，而不是再预留一个可能超出容器级重试上限的最小窗口。
func (s Settings) RetryOptions() RetryOptions {
	timeout := s.StartupTimeout
	if timeout > 0 && !s.startupDeadline.IsZero() {
		now := s.now
		if now == nil {
			now = time.Now
		}
		timeout = s.startupDeadline.Sub(now())
		if timeout < 0 {
			timeout = 0
		}
	}
	return RetryOptions{
		Timeout:        timeout,
		InitialBackoff: defaultInitialBackoff,
		MaxBackoff:     defaultMaxBackoff,
	}
}

// Retry 立即执行一次 operation，然后对瞬时失败用「带抖动的指数退避」重试，直到成功、
// 父 context 被取消、或配置的启动预算耗尽。Timeout 为零只执行一次。
func Retry(ctx context.Context, opts RetryOptions, operation func(context.Context) error) error {
	if operation == nil {
		return errors.New("database startup operation is nil")
	}
	if opts.Timeout <= 0 {
		return operation(ctx)
	}

	retryCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	backoff := opts.InitialBackoff
	if backoff <= 0 {
		backoff = defaultInitialBackoff
	}
	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = defaultMaxBackoff
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	jitter := opts.Jitter
	if jitter == nil {
		jitter = jitterDelay
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := retryCtx.Err(); err != nil {
			return retryStopped(attempt-1, err, lastErr)
		}

		operationCtx := retryCtx
		if opts.AllowOperationPastTimeout {
			operationCtx = ctx
		}
		err := operation(operationCtx)
		if err == nil {
			return nil
		}
		lastErr = err
		if err := retryCtx.Err(); err != nil {
			return retryStopped(attempt, err, lastErr)
		}
		if opts.ShouldRetry != nil && !opts.ShouldRetry(err) {
			return err
		}

		delay := jitter(backoff)
		if delay < 0 {
			delay = 0
		}
		if opts.OnRetry != nil {
			opts.OnRetry(RetryEvent{Attempt: attempt, Delay: delay, Err: err})
		}
		if err := sleep(retryCtx, delay); err != nil {
			cause := retryCtx.Err()
			if cause == nil {
				cause = err
			}
			return retryStopped(attempt, cause, lastErr)
		}

		if backoff < maxBackoff {
			backoff *= 2
			if backoff <= 0 || backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// retryStopped 拼一个说明「重试为何停下」的错误：能带上最后一次尝试的原始错误就带上。
func retryStopped(attempts int, cause, lastErr error) error {
	if lastErr == nil {
		return fmt.Errorf("database startup stopped before an attempt completed: %w", cause)
	}
	return fmt.Errorf("database startup failed after %d attempt(s): %w (last error: %w)", attempts, cause, lastErr)
}

// jitterDelay 给退避时长加抖动，落在 80%-100% 之间：既避免并发 pod 步调一致地一起
// 重连，又让「配置的最大退避」保持为硬上限。
func jitterDelay(delay time.Duration) time.Duration {
	return time.Duration(float64(delay) * (0.8 + rand.Float64()*0.2))
}

// IsTransientDatabaseError 把「整库迁移的重试」限定在连接/可用性类失败上。SQL 与迁移
// 定义错误立即失败，而不是白白消耗启动重试预算。
func IsTransientDatabaseError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return strings.HasPrefix(pgErr.Code, "08") || // 连接异常类
			pgErr.Code == "57P01" || // admin_shutdown
			pgErr.Code == "57P02" || // crash_shutdown
			pgErr.Code == "57P03" || // cannot_connect_now
			pgErr.Code == "53300" // too_many_connections
	}

	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return true
	}
	return pgconn.SafeToRetry(err) || pgconn.Timeout(err)
}

// sleepContext 可中断地睡 delay 时长。
func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// envDuration 读一个时长环境变量；缺失用默认值，非法值打 warn 并回退默认值。
// allowZero 表示「显式 0」是否合法（有些旋钮把 0 当禁用/快速失败语义）。
func envDuration(name string, def time.Duration, allowZero bool) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	value, err := time.ParseDuration(raw)
	valid := err == nil && (value > 0 || allowZero && value == 0)
	if !valid {
		slog.Warn("invalid env var, using default",
			"name", name,
			"value", raw,
			"default", def.String(),
			"error", err,
		)
		return def
	}
	return value
}
