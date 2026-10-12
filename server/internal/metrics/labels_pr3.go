// 本文件是 PR3 那批产品指标（漏斗/社区/商业）的标签归一化器：把调用方可能传出的
// 各种自由形态取值（大小写、带 URL scheme 的域名、同义拼写变体）收进固定白名单。
// 与 labels.go 属同一「标签卫生」家族，只是这批标签服务于增长/商业类指标。
package metrics

import (
	"encoding/json"
	"strings"
)

// PR3 归一化器。所有输入都先过固定白名单，防止某个行为异常的调用方撑爆指标基数；
// 每个表都留了 "unknown"/"other" 兜底桶，即使上游枚举演进走样，序列数量也保持有界。

var (
	// knownPlatforms 平台枚举：server/web/cli/mobile/ios。
	knownPlatforms = map[string]string{
		"server":  "server",
		"web":     "web",
		"cli":     "cli",
		"mobile":  "mobile",
		"ios":     "ios",
		"unknown": "unknown",
	}

	// knownSignupSources 是 signup_source 指标标签的固定分桶集合。PostHog 事件仍然会上报
	// cookie 的原始值（保留长尾给分析用）；Prometheus 侧拿分桶后的值，这样即使某个前端
	// 写坏、给每个访客都写唯一 cookie，基数也仍然有界。空 cookie 归为 "direct"
	// （无归因 = 直接访问），未知渠道归 "other"。
	knownSignupSources = map[string]string{
		"direct":       "direct",
		"google":       "google",
		"bing":         "bing",
		"duckduckgo":   "duckduckgo",
		"twitter":      "twitter",
		"x":            "twitter",
		"linkedin":     "linkedin",
		"facebook":     "facebook",
		"instagram":    "instagram",
		"github":       "github",
		"gitlab":       "gitlab",
		"hacker_news":  "hacker_news",
		"hackernews":   "hacker_news",
		"reddit":       "reddit",
		"youtube":      "youtube",
		"discord":      "discord",
		"slack":        "slack",
		"product_hunt": "product_hunt",
		"producthunt":  "product_hunt",
		"medium":       "medium",
		"dev_to":       "dev_to",
		"devto":        "dev_to",
		"email":        "email",
		"newsletter":   "email",
		"organic":      "organic",
		"referral":     "referral",
		"partner":      "partner",
		"affiliate":    "affiliate",
		"ad":           "paid",
		"ads":          "paid",
		"paid":         "paid",
		"cpc":          "paid",
		"sem":          "paid",
		"other":        "other",
	}

	// knownOnboardingPaths 引导流程走向枚举（完整走完/跳过 runtime/进云等待名单/邀请加入等）。
	knownOnboardingPaths = map[string]string{
		"full":            "full",
		"runtime_skipped": "runtime_skipped",
		"cloud_waitlist":  "cloud_waitlist",
		"skip_existing":   "skip_existing",
		"invite_accept":   "invite_accept",
		"unknown":         "unknown",
	}

	// knownAutopilotCadences autopilot 运行节奏枚举。
	knownAutopilotCadences = map[string]string{
		"hourly":  "hourly",
		"daily":   "daily",
		"weekly":  "weekly",
		"monthly": "monthly",
		"manual":  "manual",
		"webhook": "webhook",
		"unknown": "unknown",
	}

	knownAutopilotTriggers = map[string]string{
		"schedule": "schedule",
		"webhook":  "webhook",
		"manual":   "manual",
		"unknown":  "unknown",
	}

	knownAutopilotSkipReasons = map[string]string{
		"already_running":   "already_running",
		"recent_run":        "recent_run",
		"runtime_offline":   "runtime_offline",
		"throttled":         "throttled",
		"max_concurrency":   "max_concurrency",
		"trigger_disabled":  "trigger_disabled",
		"signature_invalid": "signature_invalid",
		"unknown":           "unknown",
		"other":             "other",
	}

	knownWebhookProviders = map[string]string{
		"github":  "github",
		"generic": "generic",
		"gitlab":  "gitlab",
		"stripe":  "stripe",
		"other":   "other",
	}

	knownWebhookDeliveryStatuses = map[string]string{
		"queued":     "queued",
		"dispatched": "dispatched",
		"failed":     "failed",
		"rejected":   "rejected",
		"ignored":    "ignored",
		"duplicate":  "duplicate",
		"other":      "other",
	}

	knownWebhookRateLimitGates = map[string]string{
		"absolute_ip":       "absolute_ip",
		"bad_credential_ip": "bad_credential_ip",
		"worker_trigger":    "worker_trigger",
		"other":             "other",
	}

	knownEmailRateLimitActions = map[string]string{
		"workspace_invitation": "workspace_invitation",
		"other":                "other",
	}

	knownEmailRateLimitGates = map[string]string{
		"actor":     "actor",
		"workspace": "workspace",
		"recipient": "recipient",
		"other":     "other",
	}

	knownGithubEventKinds = map[string]string{
		"pull_request":              "pull_request",
		"pull_request_review":       "pull_request_review",
		"issues":                    "issues",
		"issue_comment":             "issue_comment",
		"push":                      "push",
		"installation":              "installation",
		"installation_repositories": "installation_repositories",
		"check_run":                 "check_run",
		"check_suite":               "check_suite",
		"ping":                      "ping",
		"other":                     "other",
	}

	knownGithubActions = map[string]string{
		"opened":      "opened",
		"closed":      "closed",
		"reopened":    "reopened",
		"merged":      "merged",
		"synchronize": "synchronize",
		"edited":      "edited",
		"submitted":   "submitted",
		"created":     "created",
		"deleted":     "deleted",
		"labeled":     "labeled",
		"unlabeled":   "unlabeled",
		"assigned":    "assigned",
		"unassigned":  "unassigned",
		"requested":   "requested",
		"completed":   "completed",
		"none":        "none",
		"other":       "other",
	}

	knownGithubPRReviewResults = map[string]string{
		"approved":          "approved",
		"changes_requested": "changes_requested",
		"commented":         "commented",
		"dismissed":         "dismissed",
		"other":             "other",
	}

	knownCloudRuntimeOps = map[string]string{
		"provision": "provision",
		"terminate": "terminate",
		"status":    "status",
		"gateway":   "gateway",
		"billing":   "billing",
		"fleet":     "fleet",
		"other":     "other",
	}

	knownCloudRuntimeStatuses = map[string]string{
		"ok":      "ok",
		"4xx":     "4xx",
		"5xx":     "5xx",
		"timeout": "timeout",
		"error":   "error",
	}

	knownDaemonWSKinds = map[string]string{
		"heartbeat":     "heartbeat",
		"task_claim":    "task_claim",
		"task_complete": "task_complete",
		"task_usage":    "task_usage",
		"task_progress": "task_progress",
		"task_messages": "task_messages",
		"log":           "log",
		"other":         "other",
	}

	// knownChatOutputLocalPathKinds 是 multica_chat_output_local_path_total
	// （MUL-4899）的证据类型白名单。封闭白名单的作用是把「出问题的路径」挡在
	// Prometheus 之外：调用方传入的只是分类，永远不是回复内容的片段
	// （避免把本地文件路径等敏感字符串写进标签）。
	knownChatOutputLocalPathKinds = map[string]string{
		"file_url":     "file_url",
		"workdir_path": "workdir_path",
	}

	knownContactSalesSources = map[string]string{
		"page":        "page",
		"onboarding":  "onboarding",
		"agents_page": "agents_page",
		"unknown":     "unknown",
		"other":       "other",
	}
)

