// 本文件实现 issue 时间线（timeline）的读取：把某 issue 下的评论与活动日志合并成
// 一条按 (created_at, id) 排序的时间线返回；对超大 issue 各类独立封顶截断，通过
// X-Timeline-Truncated 响应头汇报截断、并尽力补齐被切断的评论回复线程；同时兼容旧的
// 游标分页包装形态。文件末尾提供「当前用户指派对象使用频次」聚合端点。
package handler

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TimelineEntry 是 issue 时间线里的一条记录——要么是一条活动日志，要么是一条评论。
type TimelineEntry struct {
	Type string `json:"type"` // "activity" 或 "comment"
	ID   string `json:"id"`

	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	CreatedAt string `json:"created_at"`

	// 仅活动日志拥有的字段
	Action  *string         `json:"action,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`

	// 仅评论拥有的字段
	Content     *string `json:"content,omitempty"`
	ParentID    *string `json:"parent_id,omitempty"`
	UpdatedAt   *string `json:"updated_at,omitempty"`
	Revision    int64   `json:"revision,omitempty"`
	CommentType *string `json:"comment_type,omitempty"`
	// 只由快捷动作（quick action）运行产生的评论才带这个字段，且伪造不了：通用评论端点
	// 没有接收它的请求字段。
	QuickActionID  *string              `json:"quick_action_id,omitempty"`
	Reactions      []ReactionResponse   `json:"reactions,omitempty"`
	Attachments    []AttachmentResponse `json:"attachments,omitempty"`
	ResolvedAt     *string              `json:"resolved_at,omitempty"`
	ResolvedByType *string              `json:"resolved_by_type,omitempty"`
	ResolvedByID   *string              `json:"resolved_by_id,omitempty"`
	SourceTaskID   *string              `json:"source_task_id,omitempty"`
	// Set only on a tombstone: a comment deleted while it still had replies.
	DeletedAt *string `json:"deleted_at,omitempty"`
}

// timelineHardCap 限制单个 issue 时间线载荷的行数。它是防御性安全网，不是 UX 分页窗口——
// 数据形态设计的来龙去脉见 comment.go 里 commentHardCap 的注释（#1929）。
const timelineHardCap = 2000

// timelineProbeLimit 比上限多读一行，用于区分「撞上限了」和「这个 issue 恰好只有
// timelineHardCap 行」。没有这行探针，正好卡在边界上的 issue 会被误报成截断，
// 白付一次祖先补齐（backfill）查询。
const timelineProbeLimit = timelineHardCap + 1

// 截断通过响应头而不是 body 字段来通告，因为这份不分页的响应是裸 JSON 数组
// （packages/core/api/schemas.ts 里 TimelineEntriesSchema = z.array(TimelineEntrySchema)），
// 没有地方放标志位。该头是追加式的：老客户端继续校验也不会坏。
//
// 值点名是哪几类被截断（"activity"、"comment" 或 "activity,comment"）——两个上限彼此
// 独立，实际几乎总是只有 activity 触顶。
//
// 刻意不提供配套的「从哪个窗口继续」头。早期版本发过，但 TimestampToString 是秒级
// RFC3339，真正的排序键是完整精度的 (created_at, id)，用它续读会在同一秒内跳行或重复行。
// 可续读的游标必须不透明且同时携带这两半；等真有消费方时再设计，而不是现在发个丢精度的
// 近似品。
//
// 导出是为了让 CORS 层能引用同一个标识符（server/cmd/server/router.go 的
// corsExposedHeaders）。自定义响应头若不显式暴露，浏览器 JS 看不到——这里改了名却没同步
// CORS 名单，会静默把信号关掉。
const HeaderTimelineTruncated = "X-Timeline-Truncated"

// truncatedKinds 渲染 X-Timeline-Truncated 头的取值；什么都没截断时返回 ""。
func truncatedKinds(comments, activities bool) string {
	switch {
	case comments && activities:
		return "activity,comment"
	case comments:
		return "comment"
	case activities:
		return "activity"
	default:
		return ""
	}
}

// takeNewest 把按 timelineProbeLimit 读回来的行裁到 timelineHardCap，并报告那行探针是否
// 证明还有更旧的行。rows 必须是升序的——最新 timelineHardCap 条在尾部。
func takeNewest[T any](rows []T) ([]T, bool) {
	if len(rows) <= timelineHardCap {
		return rows, false
	}
	return rows[len(rows)-timelineHardCap:], true
}

// timelinePaginatedResponse 复刻此前基于游标分页的 ListTimeline（#2128）产出的包装结构，
// 是给已安装的 Desktop 构建与 #2128~#1929 之间的旧 Web 包保留的向后兼容面：它们会带
// `?limit=`/`?before=`/`?after=`/`?around=` 并拿旧 TimelinePageSchema（entries + cursors）
// 解析响应。游标恒为 nil、has_more_after 恒为 false——新服务端一次性返回整条时间线。
// has_more_before 现在真实汇报（硬上限是否触顶），而不是写死的 false。
type timelinePaginatedResponse struct {
	Entries       []TimelineEntry `json:"entries"`
	NextCursor    *string         `json:"next_cursor"`
	PrevCursor    *string         `json:"prev_cursor"`
	HasMoreBefore bool            `json:"has_more_before"`
	HasMoreAfter  bool            `json:"has_more_after"`
	TargetIndex   *int            `json:"target_index,omitempty"`
}

