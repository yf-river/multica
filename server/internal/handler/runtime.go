// 本文件是 Agent Runtime（运行时）的 HTTP 接口层，统一处理前端对「装着 Agent 的机器/
// 进程（runtime）」的增删改查与用量报表：
//   - 列表/详情/改名/删除、public/private 可见性切换、自定义显示名（MUL-4217）；
//   - 用量统计：按天、按 Agent、按小时三类聚合，均按查看者时区切桶；
//   - 三层鉴权：谁能读（requireRuntimeReadAccess）、谁能把 Agent 绑上去
//     （canUseRuntimeForAgent）、谁能改/删（canEditRuntime / canSetRuntimeVisibility）；
//   - 删除分轻量版（有活跃 Agent 直接 409）与确认版（UnbindAgentsAndDeleteRuntime，
//     在单事务里解绑 Agent、取消任务、暂停 Autopilot 后删行）。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// AgentRuntimeResponse 是 runtime 对外返回的 JSON 结构，覆盖列表与单个详情两种场景。
type AgentRuntimeResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	DaemonID    *string `json:"daemon_id"`
	Name        string  `json:"name"`
	// CustomName 是用户手动设置的显示名覆盖（MUL-4217）；为空表示该 runtime 仍在用
	// daemon 自动起的 Name。前端展示 CustomName ?? Name，改名输入框的初值也来自这个字段。
	CustomName   *string `json:"custom_name"`
	RuntimeMode  string  `json:"runtime_mode"`
	Provider     string  `json:"provider"`
	LaunchHeader string  `json:"launch_header"`
	Status       string  `json:"status"`
	DeviceInfo   string  `json:"device_info"`
	Metadata     any     `json:"metadata"`
	OwnerID      *string `json:"owner_id"`
	// Visibility 决定谁能把 Agent 绑到这台 runtime 上："private"（默认，仅 owner 可绑）
	// 或 "public"（工作区任意成员可绑）。规则见 migration 083 与 canUseRuntimeForAgent。
	Visibility string `json:"visibility"`
	// ProfileID 非空表示该 runtime 是某个自定义 runtime_profile（MUL-3284）的实例；
	// 内置 runtime 此项为空。
	ProfileID  *string `json:"profile_id"`
	LastSeenAt *string `json:"last_seen_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// runtimeToResponse 把数据库行 db.AgentRuntime 转成对外 JSON 结构。metadata 列存的是
// JSON 文本，这里尝试解析成通用结构；解析不出或为空就回退成空对象 {}，避免前端拿到 null。
func runtimeToResponse(rt db.AgentRuntime) AgentRuntimeResponse {
	var metadata any
	if rt.Metadata != nil {
		json.Unmarshal(rt.Metadata, &metadata)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}

	return AgentRuntimeResponse{
		ID:           uuidToString(rt.ID),
		WorkspaceID:  uuidToString(rt.WorkspaceID),
		DaemonID:     textToPtr(rt.DaemonID),
		Name:         rt.Name,
		CustomName:   textToPtr(rt.CustomName),
		RuntimeMode:  rt.RuntimeMode,
		Provider:     rt.Provider,
		LaunchHeader: agent.LaunchHeader(rt.Provider),
		Status:       rt.Status,
		DeviceInfo:   rt.DeviceInfo,
		Metadata:     metadata,
		OwnerID:      uuidToPtr(rt.OwnerID),
		Visibility:   rt.Visibility,
		ProfileID:    uuidToPtr(rt.ProfileID),
		LastSeenAt:   timestampToPtr(rt.LastSeenAt),
		CreatedAt:    timestampToString(rt.CreatedAt),
		UpdatedAt:    timestampToString(rt.UpdatedAt),
	}
}

// ---------------------------------------------------------------------------
// Runtime 用量报表
// ---------------------------------------------------------------------------

// RuntimeUsageResponse 是「按天聚合的 token 用量」一行 JSON，由 daemon 采集到的
// 每次任务用量记录汇总而来（只统计 daemon 执行的任务，不含用户在本地 CLI 直接用同一工具的消耗）。
type RuntimeUsageResponse struct {
	RuntimeID        string `json:"runtime_id"`
	Date             string `json:"date"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	// Cost 拆分：CostUSDTicks 是 provider 对这批明细行的真实计费（单位 1e-10 美元），
	// 后面的 Uncosted* 是 provider 没计价的 token 数。客户端把「已计价」和「未计价估算」
	// 相加，混合两类明细的统计窗口数字才完整。见 migration 213。
	CostUSDTicks             int64 `json:"cost_usd_ticks"`
	UncostedInputTokens      int64 `json:"uncosted_input_tokens"`
	UncostedOutputTokens     int64 `json:"uncosted_output_tokens"`
	UncostedCacheReadTokens  int64 `json:"uncosted_cache_read_tokens"`
	UncostedCacheWriteTokens int64 `json:"uncosted_cache_write_tokens"`
}

