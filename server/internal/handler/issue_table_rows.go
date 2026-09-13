// 职责定位：issue 表格的行读取（POST .../table/rows）：在 REPEATABLE READ 快照里按查询规格
// 拉取一页行（含分组键/游标分页/层级）。快照保证一页内的总数与聚合一致性。
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// issueTableRowResponse 是 rows 响应里的一行：issue 完整载荷，外加层级模式下该 issue 的
// 直接子行数（非层级恒为 0）。
type issueTableRowResponse struct {
	Issue            IssueResponse `json:"issue"`
	DirectChildCount int64         `json:"direct_child_count"`
}

// issueTableRowsResponse 是 POST /table/rows 的响应体：指纹/分组键/父分支定位「这是哪一页」，
// Total 只在未分组首屏时是全量行数，Rows 是本页行，NextCursor 指向下一页。
// BranchTotal 恒等于本页行数，是给老客户端保留的兼容字段。
type issueTableRowsResponse struct {
	QueryFingerprint string                  `json:"query_fingerprint"`
	GroupKey         *string                 `json:"group_key"`
	ParentID         *string                 `json:"parent_id"`
	Total            int64                   `json:"total"`
	Rows             []issueTableRowResponse `json:"rows"`
	BranchTotal      int64                   `json:"branch_total"` // 兼容字段：恒为当前页行数。
	NextCursor       *string                 `json:"next_cursor"`
}

// resolvedIssueTableSort 是解析并验证后的排序规格：expression 是排序用的 SQL 表达式；
// direction 归一化为 asc/desc；castType 决定游标值如何解码与回写（text/integer/numeric/
// timestamptz/date）；nullsLast 把空值排到末尾（日期等可空字段）；idOnlyTie 表示并列时只用
// id 断开即可，不必再比较 created_at。
type resolvedIssueTableSort struct {
	expression string
	direction  string
	castType   string
	nullsLast  bool
	idOnlyTie  bool
}

// orderBy 把排序规格拼成 ORDER BY 片段：主表达式 + 方向，可选 NULLS LAST；并列时默认再按
// created_at、id 倒序稳定断开，idOnlyTie 的排序维度则只需 id 倒序。
func (sort resolvedIssueTableSort) orderBy() string {
	orderBy := sort.expression + " " + strings.ToUpper(sort.direction)
	if sort.nullsLast {
		orderBy += " NULLS LAST"
	}
	if sort.idOnlyTie {
		return orderBy + ", i.id DESC"
	}
	return orderBy + ", i.created_at DESC, i.id DESC"
}

