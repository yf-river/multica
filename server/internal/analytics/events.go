// events.go 是产品遥测事件名的「权威目录」：集中定义所有 canonical 事件常量与
// 各自的事件构建函数。请与 packages/core/analytics 保持同步。
package analytics

import "strings"

// 事件名目录。本文件是权威来源；请与 packages/core/analytics 保持同步。
const (
	EventSignup                        = "signup"
	EventWorkspaceCreated              = "workspace_created"
	EventRuntimeRegistered             = "runtime_registered"
	EventRuntimeReady                  = "runtime_ready"
	EventRuntimeFailed                 = "runtime_failed"
	EventRuntimeOffline                = "runtime_offline"
	EventIssueExecuted                 = "issue_executed"
	EventIssueCreated                  = "issue_created"
	EventChatMessageSent               = "chat_message_sent"
	EventAutopilotRunStarted           = "autopilot_run_started"
	EventAutopilotRunCompleted         = "autopilot_run_completed"
	EventAutopilotRunFailed            = "autopilot_run_failed"
	EventTeamInviteSent                = "team_invite_sent"
	EventTeamInviteAccepted            = "team_invite_accepted"
	EventOnboardingStarted             = "onboarding_started"
	EventOnboardingQuestionnaireSubmit = "onboarding_questionnaire_submitted"
	EventAgentCreated                  = "agent_created"
	EventOnboardingCompleted           = "onboarding_completed"
	EventCloudWaitlistJoined           = "cloud_waitlist_joined"
	EventContactSalesSubmitted         = "contact_sales_submitted"
	EventSquadCreated                  = "squad_created"
	EventAutopilotCreated              = "autopilot_created"
)

const EventSchemaVersion = 2

// metricsOnlyEvents 列出「记入 Prometheus（经 metrics.IncForEvent，给 Grafana），
// 但刻意不发给 PostHog」的每个服务端事件。metrics.RecordEvent 会查这张表，对这些名字
// 跳过 PostHog Capture，但仍递增计数器。
//
// 自 MUL-4127 起，PostHog 不再用于服务端产品分析：获客/激活/扩张漏斗改为直接从运维
// 数据库与这些 Grafana 计数器读取，于是「把每个产品事件再复制一份到 PostHog」的冗余
// 被退役了。这让所有服务端事件都变成 metrics-only——无论产品行为组还是原本高量的
// runtime/autopilot 遥测都只进 Prometheus。PostHog 现在只收前端错误/崩溃遥测
// （$exception、client_crash、client_unresponsive）；见 packages/core/analytics。
//
// 注意：agent_task_* 生命周期事件同样只进 Prometheus，但它们的 Prometheus 一侧由
// 带类型的 BusinessMetrics.RecordTask* 方法负责，所以它们从头到尾都不会构造
// analytics.Event，也就无需在此登记。
var metricsOnlyEvents = map[string]struct{}{
	// 产品行为事件——DB + Grafana 是权威（MUL-4127）；PostHog 那份冗余。
	EventSignup:                        {},
	EventWorkspaceCreated:              {},
	EventIssueCreated:                  {},
	EventIssueExecuted:                 {},
	EventChatMessageSent:               {},
	EventTeamInviteSent:                {},
	EventTeamInviteAccepted:            {},
	EventOnboardingStarted:             {},
	EventOnboardingQuestionnaireSubmit: {},
	EventAgentCreated:                  {},
	EventOnboardingCompleted:           {},
	EventCloudWaitlistJoined:           {},
	EventContactSalesSubmitted:         {},
	EventSquadCreated:                  {},
	EventAutopilotCreated:              {},
	// 高量 runtime / autopilot 执行生命周期遥测——永远只进 Prometheus（Grafana 早已
	// 携带等价计数器）。
	EventRuntimeRegistered:     {},
	EventRuntimeReady:          {},
	EventRuntimeFailed:         {},
	EventRuntimeOffline:        {},
	EventAutopilotRunStarted:   {},
	EventAutopilotRunCompleted: {},
	EventAutopilotRunFailed:    {},
}

// IsMetricsOnly 报告某事件名是否「记入 Prometheus 但绝不能发 PostHog」。
// 自 MUL-4127 起，这对每个服务端事件都为 true。见 metricsOnlyEvents。
func IsMetricsOnly(name string) bool {
	_, ok := metricsOnlyEvents[name]
	return ok
}

