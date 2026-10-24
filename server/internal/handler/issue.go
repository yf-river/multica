// 职责定位：本文件是 issue（事项）HTTP 处理层的主入口，集中实现 issue 的创建、查询
// （列表/分组/搜索/子任务）、详情、更新、删除以及批量操作。issue 与 comment、子任务、
// 附件、标签、状态目录等联动，边界是「一个 issue 及它直接带的东西」。

// 关键约定：状态字段已从固定的 enum CHECK 升级为「工作区内自定义状态目录」（见
// issuestatus），因此每个写路径都必须先把用户提交的状态字符串解析成目录里真正存在的
// canonical key（大小写/空白不敏感），并防止「解析通过后、写入前状态被归档」的竞态；
// 读路径则需要把自定义状态归类回内置的行为类别（status_category）。

// 内部组织：先是响应/请求类型与共用的状态解析辅助，然后是 SearchIssues / ListIssues /
// ListGroupedIssues / GetIssue / 子任务端点，接着是 CreateIssue / UpdateIssue /
// DeleteIssue 等写操作，最后是批量更新/删除。路由注册在 handler.go。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/channelmedia"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// IssueResponse 是单个 issue 的 JSON 响应体。列表/详情/子任务/搜索等读端点共用此结构，
// 保证客户端缓存与合并时字段形态一致。不带 Labels 的端点省略该字段，客户端据此保留旧数据。
type IssueResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Number      int32   `json:"number"`
	Identifier  string  `json:"identifier"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	// StatusCategory 表示该 issue 实际遵循哪种「平台行为类别」：7 个内置状态时与 Status
	// 相同，自定义状态时等于它继承的那个类别。端点若没有解析状态目录则该字段缺省，客户端
	// 应回退到 Status 判断，不能把空字符串当成「无类别」。 (MUL-6243)
	StatusCategory string `json:"status_category,omitempty"`
	// StatusName 是自定义状态给人看的展示名，紧挨着 key 返回。因为从非拉丁语名字派生出的
	// key（如 in_review_2）对人是不可读的，AI/客户端只拿到 status key 时无法知道人类给这个
	// 状态起了什么名字。
	//
	// 与 StatusCategory 不同，本字段总是返回：空字符串在这里有语义——「这是内置状态，
	// 请按 key 本地化」，而不是 status_category 缺省时那种「端点没解析」的意思。字段不用
	// omitempty 还让测试 TestIssueToMap_KeysMatchIssueResponse 总能见到它，防止 drift 测试
	// 在 payload 实际已漂移时仍然绿灯。 (MUL-6749)
	StatusName    string  `json:"status_name"`
	Priority      string  `json:"priority"`
	AssigneeType  *string `json:"assignee_type"`
	AssigneeID    *string `json:"assignee_id"`
	CreatorType   string  `json:"creator_type"`
	CreatorID     string  `json:"creator_id"`
	ParentIssueID *string `json:"parent_issue_id"`
	ProjectID     *string `json:"project_id"`
	Position      float64 `json:"position"`
	// Stage 把同一个父 issue 下的子任务分成有序的关卡组（null = 不分组）。某关关闭后如何
	// 触发「子任务完成 → 唤醒父任务」，见 issue_child_done.go。
	Stage           *int32  `json:"stage"`
	StartDate       *string `json:"start_date"`
	DueDate         *string `json:"due_date"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	WorkStartedAt   *string `json:"work_started_at,omitempty"`
	WorkCompletedAt *string `json:"work_completed_at,omitempty"`
	Revision        int64   `json:"revision"`
	// LastActivityAt 是最近一次有语义的 issue 动态时间。运营侧的历史回填还没跑完之前保持可空。
	LastActivityAt *string `json:"last_activity_at"`
	// Metadata 是每个 issue 的键值对元数据（见 issue_metadata.go）。总是返回（未设置时为
	// 空对象），前端可直接 `issue.metadata[key]` 而不必判空父字段。
	Metadata map[string]any `json:"metadata"`
	// Properties 是以属性定义 UUID 为 key 的自定义属性值集合（见 property.go）。同样总是返回。
	Properties  map[string]any          `json:"properties"`
	Reactions   []IssueReactionResponse `json:"reactions,omitempty"`
	Attachments []AttachmentResponse    `json:"attachments,omitempty"`
	// Labels 由列表/详情端点批量挂载，让客户端渲染标签 chip 时不必对每行发一次请求。
	// 指针 + omitempty：不加载标签的路径（如 UpdateIssue、批量更新、issue:updated 的 WS
	// 广播）完全不返回 labels 字段——客户端做本地合并时就会保留缓存里已有的标签。nil 指针 =
	// 「字段不存在，别动」；非 nil（含空切片）= 「这是权威的标签列表」。
	Labels *[]LabelResponse `json:"labels,omitempty"`
	// SourceContext 只在详情里返回。列表/看板/搜索/子任务响应有意省略这块可能很大的不可变快照。
	SourceContext *sourceContextDetailResponse `json:"source_context,omitempty"`
}

// validIssuePriorities 与 issue 表上的 CHECK 约束保持一致。写路径先在这里预校验，
// 让调用方拿到带合法取值列表的 400，而不是让数据库 CHECK 违约以 500 的形式冒上来。
var validIssuePriorities = []string{"important_urgent", "important_not_urgent", "not_important_urgent", "not_important_not_urgent"}

// validIssueStatuses is the 7 BUILT-IN status keys. Since MUL-6243 it is no
// longer the set of writable statuses — write paths validate against the
// workspace's catalog via validateIssueStatusKey — and it survives only for the
// issue-table grouping/filtering paths, which key their group descriptors and
// compound cells off a fixed status list.
//
// KNOWN LIMITATION: a custom status is therefore not yet selectable as an
// issue-table group or filter value. That is a self-contained follow-up (the
// table's group descriptors and compound cell keys need to become catalog
// driven); it is scoped out here so this change cannot alter the table view for
// workspaces that have no custom statuses.
var validIssueStatuses = issuestatus.Canonical()

// resolveIssueStatusKey 拿用户提交的状态字符串去比对工作区的状态目录，返回要真正入库的
// CANONICAL key。它是迁移 337 删掉 enum CHECK 之后在应用层补的校验，所以每个写路径都必须
// 经过这里——漏掉一个入口，未解析的 key 就会进到列里。
//
// 返回解析后的 key 而不是简单布尔值是有讲究的：解析对大小写和空白不敏感，
// `"  HUMAN_REVIEW "` 和 `"human_review"` 都能通过。若把调用方原始字符串写回库，就会存进
// 一个违反列格式约束的值，把刚被 API 接受的输入变成 500。调用方必须持久化这里返回的 key。
func (h *Handler) resolveIssueStatusKey(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, status string) (string, bool) {
	key, _, ok := h.resolveIssueStatusKeyKind(w, r, workspaceID, status)
	return key, ok
}

// resolveIssueStatusKeyKind 在 resolveIssueStatusKey 之外再返回「目标是不是自定义状态」。
// 调用方据此决定这次写操作是否要拿共享目录锁（见 runWithIssueStatusGuard）。
func (h *Handler) resolveIssueStatusKeyKind(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, status string) (string, bool, bool) {
	entry, err := issuestatus.Resolve(r.Context(), h.Queries, workspaceID, status)
	if err != nil {
		if errors.Is(err, issuestatus.ErrUnknownStatus) {
			// 提示里列「名字+标签」而不是裸 key：派生出来的 key 看不出状态含义，只列
			// `in_review_2` 的话调用方根本没法找到别人让他用的到底是哪一个。 (MUL-6749)
			allowed, listErr := issuestatus.ActiveKeyLabels(r.Context(), h.Queries, workspaceID)
			if listErr != nil || len(allowed) == 0 {
				allowed = issuestatus.Canonical()
			}
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"invalid status %q; valid values: %s", status, strings.Join(allowed, ", ")))
			return "", false, false
		}
		slog.Warn("resolve issue status failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to validate status")
		return "", false, false
	}
	return entry.Key, !entry.IsSystem, true
}

// errIssueStatusArchivedRace 表示目标自定义状态在「请求预检」和「真正写入」之间被归档了。
// 调用方把它映射成 409：请求到达时是合法的，去刷新目录后重试即可。
var errIssueStatusArchivedRace = errors.New("issue status was archived while the write was in flight")

// assertIssueStatusStillActive 是「归档竞态防护」的写半边：拿共享目录锁，并在调用方的事务
// 内部把状态再解析一遍。
//
// 二次解析才是真正关掉竞态的那一步。handler 会先在任何事务之前解析一次状态，好让参数错误
// 干净地返回 400——但归档操作可能在「预检」与「写入」之间提交。在锁内再查一遍，就能保证
// 行写入那一刻状态确实还活着。ArchiveIssueStatus 在退役状态时持有排他锁，所以两种先后顺序
// 都被覆盖：
//
//   - 先归档：归档提交后，这里的二次解析失败、写入被拒，归档状态上不会再落新任务；
//   - 先写入：归档会阻塞到本事务提交，然后把这个状态退役，而已存在的分配不受影响。
//
// 内置状态直接是空操作：它不可能被归档（由 issue_status_system_not_archivable 保证），
// 所以常见路径不拿锁、零开销。 (MUL-6243)
func assertIssueStatusStillActive(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, statusKey string) error {
	if statusKey == "" || issuestatus.IsBuiltIn(statusKey) {
		return nil
	}
	// 到处都约定「先拿目录锁、后拿行锁」，两条写路径才不会互相死锁。
	if err := qtx.LockIssueStatusCatalogShared(ctx, workspaceID); err != nil {
		return err
	}
	if _, err := issuestatus.Resolve(ctx, qtx, workspaceID, statusKey); err != nil {
		if errors.Is(err, issuestatus.ErrUnknownStatus) {
			return errIssueStatusArchivedRace
		}
		return err
	}
	return nil
}

// runWithIssueStatusGuard 让落在自定义状态上的 issue 写在事务里完成——事务内部会在共享
// 目录锁下重新校验状态仍可用（见 assertIssueStatusStillActive）。目标是内置状态则完全跳过
// 事务，走普通路径。
func (h *Handler) runWithIssueStatusGuard(ctx context.Context, workspaceID pgtype.UUID, statusKey string, fn func(q *db.Queries) error) error {
	if statusKey == "" || issuestatus.IsBuiltIn(statusKey) {
		return fn(h.Queries)
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	if err := assertIssueStatusStillActive(ctx, qtx, workspaceID, statusKey); err != nil {
		return err
	}
	if err := fn(qtx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// writeIssueStatusRaceError 把 errIssueStatusArchivedRace 渲染成 409 响应，并返回是否由它
// 处理了这个错误（供调用方决定要不要继续走通用错误分支）。
func writeIssueStatusRaceError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, errIssueStatusArchivedRace) {
		writeError(w, http.StatusConflict,
			"the target status was archived while this request was in flight; reload the status list and retry")
		return true
	}
	return false
}

// validateIssueEnum 校验枚举字段：value 必须在 allowed 白名单里，否则写 400 并在报错里
// 列出合法值。用于让客户端拿到友好错误而不是数据库违约。
func validateIssueEnum(w http.ResponseWriter, field, value string, allowed []string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s %q; valid values: %s", field, value, strings.Join(allowed, ", ")))
	return false
}

// fillStatusCategories 给一批响应补上 status_category：只处理状态是「自定义状态」的响应，
// 内置 key 的类别就等于它自己（由下面的纯构建函数填好），因此这里只负责让「客户端会按
// 类别缓存/分栏」的每个 payload 上该字段都权威。
//
// 整批共用同一个 Resolver：内置状态不花任何查询，一整页都是自定义状态时也只需读一次目录，
// 而不是每行读一次。Resolver 会把已归档状态也算进去——停在已归档状态上的 issue 仍然该归到
// 它所属类别那一栏。 (MUL-6243)
func (h *Handler) fillStatusCategories(ctx context.Context, wsID pgtype.UUID, resps []IssueResponse) {
	fill := h.newStatusCategoryFiller(ctx, wsID)
	for i := range resps {
		fill(&resps[i])
	}
}

// newStatusCategoryFiller 返回一个请求级的填充器，背后只共享一个 Resolver。一个请求构建的
// 每个响应都复用同一个填充器：Resolver 最多读一次目录，这样一整页自定义状态行只花一次查询，
// 而不是每行一次。每行都新建填充器就会把本函数要消除的 N+1 又请回来。 (MUL-6243)
func (h *Handler) newStatusCategoryFiller(ctx context.Context, wsID pgtype.UUID) func(*IssueResponse) {
	resolver := issuestatus.NewResolver(wsID)
	return func(resp *IssueResponse) {
		if resp == nil || resp.StatusCategory != "" {
			return
		}
		resp.StatusCategory = resolver.Effective(ctx, h.Queries, resp.Status)
		// 同一个 Resolver、同一次目录读取，所以展示名可以免费顺带取到；内置状态返回 ""
		// 并保持缺省。 (MUL-6749)
		resp.StatusName = resolver.Name(ctx, h.Queries, resp.Status)
	}
}

// fillStatusCategory 是单响应的形式，只给恰好构建一个响应的端点用；凡是循环的都必须用
// newStatusCategoryFiller。
func (h *Handler) fillStatusCategory(ctx context.Context, wsID pgtype.UUID, resp *IssueResponse) {
	h.newStatusCategoryFiller(ctx, wsID)(resp)
}

// issueToResponse 把数据库行 db.Issue 转成对外 JSON 响应，并拼出人类可读的
// Identifier（前缀-编号，如 MUL-123）。
func issueToResponse(i db.Issue, issuePrefix string) IssueResponse {
	identifier := issuePrefix + "-" + strconv.Itoa(int(i.Number))
	// 内置状态自己就是自己的类别，因此这里零目录查询，每个响应都会带上；自定义状态先留空，
	// 由会解析目录的端点（如子任务端点的 Resolver）补上——消费方按同样的规则回退。 (MUL-6243)
	statusCategory := ""
	if issuestatus.IsBuiltIn(i.Status) {
		statusCategory = i.Status
	}
	return IssueResponse{
		ID:              uuidToString(i.ID),
		WorkspaceID:     uuidToString(i.WorkspaceID),
		Number:          i.Number,
		Identifier:      identifier,
		Title:           i.Title,
		Description:     textToPtr(i.Description),
		Status:          i.Status,
		StatusCategory:  statusCategory,
		Priority:        i.Priority,
		AssigneeType:    textToPtr(i.AssigneeType),
		AssigneeID:      uuidToPtr(i.AssigneeID),
		CreatorType:     i.CreatorType,
		CreatorID:       uuidToString(i.CreatorID),
		ParentIssueID:   uuidToPtr(i.ParentIssueID),
		ProjectID:       uuidToPtr(i.ProjectID),
		Position:        i.Position,
		Stage:           int4ToPtr(i.Stage),
		StartDate:       dateToPtr(i.StartDate),
		DueDate:         dateToPtr(i.DueDate),
		CreatedAt:       timestampToString(i.CreatedAt),
		UpdatedAt:       timestampToString(i.UpdatedAt),
		WorkStartedAt:   timestampToPtr(i.WorkStartedAt),
		WorkCompletedAt: timestampToPtr(i.WorkCompletedAt),
		Revision:        i.Revision,
		LastActivityAt:  timestampToNanoPtr(i.LastActivityAt),
		Metadata:        parseIssueMetadata(i.Metadata),
		Properties:      parseIssueProperties(i.Properties),
	}
}

// issueListRowToResponse 把列表查询的行（不含 description 字段的结果集形态）转成 IssueResponse。
func issueListRowToResponse(i db.ListIssuesRow, issuePrefix string) IssueResponse {
	// 与 issueToResponse 同样的纯内置状态解析。 (MUL-6243)
	statusCategory := ""
	if issuestatus.IsBuiltIn(i.Status) {
		statusCategory = i.Status
	}
	identifier := issuePrefix + "-" + strconv.Itoa(int(i.Number))
	return IssueResponse{
		ID:              uuidToString(i.ID),
		WorkspaceID:     uuidToString(i.WorkspaceID),
		Number:          i.Number,
		Identifier:      identifier,
		Title:           i.Title,
		Description:     textToPtr(i.Description),
		Status:          i.Status,
		StatusCategory:  statusCategory,
		Priority:        i.Priority,
		AssigneeType:    textToPtr(i.AssigneeType),
		AssigneeID:      uuidToPtr(i.AssigneeID),
		CreatorType:     i.CreatorType,
		CreatorID:       uuidToString(i.CreatorID),
		ParentIssueID:   uuidToPtr(i.ParentIssueID),
		ProjectID:       uuidToPtr(i.ProjectID),
		Position:        i.Position,
		Stage:           int4ToPtr(i.Stage),
		StartDate:       dateToPtr(i.StartDate),
		DueDate:         dateToPtr(i.DueDate),
		CreatedAt:       timestampToString(i.CreatedAt),
		UpdatedAt:       timestampToString(i.UpdatedAt),
		WorkStartedAt:   timestampToPtr(i.WorkStartedAt),
		WorkCompletedAt: timestampToPtr(i.WorkCompletedAt),
		Revision:        i.Revision,
		LastActivityAt:  timestampToNanoPtr(i.LastActivityAt),
		Metadata:        parseIssueMetadata(i.Metadata),
		Properties:      parseIssueProperties(i.Properties),
	}
}

// labelsByIssue 一次性批量加载这批 issue 的标签，返回以 issue UUID 字符串为 key 的映射。
// 出错或入参为空时返回空 map——标签渲染不是关键路径，宁可让 issue 不带标签返回，也不让
// 整个列表请求失败。
func (h *Handler) labelsByIssue(ctx context.Context, wsUUID pgtype.UUID, issueIDs []pgtype.UUID) map[string][]LabelResponse {
	out := map[string][]LabelResponse{}
	if len(issueIDs) == 0 {
		return out
	}
	rows, err := h.Queries.ListLabelsForIssues(ctx, db.ListLabelsForIssuesParams{
		IssueIds:    issueIDs,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		slog.Warn("ListLabelsForIssues failed", "error", err)
		return out
	}
	for _, r := range rows {
		issueID := uuidToString(r.IssueID)
		out[issueID] = append(out[issueID], LabelResponse{
			ID:           uuidToString(r.ID),
			WorkspaceID:  uuidToString(r.WorkspaceID),
			ResourceType: r.ResourceType,
			Name:         r.Name,
			Description:  r.Description,
			Color:        r.Color,
			CreatedAt:    timestampToString(r.CreatedAt),
			UpdatedAt:    timestampToString(r.UpdatedAt),
		})
	}
	return out
}

// openIssueRowToResponse 把「未完成 issue」查询的行转成 IssueResponse（与列表行形态一致）。
func openIssueRowToResponse(i db.ListOpenIssuesRow, issuePrefix string) IssueResponse {
	// 与 issueToResponse 同样的纯内置状态解析。 (MUL-6243)
	statusCategory := ""
	if issuestatus.IsBuiltIn(i.Status) {
		statusCategory = i.Status
	}
	identifier := issuePrefix + "-" + strconv.Itoa(int(i.Number))
	return IssueResponse{
		ID:             uuidToString(i.ID),
		WorkspaceID:    uuidToString(i.WorkspaceID),
		Number:         i.Number,
		Identifier:     identifier,
		Title:          i.Title,
		Description:    textToPtr(i.Description),
		Status:         i.Status,
		StatusCategory: statusCategory,
		Priority:       i.Priority,
		AssigneeType:   textToPtr(i.AssigneeType),
		AssigneeID:     uuidToPtr(i.AssigneeID),
		CreatorType:    i.CreatorType,
		CreatorID:      uuidToString(i.CreatorID),
		ParentIssueID:  uuidToPtr(i.ParentIssueID),
		ProjectID:      uuidToPtr(i.ProjectID),
		Position:       i.Position,
		Stage:          int4ToPtr(i.Stage),
		StartDate:      dateToPtr(i.StartDate),
		DueDate:        dateToPtr(i.DueDate),
		CreatedAt:      timestampToString(i.CreatedAt),
		UpdatedAt:      timestampToString(i.UpdatedAt),
		Revision:       i.Revision,
		LastActivityAt: timestampToNanoPtr(i.LastActivityAt),
		Metadata:       parseIssueMetadata(i.Metadata),
		Properties:     parseIssueProperties(i.Properties),
	}
}

// IssueAssigneeGroupResponse 是「按受理人分组」结果中的一个组：组头信息 + 组内 issue 列表。
type IssueAssigneeGroupResponse struct {
	ID           string          `json:"id"`
	AssigneeType *string         `json:"assignee_type"`
	AssigneeID   *string         `json:"assignee_id"`
	Issues       []IssueResponse `json:"issues"`
	Total        int64           `json:"total"`
}

// GroupedIssuesResponse 是分组列表响应的外壳，组按 assignee 聚合。
type GroupedIssuesResponse struct {
	Groups []IssueAssigneeGroupResponse `json:"groups"`
}

// groupedIssueRow 包装 ListIssuesRow，额外带上窗口函数算出的本组总数。
type groupedIssueRow struct {
	db.ListIssuesRow
	GroupTotal int64
}

// assigneeGroupID 构造一个稳定的组标识：有受理人时形如 "assignee:<type>:<uuid>"，
// 未受理人统一归到 "assignee:unassigned"。
func assigneeGroupID(assigneeType pgtype.Text, assigneeID pgtype.UUID) string {
	if assigneeType.Valid && assigneeID.Valid {
		return "assignee:" + assigneeType.String + ":" + uuidToString(assigneeID)
	}
	return "assignee:unassigned"
}

// SearchIssueResponse 在 IssueResponse 之上扩展搜索结果特有的元信息（命中来源与片段）。
type SearchIssueResponse struct {
	IssueResponse
	MatchSource               string  `json:"match_source"`
	MatchedSnippet            *string `json:"matched_snippet,omitempty"`
	MatchedDescriptionSnippet *string `json:"matched_description_snippet,omitempty"`
	MatchedCommentSnippet     *string `json:"matched_comment_snippet,omitempty"`
}

// extractSnippet 围绕 query 第一次出现的位置截取一段上下文（命中点前后各扩一些，总长约
// 120 个 rune）。用 rune 切片是为了不把多字节 UTF-8 字符劈开（对中日韩内容尤其重要）。
// 多词查询先尝试整句匹配；整句找不到时，取最早出现的那一个词的命中位置来居中截取。
func extractSnippet(content, query string) string {
	runes := []rune(content)
	lowerRunes := []rune(strings.ToLower(content))
	queryRunes := []rune(strings.ToLower(query))

	idx := findRuneSubstring(lowerRunes, queryRunes)

	// 整句没命中就退而求其次：多词查询时取每个词里最早出现的位置。
	matchLen := len(queryRunes)
	if idx < 0 {
		terms := strings.Fields(strings.ToLower(query))
		if len(terms) > 1 {
			earliest := -1
			earliestLen := 0
			for _, term := range terms {
				termRunes := []rune(term)
				pos := findRuneSubstring(lowerRunes, termRunes)
				if pos >= 0 && (earliest < 0 || pos < earliest) {
					earliest = pos
					earliestLen = len(termRunes)
				}
			}
			if earliest >= 0 {
				idx = earliest
				matchLen = earliestLen
			}
		}
	}

	if idx < 0 {
		if len(runes) > 120 {
			return string(runes[:120]) + "..."
		}
		return content
	}
	start := idx - 40
	if start < 0 {
		start = 0
	}
	end := idx + matchLen + 80
	if end > len(runes) {
		end = len(runes)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(runes) {
		snippet = snippet + "..."
	}
	return snippet
}

// findRuneSubstring 在 haystack 里查找子串 needle 首次出现的下标，找不到返回 -1。
func findRuneSubstring(haystack, needle []rune) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// descriptionContains 判断描述文本是否包含搜索短语；对多词查询则要求所有词都出现。
func descriptionContains(desc pgtype.Text, phrase string, terms []string) bool {
	if !desc.Valid || desc.String == "" {
		return false
	}
	lower := strings.ToLower(desc.String)
	if strings.Contains(lower, strings.ToLower(phrase)) {
		return true
	}
	if len(terms) > 1 {
		for _, t := range terms {
			if !strings.Contains(lower, strings.ToLower(t)) {
				return false
			}
		}
		return true
	}
	return false
}

// escapeLike 把用户输入里的 LIKE 通配符（%、_、\）转义掉，让它们按字面匹配。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// splitSearchTerms 把查询串按空白拆成一个个搜索词，过滤掉空串。
func splitSearchTerms(q string) []string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r)
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			terms = append(terms, f)
		}
	}
	return terms
}

