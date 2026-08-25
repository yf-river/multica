package analytics

import "strings"

// Event names. Keep in sync with docs/analytics.md.
const (
	EventSignup                = "signup"
	EventWorkspaceCreated      = "workspace_created"
	EventRuntimeRegistered     = "runtime_registered"
	EventRuntimeReady          = "runtime_ready"
	EventRuntimeFailed         = "runtime_failed"
	EventRuntimeOffline        = "runtime_offline"
	EventIssueExecuted         = "issue_executed"
	EventIssueCreated          = "issue_created"
	EventChatMessageSent       = "chat_message_sent"
	EventAutopilotRunStarted   = "autopilot_run_started"
	EventAutopilotRunCompleted = "autopilot_run_completed"
	EventAutopilotRunFailed    = "autopilot_run_failed"
	EventAgentCreated          = "agent_created"
	EventFeedbackSubmitted     = "feedback_submitted"
	EventSquadCreated          = "squad_created"
	EventAutopilotCreated      = "autopilot_created"
)

const EventSchemaVersion = 2

// metricsOnlyEvents are operational / execution-lifecycle events that are
// recorded to Prometheus (via metrics.IncForEvent, for Grafana) but are
// deliberately NOT shipped to PostHog. They are high-volume runtime/autopilot
// telemetry whose per-event PostHog ingestion cost is not justified — Grafana
// already carries the equivalent counters. metrics.RecordEvent consults this
// set and skips the PostHog Capture for these names while still incrementing
// the counter. PostHog is reserved for user/product-behaviour events.
//
// Note: agent_task_* lifecycle events are also Prometheus-only, but their
// Prometheus side is handled by typed BusinessMetrics.RecordTask* methods, so
// they never build an analytics.Event in the first place and don't need an
// entry here.
var metricsOnlyEvents = map[string]struct{}{
	EventRuntimeRegistered:     {},
	EventRuntimeReady:          {},
	EventRuntimeFailed:         {},
	EventRuntimeOffline:        {},
	EventAutopilotRunStarted:   {},
	EventAutopilotRunCompleted: {},
	EventAutopilotRunFailed:    {},
}

// IsMetricsOnly reports whether an event name is operational telemetry that
// must be counted in Prometheus but not sent to PostHog. See metricsOnlyEvents.
func IsMetricsOnly(name string) bool {
	_, ok := metricsOnlyEvents[name]
	return ok
}

const (
	SourceManual    = "manual"
	SourceChat      = "chat"
	SourceAutopilot = "autopilot"
	SourceAPI       = "api"
)

// CoreProperties are the shared join and segmentation fields used by the
// canonical PostHog events. Empty values are omitted, except is_demo which is
// always stamped so dashboards can filter demo data without sparse-property
// edge cases.
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

// Platform is used as the "platform" event property so funnels can split by
// web / cli / server. Request-path events use PlatformServer as a fallback
// when the caller is a server-originating action (e.g. auto-created user);
// otherwise the frontend passes the real platform via a header / body field.
const (
	PlatformServer = "server"
	PlatformWeb    = "web"
	PlatformCLI    = "cli"
)

// Signup builds the account-created event used by the internal login flow.
// Marketing acquisition attribution is intentionally not collected in this
// build; keep the event free of external-source fields.
func Signup(userID, account string) Event {
	return Event{
		Name:       EventSignup,
		DistinctID: userID,
		SetOnce: map[string]any{
			"account": account,
		},
	}
}

// WorkspaceCreated builds the workspace_created event. "Is this the user's
// first workspace?" is deliberately not stamped here — it's derived in
// PostHog by checking whether the user has a prior workspace_created event.
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

// RuntimeRegistered fires on the first time a (workspace, daemon, provider)
// triple is upserted. The handler uses a `xmax = 0` flag returned from the
// upsert query to distinguish inserts from updates — heartbeats and repeat
// registrations never emit this event.
func RuntimeRegistered(provider string) Event {
	return runtimeEvent(EventRuntimeRegistered, provider, nil)
}

func RuntimeReady(provider string, readyDurationMS int64) Event {
	var props map[string]any
	if readyDurationMS > 0 {
		props = map[string]any{"ready_duration_ms": readyDurationMS}
	}
	return runtimeEvent(EventRuntimeReady, provider, props)
}