// 事件产生渠道（Source 维度）取值。
const (
	SourceOnboarding = "onboarding"
	SourceManual     = "manual"
	SourceChat       = "chat"
	SourceAutopilot  = "autopilot"
	SourceAPI        = "api"
)

// CoreProperties 是 canonical PostHog 事件共用的「联表 / 分段」字段。空值省略，
// 但 is_demo 总会打上——让仪表盘能过滤演示数据，而不受稀疏属性边界问题困扰。
type CoreProperties struct {
	UserID         string
	WorkspaceID    string
	AgentID        string
	TaskID         string
	IssueID        string
	ChatSessionID  string
	AutopilotRunID string
	Source         string
	RuntimeMode    string
	Provider       string
	IsDemo         bool
}

// TaskContext 是任务上下文维度字段的别名（与 CoreProperties 同构）。
type TaskContext = CoreProperties

// 新手引导的完成路径。与 packages/core/analytics 保持同步。
const (
	OnboardingPathFull           = "full"            // 走到 first_issue 流程结束
	OnboardingPathRuntimeSkipped = "runtime_skipped" // 未连接 runtime 也完成了
	OnboardingPathCloudWaitlist  = "cloud_waitlist"  // 经 cloud waitlist 软退出完成
	OnboardingPathSkipExisting   = "skip_existing"   // 从欢迎页选「我以前弄过」
	OnboardingPathInviteAccept   = "invite_accept"   // 从 /invitations 接受了至少一张邀请
	OnboardingPathUnknown        = "unknown"         // 服务端推不出路径时的兜底
)

// Platform 用作事件的 "platform" 属性，让漏斗可按 web / cli 拆分。请求路径事件在调用方
// 属于服务端发起的动作（例如自动创建的用户）时用 PlatformServer 兜底；否则前端在后续
// 迭代里经头/body 字段传真实平台。
const (
	PlatformServer = "server"
	PlatformWeb    = "web"
	PlatformCLI    = "cli"
)

// Signup 构造 signup 事件。signupSource 若有值，来自前端存的 UTM/referrer cookie；
// 否则留空。
func Signup(userID, email, signupSource string) Event {
	return Event{
		Name:       EventSignup,
		DistinctID: userID,
		Properties: map[string]any{
			"email_domain":  emailDomain(email),
			"signup_source": signupSource,
		},
		SetOnce: map[string]any{
			"email":         email,
			"signup_source": signupSource,
		},
	}
}

// WorkspaceCreated 构造 workspace_created 事件。"是不是用户的第一个 workspace？"
// 刻意不在此标注——由 PostHog 侧检查该用户此前是否有 workspace_created 事件来推导。
func WorkspaceCreated(userID, workspaceID string) Event {
	return Event{
		Name:        EventWorkspaceCreated,
		DistinctID:  userID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(nil, CoreProperties{
			UserID:      userID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
		}),
	}
}

// RuntimeRegistered 在 (workspace, daemon, provider) 三元组首次被 upsert 时触发。
// handler 用 upsert 查询返回的 `xmax = 0` 标志区分「插入」与「更新」——心跳和重复注册
// 永远不会发这个事件。
//
// daemon 用 daemon 令牌认证（无用户上下文）时 ownerID 可能为空；需要按用户归因的
// 下游漏斗退回用 `workspace_id` 作为分组键。
func RuntimeRegistered(ownerID, workspaceID, runtimeID, daemonID, provider, runtimeVersion, cliVersion string) Event {
	distinct := ownerID
	if distinct == "" {
		// 按 workspace 合成的 id 可防止 PostHog 把不同 workspace 里互不相关的 daemon
		// 注册合并成同一个 "anonymous" person。它在 workspace 内稳定，重复心跳（反正也
		// 不触发事件）至少能正确分组。
		distinct = "workspace:" + workspaceID
	}
	return Event{
		Name:        EventRuntimeRegistered,
		DistinctID:  distinct,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"runtime_id":      runtimeID,
			"daemon_id":       daemonID,
			"provider":        provider,
			"runtime_mode":    "local",
			"runtime_version": runtimeVersion,
			"cli_version":     cliVersion,
		}, CoreProperties{
			UserID:      ownerID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
			RuntimeMode: "local",
			Provider:    provider,
		}),
	}
}