// cursorPredicate 把 keyset 游标翻译成翻页 WHERE 谓词：比较排序列值与锚点行，相等时退到平级
// 断开条件（仅 id 或 created_at+id）；同时按 castType 校验游标里的值类型、按 direction 选择
// >/<，NULLS LAST 时把「空值仍排在前」也折进谓词。非法游标一律写 400。
func (sort resolvedIssueTableSort) cursorPredicate(w http.ResponseWriter, cursor *issueTableCursor, addArg func(any) string) (string, bool) {
	if cursor == nil {
		return "TRUE", true
	}
	if cursor.RowID == "" {
		writeError(w, http.StatusBadRequest, "invalid cursor")
		return "", false
	}
	rowID, err := util.ParseUUID(cursor.RowID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid cursor")
		return "", false
	}
	idRef := addArg(rowID)
	tie := fmt.Sprintf("i.id < %s::uuid", idRef)
	if !sort.idOnlyTie {
		createdAt, err := time.Parse(time.RFC3339Nano, cursor.RowCreatedAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return "", false
		}
		createdRef := addArg(createdAt)
		tie = fmt.Sprintf("(i.created_at < %s::timestamptz OR (i.created_at = %s::timestamptz AND i.id < %s::uuid))", createdRef, createdRef, idRef)
	}
	if cursor.SortIsNull {
		if !sort.nullsLast || cursor.SortValue != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return "", false
		}
		return fmt.Sprintf("(%s IS NULL AND %s)", sort.expression, tie), true
	}
	if cursor.SortValue == nil {
		writeError(w, http.StatusBadRequest, "invalid cursor")
		return "", false
	}
	switch sort.castType {
	case "integer":
		if _, err := strconv.ParseInt(*cursor.SortValue, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return "", false
		}
	case "double precision", "numeric":
		if _, err := strconv.ParseFloat(*cursor.SortValue, 64); err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return "", false
		}
	case "timestamptz":
		if _, err := time.Parse(time.RFC3339Nano, *cursor.SortValue); err != nil {
			if _, postgresErr := time.Parse("2006-01-02 15:04:05.999999999Z07", *cursor.SortValue); postgresErr != nil {
				writeError(w, http.StatusBadRequest, "invalid cursor")
				return "", false
			}
		}
	case "date":
		if _, err := time.Parse("2006-01-02", *cursor.SortValue); err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return "", false
		}
	}
	valueRef := addArg(*cursor.SortValue)
	valueExpr := fmt.Sprintf("%s::%s", valueRef, sort.castType)
	comparison := ">"
	if sort.direction == "desc" {
		comparison = "<"
	}
	predicate := fmt.Sprintf("(%s %s %s OR (%s = %s AND %s))", sort.expression, comparison, valueExpr, sort.expression, valueExpr, tie)
	// 混合方向的精确 keyset 谓词本身走不了索引；这个看似冗余的「下界」条件能让 PostgreSQL 直接
	// 从游标位置开始扫默认 position 索引，而不是把游标前的每条索引项都过滤一遍。
	if sort.expression == "i.position" {
		predicate = fmt.Sprintf("(%s >= %s AND %s)", sort.expression, valueExpr, predicate)
	}
	if sort.nullsLast {
		predicate = fmt.Sprintf("(%s IS NULL OR %s)", sort.expression, predicate)
	}
	return predicate, true
}

// issueTableOrderBy 解析行查询的 sort 字段：内置列映射到固定 SQL 表达式与 castType；未知
// 字段视为属性 key，委托 propertySortExpr 解析成属性值表达式（文本/数值需按 cast 区分）。
// 方向缺省 asc——但 last_activity 这类 idOnlyTie 维度默认倒序才是「最新在前」；非法方向写 400。
func (h *Handler) issueTableOrderBy(w http.ResponseWriter, r *http.Request, workspaceID string, sortRequest issueTableSortRequest) (resolvedIssueTableSort, bool) {
	sortField := strings.TrimSpace(sortRequest.Field)
	if sortField == "" {
		sortField = "position"
	}
	resolved := resolvedIssueTableSort{
		expression: "i.position",
		direction:  "asc",
		castType:   "double precision",
	}
	switch sortField {
	case "position":
	case "title":
		resolved.expression = "i.title"
		resolved.castType = "text"
	case "created_at", "updated_at":
		resolved.expression = "i." + sortField
		resolved.castType = "timestamptz"
	case "last_activity":
		resolved.expression = "i.last_activity_at"
		resolved.castType = "timestamptz"
		resolved.nullsLast = true
		resolved.idOnlyTie = true
	case "start_date", "due_date":
		resolved.expression = "i." + sortField
		resolved.castType = "date"
		resolved.nullsLast = true
	case "status":
		resolved.expression = "CASE i.status WHEN 'backlog' THEN 0 WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'in_review' THEN 3 WHEN 'done' THEN 4 WHEN 'blocked' THEN 5 WHEN 'cancelled' THEN 6 ELSE 7 END"
		resolved.castType = "integer"
	case "priority":
		resolved.expression = "CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END"
		resolved.castType = "integer"
	default:
		expr, handled, err := h.propertySortExpr(r, workspaceID, sortField)
		if !handled {
			writeError(w, http.StatusBadRequest, "invalid query.sort.field")
			return resolvedIssueTableSort{}, false
		}
		if err != nil {
			if strings.HasPrefix(err.Error(), "resolve sort property:") {
				slog.Warn("resolve table sort property failed", append(logger.RequestAttrs(r), "error", err)...)
				writeIssueTableQueryFailure(w, r, "failed to resolve table sort")
			} else {
				writeError(w, http.StatusBadRequest, err.Error())
			}
			return resolvedIssueTableSort{}, false
		}
		if expr != "" {
			resolved.expression = expr
			resolved.castType = "text"
			if strings.Contains(expr, "::numeric") {
				resolved.castType = "numeric"
			}
			resolved.nullsLast = true
		}
	}

	direction := strings.ToLower(strings.TrimSpace(sortRequest.Direction))
	if direction == "" {
		direction = "asc"
		if resolved.idOnlyTie {
			direction = "desc"
		}
	}
	if direction != "asc" && direction != "desc" {
		writeError(w, http.StatusBadRequest, "invalid query.sort.direction")
		return resolvedIssueTableSort{}, false
	}
	if sortField != "position" && resolved.expression != "i.position" {
		resolved.direction = direction
	}
	return resolved, true
}