func RuntimeFailed(provider, failureReason string, recoverable bool) Event {
	return runtimeEvent(EventRuntimeFailed, provider, map[string]any{
		"failure_reason": failureReason,
		"recoverable":    recoverable,
	})
}

func RuntimeOffline(provider string) Event {
	return runtimeEvent(EventRuntimeOffline, provider, nil)
}

func runtimeEvent(name, provider string, properties map[string]any) Event {
	if properties == nil {
		properties = make(map[string]any, 2)
	}
	properties["runtime_mode"] = "local"
	properties["provider"] = provider
	return Event{
		Name:       name,
		Properties: properties,
	}
}

// IssueExecuted fires at most once per issue lifetime — on the first task
// completion that flips `issues.first_executed_at` from NULL via an atomic
// UPDATE. Retries, re-assignments, and comment-triggered follow-ups never
// re-emit, which is what keeps the ≥1/≥2/≥5/≥10 funnel buckets honest.
//
// Deliberately not stamped here: the workspace's Nth-issue ordinal.
// Computing it at emit time is not atomic (two concurrent first-completions
// both read count=1, both emit n=1), and PostHog derives the same number
// exactly at query time from the event stream.
func IssueExecuted(actorID, workspaceID, issueID, taskID, agentID, source, runtimeMode, provider string, taskDurationMS int64) Event {
	return Event{
		Name:        EventIssueExecuted,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
			"task_duration_ms": taskDurationMS,
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

func AutopilotRunStarted(source string) Event {
	return autopilotRunEvent(EventAutopilotRunStarted, source)
}

func AutopilotRunCompleted(source string) Event {
	return autopilotRunEvent(EventAutopilotRunCompleted, source)
}

func AutopilotRunFailed(source string) Event {
	return autopilotRunEvent(EventAutopilotRunFailed, source)
}

// AgentCreated fires whenever a new agent is added to a workspace.
// `isFirstAgentInWorkspace` distinguishes initial setup from later additions.
//
// template is the template slug the frontend used to seed the agent
// (e.g. "coding", "planning", "writing", "assistant") — empty when the
// caller didn't come from a template picker.
func AgentCreated(actorID, workspaceID, agentID, provider, runtimeMode, template string, isFirstAgentInWorkspace bool) Event {
	return Event{
		Name:        EventAgentCreated,
		DistinctID:  actorID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(map[string]any{
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

// FeedbackSubmitted fires after a feedback row is successfully inserted.
// The raw message is stored in the DB and never broadcast — we only emit a
// coarse length bucket, an image-presence flag, the kind picker selection,
// and the client platform / version so support can segment without leaking
// content.
func FeedbackSubmitted(userID, workspaceID, kind string, messageLen int, hasImages bool, platform, appVersion string) Event {
	props := map[string]any{
		"message_length_bucket": feedbackLengthBucket(messageLen),
		"has_images":            hasImages,
	}
	if kind != "" {
		props["kind"] = kind
	}
	if platform != "" {
		props["platform"] = platform
	}
	if appVersion != "" {
		props["app_version"] = appVersion
	}
	return Event{
		Name:        EventFeedbackSubmitted,
		DistinctID:  userID,
		WorkspaceID: workspaceID,
		Properties: withCoreProperties(props, CoreProperties{
			UserID:      userID,
			WorkspaceID: workspaceID,
			Source:      "ops_feedback",
		}),
	}
}

// SquadCreated fires when a workspace member or admin creates a new squad.
// `memberCount` is the number of members the squad was seeded with at
// creation time (frontend can pre-populate via the picker).
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

// AutopilotCreated fires when a workspace member creates a new autopilot.
// `cadence` matches the autopilot.cadence enum (hourly/daily/weekly/...
// /webhook). triggerKind is the initial trigger type (schedule / webhook /
// manual) — when both schedule and webhook triggers are seeded, we report
// the dominant one (schedule wins).
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

func autopilotRunEvent(name, source string) Event {
	return Event{
		Name: name,
		Properties: map[string]any{
			"cadence":      source,
			"trigger_kind": source,
		},
	}
}

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

func nonAgentUserID(distinct string) string {
	if distinct == "" || strings.Contains(distinct, ":") {
		return ""
	}
	return distinct
}

func feedbackLengthBucket(n int) string {
	switch {
	case n < 100:
		return "0-100"
	case n < 500:
		return "100-500"
	case n < 2000:
		return "500-2000"
	default:
		return "2000+"
	}
}