// normalizeFromAllowList 通用白名单归一化：小写 + 去空白后精确查表，
// 命中返回表中规范值，未命中返回 fallback（各调用方按语义传 "unknown" 或 "other"）。
func normalizeFromAllowList(value string, allowList map[string]string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := allowList[value]; ok {
		return normalized
	}
	return fallback
}

// NormalizePlatform 归一化平台枚举（server/web/cli/mobile/ios）；认不出归 "unknown"。
func NormalizePlatform(value string) string {
	return normalizeFromAllowList(value, knownPlatforms, "unknown")
}

// NormalizeSignupSource 把原始的 multica_signup_source cookie 载荷分桶到固定的注册渠道
// 白名单。cookie 在 PostHog 侧是自由格式 JSON（utm_source / utm_medium / referrer）；
// 这里只需要一个有界的标签，所以在有 JSON 字段时取 utm_source / source / referrer /
// ref 之一，否则用裸字符串。空值 → "direct"，白名单外 → "other"。
func NormalizeSignupSource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "direct"
	}
	// JSON 形态：{"utm_source":"...","utm_medium":"...","referrer":"..."}
	if strings.HasPrefix(value, "{") {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(value), &parsed); err == nil {
			for _, key := range []string{"utm_source", "source", "referrer", "ref"} {
				if raw, ok := parsed[key]; ok {
					if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
						value = s
						break
					}
				}
			}
		}
	}
	return normalizeFromAllowList(canonicaliseSignupChannel(value), knownSignupSources, "other")
}