// identifierNumberRe 匹配 "MUL-123" / "ABC-45" 这类「字母前缀-数字」的标识符形态。
var identifierNumberRe = regexp.MustCompile(`(?i)^[a-z]+-(\d+)$`)

// parseQueryNumber 若查询串长得像标识符（如 "MUL-123"）或纯数字（如 "123"），
// 就抽出其中的 issue 编号。
func parseQueryNumber(q string) (int, bool) {
	q = strings.TrimSpace(q)
	// Check for identifier pattern like "MUL-123"
	if m := identifierNumberRe.FindStringSubmatch(q); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n, true
		}
	}
	// Check for bare number
	if n, err := strconv.Atoi(q); err == nil && n > 0 {
		return n, true
	}
	return 0, false
}

// searchResult 装动态搜索 SQL 返回的一行原始数据。
type searchResult struct {
	issue                 db.Issue
	totalCount            int64
	matchSource           string
	matchedCommentContent string
}

// buildSearchQuery 拼装 issue 搜索用的动态 SQL。用 LOWER(column) LIKE 做不区分大小写的
// 匹配，以便命中 pg_bigm 1.2 的 GIN 索引。模式串在 Go 侧先小写，SQL 里就不用对模式再套
// LOWER()；LIKE 模式（如 "%html%"）也在 Go 侧拼好，pg_bigm 才能从单个参数值里提取 bigram。
func buildSearchQuery(phrase string, terms []string, queryNum int, hasNum bool, includeClosed bool, terminalStatusKeys []string) (string, []any) {
	// 在 Go 侧小写，SQL 里只需要对列做 LOWER()。
	phrase = strings.ToLower(phrase)
	for i, t := range terms {
		terms[i] = strings.ToLower(t)
	}

	// Parameter index tracker
	argIdx := 1
	args := []any{}
	nextArg := func(val any) string {
		args = append(args, val)
		s := fmt.Sprintf("$%d", argIdx)
		argIdx++
		return s
	}

	escapedPhrase := escapeLike(phrase)
	// $1: 精确短语（title 精确命中用）
	phraseParam := nextArg(escapedPhrase)
	// $2: "%phrase%" 包含模式——预拼好给 pg_bigm 索引用
	phraseContainsParam := nextArg("%" + escapedPhrase + "%")
	// $3: "phrase%" 前缀模式
	phraseStartsWithParam := nextArg(escapedPhrase + "%")

	// $4 是 workspace_id 占位，稍后由调用方按位置填值。
	wsParam := nextArg(nil) // $4 — workspace_id, will be filled by caller position

	// 多词搜索时，为每个词构建包含匹配条件。
	var termContainsParams []string
	if len(terms) > 1 {
		for _, t := range terms {
			et := escapeLike(t)
			termContainsParams = append(termContainsParams, nextArg("%"+et+"%"))
		}
	}

	// --- WHERE 子句 ---
	var whereParts []string

	// 整句匹配：title、description 或 comment。
	//
	// comment 的 EXISTS 子查询刻意在 c.issue_id = i.id 之外还关联 c.workspace_id =
	// wsParam。多带一个 workspace 条件对正确性并非必需（comment.workspace_id 与所属 issue
	// 的外键一致），但对查询计划器至关重要：不加的话 Postgres 会把关联 EXISTS 改写成哈希子
	// 计划，把整张 comment 表里所有匹配 LIKE 的评论全部物化——对 "search" 这类常见词可能
	// 是几十万行，把 work_mem 撑爆成有损 bitmap，查询要 30 多秒。把 workspace_id 常量复制进
	// 子查询后，哈希集合塌缩到本工作区的评论，计划就能用到 idx_comment_workspace 索引
	//（迁移 135）。见 MUL-4059 的 EXPLAIN 报告。
	phraseMatch := fmt.Sprintf(
		"(LOWER(i.title) LIKE %s OR LOWER(COALESCE(i.description, '')) LIKE %s OR EXISTS (SELECT 1 FROM comment c WHERE c.issue_id = i.id AND c.workspace_id = %s AND LOWER(c.content) LIKE %s))",
		phraseContainsParam, phraseContainsParam, wsParam, phraseContainsParam,
	)
	whereParts = append(whereParts, phraseMatch)

	// 多词 AND 匹配（每个词都必须出现在某处）。与上面同样的「子查询里带 workspace_id」约定。
	if len(termContainsParams) > 1 {
		var termConditions []string
		for _, tp := range termContainsParams {
			termConditions = append(termConditions, fmt.Sprintf(
				"(LOWER(i.title) LIKE %s OR LOWER(COALESCE(i.description, '')) LIKE %s OR EXISTS (SELECT 1 FROM comment c WHERE c.issue_id = i.id AND c.workspace_id = %s AND LOWER(c.content) LIKE %s))",
				tp, tp, wsParam, tp,
			))
		}
		whereParts = append(whereParts, "("+strings.Join(termConditions, " AND ")+")")
	}

	// 编号匹配
	numParam := ""
	if hasNum {
		numParam = nextArg(queryNum)
		whereParts = append(whereParts, fmt.Sprintf("i.number = %s", numParam))
	}

	whereClause := "(" + strings.Join(whereParts, " OR ") + ")"

	if !includeClosed {
		// 只否定「已知的终态 key」：某个遗留的旧 key 不在列表里就仍可被搜到，
		// 不会因为不在白名单而从默认结果集里凭空消失。
		terminalStatusesParam := nextArg(terminalStatusKeys)
		whereClause += fmt.Sprintf(" AND NOT (i.status = ANY(%s::text[]))", terminalStatusesParam)
	}

	// --- ORDER BY 子句 ---
	// 用分档 CASE 构造相关性排序。
	var rankCases []string

	// 档 0：标识符精确命中（如 MUL-123）
	if hasNum {
		rankCases = append(rankCases, fmt.Sprintf("WHEN i.number = %s THEN 0", numParam))
	}

	// 档 1：title 精确命中
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(i.title) = %s THEN 1", phraseParam))

	// 档 2：title 以短语开头
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(i.title) LIKE %s THEN 2", phraseStartsWithParam))

	// 档 3：title 包含短语
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(i.title) LIKE %s THEN 3", phraseContainsParam))

	// 档 4：title 命中全部词（仅多词搜索）
	if len(termContainsParams) > 1 {
		var titleTerms []string
		for _, tp := range termContainsParams {
			titleTerms = append(titleTerms, fmt.Sprintf("LOWER(i.title) LIKE %s", tp))
		}
		rankCases = append(rankCases, fmt.Sprintf("WHEN (%s) THEN 4", strings.Join(titleTerms, " AND ")))
	}

	// 档 5：description 包含短语
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(COALESCE(i.description, '')) LIKE %s THEN 5", phraseContainsParam))

	// 档 6：description 命中全部词（仅多词搜索）
	if len(termContainsParams) > 1 {
		var descTerms []string
		for _, tp := range termContainsParams {
			descTerms = append(descTerms, fmt.Sprintf("LOWER(COALESCE(i.description, '')) LIKE %s", tp))
		}
		rankCases = append(rankCases, fmt.Sprintf("WHEN (%s) THEN 6", strings.Join(descTerms, " AND ")))
	}

	// 档 7：comment 包含短语。与 WHERE 里同样的「子查询带 workspace_id」约定，见上面
	// phraseMatch 的注释。
	rankCases = append(rankCases, fmt.Sprintf("WHEN EXISTS (SELECT 1 FROM comment c WHERE c.issue_id = i.id AND c.workspace_id = %s AND LOWER(c.content) LIKE %s) THEN 7", wsParam, phraseContainsParam))

	// 档 8：comment 命中全部词（仅多词搜索）
	if len(termContainsParams) > 1 {
		var commentTerms []string
		for _, tp := range termContainsParams {
			commentTerms = append(commentTerms, fmt.Sprintf("LOWER(c.content) LIKE %s", tp))
		}
		rankCases = append(rankCases, fmt.Sprintf("WHEN EXISTS (SELECT 1 FROM comment c WHERE c.issue_id = i.id AND c.workspace_id = %s AND (%s)) THEN 8", wsParam, strings.Join(commentTerms, " AND ")))
	}

	rankExpr := "CASE " + strings.Join(rankCases, " ") + " ELSE 9 END"

	// 状态优先级：在跑的先排前面
	statusRank := `CASE i.status
		WHEN 'in_progress' THEN 0
		WHEN 'in_review' THEN 1
		WHEN 'todo' THEN 2
		WHEN 'blocked' THEN 3
		WHEN 'backlog' THEN 4
		WHEN 'done' THEN 5
		WHEN 'cancelled' THEN 6
		ELSE 7
	END`

	// cancelled 是被放弃的工作。仅靠 statusRank 压不住它——那只是同一相关性档内的并列
	// 决胜条件：一个 title 整句命中的 cancelled issue 仍会排在「只包含短语」的 in_progress
	// 前面，而 cancelled 多的工作区可能把整个 LIMIT 窗口占满，把正在做的工作挤下屏。所以在
	// rankExpr 之前先贬低 cancelled——它们排在任何其它命中之后，是 LIMIT 首先丢弃的行。
	// 这与 'done' 不同：done 是值得引用的已完成工作，cancelled 是被扔掉的东西。唯一的例外
	// 是直接命中：标识符或 title 整句精确命中说明用户就是冲着那一个 issue 来的，他清楚自己
	// 在找什么。
	//
	// title 那半边逐字复用了档 1 的谓词，包括它的怪癖：phraseParam 经过了 escapeLike，
	// 因此标题里含 _ 或 % 的 issue 永远不会判等、不会被当成直接命中。这类 issue 仍可凭编号
	// 搜到；让两处谓词保持一致比绕开一个本就属于档 1 的转义 bug 更重要。
	directHitParts := []string{fmt.Sprintf("LOWER(i.title) = %s", phraseParam)}
	if hasNum {
		directHitParts = append(directHitParts, fmt.Sprintf("i.number = %s", numParam))
	}
	cancelledRank := fmt.Sprintf(
		"CASE WHEN i.status = 'cancelled' AND NOT (%s) THEN 1 ELSE 0 END",
		strings.Join(directHitParts, " OR "),
	)

	// --- match_source 表达式：告诉前端这次命中来自哪个字段 ---
	matchSourceExpr := fmt.Sprintf(`CASE
		WHEN LOWER(i.title) LIKE %s THEN 'title'
		WHEN LOWER(COALESCE(i.description, '')) LIKE %s THEN 'description'
		ELSE 'comment'
	END`, phraseContainsParam, phraseContainsParam)

	// 多词时也检查 title/description 是否命中全部词
	if len(termContainsParams) > 1 {
		var titleTerms []string
		var descTerms []string
		for _, tp := range termContainsParams {
			titleTerms = append(titleTerms, fmt.Sprintf("LOWER(i.title) LIKE %s", tp))
			descTerms = append(descTerms, fmt.Sprintf("LOWER(COALESCE(i.description, '')) LIKE %s", tp))
		}
		matchSourceExpr = fmt.Sprintf(`CASE
			WHEN LOWER(i.title) LIKE %s THEN 'title'
			WHEN (%s) THEN 'title'
			WHEN LOWER(COALESCE(i.description, '')) LIKE %s THEN 'description'
			WHEN (%s) THEN 'description'
			ELSE 'comment'
		END`,
			phraseContainsParam, strings.Join(titleTerms, " AND "),
			phraseContainsParam, strings.Join(descTerms, " AND "),
		)
	}

	// --- matched_comment_content 子查询 ---
	// 不管 match_source 是什么都返回匹配的评论内容，前端才能在 title/description 命中时
	// 也展示评论片段。c.workspace_id 与 WHERE 一致：缺了它，计划器可能选一条无视工作区
	// 边界的全局评论扫描。
	commentSubquery := fmt.Sprintf(`COALESCE(
		(SELECT c.content FROM comment c
		 WHERE c.issue_id = i.id AND c.workspace_id = %s AND LOWER(c.content) LIKE %s
		 ORDER BY c.created_at DESC LIMIT 1),
		''
	)`, wsParam, phraseContainsParam)

	if len(termContainsParams) > 1 {
		var commentTerms []string
		for _, tp := range termContainsParams {
			commentTerms = append(commentTerms, fmt.Sprintf("LOWER(c.content) LIKE %s", tp))
		}
		commentSubquery = fmt.Sprintf(`COALESCE(
			(SELECT c.content FROM comment c
			 WHERE c.issue_id = i.id AND c.workspace_id = %s AND (LOWER(c.content) LIKE %s OR (%s))
			 ORDER BY c.created_at DESC LIMIT 1),
			''
		)`, wsParam, phraseContainsParam, strings.Join(commentTerms, " AND "))
	}

	limitParam := nextArg(nil)  // placeholder
	offsetParam := nextArg(nil) // placeholder

	query := fmt.Sprintf(`SELECT i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
		i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.acceptance_criteria, i.context_refs, i.position,
		i.start_date, i.due_date, i.created_at, i.updated_at, i.last_activity_at, i.number, i.project_id,
		i.revision,
		COUNT(*) OVER() AS total_count,
		%s AS match_source,
		%s AS matched_comment_content
	FROM issue i
	WHERE i.workspace_id = %s AND %s
	ORDER BY %s, %s, %s, i.updated_at DESC
	LIMIT %s OFFSET %s`,
		matchSourceExpr,
		commentSubquery,
		wsParam,
		whereClause,
		cancelledRank,
		rankExpr,
		statusRank,
		limitParam,
		offsetParam,
	)

	return query, args
}

// SearchIssues 处理 GET /api/issues/search：按 ?q= 全文搜索本工作区的 issue（标题/描述/
// 评论），返回带相关性排序、命中来源与片段的结果，并通过 X-Total-Count 头与 total 字段
// 透出总条数。默认排除已关闭状态（include_closed=true 可放开）。走本工作区路由的前提是
// 调用者已被工作区成员/权限中间件放行。
//
// 处理要点：limit 封顶 50；GIN 索引缺失或库过载导致语句超时（SQLSTATE 57014）时返回 503
// 而非笼统的 500，方便前端提示「换个更精确的查询」。
func (h *Handler) SearchIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := h.resolveWorkspaceID(r)

	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	limit := 20
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 50 {
		limit = 50
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	includeClosed := r.URL.Query().Get("include_closed") == "true"

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	terms := splitSearchTerms(q)
	queryNum, hasNum := parseQueryNumber(q)
	var terminalStatusKeys []string
	if !includeClosed {
		resolvedKeys, err := h.terminalIssueStatusKeys(ctx, wsUUID)
		if err != nil {
			slog.Warn("expand terminal status categories failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve status categories")
			return
		}
		terminalStatusKeys = resolvedKeys
	}

	sqlQuery, args := buildSearchQuery(q, terms, queryNum, hasNum, includeClosed, terminalStatusKeys)
	// Fill placeholder args: $4 = workspace_id, last two = limit, offset
	args[3] = wsUUID
	args[len(args)-2] = limit
	args[len(args)-1] = offset

	var results []searchResult
	err := runSearchQuery(ctx, h.TxStarter, sqlQuery, args, func(rows pgx.Rows) error {
		for rows.Next() {
			var sr searchResult
			if err := rows.Scan(
				&sr.issue.ID,
				&sr.issue.WorkspaceID,
				&sr.issue.Title,
				&sr.issue.Description,
				&sr.issue.Status,
				&sr.issue.Priority,
				&sr.issue.AssigneeType,
				&sr.issue.AssigneeID,
				&sr.issue.CreatorType,
				&sr.issue.CreatorID,
				&sr.issue.ParentIssueID,
				&sr.issue.AcceptanceCriteria,
				&sr.issue.ContextRefs,
				&sr.issue.Position,
				&sr.issue.StartDate,
				&sr.issue.DueDate,
				&sr.issue.CreatedAt,
				&sr.issue.UpdatedAt,
				&sr.issue.LastActivityAt,
				&sr.issue.Number,
				&sr.issue.ProjectID,
				&sr.issue.Revision,
				&sr.totalCount,
				&sr.matchSource,
				&sr.matchedCommentContent,
			); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			results = append(results, sr)
		}
		return rows.Err()
	})
	if err != nil {
		// 语句超时以 SQLSTATE 57014 呈现。返回 503 让前端能区分「查询超时（试试更精确的
		// 查询）」与普通 500。GIN 搜索索引缺失或数据库过载时走这条快速失败路径；背景见
		// runSearchQuery 头注释。
		if isSearchStatementTimeout(err) {
			slog.Warn("search issues timed out",
				"workspace_id", workspaceID,
				"query", q,
				"timeout", searchStatementTimeout)
			writeError(w, http.StatusServiceUnavailable, "search timed out; please refine your query or try again")
			return
		}
		slog.Warn("search issues failed", "error", err, "workspace_id", workspaceID, "query", q)
		writeError(w, http.StatusInternalServerError, "failed to search issues")
		return
	}

	var total int64
	if len(results) > 0 {
		total = results[0].totalCount
	}

	prefix := h.getIssuePrefix(ctx, wsUUID)
	fillSearch := h.newStatusCategoryFiller(ctx, wsUUID)
	resp := make([]SearchIssueResponse, len(results))
	for i, sr := range results {
		sir := SearchIssueResponse{
			IssueResponse: issueToResponse(sr.issue, prefix),
			MatchSource:   sr.matchSource,
		}
		fillSearch(&sir.IssueResponse)
		// 只要存在匹配的评论就总是填上评论片段
		if sr.matchedCommentContent != "" {
			snippet := extractSnippet(sr.matchedCommentContent, q)
			sir.MatchedCommentSnippet = &snippet
			// 向后兼容：评论来源的命中仍同时设置 MatchedSnippet
			if sr.matchSource == "comment" {
				sir.MatchedSnippet = &snippet
			}
		}
		// 描述命中时填描述片段
		if sr.matchSource == "description" || descriptionContains(sr.issue.Description, q, terms) {
			if sr.issue.Description.Valid && sr.issue.Description.String != "" {
				snippet := extractSnippet(sr.issue.Description.String, q)
				sir.MatchedDescriptionSnippet = &snippet
			}
		}
		resp[i] = sir
	}

	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSON(w, http.StatusOK, map[string]any{
		"issues": resp,
		"total":  total,
	})
}

// QueryIssues 是 ListIssues 的 POST 孪生，用于过滤条件大到塞不进 GET 请求行的情况——
// 表格的「agents-working」facet 可能携带上百个 issue id，常见反向代理对请求行限制在 8KB
// 左右。请求体是与 ListIssues 查询参数「键与字符串编码完全一致」的扁平 JSON 对象；本处理
// 器重建查询串后转交给 ListIssues，两条通道因此不可能漂移。
func (h *Handler) QueryIssues(w http.ResponseWriter, r *http.Request) {
	var params map[string]string
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	values := make(url.Values, len(params))
	for key, value := range params {
		values.Set(key, value)
	}
	r.URL.RawQuery = values.Encode()
	h.ListIssues(w, r)
}