// GetRuntimeUsage 处理 GET /api/runtimes/:id/usage（按天）——先校验调用者对这台
// runtime 的读权限（无权限一律 404 打回，避免 runtime id 成为探测口），再按查看者
// 时区把 task_usage_hourly 里的小时桶归并成自然日返回。非法请求返回 500。
func (h *Handler) GetRuntimeUsage(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}

	// 所有 runtime 报表都按查看者的时区渲染。
	viewTZ := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 90, viewTZ)

	resp, err := h.listRuntimeUsage(r.Context(), rt.ID, viewTZ, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// listRuntimeUsage 从 task_usage_hourly 读取按小时桶存的用量，再按查看者时区 tz 把
// bucket_hour 投射成「本地自然日」，即返回结果的 Date 是 tz 下的日期。
func (h *Handler) listRuntimeUsage(ctx context.Context, runtimeID pgtype.UUID, tz string, since pgtype.Timestamptz) ([]RuntimeUsageResponse, error) {
	resolvedRuntimeID := uuidToString(runtimeID)
	rows, err := h.Queries.ListRuntimeUsage(ctx, db.ListRuntimeUsageParams{
		RuntimeID: runtimeID,
		Since:     since,
		Tz:        tz,
	})
	if err != nil {
		return nil, err
	}
	resp := make([]RuntimeUsageResponse, len(rows))
	for i, row := range rows {
		resp[i] = RuntimeUsageResponse{
			RuntimeID:                resolvedRuntimeID,
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
		}
	}
	return resp, nil
}

// GetRuntimeTaskActivity 处理 GET /api/runtimes/:id/task-activity——返回该 runtime
// 一天内 0..23 点各小时执行任务数的分布（用于看用户活跃时段），按查看者时区统计。
// 先做读权限校验，失败返回 404/500。
func (h *Handler) GetRuntimeTaskActivity(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}

	viewTZ := h.resolveViewingTZ(r)
	rows, err := h.Queries.GetRuntimeTaskHourlyActivity(r.Context(), db.GetRuntimeTaskHourlyActivityParams{
		RuntimeID: rt.ID,
		Tz:        viewTZ,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task activity")
		return
	}

	type HourlyActivity struct {
		Hour  int `json:"hour"`
		Count int `json:"count"`
	}

	resp := make([]HourlyActivity, len(rows))
	for i, row := range rows {
		resp[i] = HourlyActivity{Hour: int(row.Hour), Count: int(row.Count)}
	}

	writeJSON(w, http.StatusOK, resp)
}

// RuntimeUsageByAgentResponse 是「按 Agent 算成本」结果里的 (agent, provider, model) 一行。
// provider 和 model 必须留在返回里：费用是前端拿模型定价表在本地算的（刻意不存服务端，
// 改价不用回填历史数据），provider 用来区分不同服务商里重名的裸模型名；前端按 agent_id 分组求和。
type RuntimeUsageByAgentResponse struct {
	AgentID          string `json:"agent_id"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	// Cost 拆分：CostUSDTicks 是 provider 对这批明细行的真实计费（单位 1e-10 美元），
	// 后面的 Uncosted* 是 provider 没计价的 token 数。客户端把「已计价」和「未计价估算」
	// 相加，混合两类明细的统计窗口数字才完整。见 migration 213。
	CostUSDTicks             int64 `json:"cost_usd_ticks"`
	UncostedInputTokens      int64 `json:"uncosted_input_tokens"`
	UncostedOutputTokens     int64 `json:"uncosted_output_tokens"`
	UncostedCacheReadTokens  int64 `json:"uncosted_cache_read_tokens"`
	UncostedCacheWriteTokens int64 `json:"uncosted_cache_write_tokens"`
	TaskCount                int32 `json:"task_count"`
}

// GetRuntimeUsageByAgent 处理 GET /api/runtimes/:id/usage/by-agent——返回该 runtime
// 在截止窗口内每个 Agent 的 token 聚合，供详情页「按 Agent 算成本」标签渲染。
// 先做读权限校验；失败返回 404/500。
func (h *Handler) GetRuntimeUsageByAgent(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}

	// 这里不做按天切桶——时区只用来定截止点边界，让「最近 30 天」指的是查看者日历上的 30 天。
	viewTZ := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, viewTZ)

	rows, err := h.Queries.ListRuntimeUsageByAgent(r.Context(), db.ListRuntimeUsageByAgentParams{
		RuntimeID: rt.ID,
		Since:     since,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage by agent")
		return
	}

	resp := make([]RuntimeUsageByAgentResponse, len(rows))
	for i, row := range rows {
		resp[i] = RuntimeUsageByAgentResponse{
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

	writeJSON(w, http.StatusOK, resp)
}

// RuntimeUsageByHourResponse 是「按小时算用量」结果里的 (hour, model) 一行。
// 没有任务的小时 SQL 层就不返回，前端自行补 0 画出连续的 0..23 横轴；保留 model
// 同样是为了前端在本地算费用。
type RuntimeUsageByHourResponse struct {
	Hour             int    `json:"hour"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	// Cost 拆分：CostUSDTicks 是 provider 对这批明细行的真实计费（单位 1e-10 美元），
	// 后面的 Uncosted* 是 provider 没计价的 token 数。客户端把「已计价」和「未计价估算」
	// 相加，混合两类明细的统计窗口数字才完整。见 migration 213。
	CostUSDTicks             int64 `json:"cost_usd_ticks"`
	UncostedInputTokens      int64 `json:"uncosted_input_tokens"`
	UncostedOutputTokens     int64 `json:"uncosted_output_tokens"`
	UncostedCacheReadTokens  int64 `json:"uncosted_cache_read_tokens"`
	UncostedCacheWriteTokens int64 `json:"uncosted_cache_write_tokens"`
	TaskCount                int32 `json:"task_count"`
}

// GetRuntimeUsageByHour 处理 GET /api/runtimes/:id/usage/by-hour——返回该 runtime
// 在截止窗口内 0..23 点每个小时的 token 聚合，供「按小时」标签渲染。先做读权限校验，
// 失败返回 404/500。
//
// 小时维度和其它报表一样按查看者时区切桶——即 resolveViewingTZ 从请求 `?tz=` 参数
// 或已登录用户 user.timezone 字段解析出的时区。
func (h *Handler) GetRuntimeUsageByHour(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}

	viewTZ := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, viewTZ)

	rows, err := h.Queries.GetRuntimeUsageByHour(r.Context(), db.GetRuntimeUsageByHourParams{
		RuntimeID: rt.ID,
		Since:     since,
		Tz:        viewTZ,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage by hour")
		return
	}

	resp := make([]RuntimeUsageByHourResponse, len(rows))
	for i, row := range rows {
		resp[i] = RuntimeUsageByHourResponse{
			Hour:                     int(row.Hour),
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

	writeJSON(w, http.StatusOK, resp)
}

// sinceFromDays 是 parseSinceParamInTZ 去掉依赖后的纯函数核心（now 可注入以便测试）。
// 给定当前时刻、天数与 IANA 时区，返回「now 所在本地日历日往前数 days 天的本地 0 点」，
// 也就是把 now 换算到本地后，从今天零点往前退 days 天。
//
// 这个截止点会带出 N+1 个日历桶（含今天共 N+1 天），比直观的 "-(days-1)" 多一天，
// 是刻意留的余量，不是 off-by-one 的 bug：
//   - runtime 详情页 sliceWindow 用闭区间 `date >= today-days` 过滤，且它的环比差值要
//     回溯到 today-2*days，所以 today-days 这个桶必须存在，否则最早一根柱子/环比 KPI 静默丢数据。
//   - 工作区大盘在客户端再用 -(days-1) 二次过滤，后端多给的那一天在那边被裁掉，无害。
//
// 不要把它「收紧」成 -(days-1)：会弄坏 runtime 详情页。
func sinceFromDays(now time.Time, days int, loc *time.Location) time.Time {
	local := now.In(loc)
	startOfToday := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return startOfToday.AddDate(0, 0, -days)
}

// parseSinceParamInTZ 解析请求里的 "days" 查询参数，得到一个截止时刻 timestamptz。
// 截止点锚定到所给 IANA 时区的「N 天前本地 0 点」，于是 days=N 在该时区下正好包含
// N+1 个完整日历桶（今天不足一天的那桶 + 之前 N 个整天）。tzName 为空或无法解析时
// 回退 UTC——永远不报错，让 handler 保持简单。
func parseSinceParamInTZ(r *http.Request, defaultDays int, tzName string) pgtype.Timestamptz {
	return parseDaysCutoff(r, defaultDays, tzName, 0)
}

// parseExactSinceParamInTZ 与 parseSinceParamInTZ 相同，但不带那多出的一天余量：
// days=N 只给恰好 N 个日历桶（今天不足一天的那桶 + 之前 N-1 个整天），这正是工作区
// 大盘实际展示的窗口。
//
// N+1 截止点的存在是为了让「按日期分桶」的序列能比渲染范围再多取一个桶（runtime 详情页
// 的环比差值需要它），大盘在客户端用 -(days-1) 裁掉多余部分。而响应行里不带日期维度的
// 聚合没法这样裁剪，按 N+1 截止点出数会悄悄比旁边图表多覆盖一天——这种端点的行没有日期，
// 所以改用本变体。
func parseExactSinceParamInTZ(r *http.Request, defaultDays int, tzName string) pgtype.Timestamptz {
	return parseDaysCutoff(r, defaultDays, tzName, 1)
}

// parseDaysCutoff 是上面两个截止点解析器的公共实现。trimDays 把截止点往前拉：
// 传 0 保留 N+1 的余量，传 1 就把窗口收成恰好 N 个自然日。
// dayWindowNow 是所有 `days=N` 截止点读取的时钟源。
//
// 做成变量是为了让测试能钉住它。如果不钉住，夹具和必须包含它的截止点会在两个不同
// 时刻读墙上时钟——夹具在构造时读、截止点在处理请求时读，中间还隔着插入和汇聚。测试套件
// 若恰好在这段间隙跨过午夜，就会在某天造数据、却去要第二天的窗口，刚写的行被排除掉。
// 生产环境始终读真实时钟。
var dayWindowNow = time.Now

// parseDaysCutoff 的公共实现体：从 ?days= 取值（越界/非数字就用默认值），解析时区后
// 调用 sinceFromDays 算出截止点。
func parseDaysCutoff(
	r *http.Request,
	defaultDays int,
	tzName string,
	trimDays int,
) pgtype.Timestamptz {
	days := defaultDays
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	// 下限保护：到这里 days 已经 >= 1，把 1 天的窗口再 trim 1 会把截止点放到「今天 0 点」，
	// 依旧正确（“只看今天”），正好对应 days=1 的语义。
	return pgtype.Timestamptz{
		Time:  sinceFromDays(dayWindowNow(), days-trimDays, loc),
		Valid: true,
	}
}

// resolveViewingTZ 解析响应用哪个 IANA 时区渲染：优先 `?tz=` 查询参数，其次已登录用户
// 存的 user.timezone，兜底 "UTC"。非法值直接放过去而不是报错——时区只是显示层的东西。
//
// 浏览器端总是带 `?tz=`（前端用 useViewingTimezone 解析），所以下面查 GetUser 是一条
// 冷兜底路径，只有不带该参数的 API 客户端/旧版本构建会走到——不是热路径。不要把这个
// 查库的模式复制进没有 `?tz=` 客户端垫在前的 handler。
func (h *Handler) resolveViewingTZ(r *http.Request) string {
	if tz := strings.TrimSpace(r.URL.Query().Get("tz")); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil && loc != nil {
			return tz
		}
	}
	if userID := requestUserID(r); userID != "" {
		uid, err := util.ParseUUID(userID)
		if err != nil {
			slog.Warn("resolveViewingTZ: malformed X-User-ID, falling back to UTC",
				"path", r.URL.Path, "user_id", userID)
		}
		if err == nil {
			slog.Debug("resolveViewingTZ cold path: ?tz= missing, reading user.timezone",
				"path", r.URL.Path, "user_id", userID)
			if user, err := h.Queries.GetUser(r.Context(), uid); err == nil && user.Timezone.Valid {
				stored := strings.TrimSpace(user.Timezone.String)
				if stored != "" {
					if loc, err := time.LoadLocation(stored); err == nil && loc != nil {
						return stored
					}
				}
			}
		}
	}
	return "UTC"
}

// UpdateAgentRuntimeRequest 是 PATCH /api/runtimes/:id 接受的 JSON 请求体。
// 只列用户有权编辑的字段；其它 runtime 元数据（provider、daemon_id、status…）由 daemon
// 上报，在这里只读。
type UpdateAgentRuntimeRequest struct {
	// Visibility 把 runtime 在 "private"（默认，仅 owner 能把 Agent 绑上来）和
	// "public"（工作区任意成员都可以）之间切换。仅 runtime owner 可改，由
	// canSetRuntimeVisibility 把关——比本请求的其余字段更严（其余字段 admin 也能发）。
	Visibility *string `json:"visibility,omitempty"`
	// CustomName 设置或清除用户可见的显示名覆盖（MUL-4217）。传空串/纯空白表示清除
	// （回到 daemon 起的默认名）。仅 owner / 工作区 admin 可改。
	CustomName *string `json:"custom_name,omitempty"`
	// ApplyToMachine 与 CustomName 一起为 true 时，把名字应用到共享同一 daemon_id 的
	// 每一台 runtime（一台机器每个 provider 跑一个 runtime），而不是只改当前这台。
	// runtime 没有 daemon_id 时忽略此字段。
	ApplyToMachine bool `json:"apply_to_machine,omitempty"`
}

// maxRuntimeCustomNameLen 限制 runtime 自定义名的最大长度。默认名都很短
// （如 "Claude (host.local)"），100 字符既是充足余量，又不会让选择器行和机器头溢出。
const maxRuntimeCustomNameLen = 100

// UpdateAgentRuntime 处理 PATCH /api/runtimes/:id。目前可改的是 visibility 与自定义名，
// 请求结构是开放的，以后加字段（显示名、描述等）不用改路由。
// 先校验工作区成员身份；写权限由 canEditRuntime 把关，其中 visibility 变更还额外受
// owner-only 的 canSetRuntimeVisibility 限制（MUL-6126）。
func (h *Handler) UpdateAgentRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}

	rt, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}

	member, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found")
	if !ok {
		return
	}
	if !canEditRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "you can only edit your own runtimes")
		return
	}

	var req UpdateAgentRuntimeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// 任何落库前先把所有字段校验完，避免某个字段非法时留下一个只改了一半的 PATCH。
	var (
		newVisibility  string
		needVisibility bool
	)
	if req.Visibility != nil {
		v := *req.Visibility
		if v != "private" && v != "public" {
			writeError(w, http.StatusBadRequest, "visibility must be 'private' or 'public'")
			return
		}
		// 只有「真的变了」才需要 owner 门禁。admin 用 PATCH-as-PUT 的客户端改名时会原样
		// 回传没变的 visibility，他不是想共享别人的机器，所以这种无操作直接忽略而不是
		// 变成 403——与 UpdateAgent 对重复提交的 agent 权限采取同样的容忍度。
		if v != rt.Visibility {
			if !canSetRuntimeVisibility(member, rt) {
				writeError(w, http.StatusForbidden, "only the runtime owner can change its visibility")
				return
			}
			newVisibility = v
			needVisibility = true
		}
	}
	if req.CustomName != nil {
		if len([]rune(strings.TrimSpace(*req.CustomName))) > maxRuntimeCustomNameLen {
			writeError(w, http.StatusBadRequest, "custom name is too long")
			return
		}
	}

	changed := false

	// The no-dependencies fast path must lock and re-check before changing
	// visibility. A plain read followed by UPDATE would leave a narrow window
	// where another member could bind an agent to the still-public runtime.
	if needVisibility && rt.Visibility == "public" && newVisibility == "private" {
		updated, plan, didChange, err := h.makeRuntimePrivateIfNoWorkspaceDependencies(r.Context(), rt, member)
		if err != nil {
			if errors.Is(err, errRuntimeOwnerChanged) {
				writeError(w, http.StatusForbidden, "only the runtime owner can change its visibility")
				return
			}
			slog.Error("make runtime private without dependencies failed", "error", err, "runtime_id", runtimeID)
			writeError(w, http.StatusInternalServerError, "failed to check runtime dependencies")
			return
		}
		if plan.hasDependencies() {
			writeJSON(w, http.StatusConflict, h.runtimeWorkspaceAccessRevocationResponse(plan, "runtime_has_nonowner_dependents"))
			return
		}
		rt = updated
		needVisibility = false
		changed = didChange
	}

	if needVisibility {
		updated, err := h.Queries.UpdateAgentRuntimeVisibility(r.Context(), db.UpdateAgentRuntimeVisibilityParams{
			ID:         runtimeUUID,
			Visibility: newVisibility,
		})
		if err != nil {
			slog.Error("UpdateAgentRuntimeVisibility failed", "error", err, "runtime_id", runtimeID)
			writeError(w, http.StatusInternalServerError, "failed to update runtime")
			return
		}
		rt = updated
		changed = true
	}

	if req.CustomName != nil {
		// 空串/纯空白名 = 清除覆盖（写 NULL），让 runtime 回到 daemon 起的默认名。
		trimmed := strings.TrimSpace(*req.CustomName)
		customName := pgtype.Text{String: trimmed, Valid: trimmed != ""}

		if req.ApplyToMachine && rt.DaemonID.Valid {
			// 非 admin 只能给机器上自己名下的 runtime 改名；owner/admin 则可以改共享同一
			// daemon_id 的全部 runtime。owner 过滤条件为空（NULL）表示「这台机器上的所有
			// runtime」。
			var ownerFilter pgtype.UUID
			if !roleAllowed(member.Role, "owner", "admin") {
				ownerFilter = member.UserID
			}
			rows, err := h.Queries.UpdateAgentRuntimeCustomNameByDaemon(r.Context(), db.UpdateAgentRuntimeCustomNameByDaemonParams{
				CustomName:  customName,
				WorkspaceID: rt.WorkspaceID,
				DaemonID:    rt.DaemonID,
				OwnerID:     ownerFilter,
			})
			if err != nil {
				slog.Error("UpdateAgentRuntimeCustomNameByDaemon failed", "error", err, "runtime_id", runtimeID)
				writeError(w, http.StatusInternalServerError, "failed to update runtime")
				return
			}
			// 操作者必然拥有（或 admin 管理着）被 :id 指到的这台 runtime，所以它一定在
			// 更新行里——在返回前把它挑出来作为响应主体。
			for _, row := range rows {
				if uuidToString(row.ID) == uuidToString(runtimeUUID) {
					rt = row
					break
				}
			}
			changed = true
		} else {
			updated, err := h.Queries.UpdateAgentRuntimeCustomName(r.Context(), db.UpdateAgentRuntimeCustomNameParams{
				CustomName: customName,
				ID:         runtimeUUID,
			})
			if err != nil {
				slog.Error("UpdateAgentRuntimeCustomName failed", "error", err, "runtime_id", runtimeID)
				writeError(w, http.StatusInternalServerError, "failed to update runtime")
				return
			}
			rt = updated
			changed = true
		}
	}

	if changed {
		// 通知在线的客户端 runtime 元数据变了，让列表/详情页刷新——与 DeleteAgentRuntime 的
		// 做法一致。
		h.publish(protocol.EventDaemonRegister, uuidToString(rt.WorkspaceID), "member", uuidToString(member.UserID), map[string]any{
			"action": "update",
		})
	}

	writeJSON(w, http.StatusOK, runtimeToResponse(rt))
}