// RuntimeReady 在运行时「就绪」时触发。
func RuntimeReady(ownerID, workspaceID, runtimeID, daemonID, provider string, readyDurationMS int64) Event {
	distinct := ownerID
	if distinct == "" {
		distinct = "workspace:" + workspaceID
	}
	props := map[string]any{
		"runtime_id": runtimeID,
		"daemon_id":  daemonID,
	}
	if readyDurationMS > 0 {
		props["ready_duration_ms"] = readyDurationMS
	}
	return Event{
		Name:        EventRuntimeReady,
		DistinctID:  distinct,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID:      ownerID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
			RuntimeMode: "local",
			Provider:    provider,
		}),
	}
}

// RuntimeFailed 在运行时启动失败时触发。
func RuntimeFailed(ownerID, workspaceID, daemonID, provider, failureReason, errorType string, recoverable bool) Event {
	distinct := ownerID
	if distinct == "" && workspaceID != "" {
		distinct = "workspace:" + workspaceID
	}
	return Event{
		Name:        EventRuntimeFailed,
		DistinctID:  distinct,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"daemon_id":      daemonID,
			"failure_reason": failureReason,
			"error_type":     errorType,
			"recoverable":    recoverable,
		}, CoreProperties{
			UserID:      ownerID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
			RuntimeMode: "local",
			Provider:    provider,
		}),
	}
}

// RuntimeOffline 在运行时离线时触发。
func RuntimeOffline(ownerID, workspaceID, runtimeID, daemonID, provider string) Event {
	distinct := ownerID
	if distinct == "" {
		distinct = "workspace:" + workspaceID
	}
	return Event{
		Name:        EventRuntimeOffline,
		DistinctID:  distinct,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"runtime_id": runtimeID,
			"daemon_id":  daemonID,
		}, CoreProperties{
			UserID:      ownerID,
			WorkspaceID: workspaceID,
			Source:      SourceManual,
			RuntimeMode: "local",
			Provider:    provider,
		}),
	}
}

// IssueExecuted 在每条 issue 生命周期内至多触发一次——发生在「第一次任务完成把
// `issues.first_executed_at` 从 NULL 原子地翻转过来」时。重试、重新指派、评论触发的
// 后续执行都绝不重发——这正是 ≥1/≥2/≥5/≥10 漏斗各桶保持诚实的原因。
//
// 刻意不在此标注「该 workspace 的第 N 条 issue」：在触发时刻算它并不原子（两个并发
// 的首次完成都读到 count=1、都发 n=1），而 PostHog 在查询时从事件流里能精确推出同一
// 个数。
func IssueExecuted(actorID, workspaceID, issueID, taskID, agentID, source, runtimeMode, provider string, taskDurationMS int64) Event {
	return Event{
		Name:        EventIssueExecuted,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"issue_id":         issueID,
			"task_id":          taskID,
			"agent_id":         agentID,
			"task_duration_ms": taskDurationMS,
			"duration_ms":      taskDurationMS,
		}, CoreProperties{
			UserID:      nonAgentUserID(actorID),
			WorkspaceID: workspaceID,
			AgentID:     agentID,
			TaskID:      taskID,
			IssueID:     issueID,
			Source:      source,
			RuntimeMode: runtimeMode,
			Provider:    provider,
		}),
	}
}

// IssueCreated 在 issue 被创建时触发。
func IssueCreated(actorID, workspaceID, issueID, agentID, taskID, autopilotRunID, source, platform string) Event {
	props := map[string]any{}
	if platform != "" {
		props["platform"] = platform
	}
	return Event{
		Name:        EventIssueCreated,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID:         nonAgentUserID(actorID),
			WorkspaceID:    workspaceID,
			AgentID:        agentID,
			TaskID:         taskID,
			IssueID:        issueID,
			AutopilotRunID: autopilotRunID,
			Source:         source,
		}),
	}
}

// ChatMessageSent 在聊天消息发出时触发。
func ChatMessageSent(userID, workspaceID, chatSessionID, taskID, agentID, runtimeMode, provider, platform string) Event {
	props := map[string]any{}
	if platform != "" {
		props["platform"] = platform
	}
	return Event{
		Name:        EventChatMessageSent,
		DistinctID:  userID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID:        userID,
			WorkspaceID:   workspaceID,
			AgentID:       agentID,
			TaskID:        taskID,
			ChatSessionID: chatSessionID,
			Source:        SourceChat,
			RuntimeMode:   runtimeMode,
			Provider:      provider,
		}),
	}
}

