// 职责定位：issue 表格查询的公共底座：请求体类型、只读快照事务、超时与失败写回，以及把
// 行查询请求编译成（fingerprint + SQL + 参数）的编译器。rows/groups/facets 三个端点共用。
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 表格行/组/分面三个端点共享的分页与超时参数：默认每页 50、上限 100（内部多取一行探测
// next_cursor），查询整体 8 秒超时，防止过滤条件过宽时拖垮连接。
const (
	issueTableDefaultPageSize = 50
	issueTableMaxPageSize     = 100
	issueTableQueryTimeout    = 8 * time.Second
)

// withIssueTableQueryTimeout 给表格查询请求套一个整体超时上下文，覆盖编译 + 执行 + 事务提交
// 全程，超时由 writeIssueTableQueryFailure 统一按 504 回写。
func withIssueTableQueryTimeout(r *http.Request) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), issueTableQueryTimeout)
	return r.WithContext(ctx), cancel
}

// beginIssueTableSnapshot 开启 REPEATABLE READ 只读快照事务，并返回一个把 DB/Queries 都切到该
// 事务上的 Handler 快照副本。表格端点借助快照保证「一页里的总数、分面与行」读到同一份一致的
// 数据。快照必须由调用方显式 Commit/Rollback。
func (h *Handler) beginIssueTableSnapshot(ctx context.Context) (*Handler, pgx.Tx, error) {
	if h.TxStarter == nil {
		return nil, nil, errors.New("transaction starter is unavailable")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, nil, err
	}
	snapshot := *h
	snapshot.DB = tx
	snapshot.Queries = db.New(tx)
	return &snapshot, tx, nil
}

// writeIssueTableQueryFailure 按失败根因统一写回表格查询的错误：客户端取消（499）、整体超时
// （504 + 提示收窄过滤条件）、其余内部错误（500 + 原始信息）。各端点在此保持一致的错误语义。
func writeIssueTableQueryFailure(w http.ResponseWriter, r *http.Request, message string) {
	// 客户端在查询完成前离开页面时，连接上下文会被取消；这不是服务端
	// 故障，使用代理约定的 499 让日志和监控与真正的 5xx 区分开。
	if errors.Is(r.Context().Err(), context.Canceled) {
		writeError(w, 499, "request canceled")
		return
	}
	if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{
			"error":   "query_timeout",
			"message": "The table query took too long. Narrow the filters and retry.",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, message)
}

// issueTableActorRef 是受理人/创建者等「actor」在表格查询里的引用：type 为 member/agent/squad，
// ID 为该类型下实体的 UUID。
type issueTableActorRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// issueTableScope 定义表格范围：workspace（可再按受理人类型收窄到 members/agents 页签）、
// project、某受理人的 assignee、某创建人的 creator、或当前用户的 my（按 relation 收窄到
// assigned/created/involved/any）。
type issueTableScope struct {
	Kind          string              `json:"kind"`
	AssigneeTypes []string            `json:"assignee_types,omitempty"`
	ProjectID     string              `json:"project_id,omitempty"`
	Actor         *issueTableActorRef `json:"actor,omitempty"`
	Relation      string              `json:"relation,omitempty"`
}

