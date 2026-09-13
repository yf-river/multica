// 职责定位：workspace/project 仪表盘的 6 个只读聚合查询端点（tokens 用量、运行时长、失败
// 分布，各按「天」或「按 agent」两种切法）。详细约定（天数窗口的截断口径、权限控制、失败桶
// 语义）见下方本文件导读注释。
package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// Workspace / Project 仪表盘
//
// 六个只读端点支撑整个 workspace 仪表盘：
//
//   GET /api/dashboard/usage/daily        按 (date, model) 的 token 行
//   GET /api/dashboard/usage/by-agent     按 (agent, model) 的 token 行
//   GET /api/dashboard/agent-runtime      按 agent 的运行时长 + 任务数
//   GET /api/dashboard/runtime/daily      按天 的运行时长 + 任务数
//   GET /api/dashboard/failures/daily     按 (date, failure_reason) 计数
//   GET /api/dashboard/failures/by-agent  按 (agent, failure_reason) 计数
//
// 它们都接受 ?days=N（默认 30，上限 365），以及可选的 ?project_id=<uuid> 把聚合范围缩小到
// 单个项目；不传 project_id 时数据覆盖整个 workspace。
//
// 时间窗截断约定：三个「按天分桶」的序列用 parseSinceParamInTZ（N+1 个自然日，多出来的那天
// 由客户端用 `-(days-1)` 裁掉）；三个「按 agent 聚合」的用 parseExactSinceParamInTZ（恰好 N）。
// 不带日期的行客户端没法裁，若也让它们按 N+1 切，榜和 Run time / Tasks KPI 就会比旁边的
// 图与 Cost / Tokens KPI 多覆盖一个自然日——在 days=1 时单个 agent 的行可能比 workspace
// 总数还高（MUL-5551）。成对的两种口径必须用一致的窗口。
//
// 费用在客户端用「按模型定价表」计算——所以 model 维度刻意留在响应上（与 per-runtime
// 用量端点同一约定）。
//
// 权限控制：跨 workspace 的序列（usage/daily、runtime/daily、failures/daily）不含 agent
// 维度，只需是 workspace 成员即可——token 花费/运行时长/失败量都是 workspace 级别的运维
// 指标。三个「按 agent 聚合」端点额外应用 per-agent 可见性过滤：见 foldRestrictedAgents。
// ---------------------------------------------------------------------------

// restrictedAgentsRowID 是响应中「拒绝点名」的那些行被折进去的合成 agent_id。刻意不用
// UUID 形态，这样永远不会与真实 agent id 撞车；也刻意区别于客户端的「已删除 agent」桶：
// 那些 agent 已经没了，这些是还活着、还在跑的。
const restrictedAgentsRowID = "__restricted_agents__"

// foldRestrictedAgents 把 `restricted`（见 restrictedAgentIDs）点名的每一行改写成
// restrictedAgentsRowID，并把改写后「在剩余维度上撞车」的行合并（provider+model、
// failure_reason，或什么都不剩）。
//
// 为什么是「折叠」而不是「丢弃」：这些响应都是「按 agent」那一半，而它的另一半
// （usage/daily、runtime/daily、failures/daily）是 workspace 级、不过滤的——直接丢行会让
// 按 agent 的明细和旁边渲染的总数对不上。合成一个桶能保住所有求和值，又不泄露真实 agent id
// 和 per-agent 拆分。
//
// 「不泄露」的前提是保留的维度本来就不带新信息：workspace 级序列已经把每个 (provider,
// model) / failure_reason 的整空间总量摆出来了，且每个可见 agent 的行都原样返回，所以被
// 藏起来的剩余量本来就是「总数减一次可见行」。
//
// 折叠对象有两类：
//  1. 别人的「私有」agent——「私有」是全代码库都在守的承诺（agent 详情 403、ListAgents
//     过滤，连管理员都不能调用别人的私有 agent）。这三个端点以前会给整 workspace 返回裸的
//     agent UUID，等于告诉普通成员「存在一个私有 agent、它跑了多少、坏在什么上」。客户端
//     本来会把那些行折叠掉，但客户端过滤只是装饰——一条 curl 就能绕过。
//  2. kind='system' 的隐藏 builder 载体——没有任何列表端点会把它给任何人，但对
//     agent_task_queue / task_usage 做聚合时不看 kind 会把它捞出来，于是会变成没人叫得出
//     名字的裸 UUID。
//
// `rewrite` 给行盖上哨兵 id、返回「合并键」；`merge` 把后面来的行累加到已发出的行上。
// 除此之外行序保持不变，桶出现在它的第一个成员原本的位置——反正客户端会重新排序。
func foldRestrictedAgents[T any, K comparable](
	rows []T,
	restricted map[string]struct{},
	agentIDOf func(T) string,
	rewrite func(T) (T, K),
	merge func(dst, src T) T,
) []T {
	if len(restricted) == 0 {
		return rows
	}
	out := make([]T, 0, len(rows))
	bucketAt := make(map[K]int)
	for _, row := range rows {
		if _, hidden := restricted[agentIDOf(row)]; !hidden {
			out = append(out, row)
			continue
		}
		folded, key := rewrite(row)
		if i, ok := bucketAt[key]; ok {
			out[i] = merge(out[i], folded)
			continue
		}
		bucketAt[key] = len(out)
		out = append(out, folded)
	}
	return out
}