// AutopilotAssignee 描述 autopilot 配置的执行目标。agent_id 永远是「实际会执行工作」
// 的那个 agent（squad autopilot 即其队长），保证按 agent 分组的漏斗一致。assignee_*
// 字段记录原始配置，让报表不用 join 回 autopilot 行就能区分单人 agent autopilot 与
// squad autopilot。
type AutopilotAssignee struct {
	AgentID      string // 执行 agent——squad autopilot 是队长
	AssigneeType string // "agent" 或 "squad"
	SquadID      string // AssigneeType != "squad" 时为空
}

// AutopilotRunStarted 在 autopilot 运行开始时触发。
func AutopilotRunStarted(actorID, workspaceID, autopilotID, runID, cadence string, assignee AutopilotAssignee, triggerSource string) Event {
	return autopilotRunEvent(EventAutopilotRunStarted, actorID, workspaceID, autopilotID, runID, cadence, assignee, triggerSource, nil)
}

// AutopilotRunCompleted 在 autopilot 运行成功完成时触发。
func AutopilotRunCompleted(actorID, workspaceID, autopilotID, runID, cadence string, assignee AutopilotAssignee, triggerSource string, durationMS int64) Event {
	return autopilotRunEvent(EventAutopilotRunCompleted, actorID, workspaceID, autopilotID, runID, cadence, assignee, triggerSource, map[string]any{
		"duration_ms": durationMS,
	})
}

// AutopilotRunFailed 在 autopilot 运行失败时触发。
func AutopilotRunFailed(actorID, workspaceID, autopilotID, runID, cadence string, assignee AutopilotAssignee, triggerSource, failureReason, errorType string, willRetry bool, durationMS int64) Event {
	return autopilotRunEvent(EventAutopilotRunFailed, actorID, workspaceID, autopilotID, runID, cadence, assignee, triggerSource, map[string]any{
		"duration_ms":    durationMS,
		"failure_reason": failureReason,
		"error_type":     errorType,
		"will_retry":     willRetry,
	})
}

// TeamInviteSent 在 workspace 管理员创建邀请时触发。inviteMethod 目前只有 "email"；
// 将来非邮箱邀请流程可传自己的值，让本事件保持稳定。
func TeamInviteSent(inviterID, workspaceID, invitedEmail, inviteMethod string) Event {
	return Event{
		Name:        EventTeamInviteSent,
		DistinctID:  inviterID,
		WorkspaceID: workspaceID,
		Properties: map[string]any{
			"invited_email_domain": emailDomain(invitedEmail),
			"invite_method":        inviteMethod,
		},
	}
}

// TeamInviteAccepted 在被邀请者接受并加入 workspace 时触发。daysSinceInvite 让我们能
// 区分「快速接受（热）」与「长尾接受（翻出旧邮件的人）」。
func TeamInviteAccepted(inviteeID, workspaceID string, daysSinceInvite int64) Event {
	return Event{
		Name:        EventTeamInviteAccepted,
		DistinctID:  inviteeID,
		WorkspaceID: workspaceID,
		Properties: map[string]any{
			"days_since_invite": daysSinceInvite,
		},
	}
}

// OnboardingStarted 在用户 onboarding 状态「从未被碰过（没有问卷载荷）第一次变成某个
// 非空 patch」时由服务端触发。前端会在用户首次打开页面时发自己的 onboarding_started；
// 服务端这次触发的意义是点亮 Prometheus 计数器，让 Grafana 可以不依赖 SDK 往返就能与
// PostHog 漏斗交叉核对。
//
// platform 是首次 onboarding 交互时 X-Client-Platform 头的值，经
// metrics.NormalizePlatform 的固定白名单进入 `multica_onboarding_started_total{platform=...}`
// 标签。
func OnboardingStarted(userID, platform string) Event {
	props := map[string]any{}
	if platform != "" {
		props["platform"] = platform
	}
	return Event{
		Name:       EventOnboardingStarted,
		DistinctID: userID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID: userID,
			Source: SourceOnboarding,
		}),
	}
}