// canEditRuntime 判断某成员能否编辑/删除指定 runtime：工作区 owner/admin 一律可以；
// 普通成员只能动自己名下（owner_id 是自己）的 runtime。
func canEditRuntime(member db.Member, rt db.AgentRuntime) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	return rt.OwnerID.Valid && uuidToString(rt.OwnerID) == uuidToString(member.UserID)
}

// getAgentRuntime 按 id 读一行 agent_runtime，并把这次读归因到 source，用于给
// multica_agent_runtime_lookup_total 打标签（MUL-6884）。选 source 时用真正驱动这次读的
// 产品行为的 obsmetrics.RuntimeLookupSource* 常量，而不是本调用碰巧所在的文件——把轮询
// 循环算成普通 API 流量，正是这个指标想消除的混淆。
func (h *Handler) getAgentRuntime(ctx context.Context, source string, id pgtype.UUID) (db.AgentRuntime, error) {
	return h.runtimeLookup(source).Get(ctx, id)
}

// runtimeLookup 与上面同一个读取器，但不立刻执行——给那些把读取器交给共享的「是否存活」
// 辅助函数、由它来读行的 handler 用。
func (h *Handler) runtimeLookup(source string) service.RuntimeLookup {
	return service.RuntimeLookup{Queries: h.Queries, Metrics: h.Metrics, Source: source}
}