// ListTimeline 返回某 issue 的完整时间线（评论 + 活动日志合并）。为了边界兼容（#1929）
// 同时存在两种响应形态：
//
//   - 不带分页参数 → 升序扁平 `TimelineEntry[]`。匹配旧 Desktop 契约（Multica.app
//     ≤ v0.2.25）与新客户端。
//   - 带 `limit` / `before` / `after` / `around` 任一 → 包装对象：降序 entries + null
//     游标 + has_more_after=false。匹配老 v0.2.26+ 构建用 TimelinePageSchema 解析的预期；
//     游标遍历已是空操作，客户端只会看到单个完整页。
//
// 两种形态携带同一组 entries——只是分页与排序不同。基于时间的分页被移除，因为它在页
// 边界上撕开回复线程，而且按观察到的数据规模（p99 约 30 条评论/issue）游标机制纯属开销。
//
// 硬上限触发时，每类各自独立裁到最新条目，X-Timeline-Truncated 头点名哪几类受影响。
// 被窗口切断的评论线程随后在受限的 context 预算内补齐；无法补完整的线程作为一整个单位
// 被丢弃（MUL-5492）。
func (h *Handler) ListTimeline(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	ctx := r.Context()

	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       timelineProbeLimit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list comments")
		return
	}
	activities, err := h.Queries.ListActivitiesForIssue(ctx, db.ListActivitiesForIssueParams{
		IssueID: issue.ID,
		Limit:   timelineProbeLimit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list activities")
		return
	}

	q := r.URL.Query()
	wantWrapped := q.Get("limit") != "" || q.Get("before") != "" ||
		q.Get("after") != "" || q.Get("around") != ""

	// 每类列表独立封顶、各自汇报自己的截断——刻意不把它们钳到同一个公共下限。
	//
	// 早期版本确实钳过，以保证返回窗口是连续、正确交错、不含「只有其中一类」区域的切片。
	// 那是方向错了的交易：评论是人手节奏（p99 约 30，历史最大约 1.1k），几乎永远到不了
	// 上限；活动日志是机器节奏，家常便饭就触顶——所以公共下限几乎总是被 ACTIVITY 的下限
	// 主导，把已经成功取回、渲染得好好的评论裁掉。为买一个装饰性属性删除真实内容，在一个
	// 有三十条评论的繁忙 issue 上纯亏。
	//
	// 不钳的代价只是较早段里活动日志密度降低——那是元数据，不是内容——而且是被上报而非
	// 隐藏的。同时它让评论集不会被随意切在某一行上，这正是下方 completeCommentThreads
	// 会修复受影响的线程的原因。
	comments, commentsTruncated := takeNewest(comments)
	activities, activitiesTruncated := takeNewest(activities)

	// 时间线客户端从本响应里的评论推算「已解决」条、作者列表与折叠计数——只补父节点会把
	// 半截线程伪装成完整。因此在共享的 context 预算内，把受影响根线程缺失的兄弟与后代全
	// 部取回；若无法证明线程完整，则整条线程一并丢弃。
	if commentsTruncated {
		var err error
		comments, err = h.completeCommentThreads(ctx, issue.ID, issue.WorkspaceID, comments)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to complete comment threads")
			return
		}
	}

	if kinds := truncatedKinds(commentsTruncated, activitiesTruncated); kinds != "" {
		w.Header().Set(HeaderTimelineTruncated, kinds)
	}

	if wantWrapped {
		entries := h.mergeTimeline(r, comments, activities, false)
		if entries == nil {
			entries = []TimelineEntry{}
		}
		resp := timelinePaginatedResponse{
			Entries:       entries,
			HasMoreBefore: commentsTruncated || activitiesTruncated,
		}
		// `around=<id>`：在降序切片里定位锚点，让旧客户端无需再发一次请求即可滚动高亮。
		if anchor := q.Get("around"); anchor != "" {
			for i, e := range entries {
				if e.ID == anchor {
					idx := i
					resp.TargetIndex = &idx
					break
				}
			}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	entries := h.mergeTimeline(r, comments, activities, true)
	if entries == nil {
		entries = []TimelineEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// mergeTimeline 合并评论与活动日志，按 (created_at, id) 排序返回。ascending=true 时旧的
// 在前（新的扁平数组契约）；否则新的在前（旧的包装契约）。
func (h *Handler) mergeTimeline(r *http.Request, comments []db.Comment, activities []db.ActivityLog, ascending bool) []TimelineEntry {
	out := make([]TimelineEntry, 0, len(comments)+len(activities))
	out = append(out, h.commentsToEntries(r, comments)...)
	for _, a := range activities {
		out = append(out, activityToEntry(a))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			if ascending {
				return out[i].CreatedAt < out[j].CreatedAt
			}
			return out[i].CreatedAt > out[j].CreatedAt
		}
		if ascending {
			return out[i].ID < out[j].ID
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// commentsToEntries 为给定评论分别用一次批量查询取 reactions 与 attachments，返回保持
// 顺序的富化 TimelineEntry 切片。
func (h *Handler) commentsToEntries(r *http.Request, comments []db.Comment) []TimelineEntry {
	if len(comments) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, len(comments))
	for i, c := range comments {
		ids[i] = c.ID
	}
	reactions := h.groupReactions(r, ids)
	attachments := h.groupAttachments(r, ids)

	out := make([]TimelineEntry, len(comments))
	for i, c := range comments {
		content := c.Content
		commentType := c.Type
		updatedAt := timestampToString(c.UpdatedAt)
		cid := uuidToString(c.ID)
		out[i] = TimelineEntry{
			Type:           "comment",
			ID:             cid,
			ActorType:      c.AuthorType,
			ActorID:        uuidToString(c.AuthorID),
			Content:        &content,
			CommentType:    &commentType,
			QuickActionID:  uuidToPtr(c.QuickActionID),
			ParentID:       uuidToPtr(c.ParentID),
			CreatedAt:      timestampToString(c.CreatedAt),
			UpdatedAt:      &updatedAt,
			Revision:       c.Revision,
			Reactions:      reactions[cid],
			Attachments:    attachments[cid],
			ResolvedAt:     timestampToPtr(c.ResolvedAt),
			ResolvedByType: textToPtr(c.ResolvedByType),
			ResolvedByID:   uuidToPtr(c.ResolvedByID),
			SourceTaskID:   uuidToPtr(c.SourceTaskID),
			DeletedAt:      timestampToPtr(c.DeletedAt),
		}
	}
	return out
}

// activityToEntry 把一条活动日志行映射成 TimelineEntry（动作与详情原样携带，评论相关
// 字段不出现）。
func activityToEntry(a db.ActivityLog) TimelineEntry {
	action := a.Action
	actorType := ""
	if a.ActorType.Valid {
		actorType = a.ActorType.String
	}
	return TimelineEntry{
		Type:      "activity",
		ID:        uuidToString(a.ID),
		ActorType: actorType,
		ActorID:   uuidToString(a.ActorID),
		Action:    &action,
		Details:   a.Details,
		CreatedAt: timestampToString(a.CreatedAt),
	}
}

// AssigneeFrequencyEntry 表示某个用户把任务指派给某个具体目标的频次。
type AssigneeFrequencyEntry struct {
	AssigneeType string `json:"assignee_type"`
	AssigneeID   string `json:"assignee_id"`
	Frequency    int64  `json:"frequency"`
}

// GetAssigneeFrequency 返回当前用户的指派对象使用频次，数据来自「变更指派对象的活动
// 日志」与「新建 issue 时的初始指派」两处。
func (h *Handler) GetAssigneeFrequency(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)

	// 把两个来源的数据汇进同一张频次表。
	freq := map[string]int64{} // 键形如 "type:id"

	// 来源 1：该用户发起的「指派对象变更」活动。
	activityCounts, err := h.Queries.CountAssigneeChangesByActor(r.Context(), db.CountAssigneeChangesByActorParams{
		WorkspaceID: parseUUID(workspaceID),
		ActorID:     parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get assignee frequency")
		return
	}
	for _, row := range activityCounts {
		aType, _ := row.AssigneeType.(string)
		aID, _ := row.AssigneeID.(string)
		if aType != "" && aID != "" {
			freq[aType+":"+aID] += row.Frequency
		}
	}

	// 来源 2：该用户创建且带指派对象的 issue。
	issueCounts, err := h.Queries.CountCreatedIssueAssignees(r.Context(), db.CountCreatedIssueAssigneesParams{
		WorkspaceID: parseUUID(workspaceID),
		CreatorID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get assignee frequency")
		return
	}
	for _, row := range issueCounts {
		if !row.AssigneeType.Valid || !row.AssigneeID.Valid {
			continue
		}
		key := row.AssigneeType.String + ":" + uuidToString(row.AssigneeID)
		freq[key] += row.Frequency
	}

	// 组装成排序后的响应。
	result := make([]AssigneeFrequencyEntry, 0, len(freq))
	for key, count := range freq {
		// 切开 "type:id"——type 只会是 "member" 或 "agent"（不含冒号）。
		var aType, aID string
		for i := 0; i < len(key); i++ {
			if key[i] == ':' {
				aType = key[:i]
				aID = key[i+1:]
				break
			}
		}
		result = append(result, AssigneeFrequencyEntry{
			AssigneeType: aType,
			AssigneeID:   aID,
			Frequency:    count,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Frequency > result[j].Frequency
	})

	writeJSON(w, http.StatusOK, result)
}