// OnboardingQuestionnaireSubmitted 记录角色与用途首次完成的事件。
func OnboardingQuestionnaireSubmitted(userID string, role string, useCase []string, roleSkipped, useCaseSkipped, roleHasOther, useCaseHasOther bool) Event {
	// 把 nil 切片规整成 []，让 PostHog 属性值保持稳定（避免属性类型推断里 null 与
	// [] 混用）。
	if useCase == nil {
		useCase = []string{}
	}
	return Event{
		Name:       EventOnboardingQuestionnaireSubmit,
		DistinctID: userID,
		Properties: withCoreProperties(map[string]any{
			"role":               role,
			"use_case":           useCase,
			"role_skipped":       roleSkipped,
			"use_case_skipped":   useCaseSkipped,
			"role_has_other":     roleHasOther,
			"use_case_has_other": useCaseHasOther,
		}, CoreProperties{
			UserID: userID,
			Source: SourceOnboarding,
		}),
		Set: map[string]any{
			"role":     role,
			"use_case": useCase,
		},
	}
}

// AgentCreated 在「新 agent 加入 workspace」时触发——不只在 onboarding 内。
// `isFirstAgentInWorkspace` 让漏斗能把 Step 4 的信号与后来的 agent 添加区分开。
//
// template 是调用方提供的创建来源归因（例如 "agent_builder"）；空表示手工编写的 agent。
func AgentCreated(actorID, workspaceID, agentID, provider, runtimeMode, template string, isFirstAgentInWorkspace bool) Event {
	return Event{
		Name:        EventAgentCreated,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"agent_id":                    agentID,
			"provider":                    provider,
			"runtime_mode":                runtimeMode,
			"template":                    template,
			"is_first_agent_in_workspace": isFirstAgentInWorkspace,
		}, CoreProperties{
			UserID:      actorID,
			WorkspaceID: workspaceID,
			AgentID:     agentID,
			Source:      SourceManual,
			RuntimeMode: runtimeMode,
			Provider:    provider,
		}),
	}
}

// OnboardingCompleted 从 CompleteOnboarding 触发。`completionPath` 由服务端根据用户
// 到达时的状态推导（见上面 OnboardingPath* 常量）。`joinedCloudWaitlist` 表示用户在流程
// 中任一点提交过 waitlist 表单——它与 `completion_path` 正交：用户可能提交了表单却仍
// 选 CLI，所以我们两个信号都保留。
//
// onboardedAt 是 RFC3339 时间戳，以 $set_once 打在 person 上，让「在某日期前完成
// onboarding」的群组可以直接从 person_properties 查询，而无须每次重发事件。
func OnboardingCompleted(userID, workspaceID, completionPath, onboardedAt string, joinedCloudWaitlist bool) Event {
	return Event{
		Name:        EventOnboardingCompleted,
		DistinctID:  userID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"completion_path":       completionPath,
			"joined_cloud_waitlist": joinedCloudWaitlist,
		}, CoreProperties{
			UserID:      userID,
			WorkspaceID: workspaceID,
			Source:      SourceOnboarding,
		}),
		SetOnce: map[string]any{
			"onboarded_at": onboardedAt,
		},
	}
}

// CloudWaitlistJoined 在用户提交 Step 3 云 waitlist 表单时触发。`hasReason` 是存在性
// 布尔——自由文本原因留在 DB 供产品研究。
func CloudWaitlistJoined(userID string, hasReason bool) Event {
	return Event{
		Name:       EventCloudWaitlistJoined,
		DistinctID: userID,
		Properties: withCoreProperties(map[string]any{
			"has_reason": hasReason,
		}, CoreProperties{
			UserID: userID,
			Source: SourceOnboarding,
		}),
	}
}

// ContactSalesSubmitted 在一次「联系销售」询价被记录后触发。表单是公开且未认证的，
// 所以 DistinctID 为空（PostHog 会当作匿名事件）。我们带上粗粒度公司规模、国家/地区、
// 意向用例、以及表单位置桶（page / onboarding / agents_page），让销售/市场无需查运维
// DB 就能切分进线量。
//
// formSource 是页面上下文桶；CoreProperties 的 Source 保持 "marketing_contact_sales"，
// 让 PostHog 仪表盘能与其他市场事件的漏斗 join。Prometheus 一侧经
// metrics.NormalizeContactSalesSource 白名单直接读 form_source。
func ContactSalesSubmitted(inquiryID, companySize, countryRegion, useCase, formSource string, hasGoals bool) Event {
	props := map[string]any{
		"inquiry_id":     inquiryID,
		"company_size":   companySize,
		"country_region": countryRegion,
		"use_case":       useCase,
		"has_goals":      hasGoals,
	}
	if formSource != "" {
		props["form_source"] = formSource
	}
	return Event{
		Name:       EventContactSalesSubmitted,
		DistinctID: inquiryID,
		Properties: withCoreProperties(props, CoreProperties{
			Source: "marketing_contact_sales",
		}),
	}
}