// canonicaliseSignupChannel 把原始注册来源字符串归并成白名单能匹配的形状：
// 小写、去空白、URL 形态只保留主机段，并把若干常见别名统一
// （"twitter.com" → "twitter"）。这里刻意写得防御性很强——cookie 是客户端设的，
// 什么形状都可能出现。
func canonicaliseSignupChannel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	// 剥掉可选的 URL scheme，让 "https://twitter.com/foo" → "twitter.com/foo"。
	for _, scheme := range []string{"https://", "http://", "//"} {
		if strings.HasPrefix(value, scheme) {
			value = strings.TrimPrefix(value, scheme)
			break
		}
	}
	// 只保留主机段。
	if i := strings.IndexAny(value, "/?#"); i >= 0 {
		value = value[:i]
	}
	value = strings.TrimPrefix(value, "www.")
	// 把知名主机名映射到对应渠道桶。
	hostAliases := map[string]string{
		"google.com":           "google",
		"google.co.uk":         "google",
		"bing.com":             "bing",
		"duckduckgo.com":       "duckduckgo",
		"twitter.com":          "twitter",
		"x.com":                "twitter",
		"t.co":                 "twitter",
		"linkedin.com":         "linkedin",
		"lnkd.in":              "linkedin",
		"facebook.com":         "facebook",
		"fb.com":               "facebook",
		"instagram.com":        "instagram",
		"github.com":           "github",
		"gitlab.com":           "gitlab",
		"news.ycombinator.com": "hacker_news",
		"reddit.com":           "reddit",
		"old.reddit.com":       "reddit",
		"youtube.com":          "youtube",
		"youtu.be":             "youtube",
		"discord.com":          "discord",
		"discord.gg":           "discord",
		"slack.com":            "slack",
		"producthunt.com":      "product_hunt",
		"medium.com":           "medium",
		"dev.to":               "dev_to",
	}
	if mapped, ok := hostAliases[value]; ok {
		return mapped
	}
	return value
}

// NormalizeOnboardingPath 归一化引导路径枚举；认不出归 "unknown"。
func NormalizeOnboardingPath(value string) string {
	return normalizeFromAllowList(value, knownOnboardingPaths, "unknown")
}

// NormalizeAutopilotCadence 归一化 autopilot 运行节奏；认不出归 "unknown"。
func NormalizeAutopilotCadence(value string) string {
	return normalizeFromAllowList(value, knownAutopilotCadences, "unknown")
}

// NormalizeAutopilotTrigger 归一化 autopilot 触发方式；认不出归 "unknown"。
func NormalizeAutopilotTrigger(value string) string {
	return normalizeFromAllowList(value, knownAutopilotTriggers, "unknown")
}

// NormalizeAutopilotSkipReason 归一化 autopilot 跳过原因；认不出归 "other"。
func NormalizeAutopilotSkipReason(value string) string {
	return normalizeFromAllowList(value, knownAutopilotSkipReasons, "other")
}