// runtimeWorkspaceAccessRevocationPlan is the owner-visible snapshot of work
// that a public-to-private transition must revoke. It intentionally excludes
// the runtime owner's agents. Hidden Builder agents owned by teammates also
// represent work borrowing the owner's machine and must lose access.
type runtimeWorkspaceAccessRevocationPlan struct {
	Agents []db.Agent
	Tasks  []db.AgentTaskQueue
}

var errRuntimeOwnerChanged = errors.New("runtime owner changed during request")

func writeRuntimeRevocationBusy(w http.ResponseWriter, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"code":  "runtime_access_revocation_busy",
		"error": "runtime work is changing; retry to review the current plan.",
	})
	return true
}

func (p runtimeWorkspaceAccessRevocationPlan) hasDependencies() bool {
	return len(p.Agents) > 0 || len(p.Tasks) > 0
}

// makeRuntimePrivateIfNoWorkspaceDependencies handles the non-disruptive
// public-to-private path. Binding writers take the same runtime lock and
// recheck visibility before inserting/updating their agent.
//
// A non-empty plan is deliberately returned without committing. The caller
// turns it into the same confirmation response used by the disruptive path;
// the confirmed endpoint will take a fresh locked snapshot before revoking.
func (h *Handler) makeRuntimePrivateIfNoWorkspaceDependencies(ctx context.Context, rt db.AgentRuntime, member db.Member) (db.AgentRuntime, runtimeWorkspaceAccessRevocationPlan, bool, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.AgentRuntime{}, runtimeWorkspaceAccessRevocationPlan{}, false, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	lockedRuntime, err := qtx.LockAgentRuntimeForAccessChange(ctx, rt.ID)
	if err != nil {
		return db.AgentRuntime{}, runtimeWorkspaceAccessRevocationPlan{}, false, fmt.Errorf("lock runtime: %w", err)
	}
	if !canSetRuntimeVisibility(member, lockedRuntime) {
		return db.AgentRuntime{}, runtimeWorkspaceAccessRevocationPlan{}, false, errRuntimeOwnerChanged
	}
	// Another request may have made the runtime private while this PATCH was
	// waiting for its lock. The requested end state is already satisfied.
	if lockedRuntime.Visibility != "public" {
		return lockedRuntime, runtimeWorkspaceAccessRevocationPlan{}, false, nil
	}

	agents, err := qtx.ListNonOwnerAgentsByRuntime(ctx, db.ListNonOwnerAgentsByRuntimeParams{
		RuntimeID:      lockedRuntime.ID,
		RuntimeOwnerID: lockedRuntime.OwnerID,
	})
	if err != nil {
		return db.AgentRuntime{}, runtimeWorkspaceAccessRevocationPlan{}, false, fmt.Errorf("list non-owner agents: %w", err)
	}
	tasks, err := qtx.ListNonOwnerActiveTasksByRuntime(ctx, db.ListNonOwnerActiveTasksByRuntimeParams{
		RuntimeID:      lockedRuntime.ID,
		RuntimeOwnerID: lockedRuntime.OwnerID,
	})
	if err != nil {
		return db.AgentRuntime{}, runtimeWorkspaceAccessRevocationPlan{}, false, fmt.Errorf("list non-owner tasks: %w", err)
	}
	plan := runtimeWorkspaceAccessRevocationPlan{Agents: agents, Tasks: tasks}
	if plan.hasDependencies() {
		return lockedRuntime, plan, false, nil
	}

	updated, err := qtx.UpdateAgentRuntimeVisibility(ctx, db.UpdateAgentRuntimeVisibilityParams{
		ID:         lockedRuntime.ID,
		Visibility: "private",
	})
	if err != nil {
		return db.AgentRuntime{}, runtimeWorkspaceAccessRevocationPlan{}, false, fmt.Errorf("update runtime visibility: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AgentRuntime{}, runtimeWorkspaceAccessRevocationPlan{}, false, fmt.Errorf("commit transaction: %w", err)
	}
	return updated, plan, true, nil
}

// runtimeWorkspaceAccessRevocationResponse is returned for both the initial
// confirmation requirement and a stale confirmation plan. Task IDs are not
// rendered by the UI, but carrying the precise set lets the confirmed request
// fail closed if a dispatcher adds, finishes, or claims work while the dialog
// is open.
func (h *Handler) runtimeWorkspaceAccessRevocationResponse(plan runtimeWorkspaceAccessRevocationPlan, code string) map[string]any {
	// Runtime ownership does not grant access to a teammate's private agent
	// configuration. Only expose the identity needed to review the impact.
	type affectedAgent struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	agents := make([]affectedAgent, len(plan.Agents))
	for i, agent := range plan.Agents {
		agents[i] = affectedAgent{ID: uuidToString(agent.ID), Name: agent.Name}
	}
	taskIDs := make([]string, len(plan.Tasks))
	for i, task := range plan.Tasks {
		taskIDs[i] = uuidToString(task.ID)
	}
	return map[string]any{
		"error":           "making this runtime private will unbind teammates' agents, cancel their active runs, and pause their active autopilots; review and confirm first.",
		"code":            code,
		"nonowner_agents": agents,
		"active_task_ids": taskIDs,
	}
}

// requireRuntimeReadAccess protects runtime data and machine-triggering
// capabilities. Governance access is deliberately separate: workspace owners
// and admins may list, rename, or delete another member's private runtime,
// but a private machine is readable or usable only by its owner. Returning
// 404 for that case prevents a known runtime ID from becoming an oracle.
//
// source 为 multica_agent_runtime_lookup_total（MUL-6884）标注驱动本次读的产品行为，
// 在这里比别处都关键：这一个门槛既要服务很少打开的用量页，也要服务好几个 500ms 一次的
// 浏览器轮询，把它们混在一起计数会掩盖这个指标想测的轮询量。
func (h *Handler) requireRuntimeReadAccess(w http.ResponseWriter, r *http.Request, source, runtimeID string) (db.AgentRuntime, db.Member, bool) {
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return db.AgentRuntime{}, db.Member{}, false
	}

	rt, err := h.getAgentRuntime(r.Context(), source, runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return db.AgentRuntime{}, db.Member{}, false
	}

	member, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found")
	if !ok || !canUseRuntimeForAgent(member, rt) {
		if ok {
			writeError(w, http.StatusNotFound, "runtime not found")
		}
		return db.AgentRuntime{}, db.Member{}, false
	}

	return rt, member, true
}

// runtimeLiveProfile returns the custom runtime profile that owns rt, if that
// profile still exists in the same workspace. A profile-backed instance whose
// profile is gone is an orphan and stays directly deletable (MUL-4158).
//
// The profile row itself — not just "one exists" — is what the caller needs:
// the refusal it writes names the profile, so the user can tell which shared
// definition they would be reaching for if they followed the old advice.
func (h *Handler) runtimeLiveProfile(ctx context.Context, rt db.AgentRuntime) (db.RuntimeProfile, bool, error) {
	if !rt.ProfileID.Valid {
		return db.RuntimeProfile{}, false, nil
	}
	profile, err := h.Queries.GetRuntimeProfileForWorkspace(ctx, db.GetRuntimeProfileForWorkspaceParams{
		ID:          rt.ProfileID,
		WorkspaceID: rt.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RuntimeProfile{}, false, nil
		}
		return db.RuntimeProfile{}, false, err
	}
	return profile, true, nil
}