// SquadCreated 在 workspace 成员或管理员创建新 squad 时触发。`memberCount` 是创建时
// squad 预置的成员数（前端可通过选择器预填）。
func SquadCreated(actorID, workspaceID, squadID string, memberCount int) Event {
	return Event{
		Name:        EventSquadCreated,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"squad_id":     squadID,
			"member_count": int64(memberCount),
		}, CoreProperties{
			UserID:      nonAgentUserID(actorID),
			WorkspaceID: workspaceID,
			Source:      SourceManual,
		}),
	}
}

// AutopilotCreated 在 workspace 成员创建新 autopilot 时触发。`cadence` 对应
// autopilot.cadence 枚举（hourly/daily/weekly/.../webhook）。triggerKind 是初始触发器
// 类型（schedule / webhook / manual）——schedule 与 webhook 触发器同时播种时，报告
// 主导那个（schedule 优先）。
func AutopilotCreated(actorID, workspaceID, autopilotID, cadence, triggerKind string) Event {
	return Event{
		Name:        EventAutopilotCreated,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"autopilot_id": autopilotID,
			"cadence":      cadence,
			"trigger_kind": triggerKind,
		}, CoreProperties{
			UserID:      nonAgentUserID(actorID),
			WorkspaceID: workspaceID,
			Source:      SourceManual,
		}),
	}
}

// autopilotRunEvent 组装一次 autopilot 运行类事件（started/completed/failed）的公共
// 部分：把 extra 属性与核心属性合在一起，再补 autopilot 的分配信息。
func autopilotRunEvent(name, actorID, workspaceID, autopilotID, runID, cadence string, assignee AutopilotAssignee, triggerSource string, extra map[string]any) Event {
	if extra == nil {
		extra = map[string]any{}
	}
	extra["trigger_source"] = triggerSource
	extra["trigger_kind"] = triggerSource
	if cadence != "" {
		extra["cadence"] = cadence
	}
	props := withCoreProperties(extra, CoreProperties{
		UserID:         nonAgentUserID(actorID),
		WorkspaceID:    workspaceID,
		AgentID:        assignee.AgentID,
		AutopilotRunID: runID,
		Source:         SourceAutopilot,
	})
	props["autopilot_id"] = autopilotID
	if assignee.AssigneeType != "" {
		props["assignee_type"] = assignee.AssigneeType
	}
	if assignee.SquadID != "" {
		props["squad_id"] = assignee.SquadID
	}
	return Event{
		Name:        name,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties:  props,
	}
}

// withCoreProperties 把非空的共享核心字段合进事件属性，并总是打上 is_demo 标志。
func withCoreProperties(props map[string]any, core CoreProperties) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	if core.UserID != "" {
		props["user_id"] = core.UserID
	}
	if core.AgentID != "" {
		props["agent_id"] = core.AgentID
	}
	if core.TaskID != "" {
		props["task_id"] = core.TaskID
	}
	if core.IssueID != "" {
		props["issue_id"] = core.IssueID
	}
	if core.ChatSessionID != "" {
		props["chat_session_id"] = core.ChatSessionID
	}
	if core.AutopilotRunID != "" {
		props["autopilot_run_id"] = core.AutopilotRunID
	}
	if core.Source != "" {
		props["source"] = core.Source
	}
	if core.RuntimeMode != "" {
		props["runtime_mode"] = core.RuntimeMode
	}
	if core.Provider != "" {
		props["provider"] = core.Provider
	}
	props["is_demo"] = core.IsDemo
	return props
}

// nonAgentUserID 在 distinct id 带 ":"（例如 "workspace:xxx" 或带角色后缀）时返回空，
// 只把普通用户 id 原样传出。
func nonAgentUserID(distinct string) string {
	if distinct == "" || strings.Contains(distinct, ":") {
		return ""
	}
	return distinct
}

// emailDomain 提取邮箱 @ 之后的小写域名部分；不是合法邮箱返回空串。
func emailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}