// NormalizeWebhookProvider 归一化 webhook 来源方（github/gitlab/stripe…）；认不出归 "other"。
func NormalizeWebhookProvider(value string) string {
	return normalizeFromAllowList(value, knownWebhookProviders, "other")
}

// NormalizeWebhookDeliveryStatus 归一化 webhook 投递状态；认不出归 "other"。
func NormalizeWebhookDeliveryStatus(value string) string {
	return normalizeFromAllowList(value, knownWebhookDeliveryStatuses, "other")
}

// NormalizeWebhookRateLimitGate 归一化 webhook 限流闸门；认不出归 "other"。
func NormalizeWebhookRateLimitGate(value string) string {
	return normalizeFromAllowList(value, knownWebhookRateLimitGates, "other")
}

// NormalizeEmailRateLimitAction 归一化被限流的邮件动作；认不出归 "other"。
func NormalizeEmailRateLimitAction(value string) string {
	return normalizeFromAllowList(value, knownEmailRateLimitActions, "other")
}

// NormalizeEmailRateLimitGate 归一化邮件限流闸门维度（actor/workspace/recipient）；认不出归 "other"。
func NormalizeEmailRateLimitGate(value string) string {
	return normalizeFromAllowList(value, knownEmailRateLimitGates, "other")
}

// NormalizeGithubEventKind 归一化 GitHub webhook 事件类型；认不出归 "other"。
func NormalizeGithubEventKind(value string) string {
	return normalizeFromAllowList(value, knownGithubEventKinds, "other")
}

// NormalizeGithubAction 归一化 GitHub 事件动作。空值归 "none"（例如 push 事件没有动作
// 字段），非空认不出归 "other"。
func NormalizeGithubAction(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return normalizeFromAllowList(value, knownGithubActions, "other")
}

// NormalizeGithubPRReviewResult 归一化 PR review 结论；认不出归 "other"。
func NormalizeGithubPRReviewResult(value string) string {
	return normalizeFromAllowList(value, knownGithubPRReviewResults, "other")
}

// NormalizeCloudRuntimeOp 归一化云 runtime 操作类型；认不出归 "other"。
func NormalizeCloudRuntimeOp(value string) string {
	return normalizeFromAllowList(value, knownCloudRuntimeOps, "other")
}

// NormalizeCloudRuntimeStatus 把 HTTP 状态码或符号化的结果字符串收进固定桶
// {ok, 4xx, 5xx, timeout, error}。空值/未知值都归 "error"。
func NormalizeCloudRuntimeStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalized, ok := knownCloudRuntimeStatuses[value]; ok {
		return normalized
	}
	// 三位的裸数字按首位分桶：2xx 算成功、4xx/5xx 算对应错误桶，其它落 error。
	if len(value) == 3 {
		switch value[0] {
		case '2':
			return "ok"
		case '4':
			return "4xx"
		case '5':
			return "5xx"
		}
	}
	return "error"
}

// CloudRuntimeStatusForCode 把 HTTP 状态码映射成分桶标签。供 cloudruntime
// 客户端埋点用；与 NormalizeCloudRuntimeStatus 不同——它输入的是数字码而非字符串。
func CloudRuntimeStatusForCode(code int) string {
	switch {
	case code >= 200 && code < 400:
		return "ok"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "error"
	}
}

// NormalizeDaemonWSKind 归一化 daemon WebSocket 消息类型；认不出归 "other"。
func NormalizeDaemonWSKind(value string) string {
	return normalizeFromAllowList(value, knownDaemonWSKinds, "other")
}

// NormalizeContactSalesSource 归一化「联系销售」入口来源；认不出归 "other"。
func NormalizeContactSalesSource(value string) string {
	return normalizeFromAllowList(value, knownContactSalesSources, "other")
}

// NormalizeChatOutputLocalPathKind 归一化聊天输出本地路径的证据类型；认不出归 "other"。
func NormalizeChatOutputLocalPathKind(value string) string {
	return normalizeFromAllowList(value, knownChatOutputLocalPathKinds, "other")
}