// ListIssues 处理 GET /api/issues：按当前工作区列出 issue，支持状态/优先级/受理人/创建人/
// 项目/标签/日期区间/元数据/自定义属性/搜索词等多种过滤、排序与分页。调用者须是该工作区
// 成员（由中间件保证）；形如 open_only=true 时走独立的分页形态（无 LIMIT）。参数非法
// （如 UUID 拼错）一律返回 400 而不是静默放行。
func (h *Handler) ListIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// 解析可选的过滤参数。过滤条件里的 UUID 格式错误直接返回 400——悄悄强转成零 UUID
	// 会掩盖客户端 bug，还可能让查询返回空结果（或更糟：匹配到 NULL 行）。
	var priorityFilter pgtype.Text
	if p := r.URL.Query().Get("priority"); p != "" {
		priorityFilter = pgtype.Text{String: p, Valid: true}
	}
	var assigneeFilter pgtype.UUID
	if a := r.URL.Query().Get("assignee_id"); a != "" {
		id, ok := parseUUIDOrBadRequest(w, a, "assignee_id")
		if !ok {
			return
		}
		assigneeFilter = id
	}
	var assigneeIdsFilter []pgtype.UUID
	if ids := r.URL.Query().Get("assignee_ids"); ids != "" {
		for _, raw := range strings.Split(ids, ",") {
			if s := strings.TrimSpace(raw); s != "" {
				id, ok := parseUUIDOrBadRequest(w, s, "assignee_ids")
				if !ok {
					return
				}
				assigneeIdsFilter = append(assigneeIdsFilter, id)
			}
		}
	}
	var creatorFilter pgtype.UUID
	if c := r.URL.Query().Get("creator_id"); c != "" {
		id, ok := parseUUIDOrBadRequest(w, c, "creator_id")
		if !ok {
			return
		}
		creatorFilter = id
	}
	var projectFilter pgtype.UUID
	if p := r.URL.Query().Get("project_id"); p != "" {
		id, ok := parseUUIDOrBadRequest(w, p, "project_id")
		if !ok {
			return
		}
		projectFilter = id
	}
	// involves_user_id 把受理人过滤放宽，用来找出用户以「间接受理人」身份参与的 issue——
	// 例如派给他名下的 agent、或他所属/领导/放了 agent 进去的 squad。直接以成员身份被分配
	// 的排除在外，这是有意设计：`assignee_id`（标签页 1）才是直接分配，标签页 3 必须与
	// 标签页 1 不相交。
	var involvesUserFilter pgtype.UUID
	if u := r.URL.Query().Get("involves_user_id"); u != "" {
		id, ok := parseUUIDOrBadRequest(w, u, "involves_user_id")
		if !ok {
			return
		}
		involvesUserFilter = id
	}

	metadataFilter, ok := parseMetadataFilterParam(w, r.URL.Query().Get("metadata"))
	if !ok {
		return
	}
	propertiesFilter, ok := parsePropertiesFilterParam(w, r.URL.Query().Get("properties"))
	if !ok {
		return
	}
	dateFilter, ok := parseIssueDateFilter(w, r.URL.Query())
	if !ok {
		return
	}

	// open_only=true 返回所有非 done/cancelled 的 issue（无 LIMIT 分页）。
	if r.URL.Query().Get("open_only") == "true" {
		// 把解析好的 AND-of-ORs 分组序列化成单个 jsonb 参数，供静态查询展开
		//（见 ListOpenIssues 里的 properties_filter）。
		var openPropertiesFilter []byte
		if len(propertiesFilter) > 0 {
			marshaled, marshalErr := json.Marshal(propertiesFilter)
			if marshalErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to list issues")
				return
			}
			openPropertiesFilter = marshaled
		}
		terminalStatusKeys, err := h.terminalIssueStatusKeys(ctx, wsUUID)
		if err != nil {
			if writeClientClosedIfCanceled(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to resolve status categories")
			return
		}
		issues, err := h.Queries.ListOpenIssues(ctx, db.ListOpenIssuesParams{
			WorkspaceID:        wsUUID,
			TerminalStatusKeys: terminalStatusKeys,
			Priority:           priorityFilter,
			AssigneeID:         assigneeFilter,
			AssigneeIds:        assigneeIdsFilter,
			CreatorID:          creatorFilter,
			ProjectID:          projectFilter,
			InvolvesUserID:     involvesUserFilter,
			MetadataFilter:     metadataFilter,
			PropertiesFilter:   openPropertiesFilter,
		})
		if err != nil {
			if writeClientClosedIfCanceled(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to list issues")
			return
		}

		prefix := h.getIssuePrefix(ctx, wsUUID)
		ids := make([]pgtype.UUID, len(issues))
		for i, issue := range issues {
			ids[i] = issue.ID
		}
		labelsMap := h.labelsByIssue(ctx, wsUUID, ids)
		fillOpen := h.newStatusCategoryFiller(ctx, wsUUID)
		resp := make([]IssueResponse, len(issues))
		for i, issue := range issues {
			resp[i] = openIssueRowToResponse(issue, prefix)
			fillOpen(&resp[i])
			labels := labelsMap[resp[i].ID]
			if labels == nil {
				labels = []LabelResponse{}
			}
			resp[i].Labels = &labels
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"issues": resp,
			"total":  len(resp),
		})
		return
	}

	limit := 100
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	statusesFilter := splitCommaParam(r.URL.Query().Get("statuses"))
	if len(statusesFilter) == 0 {
		statusesFilter = splitCommaParam(r.URL.Query().Get("status"))
	}
	// status_category 是按「行为」而不是精确 key 过滤，因此看板的一列可以同时容纳某类别
	// 的 canonical 状态以及所有继承它的自定义状态。不这么做，看板就得一种状态一列——一次
	// 请求对应一个列。 (MUL-6243)
	statusCategoriesFilter := splitCommaParam(r.URL.Query().Get("status_categories"))
	if len(statusCategoriesFilter) == 0 {
		statusCategoriesFilter = splitCommaParam(r.URL.Query().Get("status_category"))
	}
	prioritiesFilter := splitCommaParam(r.URL.Query().Get("priorities"))
	if len(prioritiesFilter) == 0 {
		prioritiesFilter = splitCommaParam(r.URL.Query().Get("priority"))
	}

	// assignee_types 把列表收窄到派给某种受理人角色（member / agent / squad）的 issue。
	// 与 ListGroupedIssues 上的同名参数保持一致，让工作区 Members/Agents 标签页可以在服务端
	// 过滤，而不是客户端在已加载的整页里做二次过滤。
	assigneeTypesFilter := splitCommaParam(r.URL.Query().Get("assignee_types"))
	for _, assigneeType := range assigneeTypesFilter {
		if !isIssueActorType(assigneeType) {
			writeError(w, http.StatusBadRequest, "invalid assignee_types")
			return
		}
	}

	// scheduled=true 只返回 start_date / due_date 至少设了一个的 issue。Project Gantt
	// 视图用它：它只渲染可排期的行，不该为整份项目列表买单。
	var scheduledFilter pgtype.Bool
	if r.URL.Query().Get("scheduled") == "true" {
		scheduledFilter = pgtype.Bool{Bool: true, Valid: true}
	}

	// 解析排序字段与方向，构造动态 ORDER BY。
	// 手动排序（position）恒为 ASC——方向参数被忽略，因为顺序由用户拖拽决定，反转它没有
	// 产品意义。
	sortCol := "position"
	sortIsExpr := false
	sortIsProperty := false
	if s := r.URL.Query().Get("sort"); s != "" {
		switch s {
		case "position", "title", "created_at", "updated_at", "start_date", "due_date":
			sortCol = s
		case "last_activity":
			sortCol = "last_activity_at"
		case "status":
			sortCol = "CASE i.status WHEN 'backlog' THEN 0 WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'in_review' THEN 3 WHEN 'done' THEN 4 WHEN 'blocked' THEN 5 WHEN 'cancelled' THEN 6 ELSE 7 END"
			sortIsExpr = true
		case "priority":
			sortCol = "CASE i.priority WHEN 'important_urgent' THEN 0 WHEN 'important_not_urgent' THEN 1 WHEN 'not_important_urgent' THEN 2 WHEN 'not_important_not_urgent' THEN 3 ELSE 4 END"
			sortIsExpr = true
		default:
			// property:<definitionId> 按自定义属性值（类型化表达式）排序；
			// 未知/已归档的属性定义降级为按 position 排序，避免把老客户端直接打挂。
			expr, handled, sortErr := h.propertySortExpr(r, workspaceID, s)
			if !handled {
				writeError(w, http.StatusBadRequest, "invalid sort value")
				return
			}
			if sortErr != nil {
				if sortErr.Error() == "invalid sort value" || sortErr.Error() == "invalid workspace id" {
					writeError(w, http.StatusBadRequest, sortErr.Error())
					return
				}
				slog.Warn("propertySortExpr failed", append(logger.RequestAttrs(r), "error", sortErr)...)
				writeError(w, http.StatusInternalServerError, "failed to resolve sort")
				return
			}
			if expr != "" {
				sortCol = expr
				sortIsExpr = true
				sortIsProperty = true
			}
		}
	}
	sortDir := "ASC"
	if sortCol == "last_activity_at" {
		sortDir = "DESC"
	}
	if sortCol != "position" {
		if d := r.URL.Query().Get("direction"); d != "" {
			switch strings.ToLower(d) {
			case "asc":
				sortDir = "ASC"
			case "desc":
				sortDir = "DESC"
			default:
				writeError(w, http.StatusBadRequest, "invalid direction value")
				return
			}
		}
	}

	// 构造动态 SQL——与 ListGroupedIssues 同一套做法。
	where := []string{"i.workspace_id = $1"}
	args := []any{wsUUID}
	addArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if len(statusCategoriesFilter) > 0 {
		// 展开成具体的状态 key 而不是包一层 issue_effective_status() 过滤：给列套函数会让
		// (workspace_id, status) 索引失效，把两页的索引读变成整表扫描。 (MUL-6243)
		keys, err := issuestatus.ExpandCategories(r.Context(), h.Queries, wsUUID, statusCategoriesFilter)
		if err != nil {
			if writeClientClosedIfCanceled(w, err) {
				return
			}
			slog.Warn("expand status categories failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve status categories")
			return
		}
		where = append(where, fmt.Sprintf("i.status = ANY(%s::text[])", addArg(keys)))
	}
	if len(statusesFilter) > 0 {
		where = append(where, fmt.Sprintf("i.status = ANY(%s::text[])", addArg(statusesFilter)))
	}
	if len(prioritiesFilter) > 0 {
		where = append(where, fmt.Sprintf("i.priority = ANY(%s::text[])", addArg(prioritiesFilter)))
	}
	if assigneeFilter.Valid {
		where = append(where, fmt.Sprintf("i.assignee_id = %s::uuid", addArg(assigneeFilter)))
	}
	if len(assigneeIdsFilter) > 0 {
		where = append(where, fmt.Sprintf("i.assignee_id = ANY(%s::uuid[])", addArg(assigneeIdsFilter)))
	}
	if len(assigneeTypesFilter) > 0 {
		where = append(where, fmt.Sprintf("i.assignee_type = ANY(%s::text[])", addArg(assigneeTypesFilter)))
	}
	if creatorFilter.Valid {
		where = append(where, fmt.Sprintf("i.creator_id = %s::uuid", addArg(creatorFilter)))
	}
	if projectFilter.Valid {
		where = append(where, fmt.Sprintf("i.project_id = %s::uuid", addArg(projectFilter)))
	}

	// 表格 facet 必须落在服务端窗口里。放在 LIMIT/OFFSET 之后应用，会把落在后几页的命中
	// 藏起来，让 total 和用户看到/导出的行对不上。
	assigneeFilters, ok := parseActorFilterList(w, r.URL.Query().Get("assignee_filters"), "assignee_filters")
	if !ok {
		return
	}
	includeNoAssignee := r.URL.Query().Get("include_no_assignee") == "true"
	if len(assigneeFilters) > 0 || includeNoAssignee {
		ors := make([]string, 0, len(assigneeFilters)+1)
		for _, filter := range assigneeFilters {
			ors = append(ors, fmt.Sprintf(
				"(i.assignee_type = %s::text AND i.assignee_id = %s::uuid)",
				addArg(filter.actorType),
				addArg(filter.actorID),
			))
		}
		if includeNoAssignee {
			ors = append(ors, "(i.assignee_type IS NULL AND i.assignee_id IS NULL)")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	creatorFilters, ok := parseActorFilterList(w, r.URL.Query().Get("creator_filters"), "creator_filters")
	if !ok {
		return
	}
	if len(creatorFilters) > 0 {
		ors := make([]string, 0, len(creatorFilters))
		for _, filter := range creatorFilters {
			ors = append(ors, fmt.Sprintf(
				"(i.creator_type = %s::text AND i.creator_id = %s::uuid)",
				addArg(filter.actorType),
				addArg(filter.actorID),
			))
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	projectIDs, ok := parseUUIDParamList(w, r.URL.Query().Get("project_ids"), "project_ids")
	if !ok {
		return
	}
	includeNoProject := r.URL.Query().Get("include_no_project") == "true"
	if len(projectIDs) > 0 || includeNoProject {
		ors := make([]string, 0, 2)
		if len(projectIDs) > 0 {
			ors = append(ors, fmt.Sprintf("i.project_id = ANY(%s::uuid[])", addArg(projectIDs)))
		}
		if includeNoProject {
			ors = append(ors, "i.project_id IS NULL")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	labelIDs, ok := parseUUIDParamList(w, r.URL.Query().Get("label_ids"), "label_ids")
	if !ok {
		return
	}
	if len(labelIDs) > 0 {
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM issue_to_label itl WHERE itl.issue_id = i.id AND itl.label_id = ANY(%s::uuid[]))",
			addArg(labelIDs),
		))
	}
	// ids 把窗口限定为显式的 id 集合（表格的 agents-working facet 会把「正在跑的 issue id」
	// 发过来）。参数存在但列表为「空」是有意义的——必须返回空窗口而不是悄悄退化成不限集合，
	// 所以用 Has() 判断参数是否出现，而不是看解析后长度。
	if r.URL.Query().Has("ids") {
		idsFilter, ok := parseUUIDParamList(w, r.URL.Query().Get("ids"), "ids")
		if !ok {
			return
		}
		if idsFilter == nil {
			idsFilter = []pgtype.UUID{}
		}
		where = append(where, fmt.Sprintf("i.id = ANY(%s::uuid[])", addArg(idsFilter)))
	}
	if r.URL.Query().Get("top_level_only") == "true" {
		where = append(where, "i.parent_issue_id IS NULL")
	}
	where = appendIssueTableSearchFilter(where, addArg, r.URL.Query().Get("q"))
	if scheduledFilter.Valid {
		where = append(where, "(i.start_date IS NOT NULL OR i.due_date IS NOT NULL)")
	}
	if metadataFilter != nil {
		where = append(where, fmt.Sprintf("i.metadata @> %s::jsonb", addArg(string(metadataFilter))))
	}
	if propertiesFilter != nil {
		where = append(where, propertiesFilterPredicate(propertiesFilter, addArg))
	}
	where = appendIssueDateFilter(where, addArg, dateFilter)
	if involvesUserFilter.Valid {
		ref := addArg(involvesUserFilter)
		where = append(where, fmt.Sprintf(`(
    (i.assignee_type = 'agent' AND i.assignee_id IN (
       SELECT a.id FROM agent a
        WHERE a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
    OR (i.assignee_type = 'squad' AND i.assignee_id IN (
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'member'
          AND sm.member_id   = %[1]s::uuid
       UNION
       SELECT s.id
         FROM squad s
         JOIN agent a ON a.id = s.leader_id
        WHERE s.workspace_id = $1
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
       UNION
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
         JOIN agent a ON a.id = sm.member_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'agent'
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
)`, ref))
	}

	whereSql := strings.Join(where, " AND ")

	// 构造 ORDER BY 子句。
	orderBy := sortCol
	if !sortIsExpr {
		orderBy = "i." + sortCol
	}
	orderBy += " " + sortDir
	if sortCol == "start_date" || sortCol == "due_date" || sortCol == "last_activity_at" || sortIsProperty {
		// 属性值天然稀疏：无论升序降序，没设值的 issue 都排最后（与客户端比较器一致）。
		orderBy += " NULLS LAST"
	}
	// created_at 本身不是唯一键（批量导入会共享时间戳）；没有唯一末位键的话，数据库在两次
	// LIMIT/OFFSET 请求之间可能重排并列行，导致翻页边界重复或丢行。
	if sortCol == "last_activity_at" {
		orderBy += ", i.id DESC"
	} else {
		orderBy += ", i.created_at DESC, i.id DESC"
	}

	offsetRef := addArg(int64(offset))
	limitRef := addArg(int64(limit))

	query := fmt.Sprintf(`SELECT i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
       i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
       i.parent_issue_id, i.position, i.start_date, i.due_date, i.created_at, i.updated_at, i.last_activity_at, i.number, i.project_id, i.metadata, i.stage, i.properties,
	   i.revision
FROM issue i
WHERE %s
ORDER BY %s
LIMIT %s OFFSET %s`, whereSql, orderBy, limitRef, offsetRef)

	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		slog.Warn("ListIssues query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list issues")
		return
	}
	defer rows.Close()

	var issues []db.ListIssuesRow
	for rows.Next() {
		var row db.ListIssuesRow
		if err := rows.Scan(
			&row.ID,
			&row.WorkspaceID,
			&row.Title,
			&row.Description,
			&row.Status,
			&row.Priority,
			&row.AssigneeType,
			&row.AssigneeID,
			&row.CreatorType,
			&row.CreatorID,
			&row.ParentIssueID,
			&row.Position,
			&row.StartDate,
			&row.DueDate,
			&row.CreatedAt,
			&row.UpdatedAt,
			&row.LastActivityAt,
			&row.Number,
			&row.ProjectID,
			&row.Metadata,
			&row.Stage,
			&row.Properties,
			&row.Revision,
		); err != nil {
			if writeClientClosedIfCanceled(w, err) {
				return
			}
			slog.Warn("ListIssues scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list issues")
			return
		}
		issues = append(issues, row)
	}
	if err := rows.Err(); err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		slog.Warn("ListIssues rows failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list issues")
		return
	}

	// 取真实总数供前端感知分页。
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM issue i WHERE %s`, whereSql)
	// 计数查询复用同样的参数，只是去掉最后两个 OFFSET/LIMIT 占位。
	countArgs := args[:len(args)-2]
	var total int64
	if err := h.DB.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		total = int64(len(issues))
	}

	prefix := h.getIssuePrefix(ctx, wsUUID)
	ids := make([]pgtype.UUID, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	labelsMap := h.labelsByIssue(ctx, wsUUID, ids)
	resp := make([]IssueResponse, len(issues))
	for i, issue := range issues {
		resp[i] = issueListRowToResponse(issue, prefix)
		labels := labelsMap[resp[i].ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		resp[i].Labels = &labels
	}
	h.fillStatusCategories(ctx, wsUUID, resp)

	writeJSON(w, http.StatusOK, map[string]any{
		"issues": resp,
		"total":  total,
	})
}

// issueActorFilter 是「type:uuid」形式的受理人/创建人过滤条件（如 agent:<uuid>）。
type issueActorFilter struct {
	actorType string
	actorID   pgtype.UUID
}

// issueDateFilter 是一次按日期区间过滤的参数：作用于哪个列（created_at/updated_at）以及
// 半开区间 [start, end)。
type issueDateFilter struct {
	column string
	start  time.Time
	end    time.Time
}

// parseIssueDateFilter 解析 date_field/date_start/date_end 三个必须同时出现的参数，
// 校验列名与时间格式后封装成 issueDateFilter。参数非法时写 400 并返回 false。
func parseIssueDateFilter(w http.ResponseWriter, values url.Values) (*issueDateFilter, bool) {
	field := strings.TrimSpace(values.Get("date_field"))
	startRaw := strings.TrimSpace(values.Get("date_start"))
	endRaw := strings.TrimSpace(values.Get("date_end"))
	if field == "" && startRaw == "" && endRaw == "" {
		return nil, true
	}
	if field == "" || startRaw == "" || endRaw == "" {
		writeError(w, http.StatusBadRequest, "date_field, date_start, and date_end are required together")
		return nil, false
	}

	column := ""
	switch field {
	case "created_at":
		column = "created_at"
	case "updated_at":
		column = "updated_at"
	default:
		writeError(w, http.StatusBadRequest, "invalid date_field")
		return nil, false
	}

	start, err := time.Parse(time.RFC3339Nano, startRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid date_start")
		return nil, false
	}
	end, err := time.Parse(time.RFC3339Nano, endRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid date_end")
		return nil, false
	}
	if !start.Before(end) {
		writeError(w, http.StatusBadRequest, "date_start must be before date_end")
		return nil, false
	}

	return &issueDateFilter{column: column, start: start, end: end}, true
}

// appendIssueDateFilter 把日期区间条件追加进 where：end 用开区间（< end）匹配时间戳精度。
func appendIssueDateFilter(where []string, addArg func(any) string, filter *issueDateFilter) []string {
	if filter == nil {
		return where
	}
	startRef := addArg(filter.start)
	endRef := addArg(filter.end)
	return append(where, fmt.Sprintf(
		"i.%s >= %s AND i.%s < %s",
		filter.column,
		startRef,
		filter.column,
		endRef,
	))
}

// appendIssueTableSearchFilter 给普通 ListIssues 窗口加一个快速的标识/词搜索。与带相关性
// 排序的全局搜索端点不同，这个谓词保留表格当前的过滤、显式排序、total 与分页。每个词都必须
// 出现在 title 里；完整标识符（或裸编号）也按不可变的数字编号匹配。
func appendIssueTableSearchFilter(where []string, addArg func(any) string, raw string) []string {
	query := strings.TrimSpace(raw)
	if query == "" {
		return where
	}

	words := splitSearchTerms(strings.ToLower(query))
	ors := make([]string, 0, 2)
	if len(words) > 0 {
		titleMatches := make([]string, 0, len(words))
		for _, word := range words {
			pattern := "%" + escapeLike(word) + "%"
			titleMatches = append(titleMatches, fmt.Sprintf("LOWER(i.title) LIKE %s", addArg(pattern)))
		}
		ors = append(ors, "("+strings.Join(titleMatches, " AND ")+")")
	}
	if number, ok := parseQueryNumber(query); ok {
		ors = append(ors, fmt.Sprintf("i.number = %s", addArg(number)))
	}
	if len(ors) == 0 {
		return where
	}
	return append(where, "("+strings.Join(ors, " OR ")+")")
}

// splitCommaParam 把逗号分隔的参数串拆成去空白、去空串的切片。
func splitCommaParam(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// isIssueActorType 判断字符串是否为合法的受理人角色类型。
func isIssueActorType(s string) bool {
	return s == "member" || s == "agent" || s == "squad"
}

// parseUUIDParamList 把逗号分隔的 UUID 串解析成切片；任一元素非法即写 400 返回 false。
func parseUUIDParamList(w http.ResponseWriter, raw, fieldName string) ([]pgtype.UUID, bool) {
	parts := splitCommaParam(raw)
	if len(parts) == 0 {
		return nil, true
	}
	ids := make([]pgtype.UUID, 0, len(parts))
	for _, part := range parts {
		id, ok := parseUUIDOrBadRequest(w, part, fieldName)
		if !ok {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// parseActorFilterList 解析形如 "agent:<uuid>,member:<uuid>" 的 actor 过滤参数；每个元素
// 都必须是合法的角色类型 + 冒号 + UUID，否则写 400 并返回 false。
func parseActorFilterList(w http.ResponseWriter, raw, fieldName string) ([]issueActorFilter, bool) {
	parts := splitCommaParam(raw)
	if len(parts) == 0 {
		return nil, true
	}
	filters := make([]issueActorFilter, 0, len(parts))
	for _, part := range parts {
		pieces := strings.SplitN(part, ":", 2)
		if len(pieces) != 2 || !isIssueActorType(pieces[0]) || strings.TrimSpace(pieces[1]) == "" {
			writeError(w, http.StatusBadRequest, "invalid "+fieldName)
			return nil, false
		}
		id, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(pieces[1]), fieldName)
		if !ok {
			return nil, false
		}
		filters = append(filters, issueActorFilter{
			actorType: pieces[0],
			actorID:   id,
		})
	}
	return filters, true
}

// ListGroupedIssues 处理 GET /api/issues/grouped：只支持 group_by=assignee，按受理人
// 分组返回（每组带 total），用于工作区 Members/Agents/Squads 分组视图。分页按「组内偏移」
// 语义：先用 ROW_NUMBER() 给每个组内的行编号，再按窗口取行，保证跨页翻组不重不漏。
func (h *Handler) ListGroupedIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.DB == nil {
		writeError(w, http.StatusInternalServerError, "database is unavailable")
		return
	}

	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "assignee"
	}
	if groupBy != "assignee" {
		writeError(w, http.StatusBadRequest, "unsupported group_by")
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v > 0 {
			offset = v
		}
	}

	where := []string{"i.workspace_id = $1"}
	args := []any{wsUUID}
	addArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	statuses := splitCommaParam(r.URL.Query().Get("statuses"))
	if len(statuses) == 0 {
		statuses = splitCommaParam(r.URL.Query().Get("status"))
	}
	if len(statuses) > 0 {
		where = append(where, fmt.Sprintf("i.status = ANY(%s::text[])", addArg(statuses)))
	}
	// 见 ListIssues：按类别过滤是「工作区加自定义状态后看板列数仍固定」的关键。 (MUL-6243)
	statusCategories := splitCommaParam(r.URL.Query().Get("status_categories"))
	if len(statusCategories) == 0 {
		statusCategories = splitCommaParam(r.URL.Query().Get("status_category"))
	}
	if len(statusCategories) > 0 {
		// 见 ListIssues：展开成 key 才能继续走 (workspace_id, status) 索引。 (MUL-6243)
		keys, err := issuestatus.ExpandCategories(r.Context(), h.Queries, wsUUID, statusCategories)
		if err != nil {
			if writeClientClosedIfCanceled(w, err) {
				return
			}
			slog.Warn("expand status categories failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve status categories")
			return
		}
		where = append(where, fmt.Sprintf("i.status = ANY(%s::text[])", addArg(keys)))
	}

	priorities := splitCommaParam(r.URL.Query().Get("priorities"))
	if len(priorities) == 0 {
		priorities = splitCommaParam(r.URL.Query().Get("priority"))
	}
	if len(priorities) > 0 {
		where = append(where, fmt.Sprintf("i.priority = ANY(%s::text[])", addArg(priorities)))
	}

	assigneeTypes := splitCommaParam(r.URL.Query().Get("assignee_types"))
	if len(assigneeTypes) > 0 {
		for _, assigneeType := range assigneeTypes {
			if !isIssueActorType(assigneeType) {
				writeError(w, http.StatusBadRequest, "invalid assignee_types")
				return
			}
		}
		where = append(where, fmt.Sprintf("i.assignee_type = ANY(%s::text[])", addArg(assigneeTypes)))
	}

	if raw := r.URL.Query().Get("assignee_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "assignee_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.assignee_id = %s::uuid", addArg(id)))
	}
	if raw := r.URL.Query().Get("assignee_ids"); raw != "" {
		ids, ok := parseUUIDParamList(w, raw, "assignee_ids")
		if !ok {
			return
		}
		if len(ids) > 0 {
			where = append(where, fmt.Sprintf("i.assignee_id = ANY(%s::uuid[])", addArg(ids)))
		}
	}
	if raw := r.URL.Query().Get("creator_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "creator_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.creator_id = %s::uuid", addArg(id)))
	}
	if raw := r.URL.Query().Get("project_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "project_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.project_id = %s::uuid", addArg(id)))
	}
	if filter, ok := parseMetadataFilterParam(w, r.URL.Query().Get("metadata")); !ok {
		return
	} else if filter != nil {
		where = append(where, fmt.Sprintf("i.metadata @> %s::jsonb", addArg(string(filter))))
	}
	if filter, ok := parsePropertiesFilterParam(w, r.URL.Query().Get("properties")); !ok {
		return
	} else if filter != nil {
		where = append(where, propertiesFilterPredicate(filter, addArg))
	}
	// 镜像 sqlc 的 ListIssues / ListOpenIssues / CountIssues 里的 4 分支 involves_user_id
	// UNION。ListGroupedIssues 是手写动态 SQL，不与 sqlc 共享参数，所以这段要在这里逐字
	// 复刻、保持一致。按设计排除「成员直接分配」：那是标签页 1（assignee_id）的语义，
	// 标签页 3 必须与标签页 1 不相交。
	if raw := r.URL.Query().Get("involves_user_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "involves_user_id")
		if !ok {
			return
		}
		ref := addArg(id)
		where = append(where, fmt.Sprintf(`(
    (i.assignee_type = 'agent' AND i.assignee_id IN (
       SELECT a.id FROM agent a
        WHERE a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
    OR (i.assignee_type = 'squad' AND i.assignee_id IN (
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'member'
          AND sm.member_id   = %[1]s::uuid
       UNION
       SELECT s.id
         FROM squad s
         JOIN agent a ON a.id = s.leader_id
        WHERE s.workspace_id = $1
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
       UNION
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
         JOIN agent a ON a.id = sm.member_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'agent'
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
)`, ref))
	}

	assigneeFilters, ok := parseActorFilterList(w, r.URL.Query().Get("assignee_filters"), "assignee_filters")
	if !ok {
		return
	}
	includeNoAssignee := r.URL.Query().Get("include_no_assignee") == "true"
	if len(assigneeFilters) > 0 || includeNoAssignee {
		ors := make([]string, 0, len(assigneeFilters)+1)
		for _, filter := range assigneeFilters {
			ors = append(ors, fmt.Sprintf(
				"(i.assignee_type = %s::text AND i.assignee_id = %s::uuid)",
				addArg(filter.actorType),
				addArg(filter.actorID),
			))
		}
		if includeNoAssignee {
			ors = append(ors, "(i.assignee_type IS NULL AND i.assignee_id IS NULL)")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	creatorFilters, ok := parseActorFilterList(w, r.URL.Query().Get("creator_filters"), "creator_filters")
	if !ok {
		return
	}
	if len(creatorFilters) > 0 {
		ors := make([]string, 0, len(creatorFilters))
		for _, filter := range creatorFilters {
			ors = append(ors, fmt.Sprintf(
				"(i.creator_type = %s::text AND i.creator_id = %s::uuid)",
				addArg(filter.actorType),
				addArg(filter.actorID),
			))
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	projectIDs, ok := parseUUIDParamList(w, r.URL.Query().Get("project_ids"), "project_ids")
	if !ok {
		return
	}
	includeNoProject := r.URL.Query().Get("include_no_project") == "true"
	if len(projectIDs) > 0 || includeNoProject {
		ors := make([]string, 0, 2)
		if len(projectIDs) > 0 {
			ors = append(ors, fmt.Sprintf("i.project_id = ANY(%s::uuid[])", addArg(projectIDs)))
		}
		if includeNoProject {
			ors = append(ors, "i.project_id IS NULL")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	labelIDs, ok := parseUUIDParamList(w, r.URL.Query().Get("label_ids"), "label_ids")
	if !ok {
		return
	}
	if len(labelIDs) > 0 {
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM issue_to_label itl WHERE itl.issue_id = i.id AND itl.label_id = ANY(%s::uuid[]))",
			addArg(labelIDs),
		))
	}

	dateFilter, ok := parseIssueDateFilter(w, r.URL.Query())
	if !ok {
		return
	}
	where = appendIssueDateFilter(where, addArg, dateFilter)

	if groupAssigneeType := r.URL.Query().Get("group_assignee_type"); groupAssigneeType != "" {
		if groupAssigneeType == "none" {
			where = append(where, "(i.assignee_type IS NULL AND i.assignee_id IS NULL)")
		} else {
			if !isIssueActorType(groupAssigneeType) {
				writeError(w, http.StatusBadRequest, "invalid group_assignee_type")
				return
			}
			rawID := r.URL.Query().Get("group_assignee_id")
			if rawID == "" {
				writeError(w, http.StatusBadRequest, "invalid group_assignee_id")
				return
			}
			assigneeID, ok := parseUUIDOrBadRequest(w, rawID, "group_assignee_id")
			if !ok {
				return
			}
			where = append(where, fmt.Sprintf(
				"(i.assignee_type = %s::text AND i.assignee_id = %s::uuid)",
				addArg(groupAssigneeType),
				addArg(assigneeID),
			))
		}
	}

	sortCol := "position"
	sortIsExpr := false
	sortIsProperty := false
	if s := r.URL.Query().Get("sort"); s != "" {
		switch s {
		case "position", "title", "created_at", "updated_at", "start_date", "due_date":
			sortCol = s
		case "last_activity":
			sortCol = "last_activity_at"
		case "status":
			sortCol = "CASE i.status WHEN 'backlog' THEN 0 WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'in_review' THEN 3 WHEN 'done' THEN 4 WHEN 'blocked' THEN 5 WHEN 'cancelled' THEN 6 ELSE 7 END"
			sortIsExpr = true
		case "priority":
			sortCol = "CASE i.priority WHEN 'important_urgent' THEN 0 WHEN 'important_not_urgent' THEN 1 WHEN 'not_important_urgent' THEN 2 WHEN 'not_important_not_urgent' THEN 3 ELSE 4 END"
			sortIsExpr = true
		default:
			// property:<definitionId> 按自定义属性值（类型化表达式）排序；
			// 未知/已归档的属性定义降级为按 position 排序，避免把老客户端直接打挂。
			expr, handled, sortErr := h.propertySortExpr(r, workspaceID, s)
			if !handled {
				writeError(w, http.StatusBadRequest, "invalid sort value")
				return
			}
			if sortErr != nil {
				if sortErr.Error() == "invalid sort value" || sortErr.Error() == "invalid workspace id" {
					writeError(w, http.StatusBadRequest, sortErr.Error())
					return
				}
				slog.Warn("propertySortExpr failed", append(logger.RequestAttrs(r), "error", sortErr)...)
				writeError(w, http.StatusInternalServerError, "failed to resolve sort")
				return
			}
			if expr != "" {
				sortCol = expr
				sortIsExpr = true
				sortIsProperty = true
			}
		}
	}
	sortDir := "ASC"
	if sortCol == "last_activity_at" {
		sortDir = "DESC"
	}
	if sortCol != "position" {
		if d := r.URL.Query().Get("direction"); d != "" {
			switch strings.ToLower(d) {
			case "asc":
				sortDir = "ASC"
			case "desc":
				sortDir = "DESC"
			default:
				writeError(w, http.StatusBadRequest, "invalid direction value")
				return
			}
		}
	}

	intraGroupOrder := sortCol
	if !sortIsExpr {
		intraGroupOrder = "i." + sortCol
	}
	intraGroupOrder += " " + sortDir
	if sortCol == "start_date" || sortCol == "due_date" || sortCol == "last_activity_at" || sortIsProperty {
		intraGroupOrder += " NULLS LAST"
	}
	// 唯一末位键——见 ListIssues：created_at 并列会让 ROW_NUMBER() 在跨组翻页时不稳定。
	if sortCol == "last_activity_at" {
		intraGroupOrder += ", i.id DESC"
	} else {
		intraGroupOrder += ", i.created_at DESC, i.id DESC"
	}

	offsetRef := addArg(int64(offset))
	limitRef := addArg(int64(limit))
	query := fmt.Sprintf(`
WITH ranked AS (
	SELECT
		i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
		i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.position, i.start_date, i.due_date, i.created_at, i.updated_at, i.last_activity_at,
		i.number, i.project_id, i.metadata, i.stage, i.properties, i.revision,
		COUNT(*) OVER (PARTITION BY i.assignee_type, i.assignee_id) AS group_total,
		ROW_NUMBER() OVER (
			PARTITION BY i.assignee_type, i.assignee_id
			ORDER BY %s
		) AS rn
	FROM issue i
	WHERE %s
)
SELECT
	id, workspace_id, title, description, status, priority,
	assignee_type, assignee_id, creator_type, creator_id,
	parent_issue_id, position, start_date, due_date, created_at, updated_at, last_activity_at,
	number, project_id, metadata, stage, properties, revision, group_total
FROM ranked
WHERE rn > %s AND rn <= %s + %s
ORDER BY
	CASE assignee_type
		WHEN 'member' THEN 0
		WHEN 'agent' THEN 1
		WHEN 'squad' THEN 2
		ELSE 3
	END,
	assignee_type NULLS LAST,
	assignee_id NULLS LAST,
	rn`, intraGroupOrder, strings.Join(where, " AND "), offsetRef, offsetRef, limitRef)

	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		slog.Warn("ListGroupedIssues query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list grouped issues")
		return
	}
	defer rows.Close()

	groupedRows := []groupedIssueRow{}
	for rows.Next() {
		var row groupedIssueRow
		if err := rows.Scan(
			&row.ID,
			&row.WorkspaceID,
			&row.Title,
			&row.Description,
			&row.Status,
			&row.Priority,
			&row.AssigneeType,
			&row.AssigneeID,
			&row.CreatorType,
			&row.CreatorID,
			&row.ParentIssueID,
			&row.Position,
			&row.StartDate,
			&row.DueDate,
			&row.CreatedAt,
			&row.UpdatedAt,
			&row.LastActivityAt,
			&row.Number,
			&row.ProjectID,
			&row.Metadata,
			&row.Stage,
			&row.Properties,
			&row.Revision,
			&row.GroupTotal,
		); err != nil {
			if writeClientClosedIfCanceled(w, err) {
				return
			}
			slog.Warn("ListGroupedIssues scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list grouped issues")
			return
		}
		groupedRows = append(groupedRows, row)
	}
	if err := rows.Err(); err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		slog.Warn("ListGroupedIssues rows failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list grouped issues")
		return
	}

	ids := make([]pgtype.UUID, len(groupedRows))
	for i, row := range groupedRows {
		ids[i] = row.ID
	}
	labelsMap := h.labelsByIssue(ctx, wsUUID, ids)
	prefix := h.getIssuePrefix(ctx, wsUUID)
	// 整页共用一个 Resolver——每行建一个填充器就会对每行自定义状态读一次目录。 (MUL-6243)
	fillGrouped := h.newStatusCategoryFiller(ctx, wsUUID)

	groups := []IssueAssigneeGroupResponse{}
	groupIndex := map[string]int{}
	for _, row := range groupedRows {
		groupID := assigneeGroupID(row.AssigneeType, row.AssigneeID)
		idx, exists := groupIndex[groupID]
		if !exists {
			idx = len(groups)
			groupIndex[groupID] = idx
			groups = append(groups, IssueAssigneeGroupResponse{
				ID:           groupID,
				AssigneeType: textToPtr(row.AssigneeType),
				AssigneeID:   uuidToPtr(row.AssigneeID),
				Issues:       []IssueResponse{},
				Total:        row.GroupTotal,
			})
		}

		issue := issueListRowToResponse(row.ListIssuesRow, prefix)
		fillGrouped(&issue)
		labels := labelsMap[issue.ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		issue.Labels = &labels
		groups[idx].Issues = append(groups[idx].Issues, issue)
	}

	writeJSON(w, http.StatusOK, GroupedIssuesResponse{Groups: groups})
}

// GetIssue 处理 GET /api/issues/:id。经 loadIssueForUser 加载并校验该 issue 属于当前用户
// 可见的工作区（:id 可为 UUID 或 MUL-123 标识符），随后带上状态类别、标签、表情回应、
// 附件与只读的 source_context 详情快照一并返回。
func (h *Handler) GetIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	resp := issueToResponse(issue, prefix)
	h.fillStatusCategory(r.Context(), issue.WorkspaceID, &resp)
	detailLabels := h.labelsByIssue(r.Context(), issue.WorkspaceID, []pgtype.UUID{issue.ID})[uuidToString(issue.ID)]
	if detailLabels == nil {
		detailLabels = []LabelResponse{}
	}
	resp.Labels = &detailLabels

	// 取 issue 的表情回应。
	reactions, err := h.Queries.ListIssueReactions(r.Context(), issue.ID)
	if err == nil && len(reactions) > 0 {
		resp.Reactions = make([]IssueReactionResponse, len(reactions))
		for i, rx := range reactions {
			resp.Reactions[i] = issueReactionToResponse(rx)
		}
	}

	// 取 issue 级附件。
	attachments, err := h.Queries.ListAttachmentsByIssue(r.Context(), db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err == nil && len(attachments) > 0 {
		mode := attachmentURLModeFromRequest(r)
		resp.Attachments = make([]AttachmentResponse, len(attachments))
		for i, a := range attachments {
			resp.Attachments[i] = h.attachmentToResponse(a, mode)
		}
	}

	if sourceContext, err := h.issueSourceContextDetail(r.Context(), issue); err == nil {
		resp.SourceContext = sourceContext
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("load issue source context failed", append(logger.RequestAttrs(r), "issue_id", uuidToString(issue.ID), "error", err)...)
		// 冻结的 source_context 是 issue 执行契约的一部分，不是可选的装饰。没有它仍返回
		// 成功的详情响应，会让 agent 在指令悄悄不全的情况下开跑。
		writeError(w, http.StatusInternalServerError, "failed to load issue source context")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// ListChildIssues 处理 GET /api/issues/:id/children：返回某 issue 的直接子任务列表
// （:id 同样可为 UUID 或标识符，先经 loadIssueForUser 校验可见性）。
func (h *Handler) ListChildIssues(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	children, err := h.Queries.ListChildIssues(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list child issues")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	ids := make([]pgtype.UUID, len(children))
	for i, child := range children {
		ids[i] = child.ID
	}
	labelsMap := h.labelsByIssue(r.Context(), issue.WorkspaceID, ids)
	// 子任务进度是根据这些行算出来的（CLI 的 `issue children` 关卡计数等），所以它们必须带
	// 解析后的类别——自定义的 done 状态必须算作 done。整批共用一个 Resolver：内置状态仍零查询，
	// 一整批自定义状态也只读一次目录。
	statusResolver := issuestatus.NewResolver(issue.WorkspaceID)
	resp := make([]IssueResponse, len(children))
	for i, child := range children {
		resp[i] = issueToResponse(child, prefix)
		resp[i].StatusCategory = statusResolver.Effective(r.Context(), h.Queries, child.Status)
		labels := labelsMap[resp[i].ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		resp[i].Labels = &labels
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issues": resp,
	})
}

// listChildrenByParentsLimit 是一次请求最多展开多少个父级子任务的封顶值。泳道视图可见的
// 泳道数量天然受屏幕限制（通常 <= 50），但显式封顶是为了防恶意调用方拿 ANY() 在单次
// 往返里扫整个工作区的 issue 集合。
const listChildrenByParentsLimit = 200

// ListChildrenByParents 返回所给父 issue 们子任务的并集，替代泳道视图挂载时本会发出的
// N 次请求（每个可见父泳道一次 GET /issues/:id/children）。
//
// 工作区边界在查询层强制：任何不属于调用方工作区的 parent_id 只会得到零个子任务，因此
// 调用方无法跨工作区探测父 issue。
func (h *Handler) ListChildrenByParents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	raw := r.URL.Query().Get("parent_ids")
	if raw == "" {
		// 空输入是空响应（不是错误）——泳道视图即使一条可见泳道都没有也会无条件调用这里，
		// 简化客户端逻辑。
		writeJSON(w, http.StatusOK, map[string]any{"issues": []IssueResponse{}})
		return
	}

	parts := strings.Split(raw, ",")
	if len(parts) > listChildrenByParentsLimit {
		writeError(w, http.StatusBadRequest, "too many parent_ids")
		return
	}
	parentIDs := make([]pgtype.UUID, 0, len(parts))
	for _, s := range parts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, ok := parseUUIDOrBadRequest(w, s, "parent_ids")
		if !ok {
			return
		}
		parentIDs = append(parentIDs, id)
	}
	if len(parentIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"issues": []IssueResponse{}})
		return
	}

	children, err := h.Queries.ListChildrenByParents(r.Context(), db.ListChildrenByParentsParams{
		WorkspaceID: wsUUID,
		ParentIds:   parentIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list child issues")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	ids := make([]pgtype.UUID, len(children))
	for i, child := range children {
		ids[i] = child.ID
	}
	labelsMap := h.labelsByIssue(r.Context(), wsUUID, ids)
	// 子任务进度是根据这些行算出来的（CLI 的 `issue children` 关卡计数等），所以它们必须带
	// 解析后的类别——自定义的 done 状态必须算作 done。整批共用一个 Resolver：内置状态仍零查询，
	// 一整批自定义状态也只读一次目录。
	statusResolver := issuestatus.NewResolver(wsUUID)
	resp := make([]IssueResponse, len(children))
	for i, child := range children {
		resp[i] = issueToResponse(child, prefix)
		resp[i].StatusCategory = statusResolver.Effective(r.Context(), h.Queries, child.Status)
		labels := labelsMap[resp[i].ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		resp[i].Labels = &labels
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issues": resp,
	})
}

// ChildIssueProgress 处理 GET /api/issues/child-progress：返回各父 issue 下已完成/终态
// 子任务数与总数，供前端快速渲染进度（terminalIssueStatusKeys 会把自定义的 done/cancelled
// 归类展开）。
func (h *Handler) ChildIssueProgress(w http.ResponseWriter, r *http.Request) {
	wsID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace_id")
	if !ok {
		return
	}

	terminalStatusKeys, err := h.terminalIssueStatusKeys(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve status categories")
		return
	}
	rows, err := h.Queries.ChildIssueProgress(r.Context(), db.ChildIssueProgressParams{
		WorkspaceID:        wsUUID,
		TerminalStatusKeys: terminalStatusKeys,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get child issue progress")
		return
	}

	type progressEntry struct {
		ParentIssueID string `json:"parent_issue_id"`
		Total         int64  `json:"total"`
		Done          int64  `json:"done"`
	}
	resp := make([]progressEntry, len(rows))
	for i, row := range rows {
		resp[i] = progressEntry{
			ParentIssueID: uuidToString(row.ParentIssueID),
			Total:         row.Total,
			Done:          row.Done,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"progress": resp,
	})
}

// QuickCreateIssueRequest 是 POST /api/issues/quick-create 的请求体。用户在弹窗里选一个
// 角色（agent 或 squad）并输入一行自然语言；服务端先校验该角色的可达性，把快速创建任务排队，
// 立即返回 202。agent 在后台把提示翻译成 `multica issue create` 调用；成败都以收件箱通知
// 的形式回给请求者。
//
// AgentID / SquadID 必须恰好填一个。填 SquadID 时任务派给该 squad 的 leader agent，leader
// 会收到与「派给 squad 的 issue」一样的 Operating Protocol 简报，方便它像往常那样委派给成员。
//
// ProjectID 可选，让弹窗能锁定某个项目，agent 的 `multica issue create` 就会带 --project
// <uuid> 而不是让它走默认。前端会记住每个工作区里用户上次的选择，高频用户不必重打
// "in project X"。
//
// ParentIssueID 可选，由「添加子任务」入口在从某个 issue 打开弹窗时设置。agent 会以
// --parent <uuid> 透传，让新 issue 落成子任务——无论用户走手动还是 agent 模式，入口的
// 「子任务」意图都保持不变。
type QuickCreateIssueRequest struct {
	AgentID       string   `json:"agent_id,omitempty"`
	SquadID       string   `json:"squad_id,omitempty"`
	Prompt        string   `json:"prompt"`
	Priority      string   `json:"priority,omitempty"`
	DueDate       string   `json:"due_date,omitempty"`
	ProjectID     string   `json:"project_id,omitempty"`
	ParentIssueID string   `json:"parent_issue_id,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
}

// QuickCreateIssueResponse 回显已排队任务的 id，前端借此关联随后的收件箱条目——尽管完成
// 完全是异步的。
type QuickCreateIssueResponse struct {
	TaskID string `json:"task_id"`
}

// QuickCreateIssue 处理 POST /api/issues/quick-create：校验自然语言提示与角色选择后把任务
// 交给 TaskService 排队，立即返回 202 + task_id。解析校验失败（缺 prompt、priority/due_date
// 格式错、agent/squad 二选一被破坏）返回 400；角色不可达返回 422（带稳定 code），daemon
// 版本过老也返回 422。
func (h *Handler) QuickCreateIssue(w http.ResponseWriter, r *http.Request) {
	var req QuickCreateIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	priority := strings.ToLower(strings.TrimSpace(req.Priority))
	if priority != "" && priority != "important_urgent" && priority != "important_not_urgent" && priority != "not_important_urgent" && priority != "not_important_not_urgent" {
		writeError(w, http.StatusBadRequest, "priority must be one of: important_urgent, important_not_urgent, not_important_urgent, not_important_not_urgent")
		return
	}
	dueDate := strings.TrimSpace(req.DueDate)
	if dueDate != "" {
		parsed, err := util.ParseCalendarDate(dueDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
			return
		}
		dueDate = parsed.Time.Format("2006-01-02")
	}

	hasAgent := strings.TrimSpace(req.AgentID) != ""
	hasSquad := strings.TrimSpace(req.SquadID) != ""
	if hasAgent == hasSquad {
		writeError(w, http.StatusBadRequest, "exactly one of agent_id or squad_id is required")
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	requesterID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	requesterUUID, ok := parseUUIDOrBadRequest(w, requesterID, "requester_id")
	if !ok {
		return
	}

	// 把选的角色解析成真正执行任务的 agent：选 agent 就是它自己；选 squad 则是 squad 的
	// leader agent。leader 派单时会收到 squad-leader 简报（见 daemon.go），与「issue 派给
	// squad」的行为一致——在这里选 squad 功能上等于「请 squad leader 代表 squad 创建这条
	// issue」。
	var agentUUID pgtype.UUID
	var squadUUID pgtype.UUID
	if hasSquad {
		var ok bool
		squadUUID, ok = parseUUIDOrBadRequest(w, req.SquadID, "squad_id")
		if !ok {
			return
		}
		squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
			ID:          squadUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "squad not found")
			return
		}
		if squad.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "squad is archived")
			return
		}
		agentUUID = squad.LeaderID
	} else {
		var ok bool
		agentUUID, ok = parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
		if !ok {
			return
		}
	}

	// 复用 validateAssigneePair 的「工作区成员 / 未归档 / 私密 agent 归属」规则，防止用户
	// POST 一个本不该被派发的私密 agent_id（前端会过滤掉它们，但 handler 才是信任边界）。
	// 选 squad 会以解析出的 leader agent 走到这里，同样的规则适用——藏在用户够不着的 squad
	// 后面的私密 leader 同样该被拒绝。
	if status, msg := h.validateAssigneePair(
		r.Context(), r, workspaceID,
		pgtype.Text{String: "agent", Valid: true},
		agentUUID,
		scopeNoDelegation(),
	); status != 0 {
		writeError(w, status, msg)
		return
	}

	// 为下面的运行时存活检查重新加载 agent。这一步由构造保证安全：validateAssigneePair 刚
	// 确认过它存在于本工作区且调用方可视。
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	// 快速创建要求 agent 现在就能跑，所以任何「没就绪」的判定都拒绝——但要带判定自己的
	// reason_code，这样「CLI 跑不了」不再伪装成「runtime 离线」，用户就不会被引导去重连一台
	// 其实已经连上的机器（MUL-6164）。
	if verdict, err := service.AgentReadiness(r.Context(), h.runtimeLookup(obsmetrics.RuntimeLookupSourceIssue), agent); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check agent runtime")
		return
	} else if !verdict.Ready() {
		writeAgentUnavailable(w, verdict.Detail, verdict.Reason)
		return
	}

	// daemon CLI 版本闸门。agent 侧的提示 + 创建流程依赖 MinQuickCreateCLIVersion 之后引入
	// 的行为（URL 附件处理、快速创建的附件绑定、部分失败不重试）。更老的 daemon 会在 CLI
	// 部分失败时重复建 issue、丢附件绑定、或错误处理粘贴的截图 URL；在入队前关死闸门，而不是
	// 二十秒后把故障包装成收件箱失败再冒出来。开发自建的 daemon（git-describe 形态）在
	// CheckMinCLIVersion 内部豁免，让 `make daemon` 不用削弱 staging/production 也能跑。
	if status, payload := h.checkQuickCreateDaemonVersion(r.Context(), obsmetrics.RuntimeLookupSourceIssue, agent.RuntimeID); status != 0 {
		writeJSON(w, status, payload)
		return
	}
	if priority != "" || dueDate != "" {
		if status, payload := h.checkQuickCreateDaemonVersionAtLeast(
			r.Context(), obsmetrics.RuntimeLookupSourceIssue, agent.RuntimeID, agentpkg.MinQuickCreateFieldsCLIVersion,
		); status != 0 {
			writeJSON(w, status, payload)
			return
		}
	}

	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	// 可选的 project_id——先把归属校验限制在同工作区再钉到任务上。handler 是信任边界；
	// 前端只展示当前工作区的项目，但这里再查一遍，防止伪造请求夹带外部项目 ID 混进来。
	var projectUUID pgtype.UUID
	if strings.TrimSpace(req.ProjectID) != "" {
		pid, ok := parseUUIDOrBadRequest(w, req.ProjectID, "project_id")
		if !ok {
			return
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID:          pid,
			WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "project not found")
			return
		}
		projectUUID = pid
	}

	// 可选的 parent_issue_id——像普通 CreateIssue 路径那样校验同工作区归属。前端从「添加
	// 子任务」入口播种该值，但 handler 会再查一遍，防止伪造请求夹带外部 parent UUID 混进来。
	var parentIssueUUID pgtype.UUID
	if strings.TrimSpace(req.ParentIssueID) != "" {
		pid, ok := parseUUIDOrBadRequest(w, req.ParentIssueID, "parent_issue_id")
		if !ok {
			return
		}
		parent, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          pid,
			WorkspaceID: wsUUID,
		})
		if err != nil || !parent.ID.Valid {
			writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
			return
		}
		parentIssueUUID = pid
	}

	task, err := h.TaskService.EnqueueQuickCreateTask(r.Context(), wsUUID, requesterUUID, agentUUID, squadUUID, prompt, priority, dueDate, projectUUID, parentIssueUUID, attachmentIDs)
	if err != nil {
		slog.Warn("quick-create enqueue failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to enqueue quick-create task")
		return
	}

	writeJSON(w, http.StatusAccepted, QuickCreateIssueResponse{TaskID: uuidToString(task.ID)})
}