// profileInstanceDeleteRefusal explains why this one runtime row cannot be
// deleted on its own, and — the part that matters — what the user should
// actually do instead.
//
// The previous wording said only "delete its runtime profile instead", which
// is actively harmful advice for the case that produces this error most often
// (GH #8456, #6671): a retired machine's leftover row inside a profile that
// other, healthy machines still use. Following it means reaching for a
// workspace-wide delete that takes those machines' runtimes with it, and that
// a bound agent will refuse anyway. So the refusal now leads with the outcome
// the user wants — an offline row is reclaimed automatically — and states the
// blast radius of the profile delete rather than recommending it.
//
// blockers are the non-archived user agents bound to rt, matching the predicate
// retention GC applies; known is false when that read failed. They decide two
// things. Whether the promise of automatic cleanup is one this server can keep
// at all — GC skips a runtime that still has a bound agent — and, when it is
// not, which of those blockers the user can actually do anything about. Mika is
// a user-kind agent that can be neither archived nor moved, so "reassign or
// archive them" is not a universal instruction here either.
func profileInstanceDeleteRefusal(rt db.AgentRuntime, profile db.RuntimeProfile, blockers profileInstanceBlockers) map[string]any {
	known := blockers.known
	ttlDays := service.OfflineRuntimeTTLDays()
	name := rt.Name
	if rt.CustomName.Valid && strings.TrimSpace(rt.CustomName.String) != "" {
		name = rt.CustomName.String
	}

	lead := fmt.Sprintf(
		"cannot delete %q on its own: it is registered from the custom runtime profile %q.",
		name, profile.DisplayName,
	)
	scope := "Deleting the profile instead would remove this runtime on every machine that registered it, not just this one."

	parts := []string{lead}
	switch {
	case rt.Status == "online":
		parts = append(parts, fmt.Sprintf(
			"It is still online, so its daemon would register it again. Stop that daemon first; Multica then removes the runtime automatically after %d days offline, once no agent is bound to it and nothing is still running on it.",
			ttlDays,
		))
	case !known:
		// Blocker set unavailable; promise only what holds regardless of it.
		parts = append(parts, fmt.Sprintf(
			"It is offline, and Multica removes offline runtimes automatically after %d days, once no agent is bound to them and nothing is still running on them.",
			ttlDays,
		))
	case len(blockers.agents) > 0 || blockers.undrainedTasks > 0:
		// GC needs BOTH gone. Naming only the agents would send a user who
		// clears them straight back here a week later, still waiting on a task
		// nothing told them about — a deferred run left behind when its agent
		// was rebound elsewhere is the ordinary way this happens.
		var holds []string
		if n := len(blockers.agents); n > 0 {
			holds = append(holds, fmt.Sprintf("%d agent(s) are still bound to it", n))
		}
		if n := blockers.undrainedTasks; n > 0 {
			holds = append(holds, fmt.Sprintf("%d unfinished task(s) belong to it or to agents bound to it", n))
		}
		parts = append(parts, fmt.Sprintf(
			"It is offline, but %s, which holds it in place; Multica removes the runtime automatically after %d days offline once that is cleared.",
			strings.Join(holds, " and "), ttlDays,
		))
		parts = append(parts, blockingAgentRemedies(blockingAgentClassesFromAgents(blockers.agents), blockingAgentScopeInstance)...)
		if blockers.undrainedTasks > 0 {
			parts = append(parts, "Let those tasks finish, or cancel them — one can be running on a different machine if its agent was moved there.")
		}
	default:
		parts = append(parts, fmt.Sprintf(
			"It is offline with no agents bound and nothing still running on it, so Multica removes it automatically after %d days offline — this row will be reclaimed without any action from you.",
			ttlDays,
		))
	}
	parts = append(parts, scope)

	msg := strings.Join(parts, " ")

	resp := map[string]any{
		"error": msg,
		"code":  "runtime_profile_instance_delete_unsupported",
		// Structured companions to the sentence above so a client can render
		// its own localized copy instead of echoing the English (see
		// writeErrorCode's rationale). The sentence stays the fallback.
		"profile_id":              uuidToString(profile.ID),
		"profile_name":            profile.DisplayName,
		"runtime_status":          rt.Status,
		"last_seen_at":            timestampToPtr(rt.LastSeenAt),
		"auto_cleanup_after_days": ttlDays,
	}
	if known {
		resp["active_agent_count"] = len(blockers.agents)
		resp["undrained_task_count"] = blockers.undrainedTasks
	}
	return resp
}

// profileInstanceBlockers is everything retention GC checks before it will
// reclaim an offline runtime, which is more than its candidate query asks for:
// the candidate scan wants no non-archived user agent and no runtime-owned task
// with completed_at NULL, and then gcRuntime re-checks the drain across every
// user agent bound to the runtime, archived ones included. Reporting any subset
// of that promises a cleanup the sweeper then skips.
type profileInstanceBlockers struct {
	agents         []db.Agent
	undrainedTasks int64
	known          bool
}

// profileInstanceRefusalBlockers reads what would stop retention GC from
// reclaiming this runtime. A read failure is not worth failing the request
// over: it only costs the refusal its most specific sentence, so report it as
// unknown and let the caller fall back to the cautious wording.
//
// Already bounded — a single runtime's bound agents, unlike a profile's, are
// capped by what one machine can host.
func (h *Handler) profileInstanceRefusalBlockers(ctx context.Context, runtimeID pgtype.UUID) profileInstanceBlockers {
	agents, err := h.Queries.ListActiveAgentsByRuntime(ctx, runtimeID)
	if err != nil {
		slog.Warn("profile instance refusal: active agent lookup failed",
			"runtime_id", uuidToString(runtimeID), "error", err)
		return profileInstanceBlockers{}
	}
	// The same drain gate gcRuntime applies, and deliberately not just the
	// candidate query's runtime-owned predicate: gcRuntime widens it to every
	// user agent bound to this runtime, archived included, and skips the delete
	// when any of them still owns a non-terminal task. That task can sit on a
	// different machine — an agent moved away leaves its deferred run behind —
	// so a check scoped to this runtime's own rows reports a row as reclaimable
	// that the sweeper will pass over every hour.
	agentIDs, err := h.Queries.ListUserAgentIDsByRuntime(ctx, runtimeID)
	if err != nil {
		slog.Warn("profile instance refusal: bound agent id lookup failed",
			"runtime_id", uuidToString(runtimeID), "error", err)
		return profileInstanceBlockers{}
	}
	tasks, err := h.Queries.CountUndrainedTasksByRuntimeOrAgent(ctx, db.CountUndrainedTasksByRuntimeOrAgentParams{
		RuntimeIds: []pgtype.UUID{runtimeID},
		AgentIds:   agentIDs,
	})
	if err != nil {
		slog.Warn("profile instance refusal: undrained task lookup failed",
			"runtime_id", uuidToString(runtimeID), "error", err)
		return profileInstanceBlockers{}
	}
	return profileInstanceBlockers{agents: agents, undrainedTasks: tasks, known: true}
}

// canUseRuntimeForAgent 判断工作区成员能否把新 Agent 绑到、或把已有 Agent 迁到这台 runtime。
// `public` 的机器工作区内任何人都能用；`private` 只有 owner 能用，且工作区 owner/admin
// 无权覆盖（MUL-6126）——私有 runtime 是某人自己的机器，在上面跑 Agent 花的是他的额度、
// 用的是他本地文件，这不是管理员能替别人做的决定。要不要共享是 owner 自己的事，所以
// canSetRuntimeVisibility 也仅限 owner——否则 admin 多走一步就拿到同一个覆盖权。
//
// 没有 owner 的 runtime 无论什么可见性都拒绝使用：认领任务需要 owner 来铸造 agent 的任务
// token，缺 owner 任务会被取消（MUL-3292），绑上去只会得到一批一跑就挂的 Agent。
//
// 客户端也执行同一条规则（packages/core/runtimes/access.ts 里的 isRuntimeUsableForUser），
// 保证 UI、API、CLI 三方一致。visibility 列见 migration 083。
func canUseRuntimeForAgent(member db.Member, rt db.AgentRuntime) bool {
	if !rt.OwnerID.Valid {
		return false
	}
	if rt.Visibility == "public" {
		return true
	}
	return uuidToString(rt.OwnerID) == uuidToString(member.UserID)
}

// canSetRuntimeVisibility 判断成员能否把该 runtime 在 `private` / `public` 之间切换。
// 仅限 owner，且刻意比 canEditRuntime 更窄：可见性是 owner 是否愿意把机器借给工作区的
// 表态，admin 若也能改就等于保留了 canUseRuntimeForAgent 刚拿掉的覆盖权。admin 保留
// canEditRuntime 只用于组织性操作（改名、删除）——这类操作不会把别人的凭据交出去。
func canSetRuntimeVisibility(member db.Member, rt db.AgentRuntime) bool {
	return rt.OwnerID.Valid && uuidToString(rt.OwnerID) == uuidToString(member.UserID)
}

type revokeRuntimeWorkspaceAccessRequest struct {
	ExpectedNonownerAgentIDs []string `json:"expected_nonowner_agent_ids"`
	ExpectedTaskIDs          []string `json:"expected_task_ids"`
}