// dashboardRestrictedAgents 解析「本次请求看不到的 agent」集合。解析失败时写 500 并返回
// ok=false：未过滤的聚合是这份响应绝不能退化成的结果。
func (h *Handler) dashboardRestrictedAgents(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID, role string,
) (map[string]struct{}, bool) {
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	restricted, ok := h.restrictedAgentIDs(r.Context(), workspaceID, actorType, actorID, role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return nil, false
	}
	return restricted, true
}

// parseProjectIDParam 读 URL 上的 ?project_id=<uuid>。参数缺省时返回 Valid=false 的
// pgtype.UUID，让 sqlc 的可空参数落到 SQL NULL、WHERE 子句自然退化成「不按项目过滤」。
// UUID 格式非法时写 400 并返回 ok=false；调用方必须立即 return。
func parseProjectIDParam(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	raw := r.URL.Query().Get("project_id")
	if raw == "" {
		return pgtype.UUID{}, true
	}
	u, err := util.ParseUUID(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return pgtype.UUID{}, false
	}
	return u, true
}

// DashboardUsageDailyResponse 是单个 (date, provider, model) 桶。费用侧的数学在客户端用
// 「按模型定价表」完成；provider + model 留在响应上，好让客户端区分跨 provider 撞名的裸
// model id（比如 Cursor 的 `auto`）。
type DashboardUsageDailyResponse struct {
	Date             string `json:"date"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	// 费用拆分：`CostUSDTicks` 是本桶背后这些行「provider 实际收取」的金额（单位 1e-10
	// USD）；`Uncosted*` 各 token 计数来自「provider 没给定价」的行。客户端把「有价的权威值
	// + 无价的估算值」加在一起报，混合两类行的窗口才能自洽。见 migration 213。
	CostUSDTicks             int64 `json:"cost_usd_ticks"`
	UncostedInputTokens      int64 `json:"uncosted_input_tokens"`
	UncostedOutputTokens     int64 `json:"uncosted_output_tokens"`
	UncostedCacheReadTokens  int64 `json:"uncosted_cache_read_tokens"`
	UncostedCacheWriteTokens int64 `json:"uncosted_cache_write_tokens"`
	TaskCount                int32 `json:"task_count"`
}

// GetDashboardUsageDaily 返回 workspace 按 (date, model) 切分的 token 行，可选按项目收窄。
// 数据源 task_usage_hourly，按查看者所在时区切成自然日。
func (h *Handler) GetDashboardUsageDaily(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	resp, err := h.listDashboardUsageDaily(r.Context(), parseUUID(workspaceID), tz, since, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// listDashboardUsageDaily 执行按天用量查询并把结果行映射成接口结构。
func (h *Handler) listDashboardUsageDaily(
	ctx context.Context,
	workspaceID pgtype.UUID,
	tz string,
	since pgtype.Timestamptz,
	projectID pgtype.UUID,
) ([]DashboardUsageDailyResponse, error) {
	rows, err := h.Queries.ListDashboardUsageDaily(ctx, db.ListDashboardUsageDailyParams{
		WorkspaceID: workspaceID,
		Tz:          tz,
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		return nil, err
	}
	resp := make([]DashboardUsageDailyResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardUsageDailyResponse{
			Date:                     row.Date.Time.Format("2006-01-02"),
			Provider:                 row.Provider,
			Model:                    row.Model,
			InputTokens:              row.InputTokens,
			OutputTokens:             row.OutputTokens,
			CacheReadTokens:          row.CacheReadTokens,
			CacheWriteTokens:         row.CacheWriteTokens,
			CostUSDTicks:             row.CostUsdTicks,
			UncostedInputTokens:      row.UncostedInputTokens,
			UncostedOutputTokens:     row.UncostedOutputTokens,
			UncostedCacheReadTokens:  row.UncostedCacheReadTokens,
			UncostedCacheWriteTokens: row.UncostedCacheWriteTokens,
			TaskCount:                row.TaskCount,
		}
	}
	return resp, nil
}

// DashboardUsageByAgentResponse 是单个 (agent, provider, model) 行。provider 随行返回，原因
// 与 daily 响应一样（跨 provider 定价区分）；客户端按 agent_id 折叠并求和费用。
type DashboardUsageByAgentResponse struct {
	AgentID          string `json:"agent_id"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	// 费用拆分：`CostUSDTicks` 是 provider 对桶内行实际收取的金额（1e-10 USD）；`Uncosted*`
	// 是 provider 未定价行的 token 数。客户端报「权威 + 估算」，混合两类行的窗口才完整。
	// 见 migration 213。
	CostUSDTicks             int64 `json:"cost_usd_ticks"`
	UncostedInputTokens      int64 `json:"uncosted_input_tokens"`
	UncostedOutputTokens     int64 `json:"uncosted_output_tokens"`
	UncostedCacheReadTokens  int64 `json:"uncosted_cache_read_tokens"`
	UncostedCacheWriteTokens int64 `json:"uncosted_cache_write_tokens"`
	TaskCount                int32 `json:"task_count"`
}