// writeAgentUnavailable 返回 422 + 稳定的错误码，弹窗据此可以给出「换一个 agent」的提示
// 而不必解析人话原因。`code` 对已装客户端保持 agent_unavailable；reason_code 携带他们排障
// 需要的机器可读区分——agent_runtime_required（「绑定一个 runtime」）不是 runtime_offline
// （「重连这台机器」）。
func writeAgentUnavailable(w http.ResponseWriter, reason string, reasonCode dispatch.ReasonCode) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	json.NewEncoder(w).Encode(map[string]any{
		"code":        "agent_unavailable",
		"reason":      reason,
		"reason_code": reasonCode,
	})
}

// isRuntimeOnline 返回指定 runtime 当前是否可达（status == "online"）。快速创建会拒绝
// runtime 离线的 agent 提交，让用户在弹窗里就得到即时反馈，而不是二十秒后在收件箱里看到失败。
func (h *Handler) isRuntimeOnline(ctx context.Context, runtimeID pgtype.UUID) bool {
	rt, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceIssue, runtimeID)
	if err != nil {
		return false
	}
	return rt.Status == "online"
}

// checkQuickCreateDaemonVersion 用 MinQuickCreateCLIVersion 校验 daemon 注册时上报的 CLI
// 版本（存在 runtime 行 metadata.cli_version 里）。版本可接受返回 (0, nil)，否则返回可直接
// 交给 writeJSON 的 (status, payload)。
//
// 失败形态稳定，弹窗可按 `code` 字段分支并给出指向具体 runtime 的「需要升级」提示：
//
//	422 {
//	  "code": "daemon_version_unsupported",
//	  "current_version": "0.2.18" | "",
//	  "min_version":     "0.2.21",
//	  "runtime_id":      "<uuid>"
//	}
func (h *Handler) checkQuickCreateDaemonVersion(ctx context.Context, source string, runtimeID pgtype.UUID) (int, map[string]any) {
	return h.checkQuickCreateDaemonVersionAtLeast(ctx, source, runtimeID, agentpkg.MinQuickCreateCLIVersion)
}