// RevokeRuntimeWorkspaceAccess confirms the disruptive half of changing a
// public runtime back to private. The normal PATCH endpoint remains a direct
// update when no teammate is using the runtime; otherwise it returns a
// structured 409 and this endpoint applies the reviewed plan atomically.
//
// Server-side cancellation is authoritative even while the daemon is offline.
// A connected daemon observes its cancelled task through its existing polling
// path; a disconnected machine cannot receive a stop signal until it returns,
// but it cannot claim new work after the database transition commits.
func (h *Handler) RevokeRuntimeWorkspaceAccess(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}

	var req revokeRuntimeWorkspaceAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	expectedAgents, ok := parseExpectedUUIDIDs(req.ExpectedNonownerAgentIDs)
	if !ok {
		writeError(w, http.StatusBadRequest, "expected_nonowner_agent_ids must be a list of valid UUIDs")
		return
	}
	expectedTasks, ok := parseExpectedUUIDIDs(req.ExpectedTaskIDs)
	if !ok {
		writeError(w, http.StatusBadRequest, "expected_task_ids must be a list of valid UUIDs")
		return
	}

	rt, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	wsID := uuidToString(rt.WorkspaceID)
	member, ok := h.requireWorkspaceMember(w, r, wsID, "runtime not found")
	if !ok {
		return
	}
	if !canSetRuntimeVisibility(member, rt) {
		writeError(w, http.StatusForbidden, "only the runtime owner can change its visibility")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// Bindings take the same access-change lock; claim and delivery revalidate
	// authorization before executing work. Do not rely on FK checks for access.
	lockedRuntime, err := qtx.LockAgentRuntimeForAccessChange(r.Context(), rt.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock runtime")
		return
	}
	if !canSetRuntimeVisibility(member, lockedRuntime) {
		writeError(w, http.StatusForbidden, "only the runtime owner can change its visibility")
		return
	}
	if lockedRuntime.Visibility != "public" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "this runtime is already private; refresh before trying again.",
			"code":  "runtime_access_revocation_not_required",
		})
		return
	}

	currentAgents, err := qtx.ListNonOwnerAgentsByRuntimeForUpdate(r.Context(), db.ListNonOwnerAgentsByRuntimeForUpdateParams{
		RuntimeID:      lockedRuntime.ID,
		RuntimeOwnerID: lockedRuntime.OwnerID,
	})
	if err != nil {
		if writeRuntimeRevocationBusy(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock runtime agents")
		return
	}
	if _, err := qtx.LockRuntimeRevocationChatSessions(r.Context(), db.LockRuntimeRevocationChatSessionsParams{
		RuntimeID: lockedRuntime.ID, RuntimeOwnerID: lockedRuntime.OwnerID,
	}); err != nil {
		if !writeRuntimeRevocationBusy(w, err) {
			writeError(w, http.StatusInternalServerError, "failed to lock runtime chats")
		}
		return
	}
	currentTasks, err := qtx.ListNonOwnerActiveTasksByRuntimeForUpdate(r.Context(), db.ListNonOwnerActiveTasksByRuntimeForUpdateParams{
		RuntimeID:      lockedRuntime.ID,
		RuntimeOwnerID: lockedRuntime.OwnerID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock runtime tasks")
		return
	}
	plan := runtimeWorkspaceAccessRevocationPlan{Agents: currentAgents, Tasks: currentTasks}
	if !agentSetMatches(currentAgents, expectedAgents) || !taskSetMatches(currentTasks, expectedTasks) {
		body := h.runtimeWorkspaceAccessRevocationResponse(plan, "runtime_access_revocation_plan_changed")
		body["error"] = "the runtime access plan changed; please review and confirm again."
		writeJSON(w, http.StatusConflict, body)
		return
	}

	updated, err := qtx.UpdateAgentRuntimeVisibility(r.Context(), db.UpdateAgentRuntimeVisibilityParams{
		ID:         lockedRuntime.ID,
		Visibility: "private",
	})
	if err != nil {
		slog.Error("make runtime private failed", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to update runtime")
		return
	}
	cancelled, err := qtx.CancelNonOwnerTasksByRuntime(r.Context(), db.CancelNonOwnerTasksByRuntimeParams{
		RuntimeID:      lockedRuntime.ID,
		RuntimeOwnerID: lockedRuntime.OwnerID,
		FailureReason:  string(taskfailure.ReasonRuntimeAccessRevoked),
	})
	if err != nil {
		slog.Error("cancel non-owner runtime tasks failed", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to cancel runtime tasks")
		return
	}
	readyChats, err := service.PrepareRuntimeAccessCancellation(r.Context(), qtx, cancelled)
	if err != nil {
		slog.Error("prepare runtime cancellation chat settlement failed", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to settle runtime chats")
		return
	}
	unbound, err := qtx.UnbindNonOwnerAgentsFromRuntime(r.Context(), db.UnbindNonOwnerAgentsFromRuntimeParams{
		RuntimeID:      lockedRuntime.ID,
		RuntimeOwnerID: lockedRuntime.OwnerID,
	})
	if err != nil {
		slog.Error("unbind non-owner runtime agents failed", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to unbind runtime agents")
		return
	}
	unboundIDs := make([]pgtype.UUID, len(unbound))
	for i, agent := range unbound {
		unboundIDs[i] = agent.ID
	}
	paused, err := qtx.PauseAutopilotsByUnboundAgents(r.Context(), unboundIDs)
	if err != nil {
		slog.Error("pause autopilots for runtime privacy failed", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to pause affected autopilots")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit runtime privacy change")
		return
	}

	result := service.RuntimeTeardownResult{
		UnboundAgents:    unbound,
		CancelledTasks:   cancelled,
		PausedAutopilots: paused,
	}
	h.PublishRuntimeTeardown(r.Context(), result, wsID, "member", uuidToString(member.UserID), "update", true)
	for _, taskID := range readyChats {
		h.TaskService.FinalizeDeferredCancelledChat(r.Context(), taskID)
	}
	slog.Info("runtime made private and workspace access revoked",
		"runtime_id", runtimeID,
		"changed_by", uuidToString(member.UserID),
		"agents_unbound", len(unbound),
		"tasks_cancelled", len(cancelled),
		"autopilots_paused", len(paused),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"runtime":           runtimeToResponse(updated),
		"agents_unbound":    len(unbound),
		"tasks_cancelled":   len(cancelled),
		"autopilots_paused": len(paused),
	})
}

func (h *Handler) ListAgentRuntimes(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	var runtimes []db.AgentRuntime
	var err error

	if ownerFilter := r.URL.Query().Get("owner"); ownerFilter == "me" {
		runtimes, err = h.Queries.ListAgentRuntimesByOwner(r.Context(), db.ListAgentRuntimesByOwnerParams{
			WorkspaceID: parseUUID(workspaceID),
			OwnerID:     parseUUID(userID),
		})
	} else if roleAllowed(member.Role, "owner", "admin") {
		// 治理可见性延续 owner/admin 的既有契约：admin 能搜到某台私有 runtime 以便改名或
		// 删除，但逐台的行级读门槛（requireRuntimeReadAccess）仍然拒绝数据和机器访问。
		runtimes, err = h.Queries.ListAgentRuntimes(r.Context(), parseUUID(workspaceID))
	} else {
		runtimes, err = h.Queries.ListVisibleAgentRuntimes(r.Context(), db.ListVisibleAgentRuntimesParams{
			WorkspaceID: parseUUID(workspaceID),
			OwnerID:     parseUUID(userID),
		})
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return
	}

	resp := make([]AgentRuntimeResponse, len(runtimes))
	for i, rt := range runtimes {
		resp[i] = runtimeToResponse(rt)
	}

	writeJSON(w, http.StatusOK, resp)
}

// 这里说明 DeleteAgentRuntime 的两种删除形态（也是本段后续两个入口的分工）：
// 「严格版」DELETE /api/runtimes/:id：只要还有非 archived 的 Agent 绑在 runtime 上就
// 拒绝，返回 409 + 结构化 `runtime_has_active_agents`，并把挡路的 Agent 列表一并放进
// 响应体，前端无需再请求一次就能直接切到确认弹窗。「确认版」在
// POST /api/runtimes/:id/unbind-agents-and-delete（见下 UnbindAgentsAndDeleteRuntime），
// 在单个事务里执行多步拆解。
// PublishRuntimeTeardown 把一次已提交的拆解结果通过 WS 广播出去。调用方控制 actor
// 元信息以及是否附带一次 runtime 列表刷新，让自动 GC 能按工作区/批次去重这个刷新。
func (h *Handler) PublishRuntimeTeardown(ctx context.Context, res service.RuntimeTeardownResult, wsID, actorType, actorID, action string, publishRuntimeRefresh bool) {
	if h.TaskService != nil && len(res.CancelledTasks) > 0 {
		// 拆解会删掉 runtime 的系统 Agent，而系统 Agent 的聊天会话会随之消失，所以被取消的
		// 聊天任务的工作区从任务行上已无法反推——就是这个工作区。
		h.TaskService.BroadcastCancelledTasks(ctx, wsID, res.CancelledTasks)
	}
	for _, a := range res.UnboundAgents {
		// agent:status 是 Agent 更新路径已经在用的「这个 Agent 变了」通用广播；订阅方刷新
		// 行后能看到 runtime_bound=false。这里不发 agent:archived——没有归档任何东西。
		h.publish(protocol.EventAgentStatus, wsID, actorType, actorID, map[string]any{
			"agent": broadcastAgentResponse(h.agentToResponse(a)),
		})
	}
	for _, a := range res.PausedAutopilots {
		h.publish(protocol.EventAutopilotUpdated, wsID, actorType, actorID, map[string]any{
			"autopilot": autopilotToResponse(a, nil),
		})
	}
	if publishRuntimeRefresh {
		h.PublishRuntimeRefresh(wsID, actorType, actorID, action)
	}
}

// PublishRuntimeRefresh 通知在线的客户端重新拉取 runtime 状态。
func (h *Handler) PublishRuntimeRefresh(wsID, actorType, actorID, action string) {
	h.publish(protocol.EventDaemonRegister, wsID, actorType, actorID, map[string]any{"action": action})
}

func (h *Handler) publishRuntimeTeardown(ctx context.Context, res service.RuntimeTeardownResult, wsID, userID string) {
	h.PublishRuntimeTeardown(ctx, res, wsID, "member", userID, "delete", true)
}

// DeleteAgentRuntime 处理 DELETE /api/runtimes/:id（严格版）。
// 流程：校验成员身份与 canEditRuntime 写权限 → 拒绝删除「还活着的自定义 profile 实例」
// （只能删 profile 本体，MUL-3284）→ 事务内先对 runtime 行加锁再复查活跃 Agent 数，
// 仍有活跃 Agent 就 409 返回挡路清单 → 执行与确认版相同的 TeardownRuntime（解绑 Agent
// 及任务历史、取消未终态任务、只硬删系统 Agent）→ 删行并提交 → WS 广播。权限不足回
// 403，找不到回 404，任务未排空/工作区错配回 409。
func (h *Handler) DeleteAgentRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}

	rt, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}

	wsID := uuidToString(rt.WorkspaceID)
	member, ok := h.requireWorkspaceMember(w, r, wsID, "runtime not found")
	if !ok {
		return
	}

	// 权限：owner/admin 能删任意 runtime；普通成员只能删自己名下的。
	if !canEditRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "you can only delete your own runtimes")
		return
	}
	userID := uuidToString(member.UserID)

	profile, hasLiveProfile, err := h.runtimeLiveProfile(r.Context(), rt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check runtime profile")
		return
	}
	if hasLiveProfile {
		blockers := h.profileInstanceRefusalBlockers(r.Context(), rt.ID)
		writeJSON(w, http.StatusConflict, profileInstanceDeleteRefusal(rt, profile, blockers))
		return
	}
	if rt.ProfileID.Valid {
		slog.Warn("deleting orphaned profile-backed runtime instance",
			"runtime_id", uuidToString(rt.ID),
			"profile_id", uuidToString(rt.ProfileID),
			"workspace_id", wsID,
			"deleted_by", userID)
	}

	// 看看还有没有活跃（非 archived）Agent 绑在这台 runtime 上。把它们随 409 一起返回，
	// 让弹窗能直接基于这次响应画出级联方案——用户从过期列表页点删除时就省掉了一次往返。
	activeAgents, err := h.Queries.ListActiveAgentsByRuntime(r.Context(), rt.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check runtime dependencies")
		return
	}
	// 还有活跃 Agent 绑着就在任何拆解副作用发生前拒绝。用户通过
	// POST /runtimes/:id/unbind-agents-and-delete 确认方案，那边核实确认集后复用同一套拆解。
	if len(activeAgents) > 0 {
		writeJSON(w, http.StatusConflict, h.runtimeHasActiveAgentsResponse(activeAgents))
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete runtime")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 在 runtime 行的锁下面复核一遍。Agent/任务的插入会经它们的 runtime 外键拿 KEY SHARE
	// 锁，所以复核后不可能再冒出活跃 Agent、然后被这次拆解悄悄解绑。
	if _, err := qtx.LockAgentRuntime(r.Context(), rt.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock runtime")
		return
	}
	if _, err := qtx.ListUserAgentsByRuntimeForUpdate(r.Context(), rt.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock runtime dependencies")
		return
	}
	activeAgents, err = qtx.ListActiveAgentsByRuntimeForUpdate(r.Context(), rt.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check runtime dependencies")
		return
	}
	if len(activeAgents) > 0 {
		writeJSON(w, http.StatusConflict, h.runtimeHasActiveAgentsResponse(activeAgents))
		return
	}

	// 与确认版路径共用同一套拆解：解绑该 runtime 的 Agent 及其任务历史、取消仍在跑的，
	// 只删除系统 Agent。这里按定义没有活跃 Agent，但归档过的 Agent 和它们的历史仍可能
	// 绑在这台 runtime 上，也要一并处理。
	teardown, err := service.TeardownRuntime(r.Context(), qtx, rt.ID, service.RuntimeTeardownOptions{CancelNonTerminalTasks: true})
	if err != nil {
		if errors.Is(err, service.ErrRuntimeNotDrained) {
			slog.Error("runtime delete aborted: tasks not drained",
				"runtime_id", uuidToString(rt.ID), "error", err)
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "the runtime still has tasks in flight; retry in a moment.",
				"code":  "runtime_delete_not_drained",
			})
			return
		}
		if errors.Is(err, service.ErrRuntimeWorkspaceMismatch) {
			slog.Error("runtime delete aborted: agent workspace mismatch",
				"runtime_id", uuidToString(rt.ID), "error", err)
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "the runtime has an invalid cross-workspace agent binding.",
				"code":  "runtime_delete_workspace_mismatch",
			})
			return
		}
		slog.Error("runtime delete teardown failed", "runtime_id", uuidToString(rt.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete runtime")
		return
	}

	if err := qtx.DeleteAgentRuntime(r.Context(), rt.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete runtime")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete runtime")
		return
	}
	h.NotifyRuntimeGone(uuidToString(rt.ID))

	slog.Info("runtime deleted",
		"runtime_id", uuidToString(rt.ID),
		"deleted_by", userID,
		"agents_unbound", len(teardown.UnboundAgents),
		"tasks_cancelled", len(teardown.CancelledTasks),
		"autopilots_paused", len(teardown.PausedAutopilots),
	)

	h.publishRuntimeTeardown(r.Context(), teardown, wsID, userID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// runtimeHasActiveAgentsResponse 组装 DeleteAgentRuntime（轻量版拦截）与
// UnbindAgentsAndDeleteRuntime（确认后的方案变化）共用的结构化 409 响应体，形状为：
//
//	{
//	  "error": "...",
//	  "code":  "runtime_has_active_agents" | "runtime_delete_plan_changed",
//	  "active_agents": [AgentResponse, ...]
//	}
//
// 前端按 `code` 分支。发送哪个 code 由调用方决定；这里只统一 Agent 的序列化和错误文案。
func (h *Handler) runtimeHasActiveAgentsResponse(agents []db.Agent) map[string]any {
	resp := make([]AgentResponse, len(agents))
	for i, a := range agents {
		resp[i] = h.agentToResponse(a)
	}
	return map[string]any{
		"error":         "cannot delete runtime: it has active agents bound to it. Reassign them or confirm unbinding them first.",
		"code":          "runtime_has_active_agents",
		"active_agents": resp,
	}
}

// unbindAgentsAndDeleteRuntimeRequest 是「确认删除」端点的请求体结构。
// expected_active_agent_ids 是用户在弹窗里刚确认的 Agent 快照——服务端在事务内把它和
// 实时集合比对，弹窗打开到确认之间只要有任何变动就回 runtime_delete_plan_changed 拒绝。
// 这保证用户批准解绑的正是将要被解绑的那个 Agent 集合，即使有同事在同一窗口期新增或
// 归档了一个 Agent。
//
// 比对的集合刻意仍是「这台 runtime 上的活跃 Agent」：已装客户端发来的就是它，放宽集合
// 会让每个老客户端的请求都对不上而永远 409，runtime 从此删不掉。弹窗要展示的更多信息
// 应放在只读字段里，而不是塞进这个集合。
type unbindAgentsAndDeleteRuntimeRequest struct {
	ExpectedActiveAgentIDs []string `json:"expected_active_agent_ids"`
}

// UnbindAgentsAndDeleteRuntime 是「确认版删除」入口：解绑 runtime 上所有用户 Agent、
// 暂停受影响的 Autopilot、取消进行中的任务、摘掉任务历史、只硬删系统 Agent，最后删掉
// runtime 行——全部在同一个事务内完成，任何一步失败都不会留下删了一半的 runtime。
//
// 在 MUL-5559 之前，这里是先把 Agent 归档再硬删行，会把它们的每段对话一起毁掉；弹窗上
// 写的是「archive」，用户同意的和实际发生的不是一回事。现在用户的东西一律不销毁：Agent
// 以解绑状态存活，重新绑一台 runtime 就能再跑。
//
// 事务顺序跟随 workspace_revoke.go 里 revokeAndRemoveMember 的参考撤销流程，两条路径
// 因此共享同一套竞态安全属性：调度器不可能去认领一台即将消失的 runtime 上的任务；
// 提交后的发布事件也走同样的 task:cancelled → agent:status/autopilot:updated →
// daemon:register 扇形广播顺序。
//
// expected_active_agent_ids 校验是这个 UX 的承重墙：前端在弹窗打开时对 Agent 列表拍快照、
// 让用户勾选确认；弹窗还开着时若同事新增或归档了 Agent，本端点用 runtime_delete_plan_changed
// 加最新列表拒绝，用户永远不会确认一份过期的方案。
//
// 挂在 POST /api/runtimes/:id/unbind-agents-and-delete 上；对已安装客户端还兼容原始的
// /archive-agents-and-delete 路径。
func (h *Handler) UnbindAgentsAndDeleteRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}

	var req unbindAgentsAndDeleteRuntimeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	expected, ok := parseExpectedActiveAgentIDs(req.ExpectedActiveAgentIDs)
	if !ok {
		writeError(w, http.StatusBadRequest, "expected_active_agent_ids must be a list of valid UUIDs")
		return
	}

	rt, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}

	wsID := uuidToString(rt.WorkspaceID)
	member, ok := h.requireWorkspaceMember(w, r, wsID, "runtime not found")
	if !ok {
		return
	}
	if !canEditRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "you can only delete your own runtimes")
		return
	}
	userID := uuidToString(member.UserID)

	profile, hasLiveProfile, err := h.runtimeLiveProfile(r.Context(), rt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check runtime profile")
		return
	}
	if hasLiveProfile {
		blockers := h.profileInstanceRefusalBlockers(r.Context(), rt.ID)
		writeJSON(w, http.StatusConflict, profileInstanceDeleteRefusal(rt, profile, blockers))
		return
	}
	if rt.ProfileID.Valid {
		slog.Warn("deleting orphaned profile-backed runtime instance via cascade",
			"runtime_id", uuidToString(rt.ID),
			"profile_id", uuidToString(rt.ProfileID),
			"workspace_id", wsID,
			"deleted_by", userID)
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 先锁 runtime 行。PostgreSQL 对 agent.runtime_id 的外键校验需要拿父 runtime 行的
	// FOR KEY SHARE 锁，它与 FOR UPDATE 互斥——于是任何想给这台 runtime 新增/迁入 Agent
	// 的并发 INSERT/UPDATE 都会一直阻塞到我们的事务结束。这是兜底锁：防止在我们的快照和
	// 解绑之间冒出新的活跃 Agent。
	if _, err := qtx.LockAgentRuntime(r.Context(), rt.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock runtime")
		return
	}
	if _, err := qtx.ListUserAgentsByRuntimeForUpdate(r.Context(), rt.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock runtime dependencies")
		return
	}

	// 事务内用 FOR UPDATE 逐行重列活跃 Agent（对某一行并发的归档/迁移也会阻塞到我们
	// 提交为止）。在这里和 expected 集合比对，就封死了「弹窗打开 → 用户确认」这段竞态：
	// 即使弹窗开着期间有同事在该 runtime 上新建/归档了 Agent，用户批准的也恰好是服务端
	// 即将解绑的那批。
	currentActive, err := qtx.ListActiveAgentsByRuntimeForUpdate(r.Context(), rt.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enumerate active agents")
		return
	}
	if !activeAgentSetMatches(currentActive, expected) {
		// 用最新快照拒绝，前端可以据此重绘弹窗并强制用户重新确认。复用共享的响应组装
		// 函数，只把 code 换成「方案有变」信号，让弹窗能区分「你从过期页面打开的」和
		// 「你刚确认的方案在眼皮底下变了」两种情形。
		body := h.runtimeHasActiveAgentsResponse(currentActive)
		body["code"] = "runtime_delete_plan_changed"
		body["error"] = "the active agent set changed; please review and confirm again."
		writeJSON(w, http.StatusConflict, body)
		return
	}

	// 单一拆解，与轻量 DELETE 路径共用：解绑每个用户 Agent（活跃的与归档的）及其任务
	// 历史，取消正在运行/排队的任务，只硬删系统 Agent。用户配置的东西一样都没销毁——
	// Agent 只是需要一台新的 runtime。
	teardown, err := service.TeardownRuntime(r.Context(), qtx, rt.ID, service.RuntimeTeardownOptions{CancelNonTerminalTasks: true})
	if err != nil {
		if errors.Is(err, service.ErrRuntimeNotDrained) {
			slog.Error("runtime delete aborted: tasks not drained",
				"runtime_id", uuidToString(rt.ID), "error", err)
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "the runtime still has tasks in flight; retry in a moment.",
				"code":  "runtime_delete_not_drained",
			})
			return
		}
		slog.Error("runtime delete teardown failed", "runtime_id", uuidToString(rt.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to unbind agents")
		return
	}

	// 最后删掉 runtime 行本身。
	if err := qtx.DeleteAgentRuntime(r.Context(), rt.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete runtime")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}
	h.NotifyRuntimeGone(uuidToString(rt.ID))

	h.publishRuntimeTeardown(r.Context(), teardown, wsID, userID)

	slog.Info("runtime deleted, agents unbound",
		"runtime_id", uuidToString(rt.ID),
		"deleted_by", userID,
		"agents_unbound", len(teardown.UnboundAgents),
		"tasks_cancelled", len(teardown.CancelledTasks),
		"autopilots_paused", len(teardown.PausedAutopilots),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"agents_unbound":    len(teardown.UnboundAgents),
		"tasks_cancelled":   len(teardown.CancelledTasks),
		"autopilots_paused": len(teardown.PausedAutopilots),
		// agents_unbound 的过期镜像：按 archive-and-delete 契约构建的已安装客户端读这个
		// key。数量对应同一批 Agent；它们不再被归档了。
		"agents_archived": len(teardown.UnboundAgents),
	})
}