// GetDashboardUsageByAgent 返回 workspace 按 (agent, model) 切分的 token 聚合，可选按项目
// 收窄。数据源 task_usage_hourly，`?days=` 截止时间按查看者时区换算。
func (h *Handler) GetDashboardUsageByAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	restricted, ok := h.dashboardRestrictedAgents(w, r, workspaceID, member.Role)
	if !ok {
		return
	}
	// 「按 agent」的 SQL 里没有日期分组——时区只决定截止点、不决定桶轴。正因如此，截止必须
	// 用「恰好 N 天」：客户端对 parseSinceParamInTZ 交出的多出的那一天用 `-(days-1)` 裁掉，
	// 而这份响应不带日期、没法那么裁。若用 N+1 的截止，这个榜会比它上方的 Tokens/Cost KPI
	// 和图多覆盖一个自然日，于是 days=1 时单个 agent 的行可能比 workspace 总数还高（MUL-5551）。
	tz := h.resolveViewingTZ(r)
	since := parseExactSinceParamInTZ(r, 30, tz)

	resp, err := h.listDashboardUsageByAgent(r.Context(), parseUUID(workspaceID), since, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage by agent")
		return
	}
	writeJSON(w, http.StatusOK, foldRestrictedUsageByAgent(resp, restricted))
}

// providerModelKey 让「受限桶」仍按 (provider, model) 拆分，这样客户端还能用按模型定价表给
// 它估价——没了这层，这个桶的费用算不出来，榜也就对不上 Cost KPI。这正是这些行要「折叠」而
// 不是「丢弃」的全部原因。
type providerModelKey struct{ provider, model string }