// checkQuickCreateDaemonVersionAtLeast 是带最低版本参数的通用版，供额外需要更高版本的
// 功能点（如同时传了 priority/due_date）复用。
func (h *Handler) checkQuickCreateDaemonVersionAtLeast(ctx context.Context, source string, runtimeID pgtype.UUID, minimum string) (int, map[string]any) {
	rt, err := h.getAgentRuntime(ctx, source, runtimeID)
	if err != nil {
		// 在在线检查之后、这里之前的间隙里 runtime 行消失了——按不可用来处理，
		// 别用 500 把请求卡死。
		return http.StatusUnprocessableEntity, map[string]any{
			"code":   "agent_unavailable",
			"reason": "agent's runtime is no longer registered",
		}
	}
	current := readRuntimeCLIVersion(rt.Metadata)
	switch err := agentpkg.CheckMinCLIVersionFor(current, minimum); {
	case err == nil:
		return 0, nil
	case errors.Is(err, agentpkg.ErrCLIVersionMissing), errors.Is(err, agentpkg.ErrCLIVersionTooOld):
		return http.StatusUnprocessableEntity, map[string]any{
			"code":            "daemon_version_unsupported",
			"current_version": current,
			"min_version":     minimum,
			"runtime_id":      uuidToString(runtimeID),
		}
	default:
		// 防御性的兜底：版本检查里冒出的未知错误同样失败关停——这道闸门存在正是因为
		// 信不过老 daemon 跑这个流程。
		return http.StatusUnprocessableEntity, map[string]any{
			"code":            "daemon_version_unsupported",
			"current_version": current,
			"min_version":     minimum,
			"runtime_id":      uuidToString(runtimeID),
		}
	}
}

// readRuntimeCLIVersion 从 runtime 行的 metadata.cli_version 取值。metadata 列线上是
// JSONB；daemon 在注册时把 multica CLI 版本存在这个 key 下（见 DaemonRegister）。
func readRuntimeCLIVersion(metadata []byte) string {
	if len(metadata) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil {
		return ""
	}
	if v, ok := m["cli_version"].(string); ok {
		return v
	}
	return ""
}