// normalizeIssueTableGroupKey 规整 rows 请求的 group_key：group.kind=none 时必须缺省或为空，
// 分组视图则必须带非空 group_key（不然无法定位要取哪一组的行）。不合法写 400。
func normalizeIssueTableGroupKey(w http.ResponseWriter, group issueTableGroupSpec, groupKey *string) (*string, bool) {
	if group.Kind == "none" {
		if groupKey != nil && strings.TrimSpace(*groupKey) != "" {
			writeError(w, http.StatusBadRequest, "group_key must be empty when group.kind=none")
			return nil, false
		}
		return nil, true
	}
	if groupKey == nil || strings.TrimSpace(*groupKey) == "" {
		writeError(w, http.StatusBadRequest, "group_key is required")
		return nil, false
	}
	normalized := strings.TrimSpace(*groupKey)
	return &normalized, true
}

// ListIssueTableRows 处理 POST /api/issues/table/rows：在只读快照事务中执行 issue 表格的行
// 查询（分组/排序/过滤/游标/层级），返回一页行与 total。游标校验失败或与当前分支不匹配时
// 返回 409。
func (h *Handler) ListIssueTableRows(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		writeError(w, http.StatusInternalServerError, "database is unavailable")
		return
	}
	baseHandler := h
	var request issueTableRowsRequest
	if !decodeIssueTableJSON(w, r, &request) {
		return
	}
	r, cancel := withIssueTableQueryTimeout(r)
	defer cancel()
	snapshot, tx, err := h.beginIssueTableSnapshot(r.Context())
	if err != nil {
		slog.Warn("ListIssueTableRows snapshot failed", append(logger.RequestAttrs(r), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to start table query")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	h = snapshot
	limit, cursor, ok := normalizeIssueTablePage(w, request.Page)
	if !ok {
		return
	}
	groupKey, ok := normalizeIssueTableGroupKey(w, request.Group, request.GroupKey)
	if !ok {
		return
	}
	if !request.Hierarchy.Enabled && request.ParentID != nil {
		writeError(w, http.StatusBadRequest, "parent_id requires hierarchy.enabled=true")
		return
	}
	compiled, ok := h.compileIssueTableQuery(w, r, request.Query)
	if !ok {
		return
	}
	if !issueTableCursorMatches(w, cursor, compiled.fingerprint, groupKey, request.ParentID) {
		return
	}
	branchIdentity := issueTableGroupIdentity(request.Group) + ":hierarchy=" + strconv.FormatBool(request.Hierarchy.Enabled)
	if cursor != nil && cursor.BranchIdentity != branchIdentity {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "cursor_query_mismatch",
			"message": "cursor does not belong to this table branch",
		})
		return
	}
	group, ok := h.resolveIssueTableGroup(w, r, compiled.workspaceID, request.Group, true)
	if !ok {
		return
	}
	resolvedSort, ok := h.issueTableOrderBy(w, r, util.UUIDToString(compiled.workspaceID), request.Query.Sort)
	if !ok {
		return
	}

	args := append([]any(nil), compiled.args...)
	addArg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	predicateKey := ""
	if groupKey != nil {
		predicateKey = *groupKey
	}
	groupPredicate, ok := group.predicate(w, predicateKey, addArg)
	if !ok {
		return
	}
	branchPredicate := "TRUE"
	if request.Hierarchy.Enabled {
		if request.ParentID == nil {
			// 把「父 issue 是否还在视图里」保持为标量子查询：NOT EXISTS 语义等价，但 PostgreSQL
			// 会把它展开成全表 hash 反连接，在 LIMIT 之前丢掉有序的 workspace 索引。
			branchPredicate = "i.parent_issue_id IS NULL OR (SELECT parent.id FROM membership parent WHERE parent.id = i.parent_issue_id) IS NULL"
		} else {
			parentUUID, err := util.ParseUUID(*request.ParentID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid parent_id")
				return
			}
			parentRef := addArg(parentUUID)
			branchPredicate = fmt.Sprintf("i.parent_issue_id = %s::uuid AND EXISTS (SELECT 1 FROM membership parent WHERE parent.id = %s::uuid)", parentRef, parentRef)
		}
	}
	cursorPredicate, ok := resolvedSort.cursorPredicate(w, cursor, addArg)
	if !ok {
		return
	}
	limitRef := addArg(limit + 1)

	// 先取「当前页」再做层级元数据计算。旧查询会把每条命中 issue 都物化、并在 LIMIT 前聚合所有
	// 父 issue，导致 51 行的一页把整个工作区的 membership 溢出到磁盘。NOT MATERIALIZED 让
	// PostgreSQL 把 parent_id/id 谓词下推到 issue 表，用父索引服务子分支取页与逐页子行计数。
	ctePrefix := "WITH "
	pageSource := "issue"
	pagePredicate := fmt.Sprintf("(%s) AND (%s)", compiled.where, groupPredicate)
	if request.Hierarchy.Enabled {
		ctePrefix += fmt.Sprintf(`membership AS NOT MATERIALIZED (
  SELECT i.*
  FROM issue i
  WHERE %s AND (%s)
), `, compiled.where, groupPredicate)
		pageSource = "membership"
		pagePredicate = branchPredicate
	} else if group.kind == "compound" &&
		group.primary != nil &&
		group.primary.kind == "parent" &&
		group.secondaryFiltered {
		visibleRef := addArg(group.secondaryValues)
		ctePrefix += fmt.Sprintf(`membership AS NOT MATERIALIZED (
  SELECT i.*
  FROM issue i
  WHERE %s
), promoted_parents AS (
  SELECT DISTINCT child.parent_issue_id AS id
  FROM membership child
  WHERE child.parent_issue_id IS NOT NULL
    AND child.status = ANY(%s::text[])
), `, compiled.where, visibleRef)
		pageSource = "membership"
		pagePredicate = fmt.Sprintf(`(%s) AND NOT (
  i.parent_issue_id IS NULL AND
  EXISTS (SELECT 1 FROM promoted_parents p WHERE p.id = i.id)
)`, groupPredicate)
	}
	cte := fmt.Sprintf(`%spage AS MATERIALIZED (
  SELECT i.*, (%s)::text AS table_sort_key
  FROM %s i
  WHERE (%s) AND %s
  ORDER BY %s
  LIMIT %s
)`, ctePrefix, resolvedSort.expression, pageSource, pagePredicate, cursorPredicate, resolvedSort.orderBy(), limitRef)
	childCountExpr := "0::bigint"
	if request.Hierarchy.Enabled {
		childCountExpr = "(SELECT COUNT(*)::bigint FROM membership child WHERE child.parent_issue_id = i.id)"
	}

	query := fmt.Sprintf(`%s
SELECT i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
       i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
       i.parent_issue_id, i.position, i.start_date, i.due_date, i.created_at,
	       i.updated_at, i.last_activity_at, i.number, i.project_id, i.metadata, i.stage, i.properties,
	       i.revision,
	       %s AS direct_child_count, i.table_sort_key
	FROM page i
	ORDER BY %s`, cte, childCountExpr, resolvedSort.orderBy())

	rows, err := h.DB.Query(r.Context(), query, args...)
	if err != nil {
		slog.Warn("ListIssueTableRows query failed", append(logger.RequestAttrs(r), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to list table rows")
		return
	}
	defer rows.Close()

	type scannedRow struct {
		issue      db.ListIssuesRow
		childCount int64
		sortKey    pgtype.Text
	}
	scanned := make([]scannedRow, 0, limit+1)
	for rows.Next() {
		var row scannedRow
		if err := rows.Scan(
			&row.issue.ID,
			&row.issue.WorkspaceID,
			&row.issue.Title,
			&row.issue.Description,
			&row.issue.Status,
			&row.issue.Priority,
			&row.issue.AssigneeType,
			&row.issue.AssigneeID,
			&row.issue.CreatorType,
			&row.issue.CreatorID,
			&row.issue.ParentIssueID,
			&row.issue.Position,
			&row.issue.StartDate,
			&row.issue.DueDate,
			&row.issue.CreatedAt,
			&row.issue.UpdatedAt,
			&row.issue.LastActivityAt,
			&row.issue.Number,
			&row.issue.ProjectID,
			&row.issue.Metadata,
			&row.issue.Stage,
			&row.issue.Properties,
			&row.issue.Revision,
			&row.childCount,
			&row.sortKey,
		); err != nil {
			writeIssueTableQueryFailure(w, r, "failed to list table rows")
			return
		}
		scanned = append(scanned, row)
	}
	if err := rows.Err(); err != nil {
		writeIssueTableQueryFailure(w, r, "failed to list table rows")
		return
	}
	rows.Close()

	// 只有「未分组 + 非层级 + 首页」才需要查询级总数：组头在 /groups 端点拿各自的精确 total；
	// 子分支与续页若再对全量 membership 求 COUNT，是这次翻页不必付出的代价。
	var total int64
	if cursor == nil && request.Group.Kind == "none" && request.ParentID == nil {
		if err := h.DB.QueryRow(r.Context(), fmt.Sprintf("SELECT COUNT(*)::bigint FROM issue i WHERE %s", compiled.where), compiled.args...).Scan(&total); err != nil {
			slog.Warn("ListIssueTableRows total count failed", append(logger.RequestAttrs(r), "error", err)...)
			writeIssueTableQueryFailure(w, r, "failed to count table rows")
			return
		}
	}

	var nextCursor *string
	if len(scanned) > limit {
		scanned = scanned[:limit]
		last := scanned[len(scanned)-1]
		var sortValue *string
		if last.sortKey.Valid {
			value := last.sortKey.String
			sortValue = &value
		}
		nextCursor = encodeIssueTableCursor(issueTableCursor{
			Version:          1,
			QueryFingerprint: compiled.fingerprint,
			GroupKey:         groupKey,
			ParentID:         request.ParentID,
			BranchIdentity:   branchIdentity,
			SortValue:        sortValue,
			SortIsNull:       !last.sortKey.Valid,
			RowCreatedAt:     last.issue.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
			RowID:            util.UUIDToString(last.issue.ID),
		})
	}

	// 行窗口、可选的根总数与游标是本次快照的权威结论，先提交再去做「尽力而为」的展示增强：
	// PostgreSQL 在任一条语句出错后会中止整笔事务，若把 getIssuePrefix 或 labelsByIssue 留在
	// 快照内执行，它们「有意容忍」的失败会变成整页 Commit 的致命错误。
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("ListIssueTableRows snapshot commit failed", append(logger.RequestAttrs(r), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to finish table query")
		return
	}
	committed = true

	prefix := baseHandler.getIssuePrefix(r.Context(), compiled.workspaceID)
	issueIDs := make([]pgtype.UUID, len(scanned))
	for index, row := range scanned {
		issueIDs[index] = row.issue.ID
	}
	labelsByIssue := baseHandler.labelsByIssue(r.Context(), compiled.workspaceID, issueIDs)
	// 整页共用一个 Resolver，见 newStatusCategoryFiller。（MUL-6243）
	fillTableRow := baseHandler.newStatusCategoryFiller(r.Context(), compiled.workspaceID)
	responseRows := make([]issueTableRowResponse, len(scanned))
	for index, row := range scanned {
		issue := issueListRowToResponse(row.issue, prefix)
		fillTableRow(&issue)
		labels := labelsByIssue[issue.ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		issue.Labels = &labels
		responseRows[index] = issueTableRowResponse{
			Issue:            issue,
			DirectChildCount: row.childCount,
		}
	}

	response := issueTableRowsResponse{
		QueryFingerprint: compiled.fingerprint,
		GroupKey:         groupKey,
		ParentID:         request.ParentID,
		Total:            total,
		Rows:             responseRows,
		BranchTotal:      int64(len(responseRows)),
		NextCursor:       nextCursor,
	}
	writeJSON(w, http.StatusOK, response)
}