// foldRestrictedUsageByAgent 把被限制的 agent 行按 (provider, model) 折进受限桶并做合并。
func foldRestrictedUsageByAgent(
	rows []DashboardUsageByAgentResponse,
	restricted map[string]struct{},
) []DashboardUsageByAgentResponse {
	return foldRestrictedAgents(
		rows,
		restricted,
		func(row DashboardUsageByAgentResponse) string { return row.AgentID },
		func(row DashboardUsageByAgentResponse) (DashboardUsageByAgentResponse, providerModelKey) {
			key := providerModelKey{provider: row.Provider, model: row.Model}
			row.AgentID = restrictedAgentsRowID
			return row, key
		},
		func(dst, src DashboardUsageByAgentResponse) DashboardUsageByAgentResponse {
			dst.InputTokens += src.InputTokens
			dst.OutputTokens += src.OutputTokens
			dst.CacheReadTokens += src.CacheReadTokens
			dst.CacheWriteTokens += src.CacheWriteTokens
			dst.CostUSDTicks += src.CostUSDTicks
			dst.UncostedInputTokens += src.UncostedInputTokens
			dst.UncostedOutputTokens += src.UncostedOutputTokens
			dst.UncostedCacheReadTokens += src.UncostedCacheReadTokens
			dst.UncostedCacheWriteTokens += src.UncostedCacheWriteTokens
			dst.TaskCount += src.TaskCount
			return dst
		},
	)
}