// CreateIssueRequest 是 POST /api/issues 的请求体（标题必填，其余字段选填）。
type CreateIssueRequest struct {
	Title         string   `json:"title"`
	Description   *string  `json:"description"`
	Status        string   `json:"status"`
	Priority      string   `json:"priority"`
	AssigneeType  *string  `json:"assignee_type"`
	AssigneeID    *string  `json:"assignee_id"`
	ParentIssueID *string  `json:"parent_issue_id"`
	ProjectID     *string  `json:"project_id"`
	Stage         *int32   `json:"stage,omitempty"`
	StartDate     *string  `json:"start_date"`
	DueDate       *string  `json:"due_date"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
	// LabelIDs 是创建时要在同一事务里挂到新 issue 上的 issue 级标签。未知或非 issue 标签
	// 以 400（service.ErrIssueLabelNotFound）拒绝，而不是被悄悄丢掉。
	LabelIDs []string `json:"label_ids,omitempty"`
	// OriginType / OriginID 给新 issue 盖来源章，平台内部流程之后才能确定性地找到它。只有
	// 可信调用方该设置——目前是 daemon CLI 为快速创建任务透传
	//（origin_type=quick_create，origin_id=agent_task_queue.id）。
	OriginType *string `json:"origin_type,omitempty"`
	OriginID   *string `json:"origin_id,omitempty"`

	AllowDuplicate bool `json:"allow_duplicate,omitempty"`
}

// duplicateIssueMessage 生成发现活跃重复 issue 时给客户端的提示文案。
func duplicateIssueMessage(issue IssueResponse) string {
	return issueguard.DuplicateMessage(issue.Identifier, issue.Title, issue.Status)
}

// CreateIssue 处理 POST /api/issues：校验并创建一条 issue。创建会走 IssueService.Create
// 的原子事务（父级边界、项目存在性、查重都在里面判定），成功广播 issue:created 事件并返回
// 201 + 完整响应。各类业务失败各有专属状态码：活跃重复返回 409 + 已存在 issue，父级/项目/
// 标签找不到返回 400，目标状态在写入间隙被归档返回 409。
func (h *Handler) CreateIssue(w http.ResponseWriter, r *http.Request) {
	var req CreateIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// 从上下文取创建者（由 auth 中间件写入）。
	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	status := req.Status
	if status == "" {
		status = "todo"
	}
	priority := req.Priority
	if priority == "" {
		priority = "not_important_not_urgent"
	}
	status, ok = h.resolveIssueStatusKey(w, r, wsUUID, status)
	if !ok {
		return
	}
	if !validateIssueEnum(w, "priority", priority, validIssuePriorities) {
		return
	}
	if req.Stage != nil && *req.Stage < 1 {
		writeError(w, http.StatusBadRequest, "stage must be >= 1")
		return
	}

	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if req.AssigneeType != nil {
		assigneeType = pgtype.Text{String: *req.AssigneeType, Valid: true}
	}
	if req.AssigneeID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
		if !ok {
			return
		}
		assigneeID = id
	}

	var parentIssueID pgtype.UUID
	var projectID pgtype.UUID
	var parentIssue *db.Issue
	if req.ParentIssueID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.ParentIssueID, "parent_issue_id")
		if !ok {
			return
		}
		parentIssueID = id
		if assigneeType.Valid && (assigneeType.String == "agent" || assigneeType.String == "squad") {
			parent, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
				ID:          parentIssueID,
				WorkspaceID: wsUUID,
			})
			if err != nil || !parent.ID.Valid {
				writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
				return
			}
			parentIssue = &parent
		}
	}

	// 无父级的 agent/squad 受理人创建没有「可以绑 autopilot 授权」的 issue，因此 scope 指定
	// 创建本身：只有经验证、仍在跑的 run_only autopilot 任务能借这里（MUL-6691——报告的
	// 流程是 leader 从零创建 DRA-109/DRA-110，而不是在 autopilot 建的 issue 下面）。
	assignScope := scopeChildOf(parentIssue)
	if req.ParentIssueID == nil {
		assignScope = scopeNewTopLevelIssue()
	}

	if status, msg := h.validateAssigneePair(r.Context(), r, workspaceID, assigneeType, assigneeID, assignScope); status != 0 {
		writeError(w, status, msg)
		return
	}

	if req.ProjectID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
		if !ok {
			return
		}
		projectID = id
	}
	// 项目存在性与最终的父级边界检查在 IssueService.Create 里与创建原子地完成。handler 只
	// 预载调用方提供的父级，因为受理人闸门需要把 autopilot 授权的回退绑定到这条服务端验证
	// 过的 issue 上（在放行之前）。
	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	labelIDs, ok := parseUUIDSliceOrBadRequest(w, req.LabelIDs, "label_ids")
	if !ok {
		return
	}

	var startDate pgtype.Date
	if req.StartDate != nil && *req.StartDate != "" {
		d, err := util.ParseCalendarDate(*req.StartDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start_date format, expected YYYY-MM-DD")
			return
		}
		startDate = d
	}

	var dueDate pgtype.Date
	if req.DueDate != nil && *req.DueDate != "" {
		d, err := util.ParseCalendarDate(*req.DueDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
			return
		}
		dueDate = d
	}

	// 确定创建者身份：agent（经 X-Agent-ID 头）或 member。
	creatorType, actualCreatorID := h.resolveActor(r, creatorID, workspaceID)

	// 可选的来源盖章（quick-create / autopilot）。只接受白名单内的 origin_type，
	// 其余一律拒绝，防止恶意调用方铸造任意的 origin 标签。两个字段必须成对出现。
	var originType pgtype.Text
	var originID pgtype.UUID
	if req.OriginType != nil || req.OriginID != nil {
		if req.OriginType == nil || req.OriginID == nil {
			writeError(w, http.StatusBadRequest, "origin_type and origin_id must be provided together")
			return
		}
		switch *req.OriginType {
		case "quick_create":
			// 允许——daemon CLI 从快速创建任务透传。
		default:
			writeError(w, http.StatusBadRequest, "unsupported origin_type")
			return
		}
		oid, ok := parseUUIDOrBadRequest(w, *req.OriginID, "origin_id")
		if !ok {
			return
		}
		originType = pgtype.Text{String: *req.OriginType, Valid: true}
		originID = oid
	} else if creatorType == "agent" {
		// MUL-4305：agent 走普通创建路径建 issue 时没有显式来源，历史上导致新 issue 无归属。
		// 任何后来由它派生的运行（agent 分配、squad-leader 触发）都会丢掉顶层的人类发起人，
		// 那些运行的 A2A @提及也就过不了针对私密 agent 的 canInvokeAgent 闸门。把执行中的
		// task 盖成 issue 的来源，让 resolveOriginatorForIssueTask 能继承其发起人——与
		// CreateComment 用 comment.source_task_id 是同一招（MUL-4015）。
		//
		// task id 取自服务端信任的 X-Task-ID：resolveActor 只有两种情况会返回
		// creatorType=="agent"——X-Actor-Source=task_token（auth 中间件已从 mat_ token 绑好
		// X-Agent-ID/X-Task-ID 并剥掉任何客户端值），或 X-Agent-ID/X-Task-ID 这一对经过 DB
		// 校验。成员伪造的 X-Task-ID 到不了这里，因为它会解析成 creatorType=="member"。
		// 即便如此，仍先复查 task 确实属于当前 agent 再信任它。
		if taskIDHeader := r.Header.Get("X-Task-ID"); taskIDHeader != "" {
			if taskUUID, perr := util.ParseUUID(taskIDHeader); perr == nil {
				if task, terr := h.Queries.GetAgentTask(r.Context(), taskUUID); terr == nil && uuidToString(task.AgentID) == actualCreatorID {
					originType = pgtype.Text{String: "agent_create", Valid: true}
					originID = taskUUID
				}
			}
		}
	}

	// 前缀是工作区级的；先算一次，让广播 payload 构建器和 HTTP 响应共用同一个值。
	prefix := h.getIssuePrefix(r.Context(), wsUUID)

	// 本次 create 共用一个填充器，广播 payload 与下方 HTTP 响应共享——自定义状态创建时
	// 每个请求读一次目录，而不是每个 payload 读一次。 (MUL-6243)
	fillCreated := h.newStatusCategoryFiller(r.Context(), wsUUID)

	// 埋点用的 agent id：派给 agent 时是受理 agent，否则是 agent 写的 issue 的创建 agent。
	// 在这里（而非 service 里）解析，因为创建者身份是 HTTP 侧的。
	analyticsAgentID := ""
	if assigneeType.Valid && assigneeType.String == "agent" {
		analyticsAgentID = uuidToString(assigneeID)
	}
	if creatorType == "agent" && analyticsAgentID == "" {
		analyticsAgentID = actualCreatorID
	}

	attachmentMode := attachmentURLModeFromRequest(r)
	buildAttachmentResponses := func(atts []db.Attachment) []AttachmentResponse {
		if len(atts) == 0 {
			return nil
		}
		out := make([]AttachmentResponse, len(atts))
		for i, a := range atts {
			out[i] = h.attachmentToResponse(a, attachmentMode)
		}
		return out
	}

	res, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:    wsUUID,
		Title:          req.Title,
		Description:    ptrToText(req.Description),
		Status:         status,
		Priority:       priority,
		AssigneeType:   assigneeType,
		AssigneeID:     assigneeID,
		CreatorType:    creatorType,
		CreatorID:      parseUUID(actualCreatorID),
		ParentIssueID:  parentIssueID,
		ProjectID:      projectID,
		StartDate:      startDate,
		DueDate:        dueDate,
		OriginType:     originType,
		OriginID:       originID,
		Stage:          ptrToInt4(req.Stage),
		AttachmentIDs:  attachmentIDs,
		LabelIDs:       labelIDs,
		AllowDuplicate: req.AllowDuplicate,
	}, service.IssueCreateOpts{
		ActorID:          actualCreatorID,
		AnalyticsAgentID: analyticsAgentID,
		Platform:         func() string { p, _, _ := middleware.ClientMetadataFromContext(r.Context()); return p }(),
		BroadcastPayload: func(issue db.Issue, atts []db.Attachment, labels []db.IssueLabel) map[string]any {
			payload := issueToResponse(issue, prefix)
			// 其他标签页收到的广播同样必须带类别——只给下面的 HTTP 响应填充就晚了，收到一条
			// 无法分栏的创建会让它们被迫整刷。与 HTTP 响应共享同一个填充器，自定义状态创建时
			// 每请求读一次目录而不是每 payload 一次。 (MUL-6243)
			fillCreated(&payload)
			payload.Attachments = buildAttachmentResponses(atts)
			// 带上权威的标签快照，让每个在线的客户端直接把新 issue 渲染成已贴标状态。
			// 非 nil（即使为空）指针 = 权威列表；旧流程单独的 issue_labels:changed 广播已移除。
			labelResponses := labelsToResponse(labels)
			payload.Labels = &labelResponses
			return map[string]any{"issue": payload}
		},
	})

	if errors.Is(err, service.ErrActiveDuplicate) {
		dup := *res.DuplicateIssue
		existing := issueToResponse(dup, h.getIssuePrefix(r.Context(), dup.WorkspaceID))
		h.fillStatusCategory(r.Context(), dup.WorkspaceID, &existing)
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":  "active_duplicate_issue",
			"error": duplicateIssueMessage(existing),
			"issue": existing,
		})
		return
	}
	if errors.Is(err, service.ErrParentIssueNotFound) {
		writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
		return
	}
	if errors.Is(err, service.ErrProjectNotFound) {
		writeError(w, http.StatusBadRequest, "project not found in this workspace")
		return
	}
	if errors.Is(err, service.ErrIssueLabelNotFound) {
		writeError(w, http.StatusBadRequest, "one or more labels not found in this workspace")
		return
	}
	if errors.Is(err, service.ErrIssueStatusUnavailable) {
		writeError(w, http.StatusConflict,
			"the target status was archived while this request was in flight; reload the status list and retry")
		return
	}
	if err != nil {
		slog.Warn("create issue failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create issue: "+err.Error())
		return
	}

	issue := res.Issue
	slog.Info("issue created", append(logger.RequestAttrs(r), "issue_id", uuidToString(issue.ID), "title", issue.Title, "status", issue.Status, "workspace_id", workspaceID)...)

	resp := issueToResponse(issue, prefix)
	fillCreated(&resp)
	resp.Attachments = buildAttachmentResponses(res.Attachments)
	// 回显创建事务里挂上的权威标签。总是非 nil（没有时是空切片），让新版客户端能确认后端
	// 理解了 label_ids，从而跳过它旧的「创建后补挂标签」回退逻辑。
	labelResponses := labelsToResponse(res.Labels)
	resp.Labels = &labelResponses
	// 直接创建在审核中的任务同样需要审查接手。
	if issuestatus.Effective(r.Context(), h.Queries, issue.WorkspaceID, issue.Status) == issuestatus.InReview {
		h.startReviewRound(r.Context(), issue)
	}
	writeJSON(w, http.StatusCreated, resp)
}

// UpdateIssueRequest 是 PATCH/PUT /api/issues/:id 的请求体。用指针 + expected_revision 等
// 手段表达「只改提交的字段」语义，并靠 rawFields 区分「字段没发」与「显式发 null 清空」。
type UpdateIssueRequest struct {
	ExpectedRevision *int64  `json:"expected_revision,omitempty"`
	Title            *string `json:"title"`
	// TitleBase 是编辑器产出 Title 之前采用的旧标题。它让标题编辑与无关的 issue 变更解耦，
	// 防止并发修改互相踩。
	TitleBase   *string `json:"title_base,omitempty"`
	Description *string `json:"description"`
	// DescriptionBase 是编辑器产出 Description 之前采信的权威 Markdown。它让服务端能保住
	// 该 base 之后异步到达的渠道媒体，同时不阻碍用户删除 base 里已有的媒体。老客户端不带
	// 它，会得到保守的渠道媒体保留行为。
	DescriptionBase *string  `json:"description_base,omitempty"`
	Status          *string  `json:"status"`
	Priority        *string  `json:"priority"`
	AssigneeType    *string  `json:"assignee_type"`
	AssigneeID      *string  `json:"assignee_id"`
	Position        *float64 `json:"position"`
	StartDate       *string  `json:"start_date"`
	DueDate         *string  `json:"due_date"`
	ParentIssueID   *string  `json:"parent_issue_id"`
	ProjectID       *string  `json:"project_id"`
	Stage           *int32   `json:"stage"`
	// AttachmentIDs 让描述编辑器把新上传的文件绑到本 issue，这样它们会出现在
	// GET /api/issues/:id/attachments 里，编辑器刷新后预览 Eye 仍然有效。已有绑定是幂等的——
	// 重复发同一个 id 是无操作。
	AttachmentIDs []string `json:"attachment_ids"`
	// SuppressRun 为 true 时照常应用受理人/状态变更，但跳过这次写入本会触发的 agent 运行
	// （「暂时不启动」——MUL-3375）。它不是撤销：变更已生效，之后可以手动运行/重跑来启动。
	// 可选；缺省或 false 保持现状。与 comment 的 suppress_agent_ids 对应。
	SuppressRun bool `json:"suppress_run,omitempty"`
	// HandoffNote 是可选的一段自由文本，在本次写入会启动 agent/squad 运行时注入运行的
	// 开场上下文（「交接说明」——MUL-3375）。只有真启动了运行才消费它：SuppressRun=true
	// 或未触发运行的写会丢掉它。绝不伪造 comment。
	HandoffNote string `json:"handoff_note,omitempty"`
}

// mergeIssueChannelMediaDescription 把渠道媒体（如图片块）合并进新的描述里：编辑器串行化
// 时可能弄丢/弄乱这些媒体标记，这里依据附件表与编辑器提交的 base 还原它们。
func mergeIssueChannelMediaDescription(current, incoming string, base *string, attachments []db.Attachment) string {
	currentIDs := channelmedia.MarkedIDs(current)
	if len(currentIDs) == 0 {
		return incoming
	}

	baseIDs := map[string]bool{}
	if base != nil {
		for _, id := range channelmedia.MarkedIDs(*base) {
			baseIDs[id] = true
		}
	}
	attachmentsByID := make(map[string]db.Attachment, len(attachments))
	for _, attachment := range attachments {
		attachmentsByID[uuidToString(attachment.ID)] = attachment
	}

	merged := incoming
	for _, id := range currentIDs {
		attachment, exists := attachmentsByID[id]
		if !exists {
			// 已被删除的附件不能靠过期的 Markdown 复活。
			continue
		}
		downloadPath := channelmedia.DownloadPath(id)
		hasLink := strings.Contains(merged, downloadPath)
		knownToEditor := base != nil && baseIDs[id]
		if knownToEditor && !hasLink {
			// 编辑器采信了这块媒体又亲手移除了它的链接：保留用户的显式删除，别把它当竞态
			// 重新塞回来。
			continue
		}
		if !hasLink {
			merged = channelmedia.Append(merged, channelmedia.Block(
				id,
				attachment.Filename,
				strings.HasPrefix(attachment.ContentType, "image/"),
			))
			continue
		}
		// Tiptap 串行化一整段完好的图片时可能漏掉 HTML 注释。在不动可见链接的前提下
		// 补回溯源标记。
		if !channelmedia.HasMarker(merged, id) {
			merged = channelmedia.Append(merged, channelmedia.Marker(id))
		}
	}
	return merged
}

// refreshUntouchedNullableIssueParams 用当前行的值回填「这次没被请求触碰」的可空列参数，
// 保证 COALESCE 更新不会把没改的字段清掉。
func refreshUntouchedNullableIssueParams(params *db.UpdateIssueParams, current db.Issue, rawFields map[string]json.RawMessage) {
	_, assigneeTypeTouched := rawFields["assignee_type"]
	_, assigneeIDTouched := rawFields["assignee_id"]
	// assignee_type 与 assignee_id 是同一个被校验过的值。只给了半边时，用预校验前的
	// 另一半续上，而不要拿本请求这半边去配一个并发写进来、从未与它一起校验过的另一半。
	if !assigneeTypeTouched && !assigneeIDTouched {
		params.AssigneeType = current.AssigneeType
		params.AssigneeID = current.AssigneeID
	}
	if _, touched := rawFields["start_date"]; !touched {
		params.StartDate = current.StartDate
	}
	if _, touched := rawFields["due_date"]; !touched {
		params.DueDate = current.DueDate
	}
	if _, touched := rawFields["parent_issue_id"]; !touched {
		params.ParentIssueID = current.ParentIssueID
	}
	if _, touched := rawFields["project_id"]; !touched {
		params.ProjectID = current.ProjectID
	}
	if _, touched := rawFields["stage"]; !touched {
		params.Stage = current.Stage
	}
}

// errIssueFieldConflict 表示基于 title_base/description_base 的乐观并发冲突：编辑器依据的
// 基线已不是当前值（比如另一端的编辑先落了库）。
var errIssueFieldConflict = errors.New("issue text field conflict")

// updateIssueAtomically 在单事务里应用一次 issue 变更并返回更新前后两行。
func (h *Handler) updateIssueAtomically(ctx context.Context, workspaceID pgtype.UUID, params db.UpdateIssueParams, rawFields map[string]json.RawMessage, titleBase, descriptionBase *string, attachmentIDs []pgtype.UUID, statusKey string) (db.Issue, db.Issue, bool, error) {
	issue, previous, attachmentsChanged, _, err := h.updateIssueAtomicallyWithEvent(ctx, workspaceID, params, rawFields, titleBase, descriptionBase, attachmentIDs, statusKey, nil)
	return issue, previous, attachmentsChanged, err
}

type issueUpdateEventBuilder func(updated, previous db.Issue, attachmentsChanged bool) []events.Event

// updateIssueAtomicallyWithEvent 应用一次 issue 变更，并在提交前把变更事实记录成持久化
// 事件。builder 为 nil 时保留底层辅助函数在批量路径里的历史用法；面向用户的单条更新路径
// 会提供事件快照构建器。
func (h *Handler) updateIssueAtomicallyWithEvent(ctx context.Context, workspaceID pgtype.UUID, params db.UpdateIssueParams, rawFields map[string]json.RawMessage, titleBase, descriptionBase *string, attachmentIDs []pgtype.UUID, statusKey string, buildEvents issueUpdateEventBuilder) (db.Issue, db.Issue, bool, []events.Event, error) {
	if h.TxStarter == nil {
		return db.Issue{}, db.Issue{}, false, nil, errors.New("atomic issue update requires transaction starter")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.Issue{}, db.Issue{}, false, nil, fmt.Errorf("begin atomic issue update: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	// 本路径自己开事务，因此自己带归档竞态防护，而不走 runWithIssueStatusGuard。目录锁必须
	// 始终先于附件锁与 issue 行锁。 (MUL-6243)
	if err := assertIssueStatusStillActive(ctx, qtx, workspaceID, statusKey); err != nil {
		return db.Issue{}, db.Issue{}, false, nil, err
	}
	if len(attachmentIDs) > 0 {
		if _, err := qtx.LockAttachmentsForIssueLink(ctx, db.LockAttachmentsForIssueLinkParams{
			WorkspaceID:   workspaceID,
			AttachmentIds: attachmentIDs,
		}); err != nil {
			return db.Issue{}, db.Issue{}, false, nil, fmt.Errorf("lock issue attachments: %w", err)
		}
	}
	current, err := qtx.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{
		ID:          params.ID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return db.Issue{}, db.Issue{}, false, nil, fmt.Errorf("lock issue for update: %w", err)
	}

	if params.Title.Valid && titleBase != nil && current.Title != *titleBase && current.Title != params.Title.String {
		return db.Issue{}, current, false, nil, errIssueFieldConflict
	}

	if params.Description.Valid {
		attachments, listErr := qtx.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{
			IssueID:     current.ID,
			WorkspaceID: current.WorkspaceID,
		})
		if listErr != nil {
			return db.Issue{}, current, false, nil, fmt.Errorf("list issue attachments for description merge: %w", listErr)
		}
		currentDescription := ""
		if current.Description.Valid {
			currentDescription = current.Description.String
		}
		incomingDescription := params.Description.String
		if descriptionBase != nil && currentDescription != *descriptionBase && currentDescription != incomingDescription {
			baseWithLateMedia := mergeIssueChannelMediaDescription(currentDescription, *descriptionBase, descriptionBase, attachments)
			if currentDescription != baseWithLateMedia {
				return db.Issue{}, current, false, nil, errIssueFieldConflict
			}
		}
		params.Description = pgtype.Text{
			String: mergeIssueChannelMediaDescription(currentDescription, incomingDescription, descriptionBase, attachments),
			Valid:  true,
		}
	}
	refreshUntouchedNullableIssueParams(&params, current, rawFields)

	issue, err := qtx.UpdateIssue(ctx, params)
	if err != nil {
		return db.Issue{}, current, false, nil, fmt.Errorf("update locked issue: %w", err)
	}

	attachmentsChanged := false
	if len(attachmentIDs) > 0 {
		linked, linkErr := qtx.LinkAttachmentsToIssue(ctx, db.LinkAttachmentsToIssueParams{
			IssueID:       issue.ID,
			WorkspaceID:   issue.WorkspaceID,
			AttachmentIds: attachmentIDs,
			BumpRevision:  issue.Revision == current.Revision,
		})
		if linkErr != nil {
			return db.Issue{}, current, false, nil, fmt.Errorf("link issue attachments: %w", linkErr)
		}
		attachmentsChanged = linked.LinkedCount > 0
		if linked.IssueRevision > 0 {
			issue, err = qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
				ID:          issue.ID,
				WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				return db.Issue{}, current, false, nil, fmt.Errorf("reload issue after attachment link: %w", err)
			}
		}
	}
	var persisted []events.Event
	if buildEvents != nil && (issue.Revision != current.Revision || attachmentsChanged) {
		for _, event := range buildEvents(issue, current, attachmentsChanged) {
			if event.Type == "" {
				continue
			}
			event, err = eventoutbox.Enqueue(ctx, qtx, event)
			if err != nil {
				return db.Issue{}, current, false, nil, fmt.Errorf("record issue update event: %w", err)
			}
			persisted = append(persisted, event)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Issue{}, current, false, nil, fmt.Errorf("commit atomic issue update: %w", err)
	}
	return issue, current, attachmentsChanged, persisted, nil
}

// UpdateIssue 处理 PATCH/PUT /api/issues/:id：只更新请求里显式提交的字段。用
// title_base/description_base 做乐观并发冲突检测；expected_revision 不匹配返回 409；父级
// 会做自引用/环检测。更新若触发 agent 运行则按 WillEnqueueRun 的规则派发（SuppressRun 可
// 关闭）；状态推进到终态时通知父 issue。
func (h *Handler) UpdateIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prevIssue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	userID := requestUserID(r)
	workspaceID := uuidToString(prevIssue.WorkspaceID)

	// 以原始字节读 body，以便判断哪些字段被显式发送过。
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req UpdateIssueRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 记录 JSON 里显式出现过哪些字段（即使是 null）。
	var rawFields map[string]json.RawMessage
	json.Unmarshal(bodyBytes, &rawFields)

	// 用当前值预填可空字段（裸 sqlc.narg）。
	params := db.UpdateIssueParams{
		ID:            prevIssue.ID,
		AssigneeType:  prevIssue.AssigneeType,
		AssigneeID:    prevIssue.AssigneeID,
		StartDate:     prevIssue.StartDate,
		DueDate:       prevIssue.DueDate,
		ParentIssueID: prevIssue.ParentIssueID,
		ProjectID:     prevIssue.ProjectID,
		Stage:         prevIssue.Stage,
	}
	if req.ExpectedRevision != nil {
		if *req.ExpectedRevision < 1 {
			writeError(w, http.StatusBadRequest, "expected_revision must be a positive integer")
			return
		}
		if prevIssue.Revision != *req.ExpectedRevision {
			writeRevisionConflict(w, "issue", prevIssue.ID, *req.ExpectedRevision, prevIssue.Revision)
			return
		}
		params.ExpectedRevision = pgtype.Int8{Int64: *req.ExpectedRevision, Valid: true}
	}

	// COALESCE 类字段——只在显式提供时设置
	if req.Title != nil {
		params.Title = pgtype.Text{String: *req.Title, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	// statusKeyForGuard 在本次请求设置了状态时是解析出的 key，否则为空。空 = 「本次写入
	// 不碰状态」，防护逻辑把它当作没什么需要保护。
	statusKeyForGuard := ""
	if req.Status != nil {
		statusKey, _, ok := h.resolveIssueStatusKeyKind(w, r, prevIssue.WorkspaceID, *req.Status)
		if !ok {
			return
		}
		statusKeyForGuard = statusKey
		params.Status = pgtype.Text{String: statusKey, Valid: true}
	}
	if req.Priority != nil {
		if !validateIssueEnum(w, "priority", *req.Priority, validIssuePriorities) {
			return
		}
		params.Priority = pgtype.Text{String: *req.Priority, Valid: true}
	}
	if req.Position != nil {
		params.Position = pgtype.Float8{Float64: *req.Position, Valid: true}
	}
	// 可空字段——只在 JSON 里显式出现时覆盖
	if _, ok := rawFields["assignee_type"]; ok {
		if req.AssigneeType != nil {
			params.AssigneeType = pgtype.Text{String: *req.AssigneeType, Valid: true}
		} else {
			params.AssigneeType = pgtype.Text{Valid: false} // explicit null = unassign
		}
	}
	if _, ok := rawFields["assignee_id"]; ok {
		if req.AssigneeID != nil {
			id, ok := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
			if !ok {
				return
			}
			params.AssigneeID = id
		} else {
			params.AssigneeID = pgtype.UUID{Valid: false} // explicit null = unassign
		}
	}
	if _, ok := rawFields["start_date"]; ok {
		if req.StartDate != nil && *req.StartDate != "" {
			d, err := util.ParseCalendarDate(*req.StartDate)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid start_date format, expected YYYY-MM-DD")
				return
			}
			params.StartDate = d
		} else {
			params.StartDate = pgtype.Date{Valid: false} // explicit null = clear date
		}
	}
	if _, ok := rawFields["due_date"]; ok {
		if req.DueDate != nil && *req.DueDate != "" {
			d, err := util.ParseCalendarDate(*req.DueDate)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
				return
			}
			params.DueDate = d
		} else {
			params.DueDate = pgtype.Date{Valid: false} // explicit null = clear date
		}
	}
	if _, ok := rawFields["parent_issue_id"]; ok {
		if req.ParentIssueID != nil {
			newParentID, ok := parseUUIDOrBadRequest(w, *req.ParentIssueID, "parent_issue_id")
			if !ok {
				return
			}
			// Cannot set self as parent. Compare against prevIssue.ID (the
			// resolved entity), not the raw URL string — `id` may be an
			// identifier like "MUL-7".
			if newParentID == prevIssue.ID {
				writeError(w, http.StatusBadRequest, "an issue cannot be its own parent")
				return
			}
			// Validate parent exists in the same workspace.
			if _, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
				ID:          newParentID,
				WorkspaceID: prevIssue.WorkspaceID,
			}); err != nil {
				writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
				return
			}
			// Cycle detection: walk up from the new parent to ensure we don't reach this issue.
			cursor := newParentID
			for depth := 0; depth < 10; depth++ {
				ancestor, err := h.Queries.GetIssue(r.Context(), cursor)
				if err != nil || !ancestor.ParentIssueID.Valid {
					break
				}
				if ancestor.ParentIssueID == prevIssue.ID {
					writeError(w, http.StatusBadRequest, "circular parent relationship detected")
					return
				}
				cursor = ancestor.ParentIssueID
			}
			params.ParentIssueID = newParentID
		} else {
			params.ParentIssueID = pgtype.UUID{Valid: false} // explicit null = remove parent
		}
	}
	if _, ok := rawFields["project_id"]; ok {
		if req.ProjectID != nil {
			projectUUID, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
			if !ok {
				return
			}
			if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
				ID:          projectUUID,
				WorkspaceID: prevIssue.WorkspaceID,
			}); err != nil {
				if !isNotFound(err) {
					slog.Error("update issue: validate project scope",
						append(logger.RequestAttrs(r), "project_id", uuidToString(projectUUID), "error", err)...)
					writeError(w, http.StatusInternalServerError, "failed to validate project")
					return
				}
				writeError(w, http.StatusBadRequest, "project not found in this workspace")
				return
			}
			params.ProjectID = projectUUID
		} else {
			params.ProjectID = pgtype.UUID{Valid: false}
		}
	}
	if _, ok := rawFields["stage"]; ok {
		if req.Stage != nil {
			if *req.Stage < 1 {
				writeError(w, http.StatusBadRequest, "stage must be >= 1")
				return
			}
			params.Stage = pgtype.Int4{Int32: *req.Stage, Valid: true}
		} else {
			params.Stage = pgtype.Int4{Valid: false} // explicit null = unstage
		}
	}

	// 校验最终 (assignee_type, assignee_id) 组合——仅当调用方动了这两个字段的任何一个。
	// 没被修改的现有数据保持原样。
	//
	// scope 指向「这条 issue 本身」：一个无可归属的 autopilot 运行若已被验证确实持有这条
	// issue 上的工作，也可以像在它下面建子任务时那样把它指向私密 agent。MUL-6691 之前这里
	// 传 nil，导致报告的流程——建好 DRA-109 不分配、随后再分配——被拒绝，尽管创建路径上
	// 一模一样的血缘是被接受的。
	_, touchedType := rawFields["assignee_type"]
	_, touchedID := rawFields["assignee_id"]
	if touchedType || touchedID {
		if status, msg := h.validateAssigneePair(r.Context(), r, workspaceID, params.AssigneeType, params.AssigneeID, scopeExistingIssue(&prevIssue)); status != 0 {
			writeError(w, status, msg)
			return
		}
	}

	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	var issue db.Issue
	var persistedEvents []events.Event
	issue, lockedPrev, _, recorded, err := h.updateIssueAtomicallyWithEvent(
		r.Context(), prevIssue.WorkspaceID, params, rawFields, req.TitleBase, req.DescriptionBase, attachmentIDs, statusKeyForGuard,
		func(updated, previous db.Issue, attachmentsChanged bool) []events.Event {
			result := []events.Event{buildIssueUpdatedEvent(r.Context(), h, updated, previous, req, rawFields, actorType, actorID)}
			if attachmentsChanged {
				result = append(result, buildIssueAttachmentsChangedEvent(updated, actorType, actorID))
			}
			return result
		},
	)
	persistedEvents = recorded
	if lockedPrev.ID.Valid {
		prevIssue = lockedPrev
	}
	if err != nil {
		if writeIssueStatusRaceError(w, err) {
			return
		}
		if errors.Is(err, errIssueFieldConflict) {
			writeEditConflict(w, "issue", prevIssue.ID)
			return
		}
		if errors.Is(err, pgx.ErrNoRows) && req.ExpectedRevision != nil {
			current, reloadErr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: prevIssue.ID, WorkspaceID: prevIssue.WorkspaceID})
			if reloadErr == nil {
				writeRevisionConflict(w, "issue", current.ID, *req.ExpectedRevision, current.Revision)
				return
			}
		}
		slog.Warn("update issue failed", append(logger.RequestAttrs(r), "error", err, "issue_id", id, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to update issue: "+err.Error())
		return
	}

	// 修改 1. issue 标题和描述也存在库里：agent 在运行中改写需求时，文件 diff 看不见
	//         这次改动，审核需要单独一条文本差异。
	if issue.Title != prevIssue.Title || issue.Description.String != prevIssue.Description.String {
		h.recordRunTextChange(r.Context(), h.Queries, r, workspaceID,
			runTextTarget{Kind: "issue", ID: uuidToString(issue.ID), Name: issue.Title}, "title", prevIssue.Title, issue.Title)
		h.recordRunTextChange(r.Context(), h.Queries, r, workspaceID,
			runTextTarget{Kind: "issue", ID: uuidToString(issue.ID), Name: issue.Title}, "description", prevIssue.Description.String, issue.Description.String)
	}

	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	resp := issueToResponse(issue, prefix)
	slog.Info("issue updated", append(logger.RequestAttrs(r), "issue_id", id, "workspace_id", workspaceID)...)

	h.fillStatusCategory(r.Context(), issue.WorkspaceID, &resp)
	assigneeChanged := (req.AssigneeType != nil || req.AssigneeID != nil) &&
		(prevIssue.AssigneeType.String != issue.AssigneeType.String || uuidToString(prevIssue.AssigneeID) != uuidToString(issue.AssigneeID))
	statusChanged := req.Status != nil && prevIssue.Status != issue.Status
	// project_changed gates the client's per-project issue-list refetch the way
	// status/assignee flags gate theirs. Without it the client must diff
	// project_id against its own cache, which breaks once an optimistic local
	// move has overwritten the cached value (MUL-3669 / #4548).

	for _, persistedEvent := range persistedEvents {
		if persistedEvent.ID != "" {
			h.publishEvent(persistedEvent)
		}
	}

	// 协调任务队列。这次写入是否启动 agent 运行——以及为谁（agent 受理人或 squad leader）——
	// 由唯一的 WillEnqueueRun 谓词决定，与预览端点逐字共享，保证两者永不漂移（MUL-3375）。
	//
	// 换受理人「故意不」取消 issue 上现有的任务（#4963 / MUL-4113）。之前「取消 issue 上所有
	// 活动任务」太粗暴：它会悄悄丢弃无关的在途工作（为另一个 agent 触发的 @提及运行、squad
	// 任务）且不重新排队，还会自我取消一个在内部把 issue 换人的运行。归属交接不再意味着打断；
	// 新受理人的运行（若有）由下面的 WillEnqueueRun 入队，与已经在跑的任务并行。任何状态变更
	// ——包括 → cancelled——都不取消活动任务：用户在 issue 上点「取消」并不意味着要停止在跑的
	// agent 运行，这个隐式耦合已移除（MUL-4465）。删除 issue 仍会取消其任务（见 DeleteIssue），
	// 因为任务的归属 issue 已不存在。
	if trigger, ok := h.IssueService.WillEnqueueRun(r.Context(),
		service.IssueTriggerInput{
			Issue:           issue,
			PrevStatus:      prevIssue.Status,
			AssigneeChanged: assigneeChanged,
			StatusChanged:   statusChanged,
		},
		h.issueTriggerWriteProbe(r, actorType, actorID, issue),
	); ok && !req.SuppressRun {
		h.dispatchIssueRun(r.Context(), issue, trigger, actorType, actorID, req.HandoffNote)
	}

	// 平台驱动的父级通知：当本 issue 转入 done 且它有父级时，在父级上发一条系统级顶层评论
	//（MUL-2538——替代 PR #2918 里引发自我 @提及循环的 agent-prompt 规则）。辅助函数会
	// 先看「确为转入 + 有父级」再动作，失败也是尽力而为。
	if statusChanged {
		h.notifyParentOfChildDone(r.Context(), prevIssue, issue)
		// 任务进入审核中时交给审查 Agent 接手；只在类别变化时派发。
		h.afterIssueStatusChangeForReview(r.Context(), prevIssue, issue)
		// 3. 完成与重开同时开关质量监督的完成轮次。
		h.afterIssueStatusChangeForSupervision(r.Context(), prevIssue, issue)
	}

	writeJSON(w, http.StatusOK, resp)
}

// validateAssigneePair 校验 (assignee_type, assignee_id) 是否指向工作区里真实存在的实体。
// 对 agent 与 squad 受理人，还会拒绝已归档目标并跑 INVOKE 闸门——是 canInvokeAgent 而不是
// 更宽松的 canAccessPrivateAgent 视图闸门：给 issue 分配会产生一次运行，所以必须与 chat /
// @提及（MUL-3963）通过同一条判定。私密 agent 只允许 owner，没有工作区 admin 绕过，也没有
// 无条件的 agent 对 agent 绕过——agent 调用者（X-Agent-ID）与别处一样按顶层人类发起人判定。
// scope 指定这份分配属于哪个工作：无归属的 autopilot 运行只能在 scope 内借用 autopilot
// 授权——建子任务是父 issue、更新是 issue 本身、建无父 issue 则是该运行自己验证过的
// autopilot（MUL-4857、MUL-6691）。它绝不改变新 issue 或任务的归属。
//
// 返回 (statusCode, errorMessage)。statusCode == 0 表示组合合法；调用方应把任何非零
// status 当作拒绝原样返回给客户端。
func (h *Handler) validateAssigneePair(ctx context.Context, r *http.Request, workspaceID string, assigneeType pgtype.Text, assigneeID pgtype.UUID, scope assignAuthorityScope) (int, string) {
	// 两者都没设置 → 未分配 issue，合法。
	if !assigneeType.Valid && !assigneeID.Valid {
		return 0, ""
	}
	// type/id 只给了半边 → 调用方必须成对提供。
	if assigneeType.Valid != assigneeID.Valid {
		return http.StatusBadRequest, "assignee_type and assignee_id must be provided together"
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return http.StatusBadRequest, "invalid workspace_id"
	}
	switch assigneeType.String {
	case "member":
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID:      assigneeID,
			WorkspaceID: wsUUID,
		}); err != nil {
			return http.StatusBadRequest, "assignee_id does not refer to a member of this workspace"
		}
		return 0, ""
	case "agent":
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			return http.StatusBadRequest, "assignee_id does not refer to an agent of this workspace"
		}
		if agent.ArchivedAt.Valid {
			return http.StatusBadRequest, "cannot assign to archived agent"
		}
		actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
		effectiveInvoker := h.effectiveInvocationAuthorityFromRequest(r, scope, agent, actorType, actorID, workspaceID)
		if !h.canInvokeAgent(ctx, agent, actorType, actorID, effectiveInvoker, workspaceID) {
			// 指明「缺什么权限」，而不是「目标是什么配置」：旧的 "private agent" 措辞既泄露了
			// agent 的权限模式，对 scoped 到特定人身上的 public_to agent 又根本是错的。这里
			// 不追求完全的枚举安全——上面的 not-in-workspace 分支仍以 400（而不是这里的 403）
			// 作答，存在性仍可探测；能保证的只是「拒绝原因不再点名目标的权限模式」
			//（MUL-6380 / GH #7180）。
			return http.StatusForbidden, "you do not have permission to assign work to this agent"
		}
		return 0, ""
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			return http.StatusBadRequest, "assignee_id does not refer to a squad in this workspace"
		}
		if squad.ArchivedAt.Valid {
			return http.StatusBadRequest, "cannot assign to an archived squad"
		}
		leader, err := h.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil || leader.ArchivedAt.Valid {
			return http.StatusBadRequest, "squad leader is archived; cannot assign to this squad"
		}
		actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
		// 修改 1. squad 分支刻意不接任务级委托继承：领袖真正入队时还会被 enqueueSquadLeaderTask 用
		// OriginatorForIssueTask 再判一次（那是领袖任务行上的归因来源），两处解析不同会让指派
		// 成功返回却什么都不跑。这条路径保持既有边界——无法归因的运行指派私有领袖仍失败关闭。
		effectiveInvoker := h.baseInvocationAuthorityFromRequest(r, scope, actorType, actorID, workspaceID)
		if !h.canInvokeAgent(ctx, leader, actorType, actorID, effectiveInvoker, workspaceID) {
			// 与上面 agent 分支同样的措辞规则；「this squad」避免泄露 leader agent 的
			// 权限模式。
			return http.StatusForbidden, "you do not have permission to assign work to this squad"
		}
		return 0, ""
	default:
		return http.StatusBadRequest, "assignee_type must be 'member', 'agent', or 'squad'"
	}
}

// shouldEnqueueAgentTask 判断一次 issue 创建/分配是否应当触发被分配 agent 运行。
// backlog 类状态会跳过——backlog 是停车位，issue 可以先预分配而不立即触发执行；移出
// backlog 的触发单独在 UpdateIssue 处理。
func (h *Handler) shouldEnqueueAgentTask(ctx context.Context, issue db.Issue) bool {
	// 属于 backlog 类别的自定义状态与 Backlog 一样停车。 (MUL-6243)
	if issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status) == "backlog" {
		return false
	}
	return h.isAgentAssigneeReady(ctx, issue)
}

// shouldEnqueueAssigneeFallback 判断评论路由能否回退到 issue 的被分配 agent。任何状态都
// 触发——评论是对话式的，哪个阶段都可能发生，包括完成后（例如对一条 done issue 的追问）。
//
// 镜像 resolveMentionedAgentCommentTriggers 在 @提及路径上应用的私密 agent 闸门：一旦
// owner/admin 把私密 agent 分配到 issue 上，它的 UUID 就「焊」到了 issue，对所有能看见这条
// issue 的成员保持可见。没有这个检查，这些成员随便评论一句就能给私密 agent 派发新任务
// （#3300）。
func (h *Handler) shouldEnqueueAssigneeFallback(ctx context.Context, issue db.Issue, actorType, actorID string, opts commentTriggerComputeOptions) bool {
	_, hasPending, ok := h.assigneeFallbackAgent(ctx, issue, actorType, actorID, opts)
	return ok && !hasPending
}

// assigneeFallbackAgent 解析 issue 的被分配 agent 是否可作为本次评论触发的回退目标；返回
// (agent, 是否有待处理任务, 是否可用)。被分配的不是 agent、agent 已归档/没绑 runtime、
// 或调用方无权调用该 agent 时都返回不可用。
func (h *Handler) assigneeFallbackAgent(ctx context.Context, issue db.Issue, actorType, actorID string, opts commentTriggerComputeOptions) (db.Agent, bool, bool) {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return db.Agent{}, false, false
	}
	agent, err := h.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return db.Agent{}, false, false
	}
	if !h.canInvokeAgent(ctx, agent, actorType, actorID, opts.effectiveInvokerFor(agent), uuidToString(issue.WorkspaceID)) {
		return db.Agent{}, false, false
	}
	// 合并队列：有待处理任务仍是合法的路由目标，但真正会入队的调用方用它避免叠加重复任务。
	hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, issue.AssigneeID, opts)
	if err != nil {
		return db.Agent{}, false, false
	}
	return agent, hasPending, true
}

// isAgentRunningOnIssue 判断调用 agent 的当前任务（由 X-Task-ID 标识）是否正跑在被「推进」的
// 这条 issue 上。这是 backlog→active 唯一真正的自循环：agent 把自己正在执行的那条 issue 翻转
// 一下，会立刻把自己重新入队，跑完又翻回去，如此往复。
//
// 同一 agent 的跨 issue 交接（agent A 在 I1 上完成任务后推进 I2——哪怕 I2 也派给 A）不是循环，
// 必须触发：这是文档化的串行子任务链。member 角色永远不匹配。
//
// actorType 为 "agent" 时 X-Task-ID 保证存在且一致：否则 resolveActor 会把角色降级成
// "member"（handler.go 里的 resolveActor）。这里仍防御性地复查——未来调用方可能走别的路径
// 传 agent 身份。
func (h *Handler) isAgentRunningOnIssue(r *http.Request, actorType string, issue db.Issue) bool {
	if actorType != "agent" {
		return false
	}
	taskIDStr := r.Header.Get("X-Task-ID")
	if taskIDStr == "" {
		return false
	}
	taskUUID, err := util.ParseUUID(taskIDStr)
	if err != nil {
		return false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		return false
	}
	if !task.IssueID.Valid {
		return false
	}
	return uuidToString(task.IssueID) == uuidToString(issue.ID)
}

// isAgentAssigneeReady 判断一条 issue 是否派给了活跃且绑了可用 runtime 的 agent。
func (h *Handler) isAgentAssigneeReady(ctx context.Context, issue db.Issue) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return false
	}

	agent, err := h.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil {
		return false
	}
	// 用共享的判定而不是本地重查（service.AgentReadiness）。只有 BLOCKED 判定才拦住入队：
	// 离线机器仍会入队，因为那部分活等机器回来就能跑。
	verdict, err := service.AgentReadiness(ctx, h.runtimeLookup(obsmetrics.RuntimeLookupSourceIssue), agent)
	if err != nil || !verdict.Blocked() {
		return err == nil
	}
	// 分配这条动作没有给分配者阅读的结果面，所以需要人工修复的拒绝会把原因留在 issue 上
	//（MUL-6164）。未绑定的 agent 保持沉默跳过：agent 列表已经展示它没有 runtime，这里
	// 没有新信息。
	if verdict.Reason == ReasonRuntimeUnusable {
		h.noteRuntimeUnusable(ctx, issue, agent, verdict)
	}
	return false
}

// DeleteIssue 处理 DELETE /api/issues/:id：删除 issue。删除前会取消该 issue 上的所有任务、
// 终止关联的 autopilot 运行；删除事务内一并处理子任务脱离、附件 URL 收集与 source_context
// 清理，之后异步删除 S3 对象并广播删除事件。响应 204。
func (h *Handler) DeleteIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	h.TaskService.CancelTasksForIssue(r.Context(), issue.ID)
	// 先让关联的 autopilot 运行失败收尾再删（ON DELETE SET NULL 会清掉 issue_id）。
	if err := h.AutopilotService.FailAutopilotRunsByIssue(r.Context(), issue.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to settle linked autopilot runs")
		return
	}

	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	deleteResult, persistedEvents, err := h.deleteIssueAndCollectAttachmentURLsWithEvents(r.Context(), issue, nil, actorType, actorID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete issue")
		return
	}

	h.deleteS3Objects(r.Context(), deleteResult.AttachmentURLs)
	// 总是广播解析后的 UUID——前端缓存按 UUID 做 key，标识符形态的 payload（"MUL-123"）
	// 会让其他客户端在「按标识符路径删除」后留下过期条目。
	resolvedID := uuidToString(issue.ID)
	for _, event := range persistedEvents {
		h.publishEvent(event)
	}
	slog.Info("issue deleted", append(logger.RequestAttrs(r), "issue_id", resolvedID, "workspace_id", uuidToString(issue.WorkspaceID))...)
	w.WriteHeader(http.StatusNoContent)
}

// issueDeleteResult 汇总删除的副作用：需要删的附件 URL 与被剥离父级的直接子 issue。
type issueDeleteResult struct {
	AttachmentURLs   []string
	DetachedChildren []db.Issue
}

// deleteIssueAndCollectAttachmentURLs 序列化单条 issue 删除与渠道媒体的绑定操作。删除侧的
// FOR UPDATE 与绑定者的 FOR KEY SHARE 冲突，URL 收集只在拿到锁之后进行：先绑定则新 URL 被
// 收集；先删除则绑定回滚、不消耗它的持久化对象意图。
func (h *Handler) deleteIssueAndCollectAttachmentURLs(ctx context.Context, issue db.Issue, excludedIssueIDs []pgtype.UUID) (issueDeleteResult, error) {
	return h.deleteIssuesAndCollectAttachmentURLs(ctx, []db.Issue{issue}, excludedIssueIDs)
}

// deleteIssuesAndCollectAttachmentURLs 是批量版；无事件版本（内部非 HTTP 调用用）。
func (h *Handler) deleteIssuesAndCollectAttachmentURLs(ctx context.Context, issues []db.Issue, excludedIssueIDs []pgtype.UUID) (issueDeleteResult, error) {
	result, _, err := h.deleteIssuesAndCollectAttachmentURLsWithEvents(ctx, issues, excludedIssueIDs, "", "")
	return result, err
}

// deleteIssueAndCollectAttachmentURLsWithEvents 让删除事实与行删除落在同一事务里。可选的
// actor 用于 HTTP 删除路径；内部调用方可用旧的包装版本，只需存储清理并自行发布聚合事件。
func (h *Handler) deleteIssueAndCollectAttachmentURLsWithEvents(ctx context.Context, issue db.Issue, excludedIssueIDs []pgtype.UUID, actorType, actorID string) (issueDeleteResult, []events.Event, error) {
	return h.deleteIssuesAndCollectAttachmentURLsWithEvents(ctx, []db.Issue{issue}, excludedIssueIDs, actorType, actorID)
}

// deleteIssuesAndCollectAttachmentURLsWithEvents 在单事务里删除多条 issue：先按 UUID 排序
// 以获得一致的锁顺序（防死锁），逐条加删除锁、剥离直接子级、收集附件 URL、清理
// source_context 与 S3 删除意图，并在同一事务里入队每条删除/剥离事件后提交。
func (h *Handler) deleteIssuesAndCollectAttachmentURLsWithEvents(ctx context.Context, issues []db.Issue, excludedIssueIDs []pgtype.UUID, actorType, actorID string) (issueDeleteResult, []events.Event, error) {
	// 按 UUID 排序：多条删除共用同一锁顺序，避免两条并发请求以相反顺序锁同一批行而死锁。
	sort.Slice(issues, func(i, j int) bool {
		return uuidToString(issues[i].ID) < uuidToString(issues[j].ID)
	})
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return issueDeleteResult{}, nil, fmt.Errorf("begin issue delete: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	result := issueDeleteResult{}
	persistedEvents := make([]events.Event, 0, len(issues))
	var supervisionTasks []pgtype.UUID
	for _, issue := range issues {
		if _, err := qtx.LockIssueForDelete(ctx, db.LockIssueForDeleteParams{
			ID: issue.ID, WorkspaceID: issue.WorkspaceID,
		}); err != nil {
			return issueDeleteResult{}, nil, fmt.Errorf("lock issue for delete: %w", err)
		}
		detached, err := qtx.DetachDirectChildIssues(ctx, db.DetachDirectChildIssuesParams{
			WorkspaceID: issue.WorkspaceID, ParentIssueID: issue.ID, ExcludedIssueIds: excludedIssueIDs,
		})
		if err != nil {
			return issueDeleteResult{}, nil, fmt.Errorf("detach child issues: %w", err)
		}
		result.DetachedChildren = append(result.DetachedChildren, detached...)
		if actorType != "" && actorID != "" {
			for _, child := range detached {
				event, eventErr := eventoutbox.Enqueue(ctx, qtx, buildIssueDetachedEvent(child, actorType, actorID))
				if eventErr != nil {
					return issueDeleteResult{}, nil, fmt.Errorf("record detached issue event: %w", eventErr)
				}
				persistedEvents = append(persistedEvents, event)
			}
		}
		attachmentURLs, err := qtx.ListAttachmentURLsByIssueOrComments(ctx, issue.ID)
		if err != nil {
			return issueDeleteResult{}, nil, fmt.Errorf("list issue attachment URLs: %w", err)
		}
		result.AttachmentURLs = append(result.AttachmentURLs, attachmentURLs...)
		if sourceContext, contextErr := qtx.GetIssueSourceContextByIssue(ctx, db.GetIssueSourceContextByIssueParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID}); contextErr == nil {
			contextAttachments, listErr := qtx.ListAttachmentsBySourceContext(ctx, db.ListAttachmentsBySourceContextParams{
				WorkspaceID: issue.WorkspaceID, SourceContextID: sourceContext.ID,
			})
			if listErr != nil {
				return issueDeleteResult{}, nil, fmt.Errorf("list source context attachments: %w", listErr)
			}
			// 存储接口的旧批量删除没有结果通道。在移除 DB 归属前先记录持久化对象意图，
			// 让周期性协调器能重试任何不明确或失败的尽力删除。
			if h.Storage != nil {
				for _, clone := range contextAttachments {
					storageKey := h.Storage.KeyFromURL(clone.Url)
					if storageKey == "" {
						continue
					}
					if _, intentErr := qtx.RecordSourceContextDeletionObjectIntent(ctx, db.RecordSourceContextDeletionObjectIntentParams{
						StorageKey: storageKey, WorkspaceID: issue.WorkspaceID, SourceContextID: sourceContext.ID,
						AttachmentID: clone.ID, ObjectUrl: clone.Url,
					}); intentErr != nil {
						return issueDeleteResult{}, nil, fmt.Errorf("record source context delete intent: %w", intentErr)
					}
				}
			}
			clones, cloneErr := qtx.DeleteAttachmentsBySourceContext(ctx, db.DeleteAttachmentsBySourceContextParams{WorkspaceID: issue.WorkspaceID, SourceContextID: sourceContext.ID})
			if cloneErr != nil {
				return issueDeleteResult{}, nil, fmt.Errorf("delete source context attachments: %w", cloneErr)
			}
			for _, clone := range clones {
				result.AttachmentURLs = append(result.AttachmentURLs, clone.Url)
			}
			if _, contextErr = qtx.DeleteIssueSourceContextByIssue(ctx, db.DeleteIssueSourceContextByIssueParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID}); contextErr != nil {
				return issueDeleteResult{}, nil, fmt.Errorf("delete issue source context: %w", contextErr)
			}
		} else if !errors.Is(contextErr, pgx.ErrNoRows) {
			return issueDeleteResult{}, nil, fmt.Errorf("load issue source context for delete: %w", contextErr)
		}
		if actorType != "" && actorID != "" {
			event, eventErr := eventoutbox.Enqueue(ctx, qtx, buildIssueDeletedEvent(issue, actorType, actorID))
			if eventErr != nil {
				return issueDeleteResult{}, nil, fmt.Errorf("record issue delete event: %w", eventErr)
			}
			persistedEvents = append(persistedEvents, event)
		}
		var reviewTask pgtype.UUID
		err = tx.QueryRow(ctx, `SELECT task_id FROM supervision_batch WHERE workspace_id=$1 AND issue_id=$2 AND status='running'`, issue.WorkspaceID, issue.ID).Scan(&reviewTask)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return issueDeleteResult{}, nil, err
		}
		if reviewTask.Valid {
			supervisionTasks = append(supervisionTasks, reviewTask)
		}
		if err := deleteDeliveryData(ctx, tx, uuidToString(issue.WorkspaceID), uuidToString(issue.ID)); err != nil {
			return issueDeleteResult{}, nil, err
		}
		if err := deleteSupervisionData(ctx, tx, uuidToString(issue.WorkspaceID), uuidToString(issue.ID)); err != nil {
			return issueDeleteResult{}, nil, err
		}
		if err := qtx.DeleteIssue(ctx, db.DeleteIssueParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
			return issueDeleteResult{}, nil, fmt.Errorf("delete issue: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return issueDeleteResult{}, nil, fmt.Errorf("commit issue delete: %w", err)
	}
	for _, task := range supervisionTasks {
		if _, err := h.TaskService.CancelTaskByUser(ctx, task); err != nil {
			slog.Error("停止已删除任务的监督运行失败", "task_id", uuidToString(task), "error", err)
		}
	}
	return result, persistedEvents, nil
}

// publishDetachedChildren 把被删父级剥离出来的子 issue 以「已更新」事件逐个广播（去掉父级
// 后前端需要刷新渲染）。
func (h *Handler) publishDetachedChildren(ctx context.Context, children []db.Issue, actorType, actorID string) {
	for _, child := range children {
		response := issueToResponse(child, h.getIssuePrefix(ctx, child.WorkspaceID))
		h.fillStatusCategory(ctx, child.WorkspaceID, &response)
		h.publish(protocol.EventIssueUpdated, uuidToString(child.WorkspaceID), actorType, actorID, map[string]any{"issue": response})
	}
}

// ---------------------------------------------------------------------------
// 批量操作
// ---------------------------------------------------------------------------

// BatchUpdateIssuesRequest 是一次批量更新的请求体：issue_ids 列表 + 一份共用的 Updates。
type BatchUpdateIssuesRequest struct {
	IssueIDs []string           `json:"issue_ids"`
	Updates  UpdateIssueRequest `json:"updates"`
}

// BatchUpdateIssues 处理 POST /api/issues/batch-update：把同一份字段更新应用到多个 issue 上
// （静默跳过解析失败/越界/校验失败的单条）。状态/项目这类整批共享的目标在循环外统一校验，
// 坏值直接拒绝整批（避免 `{"updated": N}` 谎报成功）。循环后按最终状态统一评估父级关卡通知。
func (h *Handler) BatchUpdateIssues(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req BatchUpdateIssuesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.IssueIDs) == 0 {
		writeError(w, http.StatusBadRequest, "issue_ids is required")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 检测 "updates" 里哪些字段被显式设置过（包括 null）。
	var rawTop map[string]json.RawMessage
	json.Unmarshal(bodyBytes, &rawTop)
	var rawUpdates map[string]json.RawMessage
	if raw, exists := rawTop["updates"]; exists {
		json.Unmarshal(raw, &rawUpdates)
	}

	// updates 里一个变更字段都没有时直接短路。否则下面循环会跑 N 条无操作 UPDATE（每个
	// if 守卫都跳过，每条 COALESCE 都保留现值）并上报 `{"updated": N}`——响应兴高采烈地
	// 声称成功，其实什么都没变。会撞上这条路径的真实案例大多是调用方写错（status 放到了顶层、
	// "update" 拼错成单数）。在这里说真话——`{"updated": 0}`——既能保持线上形态稳定，
	// 又能让计数与现实相符。见 multica-ai/multica#1660。
	hasMutation := req.Updates.Title != nil ||
		req.Updates.Description != nil ||
		req.Updates.Status != nil ||
		req.Updates.Priority != nil ||
		req.Updates.Position != nil
	if !hasMutation {
		for _, k := range []string{"assignee_type", "assignee_id", "start_date", "due_date", "parent_issue_id", "project_id", "stage"} {
			if _, ok := rawUpdates[k]; ok {
				hasMutation = true
				break
			}
		}
	}
	if !hasMutation {
		writeJSON(w, http.StatusOK, map[string]any{"updated": 0})
		return
	}
	if req.Updates.Priority != nil {
		if !validateIssueEnum(w, "priority", *req.Updates.Priority, validIssuePriorities) {
			return
		}
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	// 状态要对着本工作区目录校验，所以得等上面拿到 wsUUID。整批只查一次——每条 issue 共享
	// 同一个工作区——且失败就拒绝而不是悄悄跳过，坏状态不能报出 `{"updated": N}`。 (MUL-6243)
	batchStatusKey := ""
	if req.Updates.Status != nil {
		batchStatusKey, _, ok = h.resolveIssueStatusKeyKind(w, r, wsUUID, *req.Updates.Status)
		if !ok {
			return
		}
	}
	// 整批共享同一个 project_id，因此在这里统一查一次而不是逐条查；同样整批拒绝而不是像
	// 循环里的逐条守卫那样跳过：外部项目会让整个请求失效。
	batchProjectID := pgtype.UUID{Valid: false}
	if _, ok := rawUpdates["project_id"]; ok && req.Updates.ProjectID != nil {
		projectUUID, ok := parseUUIDOrBadRequest(w, *req.Updates.ProjectID, "project_id")
		if !ok {
			return
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID:          projectUUID,
			WorkspaceID: wsUUID,
		}); err != nil {
			if !isNotFound(err) {
				slog.Error("batch update issues: validate project scope",
					append(logger.RequestAttrs(r), "project_id", uuidToString(projectUUID), "error", err)...)
				writeError(w, http.StatusInternalServerError, "failed to validate project")
				return
			}
			writeError(w, http.StatusBadRequest, "project not found in this workspace")
			return
		}
		batchProjectID = projectUUID
	}

	updated := 0
	// 整批共用一个 Resolver——逐条填充会对每条自定义状态行查一次目录。 (MUL-6243)
	fillBatch := h.newStatusCategoryFiller(r.Context(), wsUUID)
	// 本批里转入终态的子任务被收进来，等循环结束、基于整批最终状态再统一评估一次父级/关卡
	// 通知（MUL-4155），而不是在批中间逐子任务评估。
	var childDoneCompleted []db.Issue
	for _, issueID := range req.IssueIDs {
		issueUUID, err := util.ParseUUID(issueID)
		if err != nil {
			continue
		}
		prevIssue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          issueUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			continue
		}

		params := db.UpdateIssueParams{
			ID:            prevIssue.ID,
			AssigneeType:  prevIssue.AssigneeType,
			AssigneeID:    prevIssue.AssigneeID,
			StartDate:     prevIssue.StartDate,
			DueDate:       prevIssue.DueDate,
			ParentIssueID: prevIssue.ParentIssueID,
			ProjectID:     prevIssue.ProjectID,
			Stage:         prevIssue.Stage,
		}

		if req.Updates.Title != nil {
			params.Title = pgtype.Text{String: *req.Updates.Title, Valid: true}
		}
		if req.Updates.Description != nil {
			params.Description = pgtype.Text{String: *req.Updates.Description, Valid: true}
		}
		if req.Updates.Status != nil {
			params.Status = pgtype.Text{String: batchStatusKey, Valid: true}
		}
		if req.Updates.Priority != nil {
			params.Priority = pgtype.Text{String: *req.Updates.Priority, Valid: true}
		}
		if req.Updates.Position != nil {
			params.Position = pgtype.Float8{Float64: *req.Updates.Position, Valid: true}
		}
		if _, ok := rawUpdates["assignee_type"]; ok {
			if req.Updates.AssigneeType != nil {
				params.AssigneeType = pgtype.Text{String: *req.Updates.AssigneeType, Valid: true}
			} else {
				params.AssigneeType = pgtype.Text{Valid: false}
			}
		}
		if _, ok := rawUpdates["assignee_id"]; ok {
			if req.Updates.AssigneeID != nil {
				assigneeUUID, err := util.ParseUUID(*req.Updates.AssigneeID)
				if err != nil {
					continue
				}
				params.AssigneeID = assigneeUUID
			} else {
				params.AssigneeID = pgtype.UUID{Valid: false}
			}
		}
		if _, ok := rawUpdates["start_date"]; ok {
			if req.Updates.StartDate != nil && *req.Updates.StartDate != "" {
				d, err := util.ParseCalendarDate(*req.Updates.StartDate)
				if err != nil {
					continue
				}
				params.StartDate = d
			} else {
				params.StartDate = pgtype.Date{Valid: false}
			}
		}
		if _, ok := rawUpdates["due_date"]; ok {
			if req.Updates.DueDate != nil && *req.Updates.DueDate != "" {
				d, err := util.ParseCalendarDate(*req.Updates.DueDate)
				if err != nil {
					continue
				}
				params.DueDate = d
			} else {
				params.DueDate = pgtype.Date{Valid: false}
			}
		}

		if _, ok := rawUpdates["parent_issue_id"]; ok {
			if req.Updates.ParentIssueID != nil {
				newParentID, err := util.ParseUUID(*req.Updates.ParentIssueID)
				if err != nil {
					continue
				}
				// Cannot set self as parent.
				if newParentID == prevIssue.ID {
					continue
				}
				// Validate parent exists in the same workspace.
				if _, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
					ID:          newParentID,
					WorkspaceID: prevIssue.WorkspaceID,
				}); err != nil {
					continue
				}
				// Cycle detection: walk up from the new parent to ensure we don't reach this issue.
				cycleDetected := false
				cursor := newParentID
				for depth := 0; depth < 10; depth++ {
					ancestor, err := h.Queries.GetIssue(r.Context(), cursor)
					if err != nil || !ancestor.ParentIssueID.Valid {
						break
					}
					if ancestor.ParentIssueID == prevIssue.ID {
						cycleDetected = true
						break
					}
					cursor = ancestor.ParentIssueID
				}
				if cycleDetected {
					continue
				}
				params.ParentIssueID = newParentID
			} else {
				params.ParentIssueID = pgtype.UUID{Valid: false}
			}
		}
		if _, ok := rawUpdates["project_id"]; ok {
			// Resolved before the loop; an explicit null stays invalid and clears.
			params.ProjectID = batchProjectID
		}
		if _, ok := rawUpdates["stage"]; ok {
			if req.Updates.Stage != nil {
				if *req.Updates.Stage < 1 {
					continue
				}
				params.Stage = pgtype.Int4{Int32: *req.Updates.Stage, Valid: true}
			} else {
				params.Stage = pgtype.Int4{Valid: false} // explicit null = unstage
			}
		}

		// 当这次批量更新碰到受理人两个字段中任一字段时，校验结果组合。失败时静默跳过该条。
		//
		// scope 指向「本条 issue」（prevIssue 是本轮迭代的那行），因此批次里一条被绑定的
		// issue 永远不会把它的授权借给别的条目：未绑定的条目只是校验失败并被跳过。这确实
		// 是 agent 可达的真实鉴权点——task token 以它绑定的工作区成员身份认证，所以上面的
		// requireUserID 会被满足，而 resolveActor 仍把调用方归类为 agent（MUL-6691）。
		_, batchTouchedType := rawUpdates["assignee_type"]
		_, batchTouchedID := rawUpdates["assignee_id"]
		if batchTouchedType || batchTouchedID {
			if status, _ := h.validateAssigneePair(r.Context(), r, workspaceID, params.AssigneeType, params.AssigneeID, scopeExistingIssue(&prevIssue)); status != 0 {
				continue
			}
		}

		actorType, actorID := h.resolveActor(r, userID, workspaceID)
		// 批量写与单条更新走同一条事务/事件路径。这防止进程在 UPDATE 与 publish 之间退出时，
		// 出现某条变更可见却没有持久化投影。
		var issue db.Issue
		var lockedPrev db.Issue
		var persistedEvents []events.Event
		issue, lockedPrev, _, persistedEvents, err = h.updateIssueAtomicallyWithEvent(
			r.Context(), prevIssue.WorkspaceID, params, rawUpdates, nil, nil, nil, batchStatusKey,
			func(updated, previous db.Issue, attachmentsChanged bool) []events.Event {
				result := []events.Event{buildIssueUpdatedEvent(r.Context(), h, updated, previous, req.Updates, rawUpdates, actorType, actorID)}
				if attachmentsChanged {
					result = append(result, buildIssueAttachmentsChangedEvent(updated, actorType, actorID))
				}
				return result
			},
		)
		if err == nil {
			prevIssue = lockedPrev
			for _, persistedEvent := range persistedEvents {
				h.publishEvent(persistedEvent)
			}
		}
		if err != nil {
			// 归档竞态是整批共享目标状态的属性，不是某条 issue 的属性，所以剩余每条都会以同样
			// 的方式失败。用 409 中止而不是上报部分成功。
			if writeIssueStatusRaceError(w, err) {
				return
			}
			slog.Warn("batch update issue failed", "issue_id", issueID, "error", err)
			continue
		}

		prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
		resp := issueToResponse(issue, prefix)
		fillBatch(&resp)
		assigneeChanged := (req.Updates.AssigneeType != nil || req.Updates.AssigneeID != nil) &&
			(prevIssue.AssigneeType.String != issue.AssigneeType.String || uuidToString(prevIssue.AssigneeID) != uuidToString(issue.AssigneeID))
		statusChanged := req.Updates.Status != nil && prevIssue.Status != issue.Status

		// 换受理人不取消现有任务（#4963 / MUL-4113）——镜像 UpdateIssue，理由见那个 handler。
		//
		// 与 UpdateIssue 共用同一条谓词——批处理不得再自造一份入队规则副本（历史上正是这份
		// 副本造成四个入口漂移，MUL-3375）。suppress_run 对整批生效。
		if trigger, ok := h.IssueService.WillEnqueueRun(r.Context(),
			service.IssueTriggerInput{
				Issue:           issue,
				PrevStatus:      prevIssue.Status,
				AssigneeChanged: assigneeChanged,
				StatusChanged:   statusChanged,
			},
			h.issueTriggerWriteProbe(r, actorType, actorID, issue),
		); ok && !req.Updates.SuppressRun {
			h.dispatchIssueRun(r.Context(), issue, trigger, actorType, actorID, req.Updates.HandoffNote)
		}

		// 任何状态变更——包括 → cancelled——都不取消这里的活动任务，镜像 UpdateIssue
		//（MUL-4465）。理由见那个 handler。

		// 平台驱动的父级通知，镜像 UpdateIssue（MUL-2538）但推迟到循环之后。在这里按
		// 子任务逐条评估关卡屏障，会读到批中某个子任务的中途快照：一批同时关闭几个关卡时，
		// 会误发过期的「推进 Stage N+1」唤醒（MUL-4155）。所以先把终态转换收集起来，让下面
		// 的 notifyParentsOfBatchChildDone 基于整批最终已提交状态、对每个父级只评估一次。
		// 转换守卫与 notifyParentOfChildDone 相同：子任务从非终态 → 终态。终态测试前先把
		// 两边都解析成它们继承的 canonical 状态，这样把最后一个子任务移到「自定义的
		// done/cancelled」上的批次仍然会走进关卡屏障。字面比较会让 childDoneCompleted 为空、
		// 整个 notifyParentsOfBatchChildDone 被悄悄跳过。 (MUL-6243)
		if statusChanged && issue.ParentIssueID.Valid {
			prevTerminal := isTerminalChildStatus(
				issuestatus.Effective(r.Context(), h.Queries, prevIssue.WorkspaceID, prevIssue.Status))
			nowTerminal := isTerminalChildStatus(
				issuestatus.Effective(r.Context(), h.Queries, issue.WorkspaceID, issue.Status))
			if !prevTerminal && nowTerminal {
				childDoneCompleted = append(childDoneCompleted, issue)
			}
		}

		// 批量改状态与单选更新走同一入口：任务进入审核中时交给审查 Agent 接手。
		if statusChanged {
			h.afterIssueStatusChangeForReview(r.Context(), prevIssue, issue)
			h.afterIssueStatusChangeForSupervision(r.Context(), prevIssue, issue)
		}

		updated++
	}

	// 对整批最终状态做聚合的父级/关卡通知，让每个受影响的父级最多收到一次准确的评论 + 唤醒，
	// 与 issue_ids 的顺序无关（MUL-4155）。尽力而为；失败不中止整批。单条 UpdateIssue 不变，
	// 仍就地通知。
	h.notifyParentsOfBatchChildDone(r.Context(), childDoneCompleted)

	slog.Info("batch update issues", append(logger.RequestAttrs(r), "count", updated)...)
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}

// BatchDeleteIssuesRequest 是一次批量删除的请求体。
type BatchDeleteIssuesRequest struct {
	IssueIDs []string `json:"issue_ids"`
}

// BatchDeleteIssues 处理 POST /api/issues/batch-delete：删除一批 issue（解析失败/越界/已删
// 的 id 静默跳过），返回实际删掉的条数。删除前逐条取消任务、终止关联 autopilot 运行；
// 同批内的父级不会在删除时把自己的子级也删掉（它们都在 excludedIDs 里）。
func (h *Handler) BatchDeleteIssues(w http.ResponseWriter, r *http.Request) {
	var req BatchDeleteIssuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.IssueIDs) == 0 {
		writeError(w, http.StatusBadRequest, "issue_ids is required")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	issues := make([]db.Issue, 0, len(req.IssueIDs))
	excludedIDs := make([]pgtype.UUID, 0, len(req.IssueIDs))
	seenIssueIDs := make(map[pgtype.UUID]struct{}, len(req.IssueIDs))
	for _, issueID := range req.IssueIDs {
		issueUUID, err := util.ParseUUID(issueID)
		if err != nil {
			continue
		}
		if _, duplicate := seenIssueIDs[issueUUID]; duplicate {
			continue
		}
		issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          issueUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			continue
		}

		seenIssueIDs[issueUUID] = struct{}{}
		issues = append(issues, issue)
		excludedIDs = append(excludedIDs, issue.ID)
		h.TaskService.CancelTasksForIssue(r.Context(), issue.ID)
		if err := h.AutopilotService.FailAutopilotRunsByIssue(r.Context(), issue.ID); err != nil {
			slog.Warn("batch delete: failed to settle linked autopilot runs", "issue_id", uuidToString(issue.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to settle linked autopilot runs")
			return
		}
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	deleteResult, persistedEvents, err := h.deleteIssuesAndCollectAttachmentURLsWithEvents(r.Context(), issues, excludedIDs, actorType, actorID)
	if err != nil {
		slog.Warn("batch delete issues failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete issues")
		return
	}
	h.deleteS3Objects(r.Context(), deleteResult.AttachmentURLs)
	for _, event := range persistedEvents {
		h.publishEvent(event)
	}
	deleted := len(issues)

	slog.Info("batch delete issues", append(logger.RequestAttrs(r), "count", deleted)...)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}
