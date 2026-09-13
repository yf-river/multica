// Package analytics 把产品遥测事件投递到外部分析后端（PostHog）。事件喂养
// 获客 → 激活 → 扩张漏斗——事件契约见 events.go。
//
// 设计要点：
//   - Capture 非阻塞。请求 handler 绝不能等待分析网络 I/O，因此把事件入一个有界
//     队列，由后台 worker 分批刷给 PostHog。
//   - 队列满时事件被丢弃（并计数）。分析后端挂掉绝不能拖累产品。
//   - POSTHOG_API_KEY 为空时本包运行 no-op client，让本地开发与自托管实例零摩擦。
package analytics

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// Event 是单条分析采集。字段镜像 PostHog 的 /capture/ 形态，但保持与框架无关，
// 便于日后接入其它后端。
type Event struct {
	// Name 事件名（如 "signup"、"workspace_created"）。
	Name string

	// DistinctID 标识该事件所属的人。登录用户是 user.id；匿名事件应传前端之前
	// 用过的 anon_id，以便身份合并生效。
	DistinctID string

	// WorkspaceID 把事件限定到某个工作区。事件关于工作区级动作
	//（workspace_created、issue_executed……）时为必填；工作区创建前的事件
	//（signup）允许为空。
	WorkspaceID string

	// Properties 是自由形态的事件属性袋。只应放可序列化值（字符串、数字、布尔、
	// 同类的嵌套 map/切片）。绝不放原始 PII（如完整邮箱）——用 email_domain。
	Properties map[string]any

	// SetOnce 属性挂在 person 记录上，只在首次出现时写入。用于获客归因
	//（initial_utm_source 等），避免后续事件覆盖最初来源。
	SetOnce map[string]any

	// Set 属性挂在 person 记录上，每次写入都覆盖。用于用户在上线过程中确实可能
	// 改变的群组信号（role、use_case、platform_preference）。
	Set map[string]any

	// Timestamp 可选；为零时 client 会填入 time.Now()。
	Timestamp time.Time
}

// Client 是其余代码依赖的窄接口。handler 调 Capture 后继续干活；缓冲、分批与
// 投递是实现的职责。
type Client interface {
	Capture(e Event)
	// Close 排空待发事件。优雅停机时调用一次。
	Close()
}

// NewFromEnv 从环境变量构造 Client：
//
//   - POSTHOG_API_KEY：项目 API key。空 → no-op client。
//   - POSTHOG_HOST：API 主机（默认 https://us.i.posthog.com）。
//   - ANALYTICS_ENVIRONMENT：production/staging/dev。默认取自 APP_ENV。
//   - ANALYTICS_DISABLED：设为 "true"/"1" 即使 POSTHOG_API_KEY 已设置也强制
//     用 no-op client（对 CI 和自托管关闭很实用）。
func NewFromEnv() Client {
	if isDisabled() {
		slog.Info("analytics disabled via ANALYTICS_DISABLED")
		return NoopClient{}
	}
	key := os.Getenv("POSTHOG_API_KEY")
	if key == "" {
		slog.Info("analytics: POSTHOG_API_KEY not set, using noop client")
		return NoopClient{}
	}
	host := os.Getenv("POSTHOG_HOST")
	if host == "" {
		host = "https://us.i.posthog.com"
	}
	slog.Info("analytics: posthog client enabled", "host", host)
	return NewPostHogClient(PostHogConfig{
		APIKey:      key,
		Host:        host,
		Environment: EnvironmentFromEnv(),
	})
}

// isDisabled 判断是否通过 ANALYTICS_DISABLED 显式关闭了分析上报。
func isDisabled() bool {
	v := os.Getenv("ANALYTICS_DISABLED")
	return v == "true" || v == "1"
}

// EnvironmentFromEnv 解析当前环境标签：优先 ANALYTICS_ENVIRONMENT，回退 APP_ENV，
// 都没有则为 "dev"。
func EnvironmentFromEnv() string {
	if v := normalizeEnvironment(os.Getenv("ANALYTICS_ENVIRONMENT")); v != "" {
		return v
	}
	if v := normalizeEnvironment(os.Getenv("APP_ENV")); v != "" {
		return v
	}
	return "dev"
}

// normalizeEnvironment 把大小写/别名不一的输入归一成 production/staging/dev 三档。
func normalizeEnvironment(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "production", "prod":
		return "production"
	case "staging", "stage":
		return "staging"
	case "development", "dev", "test", "local":
		return "dev"
	default:
		return ""
	}
}

// NoopClient 静默丢弃所有事件。用于测试、POSTHOG_API_KEY 未设置的本地开发，
// 以及选择关闭上报的自托管实例。
type NoopClient struct{}

func (NoopClient) Capture(Event) {}
func (NoopClient) Close()        {}