// listDashboardUsageByAgent 执行「按 agent」用量查询并映射行结构。
func (h *Handler) listDashboardUsageByAgent(
	ctx context.Context,
	workspaceID pgtype.UUID,
	since pgtype.Timestamptz,
	projectID pgtype.UUID,
) ([]DashboardUsageByAgentResponse, error) {
	rows, err := h.Queries.ListDashboardUsageByAgent(ctx, db.ListDashboardUsageByAgentParams{
		WorkspaceID: workspaceID,
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		return nil, err
	}
	resp := make([]DashboardUsageByAgentResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardUsageByAgentResponse{
			AgentID:                  uuidToString(row.AgentID),
			Provider:                 row.Provider,
			Model:                    row.Model,
			InputTokens:              row.InputTokens,
			OutputTokens:             row.OutputTokens,
			CacheReadTokens:          row.CacheReadTokens,
			CacheWriteTokens:         row.CacheWriteTokens,
			CostUSDTicks:             row.CostUsdTicks,
			UncostedInputTokens:      row.UncostedInputTokens,
			UncostedOutputTokens:     row.UncostedOutputTokens,
			UncostedCacheReadTokens:  row.UncostedCacheReadTokens,
			UncostedCacheWriteTokens: row.UncostedCacheWriteTokens,
			TaskCount:                row.TaskCount,
		}
	}
	return resp, nil
}

// DashboardAgentRunTimeResponse 是单个 agent 在窗口内「所有终态任务」的总运行时长。包含
// failed 与 cancelled 的任务——这样仪表盘能揭示有多少执行时间花在了没成功的运行上。
// FailedCount 与 CancelledCount 是 TaskCount 的两个互斥子集；成功数由客户端用余数推得。
type DashboardAgentRunTimeResponse struct {
	AgentID        string `json:"agent_id"`
	TotalSeconds   int64  `json:"total_seconds"`
	TaskCount      int32  `json:"task_count"`
	FailedCount    int32  `json:"failed_count"`
	CancelledCount int32  `json:"cancelled_count"`
}

// GetDashboardAgentRunTime 返回 workspace 各 agent 的任务总运行时长（秒）与任务数，可选按
// 项目收窄。只有终态任务（completed / failed / cancelled）且 started_at 与 completed_at
// 都非空才计入——排队/运行中的任务没有有限时长。
func (h *Handler) GetDashboardAgentRunTime(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	restricted, ok := h.dashboardRestrictedAgents(w, r, workspaceID, member.Role)
	if !ok {
		return
	}
	// 截止时间按查看者时区换算，好让「最近 N 天」窗口与旁边的按 agent 费用卡
	// (GetDashboardUsageByAgent) 对齐。用「恰好 N 天」也是同理：这些行不带日期，客户端没法
	// 裁掉 parseSinceParamInTZ 多给的那个自然日。这份响应同时喂「榜的 Time/Tasks 列」和
	// 「Run time / Tasks KPI 卡」，N+1 截止会让这两块比旁边 Cost / Tokens 卡多跨一天
	// （MUL-5551）。
	tz := h.resolveViewingTZ(r)
	since := parseExactSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardAgentRunTime(r.Context(), db.ListDashboardAgentRunTimeParams{
		WorkspaceID: parseUUID(workspaceID),
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent runtime")
		return
	}

	resp := make([]DashboardAgentRunTimeResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardAgentRunTimeResponse{
			AgentID:        uuidToString(row.AgentID),
			TotalSeconds:   row.TotalSeconds,
			TaskCount:      row.TaskCount,
			FailedCount:    row.FailedCount,
			CancelledCount: row.CancelledCount,
		}
	}
	writeJSON(w, http.StatusOK, foldRestrictedAgentRunTime(resp, restricted))
}

// 运行时长行除了 agent 之外没有任何维度，所以所有受限行都合成一个桶——因此合并键是空结构体。
func foldRestrictedAgentRunTime(
	rows []DashboardAgentRunTimeResponse,
	restricted map[string]struct{},
) []DashboardAgentRunTimeResponse {
	return foldRestrictedAgents(
		rows,
		restricted,
		func(row DashboardAgentRunTimeResponse) string { return row.AgentID },
		func(row DashboardAgentRunTimeResponse) (DashboardAgentRunTimeResponse, struct{}) {
			row.AgentID = restrictedAgentsRowID
			return row, struct{}{}
		},
		func(dst, src DashboardAgentRunTimeResponse) DashboardAgentRunTimeResponse {
			dst.TotalSeconds += src.TotalSeconds
			dst.TaskCount += src.TaskCount
			dst.FailedCount += src.FailedCount
			dst.CancelledCount += src.CancelledCount
			return dst
		},
	)
}

// DashboardRunTimeDailyResponse 是单个 (date) 桶的终态任务运行时长与计数。支撑仪表盘每日
// Time 和 Tasks 图——与 Tokens / Cost 同一开关，不同指标。FailedCount 与 CancelledCount
// 是 TaskCount 的互斥子集。
type DashboardRunTimeDailyResponse struct {
	Date           string `json:"date"`
	TotalSeconds   int64  `json:"total_seconds"`
	TaskCount      int32  `json:"task_count"`
	FailedCount    int32  `json:"failed_count"`
	CancelledCount int32  `json:"cancelled_count"`
}

// GetDashboardRunTimeDaily 返回 workspace 按天聚合的任务总运行时长与任务数，可选按项目收窄。
// 只有终态任务（completed / failed / cancelled）且 started_at 与 completed_at 都非空才计入。
// 按 completed_at 分桶，让日界与旁边的按 agent 运行时长卡对齐。
func (h *Handler) GetDashboardRunTimeDaily(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	// 按查看者时区切日桶，让 Time / Tasks 图与 Cost / Tokens 图用同一条日历日切法。
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardRunTimeDaily(r.Context(), db.ListDashboardRunTimeDailyParams{
		WorkspaceID: parseUUID(workspaceID),
		Tz:          tz,
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list daily runtime")
		return
	}

	resp := make([]DashboardRunTimeDailyResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardRunTimeDailyResponse{
			Date:           row.Date.Time.Format("2006-01-02"),
			TotalSeconds:   row.TotalSeconds,
			TaskCount:      row.TaskCount,
			FailedCount:    row.FailedCount,
			CancelledCount: row.CancelledCount,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// 失败聚合
//
// 两个端点都返回「所有」终态任务，而不只是失败的：FailureReason 为 "" 的那一行携带该桶的
// 成功数。客户端需要这个分母才能渲染出「错误率」，而且同 payload 一起下发能保证分子分母走
// 同一套过滤——若改从运行时长端点推分母会静默对不上：那套要求 started_at IS NOT NULL，
// 而一个在队列里过期的任务从未开始过。
//
// FailureReason 的取值是 server/pkg/taskfailure 里的权威分类（21 种），外加对 NULL/空列
// 的失败行统一给 "unclassified"。客户端把它们折成少量展示类别；原始 reason 留在线上，这样
// 映射可以只改前端、不用动后端发版。
// ---------------------------------------------------------------------------

// DashboardFailureDailyResponse 是单个 (date, failure_reason) 终态任务计数组。
// FailureReason == "" 是「成功」桶。
type DashboardFailureDailyResponse struct {
	Date          string `json:"date"`
	FailureReason string `json:"failure_reason"`
	TaskCount     int32  `json:"task_count"`
}

// GetDashboardFailuresDaily 返回 workspace 按 (date, failure_reason) 切分的终态任务计数，
// 可选按项目收窄。支撑 Usage 页的 Errors 趋势与按错误类别的分解图。
func (h *Handler) GetDashboardFailuresDaily(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	// 与其他每日序列一致，按查看者时区分自然日，Errors tab 才能和 Cost / Tokens / Time /
	// Tasks 对齐。
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardFailuresDaily(r.Context(), db.ListDashboardFailuresDailyParams{
		WorkspaceID: parseUUID(workspaceID),
		Tz:          tz,
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list daily failures")
		return
	}

	resp := make([]DashboardFailureDailyResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardFailureDailyResponse{
			Date:          row.Date.Time.Format("2006-01-02"),
			FailureReason: row.FailureReason,
			TaskCount:     row.TaskCount,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// DashboardFailureByAgentResponse 是单个 (agent, failure_reason) 终态任务计数组。
// FailureReason == "" 是「成功」桶。
type DashboardFailureByAgentResponse struct {
	AgentID       string `json:"agent_id"`
	FailureReason string `json:"failure_reason"`
	TaskCount     int32  `json:"task_count"`
}

// GetDashboardFailuresByAgent 返回 workspace 按 (agent, failure_reason) 切分的终态任务计数，
// 可选按项目收窄。支撑 Usage 页的「错误大户」列表。
func (h *Handler) GetDashboardFailuresByAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	restricted, ok := h.dashboardRestrictedAgents(w, r, workspaceID, member.Role)
	if !ok {
		return
	}
	// SQL 里没有日期分组，客户端没法像裁「按天」序列那样裁这份响应。所以在服务端把窗口收窄
	// 成恰好 `days` 个自然日——与 Errors 图在 `-(days-1)` 过滤后渲染的跨度一致。若用默认的
	// N+1 截止，这张卡的窗口会比旁边的图宽一天，小窗口（如 days=1）时两侧就会各说各话。
	tz := h.resolveViewingTZ(r)
	since := parseExactSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardFailuresByAgent(r.Context(), db.ListDashboardFailuresByAgentParams{
		WorkspaceID: parseUUID(workspaceID),
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list failures by agent")
		return
	}

	resp := make([]DashboardFailureByAgentResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardFailureByAgentResponse{
			AgentID:       uuidToString(row.AgentID),
			FailureReason: row.FailureReason,
			TaskCount:     row.TaskCount,
		}
	}
	writeJSON(w, http.StatusOK, foldRestrictedFailuresByAgent(resp, restricted))
}

// 受限桶保留 failure_reason 拆分：客户端像处理任何别的 agent 一样，用这些原始行推它的失败率
// 与类别构成；其中 failure_reason == "" 的成功行就是分母，保证「错误大户」列表能和它上面的
// workspace 失败总数对上账。
func foldRestrictedFailuresByAgent(
	rows []DashboardFailureByAgentResponse,
	restricted map[string]struct{},
) []DashboardFailureByAgentResponse {
	return foldRestrictedAgents(
		rows,
		restricted,
		func(row DashboardFailureByAgentResponse) string { return row.AgentID },
		func(row DashboardFailureByAgentResponse) (DashboardFailureByAgentResponse, string) {
			reason := row.FailureReason
			row.AgentID = restrictedAgentsRowID
			return row, reason
		},
		func(dst, src DashboardFailureByAgentResponse) DashboardFailureByAgentResponse {
			dst.TaskCount += src.TaskCount
			return dst
		},
	)
}