// parseExpectedActiveAgentIDs 校验级联端点收到的 expected_active_agent_ids 列表。
// nil / 空是允许的（空集也是合法方案："我确认没有活跃 Agent"——级联随后直接删 runtime、
// 不触发解绑）。只要有一个 UUID 格式非法就返回 ok=false，让 handler 回 400，而不是悄悄
// 去匹配一个不同的集合。
func parseExpectedActiveAgentIDs(raw []string) (map[string]struct{}, bool) {
	return parseExpectedUUIDIDs(raw)
}

// parseExpectedUUIDIDs validates a confirmation snapshot sent by a client.
// Duplicates intentionally collapse into one set member: the server compares
// the effect set, not its transport ordering.
func parseExpectedUUIDIDs(raw []string) (map[string]struct{}, bool) {
	out := make(map[string]struct{}, len(raw))
	for _, s := range raw {
		u, err := util.ParseUUID(s)
		if err != nil || !u.Valid {
			return nil, false
		}
		out[uuidToString(u)] = struct{}{}
	}
	return out, true
}

// activeAgentSetMatches 判断 runtime 上实时的活跃 Agent 集合是否等于前端确认过的快照。
// 不比较顺序——前端可能按任意顺序渲染；判断「方案变没变」看的是数量 + 成员关系。
func activeAgentSetMatches(current []db.Agent, expected map[string]struct{}) bool {
	return agentSetMatches(current, expected)
}

func agentSetMatches(current []db.Agent, expected map[string]struct{}) bool {
	if len(current) != len(expected) {
		return false
	}
	for _, a := range current {
		if _, ok := expected[uuidToString(a.ID)]; !ok {
			return false
		}
	}
	return true
}

func taskSetMatches(current []db.AgentTaskQueue, expected map[string]struct{}) bool {
	if len(current) != len(expected) {
		return false
	}
	for _, task := range current {
		if _, ok := expected[uuidToString(task.ID)]; !ok {
			return false
		}
	}
	return true
}