// issueTableDateFilterRequest 是按时间字段过滤的范围：Field 限 created_at/updated_at，Start/End
// 为 RFC3339Nano，闭开区间 [Start, End)。
type issueTableDateFilterRequest struct {
	Field string `json:"field"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// issueTableFiltersRequest 是行查询里除 scope 外的所有过滤维度：状态/优先级/受理人（含未分配）/
// 创建者/项目（含无项目）/标签/属性/日期/正在运行（working）/是否含子 issue。
type issueTableFiltersRequest struct {
	Statuses          []string             `json:"statuses,omitempty"`
	Priorities        []string             `json:"priorities,omitempty"`
	Assignees         []issueTableActorRef `json:"assignees,omitempty"`
	IncludeNoAssignee bool                 `json:"include_no_assignee,omitempty"`
	Creators          []issueTableActorRef `json:"creators,omitempty"`
	ProjectIDs        []string             `json:"project_ids,omitempty"`
	IncludeNoProject  bool                 `json:"include_no_project,omitempty"`
	LabelIDs          []string             `json:"label_ids,omitempty"`
	// Properties 用原始 JSON 表达，让「算子对象（{op, value}）」与「纯字符串」两种成员形态都能
	// 原样往返到 parsePropertiesFilterParam。
	Properties       map[string][]json.RawMessage `json:"properties,omitempty"`
	Date             *issueTableDateFilterRequest `json:"date,omitempty"`
	WorkingOnly      bool                         `json:"working_only,omitempty"`
	WorkingIssueIDs  []string                     `json:"working_issue_ids,omitempty"`
	IncludeSubIssues *bool                        `json:"include_sub_issues,omitempty"`
}

// issueTableSortRequest 声明排序：Field 取内置列（position/title/created_at/…）或某个属性
// key，Direction 为 asc/desc。
type issueTableSortRequest struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// issueTableQuerySpec 是行查询的核心请求体：scope 限定可见范围，filters 过滤，search 全文搜，
// sort 排序。
type issueTableQuerySpec struct {
	Scope   issueTableScope          `json:"scope"`
	Filters issueTableFiltersRequest `json:"filters"`
	Search  string                   `json:"search,omitempty"`
	Sort    issueTableSortRequest    `json:"sort"`
}

// issueTableGroupSpec 描述「分组规格」：Kind 决定按哪个维度分（none/status/status_category/
// assignee/project/parent/property/compound）；property 分组带 PropertyID，IncludeEmpty 要求把
// 无行选项也补零行返回；compound 复合分组由 Primary 主维度 + Secondary 次级轴
// （status/status_category）组成，SecondaryValues 可选地只展示指定的次级值。
type issueTableGroupSpec struct {
	Kind            string   `json:"kind"`
	PropertyID      string   `json:"property_id,omitempty"`
	IncludeEmpty    bool     `json:"include_empty,omitempty"`
	Primary         string   `json:"primary,omitempty"`
	Secondary       string   `json:"secondary,omitempty"`
	SecondaryValues []string `json:"secondary_values,omitempty"`
}

// issueTablePageRequest 是三个端点统一的分页参数：Limit 缺省取 issueTableDefaultPageSize，
// Cursor 是 base64url 编码的不透明游标；续页时必须原样带上首页同一份 query。
type issueTablePageRequest struct {
	Limit  int     `json:"limit,omitempty"`
	Cursor *string `json:"cursor,omitempty"`
}

// issueTableHierarchyRequest 打开/关闭表格的「层级分支」模式：开启后行查询才能带 parent_id
// 落到某个父 issue 的子分支。
type issueTableHierarchyRequest struct {
	Enabled bool `json:"enabled"`
}

// issueTableGroupsRequest 是 POST /table/groups 的请求体：query 与 rows/facets 同源，group
// 声明分组维度，page 走统一分页。
type issueTableGroupsRequest struct {
	Query issueTableQuerySpec   `json:"query"`
	Group issueTableGroupSpec   `json:"group"`
	Page  issueTablePageRequest `json:"page"`
}

// issueTableRowsRequest 是 POST /table/rows 的请求体：query 定义行集；group.kind=none 时按
// 平铺行返回，否则返回 group_key 指定组内的行；hierarchy.enabled 后可选带 parent_id 只查该
// 父 issue 的子分支。page 走统一分页。
type issueTableRowsRequest struct {
	Query     issueTableQuerySpec        `json:"query"`
	Group     issueTableGroupSpec        `json:"group"`
	GroupKey  *string                    `json:"group_key"`
	Hierarchy issueTableHierarchyRequest `json:"hierarchy"`
	ParentID  *string                    `json:"parent_id"`
	Page      issueTablePageRequest      `json:"page"`
}

// issueTableFacetSpec 声明一个要统计的分面：Kind 取内置维度（status/priority/assignee/creator/
// project/label/working_agents）或 property；property 分面须带 PropertyID。
type issueTableFacetSpec struct {
	Kind       string `json:"kind"`
	PropertyID string `json:"property_id,omitempty"`
}

// issueTableFacetsRequest 是 POST /table/facets 的请求体：query 与 rows/groups 同源，Facets
// 列出要统计的分面（最多 issueTableMaxFacets 个），IncludeTotal 缺省为 true 时额外返回行总数。
type issueTableFacetsRequest struct {
	Query        issueTableQuerySpec   `json:"query"`
	Facets       []issueTableFacetSpec `json:"facets"`
	IncludeTotal *bool                 `json:"include_total,omitempty"`
}

// issueTableSQL 是一次编译好的行查询：where 是可拼进 SQL 的 WHERE 片段，args 是它的参数，
// fingerprint 是该查询的规范指纹，workspaceID 是已解析的当前工作区。三个端点共用此产物。
type issueTableSQL struct {
	where          string
	args           []any
	fingerprint    string
	workspaceID    pgtype.UUID
	crossWorkspace bool
}

// issueTableCursor 是行/组翻页游标的内容：先 JSON 序列化再 base64url 进 NextCursor，回传时
// 原样解码。Version 为游标格式演进预留；QueryFingerprint 与 GroupKey/ParentID/BranchIdentity
// 防止游标被跨查询复用；Sort*/Row* 是 keyset 定位下一页的锚点。
type issueTableCursor struct {
	Version          int     `json:"v"`
	QueryFingerprint string  `json:"query"`
	GroupKey         *string `json:"group_key,omitempty"`
	ParentID         *string `json:"parent_id,omitempty"`
	GroupOrder       *int    `json:"group_order,omitempty"`
	GroupSortKey     *string `json:"group_sort_key,omitempty"`
	GroupCursorKey   *string `json:"group_cursor_key,omitempty"`
	BranchIdentity   string  `json:"branch_identity,omitempty"`
	SortValue        *string `json:"sort_value,omitempty"`
	SortIsNull       bool    `json:"sort_is_null,omitempty"`
	RowCreatedAt     string  `json:"row_created_at,omitempty"`
	RowID            string  `json:"row_id,omitempty"`
}

// decodeIssueTableJSON 按表格端点的严格约定解码请求体：限制 1 MiB、拒绝未知字段、且正文必须
// 恰好是一个 JSON 对象（不允许尾随多余内容）。失败已写回 400 并返回 false。
func decodeIssueTableJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid issue table query: "+err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid issue table query: request must contain one JSON object")
		return false
	}
	return true
}

// normalizeIssueTablePage 把统一分页参数解析成内部 (limit, cursor)：limit 缺省落默认页大小、
// 越界写 400；cursor 为空返回 nil，非空则 base64url 解码并校验游标版本。失败已写回 400。
func normalizeIssueTablePage(w http.ResponseWriter, page issueTablePageRequest) (int, *issueTableCursor, bool) {
	limit := page.Limit
	if limit == 0 {
		limit = issueTableDefaultPageSize
	}
	if limit < 1 || limit > issueTableMaxPageSize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("page.limit must be between 1 and %d", issueTableMaxPageSize))
		return 0, nil, false
	}
	if page.Cursor == nil || strings.TrimSpace(*page.Cursor) == "" {
		return limit, nil, true
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*page.Cursor)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid cursor")
		return 0, nil, false
	}
	var cursor issueTableCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.Version != 1 {
		writeError(w, http.StatusBadRequest, "invalid cursor")
		return 0, nil, false
	}
	return limit, &cursor, true
}

// encodeIssueTableCursor 把游标结构 JSON 序列化后 base64url 编码成不透明字符串；编码失败返回
// nil（仅理论可能），调用方不再单独上报。
func encodeIssueTableCursor(cursor issueTableCursor) *string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return nil
	}
	value := base64.RawURLEncoding.EncodeToString(encoded)
	return &value
}

// issueTableCursorMatches 校验游标是否与本次查询同源：查询指纹必须一致，行游标的 group key 与
// 父分支（parentID）也要逐项一致，否则说明游标来自另一张表查询——写 409 让前端放弃续页。
func issueTableCursorMatches(w http.ResponseWriter, cursor *issueTableCursor, fingerprint string, groupKey, parentID *string) bool {
	if cursor == nil {
		return true
	}
	if cursor.QueryFingerprint != fingerprint || !equalOptionalString(cursor.GroupKey, groupKey) || !equalOptionalString(cursor.ParentID, parentID) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "cursor_query_mismatch",
			"message": "cursor does not belong to this table query",
		})
		return false
	}
	return true
}

// equalOptionalString 比较两个可空字符串：两者都为空视为相等，一空一非空视为不等。
func equalOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// canonicalIssueTableFingerprint 对 (workspace, query) 生成规范指纹：先把查询里所有集合类字段
// 去空白、去重、排序（保证语义相同的不同书写顺序得到同一指纹），连同 workspace 序列化后取
// SHA-256。游标靠它与 rows 首页校验「是否同一查询」。
func canonicalIssueTableFingerprint(workspaceID string, spec issueTableQuerySpec) (string, error) {
	explicitEmptyAssignees :=
		spec.Filters.Assignees != nil && len(spec.Filters.Assignees) == 0
	explicitEmptyWorkingIssues :=
		spec.Filters.WorkingIssueIDs != nil && len(spec.Filters.WorkingIssueIDs) == 0
	normalized := spec
	normalized.Search = strings.TrimSpace(normalized.Search)
	normalized.Scope.AssigneeTypes = sortedUniqueStrings(normalized.Scope.AssigneeTypes)
	normalized.Filters.Statuses = sortedUniqueStrings(normalized.Filters.Statuses)
	normalized.Filters.Priorities = sortedUniqueStrings(normalized.Filters.Priorities)
	normalized.Filters.ProjectIDs = sortedUniqueStrings(normalized.Filters.ProjectIDs)
	normalized.Filters.LabelIDs = sortedUniqueStrings(normalized.Filters.LabelIDs)
	normalized.Filters.Assignees = sortedUniqueActors(normalized.Filters.Assignees)
	normalized.Filters.WorkingIssueIDs = sortedUniqueStrings(normalized.Filters.WorkingIssueIDs)
	normalized.Filters.Creators = sortedUniqueActors(normalized.Filters.Creators)
	for key, values := range normalized.Filters.Properties {
		normalized.Filters.Properties[key] = sortedUniqueRawJSON(values)
	}
	encoded, err := json.Marshal(struct {
		WorkspaceID                string              `json:"workspace_id"`
		Query                      issueTableQuerySpec `json:"query"`
		ExplicitEmptyAssignees     bool                `json:"explicit_empty_assignees,omitempty"`
		ExplicitEmptyWorkingIssues bool                `json:"explicit_empty_working_issues,omitempty"`
	}{
		WorkspaceID:                workspaceID,
		Query:                      normalized,
		ExplicitEmptyAssignees:     explicitEmptyAssignees,
		ExplicitEmptyWorkingIssues: explicitEmptyWorkingIssues,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// sortedUniqueStrings 把字符串集合规整为「去空白、丢空串、去重、排序」的切片；结果为空时
// 返回 nil。指纹规范化与构造 `= ANY(...)` 参数共用此规整。
func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	return result
}

// sortedUniqueRawJSON 对属性过滤成员做指纹规范化：成员是原始 JSON（纯字符串与算子对象两种
// 形态），按字节精确去重 + 排序——保证同一查询无论成员书写顺序如何，再序列化回指纹输入时
// 字节完全一致。
func sortedUniqueRawJSON(values []json.RawMessage) []json.RawMessage {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		key := string(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	sort.Strings(unique)
	result := make([]json.RawMessage, len(unique))
	for i, value := range unique {
		result[i] = json.RawMessage(value)
	}
	return result
}

// sortedUniqueActors 把 actor 列表按 type:id 去重并排序，用于指纹规范化；结果为空时返回 nil。
func sortedUniqueActors(values []issueTableActorRef) []issueTableActorRef {
	seen := make(map[string]issueTableActorRef, len(values))
	for _, value := range values {
		key := value.Type + ":" + value.ID
		seen[key] = value
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]issueTableActorRef, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// issueTableContainsString 判断 candidate 是否在 values 里。调用处的白名单（内置状态/优先级、
// actor 类型）都很短，线性查找足够。
func issueTableContainsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// parseIssueTableUUIDList 把字符串 UUID 列表逐项解析为 pgtype.UUID；任一项非法即写 400（错误
// 信息用 field 点名是哪个参数）并返回 false。
func parseIssueTableUUIDList(w http.ResponseWriter, values []string, field string) ([]pgtype.UUID, bool) {
	result := make([]pgtype.UUID, 0, len(values))
	for _, raw := range values {
		parsed, err := util.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid "+field)
			return nil, false
		}
		result = append(result, parsed)
	}
	return result, true
}

// parseIssueTableActor 校验并解析一个 actor 引用：type 必须属于 member/agent/squad、ID 必须
// 是合法 UUID，产出内部 issueActorFilter；非法则写 400 并返回 false。
func parseIssueTableActor(w http.ResponseWriter, actor issueTableActorRef, field string) (issueActorFilter, bool) {
	if !isIssueActorType(actor.Type) {
		writeError(w, http.StatusBadRequest, "invalid "+field)
		return issueActorFilter{}, false
	}
	id, err := util.ParseUUID(actor.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid "+field)
		return issueActorFilter{}, false
	}
	return issueActorFilter{actorType: actor.Type, actorID: id}, true
}

// appendIssueTableInvolvedPredicate 追加「用户 involved」谓词，覆盖三类委托：该用户名下的
// agent 直接受理、用户作为成员/组长或名下 agent 作为成员所在的 squad 受理。按任务所属工作区解析，
// 其余参数经 addArg 分配占位符。
func appendIssueTableInvolvedPredicate(where []string, addArg func(any) string, userID pgtype.UUID) []string {
	ref := addArg(userID)
	return append(where, fmt.Sprintf(`(
    (i.assignee_type = 'agent' AND i.assignee_id IN (
       SELECT a.id FROM agent a
        WHERE a.workspace_id = i.workspace_id
          AND a.owner_id     = %[1]s::uuid
    ))
    OR (i.assignee_type = 'squad' AND i.assignee_id IN (
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
        WHERE s.workspace_id = i.workspace_id
          AND sm.member_type = 'member'
          AND sm.member_id   = %[1]s::uuid
       UNION
       SELECT s.id
         FROM squad s
         JOIN agent a ON a.id = s.leader_id
        WHERE s.workspace_id = i.workspace_id
          AND a.workspace_id = i.workspace_id
          AND a.owner_id     = %[1]s::uuid
       UNION
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
         JOIN agent a ON a.id = sm.member_id
        WHERE s.workspace_id = i.workspace_id
          AND sm.member_type = 'agent'
          AND a.workspace_id = i.workspace_id
          AND a.owner_id     = %[1]s::uuid
    ))
)`, ref))
}

// compileIssueTableQuery 把一次行查询规格编译成 issueTableSQL：按 scope 解析可见范围、按
// filters 逐维度追加 WHERE 片段并参数化收集到 args，最后接 search 全文搜；任一字段非法都写
// 4xx 并返回 false。编译产物被 rows/groups/facets 三个端点直接复用。
func (h *Handler) compileIssueTableQuery(w http.ResponseWriter, r *http.Request, spec issueTableQuerySpec) (issueTableSQL, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return issueTableSQL{}, false
	}
	fingerprint, err := canonicalIssueTableFingerprint(util.UUIDToString(workspaceUUID), spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to canonicalize table query")
		return issueTableSQL{}, false
	}

	crossWorkspace := spec.Scope.Kind == "my" && r.Header.Get("X-Actor-Source") != "task_token"
	where := []string{"i.workspace_id = $1"}
	args := []any{workspaceUUID}
	addArg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}

	// 任意非空的状态 KEY 都合法，不只那 7 个内置。状态过滤点名的是用户勾选的具体状态，自
	// MUL-6243 起工作区可以有自定义状态——若在这里拒绝它们会让整个请求 400：按自定义状态筛
	// 看板报错而不是收窄。谓词是参数化的 `status = ANY(...)`，未知 key 自然匹配不到任何行；加上
	// 长度上限，防止任意大段文本冒充状态 key 混进来。
	for _, status := range spec.Filters.Statuses {
		if status == "" || len(status) > 64 {
			writeError(w, http.StatusBadRequest, "invalid filters.statuses")
			return issueTableSQL{}, false
		}
	}
	if len(spec.Filters.Statuses) > 0 {
		statusRef := addArg(sortedUniqueStrings(spec.Filters.Statuses))
		if crossWorkspace {
			where = append(where, fmt.Sprintf("(i.status = ANY(%s::text[]) OR (%s) = ANY(%s::text[]))", statusRef, issueStatusCategorySQL, statusRef))
		} else {
			where = append(where, fmt.Sprintf("i.status = ANY(%s::text[])", statusRef))
		}
	}
	for _, priority := range spec.Filters.Priorities {
		if !issueTableContainsString(validIssuePriorities, priority) {
			writeError(w, http.StatusBadRequest, "invalid filters.priorities")
			return issueTableSQL{}, false
		}
	}
	if len(spec.Filters.Priorities) > 0 {
		where = append(where, fmt.Sprintf("i.priority = ANY(%s::text[])", addArg(sortedUniqueStrings(spec.Filters.Priorities))))
	}

	// workspace 与 project 两种 scope 共用可选的「受理人类型收窄」（两种视图上方的 Members/
	// Agents 页签），抽成闭包避免两处重复同样的校验与拼装。
	appendAssigneeTypes := func() bool {
		for _, actorType := range spec.Scope.AssigneeTypes {
			if !isIssueActorType(actorType) {
				writeError(w, http.StatusBadRequest, "invalid scope.assignee_types")
				return false
			}
		}
		if len(spec.Scope.AssigneeTypes) > 0 {
			where = append(where, fmt.Sprintf("i.assignee_type = ANY(%s::text[])", addArg(sortedUniqueStrings(spec.Scope.AssigneeTypes))))
		}
		return true
	}
	switch spec.Scope.Kind {
	case "workspace":
		if !appendAssigneeTypes() {
			return issueTableSQL{}, false
		}
	case "project":
		projectID, err := util.ParseUUID(spec.Scope.ProjectID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid scope.project_id")
			return issueTableSQL{}, false
		}
		where = append(where, fmt.Sprintf("i.project_id = %s::uuid", addArg(projectID)))
		if !appendAssigneeTypes() {
			return issueTableSQL{}, false
		}
	case "assignee":
		if spec.Scope.Actor == nil {
			writeError(w, http.StatusBadRequest, "scope.actor is required")
			return issueTableSQL{}, false
		}
		actor, ok := parseIssueTableActor(w, *spec.Scope.Actor, "scope.actor")
		if !ok {
			return issueTableSQL{}, false
		}
		where = append(where, fmt.Sprintf("i.assignee_type = %s::text AND i.assignee_id = %s::uuid", addArg(actor.actorType), addArg(actor.actorID)))
	case "creator":
		if spec.Scope.Actor == nil {
			writeError(w, http.StatusBadRequest, "scope.actor is required")
			return issueTableSQL{}, false
		}
		actor, ok := parseIssueTableActor(w, *spec.Scope.Actor, "scope.actor")
		if !ok {
			return issueTableSQL{}, false
		}
		where = append(where, fmt.Sprintf("i.creator_type = %s::text AND i.creator_id = %s::uuid", addArg(actor.actorType), addArg(actor.actorID)))
	case "my":
		userID, ok := requireUserID(w, r)
		if !ok {
			return issueTableSQL{}, false
		}
		userUUID, err := util.ParseUUID(userID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not authenticated")
			return issueTableSQL{}, false
		}
		// 聚合范围由当前用户的工作区成员关系决定。
		if crossWorkspace {
			where[0] = fmt.Sprintf("i.workspace_id IN (SELECT m.workspace_id FROM member m WHERE m.user_id = %s::uuid) AND $1::uuid IS NOT NULL", addArg(userUUID))
		}
		relation := spec.Scope.Relation
		if relation == "" {
			relation = "any"
		}
		switch relation {
		case "assigned":
			where = append(where, fmt.Sprintf("i.assignee_type = 'member' AND i.assignee_id = %s::uuid", addArg(userUUID)))
		case "created":
			where = append(where, fmt.Sprintf("i.creator_type = 'member' AND i.creator_id = %s::uuid", addArg(userUUID)))
		case "involved":
			where = appendIssueTableInvolvedPredicate(where, addArg, userUUID)
		case "any":
			assignedRef := addArg(userUUID)
			createdRef := addArg(userUUID)
			involved := appendIssueTableInvolvedPredicate(nil, addArg, userUUID)[0]
			where = append(where, fmt.Sprintf("((i.assignee_type = 'member' AND i.assignee_id = %s::uuid) OR (i.creator_type = 'member' AND i.creator_id = %s::uuid) OR %s)", assignedRef, createdRef, involved))
		default:
			writeError(w, http.StatusBadRequest, "invalid scope.relation")
			return issueTableSQL{}, false
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid scope.kind")
		return issueTableSQL{}, false
	}

	if spec.Filters.Assignees != nil || spec.Filters.IncludeNoAssignee {
		ors := make([]string, 0, len(spec.Filters.Assignees)+1)
		for _, value := range spec.Filters.Assignees {
			actor, ok := parseIssueTableActor(w, value, "filters.assignees")
			if !ok {
				return issueTableSQL{}, false
			}
			ors = append(ors, fmt.Sprintf("(i.assignee_type = %s::text AND i.assignee_id = %s::uuid)", addArg(actor.actorType), addArg(actor.actorID)))
		}
		if spec.Filters.IncludeNoAssignee {
			ors = append(ors, "(i.assignee_type IS NULL AND i.assignee_id IS NULL)")
		}
		if len(ors) == 0 {
			// Assignees 省略 = 不施加受理人过滤；显式传空数组 = 匹配空集（FALSE），让 API 调用方
			// 能保留一个「故意激活但为空」的谓词。
			where = append(where, "FALSE")
		} else {
			where = append(where, "("+strings.Join(ors, " OR ")+")")
		}
	}

	if len(spec.Filters.Creators) > 0 {
		ors := make([]string, 0, len(spec.Filters.Creators))
		for _, value := range spec.Filters.Creators {
			actor, ok := parseIssueTableActor(w, value, "filters.creators")
			if !ok {
				return issueTableSQL{}, false
			}
			ors = append(ors, fmt.Sprintf("(i.creator_type = %s::text AND i.creator_id = %s::uuid)", addArg(actor.actorType), addArg(actor.actorID)))
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	projectIDs, ok := parseIssueTableUUIDList(w, spec.Filters.ProjectIDs, "filters.project_ids")
	if !ok {
		return issueTableSQL{}, false
	}
	if len(projectIDs) > 0 || spec.Filters.IncludeNoProject {
		ors := make([]string, 0, 2)
		if len(projectIDs) > 0 {
			ors = append(ors, fmt.Sprintf("i.project_id = ANY(%s::uuid[])", addArg(projectIDs)))
		}
		if spec.Filters.IncludeNoProject {
			ors = append(ors, "i.project_id IS NULL")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	labelIDs, ok := parseIssueTableUUIDList(w, spec.Filters.LabelIDs, "filters.label_ids")
	if !ok {
		return issueTableSQL{}, false
	}
	if len(labelIDs) > 0 {
		where = append(where, fmt.Sprintf("EXISTS (SELECT 1 FROM issue_to_label itl WHERE itl.issue_id = i.id AND itl.label_id = ANY(%s::uuid[]))", addArg(labelIDs)))
	}

	if len(spec.Filters.Properties) > 0 {
		raw, err := json.Marshal(spec.Filters.Properties)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid filters.properties")
			return issueTableSQL{}, false
		}
		compiled, ok := parsePropertiesFilterParam(w, string(raw))
		if !ok {
			return issueTableSQL{}, false
		}
		if len(compiled) > 0 {
			where = append(where, propertiesFilterPredicate(compiled, addArg))
		}
	}

	if spec.Filters.Date != nil {
		column := ""
		switch spec.Filters.Date.Field {
		case "created_at", "updated_at":
			column = spec.Filters.Date.Field
		default:
			writeError(w, http.StatusBadRequest, "invalid filters.date.field")
			return issueTableSQL{}, false
		}
		start, startErr := time.Parse(time.RFC3339Nano, spec.Filters.Date.Start)
		end, endErr := time.Parse(time.RFC3339Nano, spec.Filters.Date.End)
		if startErr != nil || endErr != nil || !start.Before(end) {
			writeError(w, http.StatusBadRequest, "invalid filters.date range")
			return issueTableSQL{}, false
		}
		where = append(where, fmt.Sprintf("i.%s >= %s AND i.%s < %s", column, addArg(start), column, addArg(end)))
	}

	if spec.Filters.WorkingOnly {
		where = append(where, "EXISTS (SELECT 1 FROM agent_task_queue atq WHERE atq.issue_id = i.id AND atq.status = 'running')")
	}
	workingIssueIDs, ok := parseIssueTableUUIDList(w, spec.Filters.WorkingIssueIDs, "filters.working_issue_ids")
	if !ok {
		return issueTableSQL{}, false
	}
	if spec.Filters.WorkingIssueIDs != nil {
		if len(workingIssueIDs) == 0 {
			where = append(where, "FALSE")
		} else {
			where = append(where, fmt.Sprintf(
				"i.id = ANY(%s::uuid[])",
				addArg(workingIssueIDs),
			))
		}
	}
	if spec.Filters.IncludeSubIssues != nil && !*spec.Filters.IncludeSubIssues {
		where = append(where, "i.parent_issue_id IS NULL")
	}
	where = appendIssueTableSearchFilter(where, addArg, spec.Search)

	return issueTableSQL{
		where:          strings.Join(where, " AND "),
		args:           args,
		fingerprint:    fingerprint,
		workspaceID:    workspaceUUID,
		crossWorkspace: crossWorkspace,
	}, true
}
