// 职责定位：issue 表格的「分面统计」（facet counts）：在基础行查询之上对若干 facet 各算一次
// 计数与可访问值，供前端筛选项展示计数。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// issueTableMaxFacets 限制单次 facet 请求最多 32 个。工作区属性目录上限 20 个活跃定义，六个
// 内置维度加属性目录在这个上限下仍有富余；更重要的是防止单次请求在同一个快照事务里排上百次
// 顺序聚合扫描。
const issueTableMaxFacets = 32

// issueTableFacetValueResponse 是一个分面取值桶：Key 是去重后的取值（含 __none__ 这类哨兵），
// Count 是当前查询下命中该值的 issue 数。
type issueTableFacetValueResponse struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// issueTableFacetResponse 是一个分面的结果：Kind/PropertyID 标明分的是哪个维度，Values 是该
// 维度下各可选值及其计数。
type issueTableFacetResponse struct {
	Kind       string                         `json:"kind"`
	PropertyID string                         `json:"property_id,omitempty"`
	Values     []issueTableFacetValueResponse `json:"values"`
}

// issueTableFacetsResponse 是 /table/facets 的响应体：与行查询同源的指纹、总行数与各分面结果。
type issueTableFacetsResponse struct {
	QueryFingerprint string                    `json:"query_fingerprint"`
	Total            int64                     `json:"total"`
	Facets           []issueTableFacetResponse `json:"facets"`
}

// issueTableQueryWithoutFacet 返回一份「去掉了某分面自己的维度」的查询副本——统计该分面可选值
// 时，正是要看用户把它切到任意选项后还会看到哪些行。Filters 里其余维度与嵌套 map 都深拷贝，
// 避免原地改动污染调用方的查询。
func issueTableQueryWithoutFacet(input issueTableQuerySpec, facet issueTableFacetSpec) issueTableQuerySpec {
	output := input
	output.Filters = input.Filters
	if input.Filters.Properties != nil {
		output.Filters.Properties = make(map[string][]json.RawMessage, len(input.Filters.Properties))
		for propertyID, values := range input.Filters.Properties {
			output.Filters.Properties[propertyID] = append([]json.RawMessage(nil), values...)
		}
	}

	switch facet.Kind {
	case "status":
		output.Filters.Statuses = nil
	case "priority":
		output.Filters.Priorities = nil
	case "assignee":
		output.Filters.Assignees = nil
		output.Filters.IncludeNoAssignee = false
	case "creator":
		output.Filters.Creators = nil
	case "project":
		output.Filters.ProjectIDs = nil
		output.Filters.IncludeNoProject = false
	case "label":
		output.Filters.LabelIDs = nil
	case "property":
		delete(output.Filters.Properties, facet.PropertyID)
	case "working_agents":
		// 和其它分面一样是析取式：摘掉这个维度自身的过滤条件，答案就是「你若把 agents-working
		// 开关打开会看到哪些 agent」——这正等于开关已打开时的答案，因此头部 chip 的计数在
		// 开关切换前后保持稳定。
		output.Filters.WorkingIssueIDs = nil
		output.Filters.WorkingOnly = false
	}
	return output
}

// issueTableFacetIdentity 生成分面的稳定身份串：property 分面按 property_id 区分（同一 property
// 不能重复请求两次），其余分面直接以 kind 为身份。
func issueTableFacetIdentity(facet issueTableFacetSpec) string {
	if facet.Kind == "property" {
		return "property:" + facet.PropertyID
	}
	return facet.Kind
}

// issueTableBaseFacetExpression 返回某个内置分面在基础行 WHERE 上可以直接 GROUP BY 的取值
// 表达式，并给出「该维度当前是否已被过滤」：已带过滤的维度无法作为基座分面批量统计（见
// ListIssueTableFacets 对 batch 与 individual 的切分）。不支持的 kind 返回 ok=false。
func issueTableBaseFacetExpression(query issueTableQuerySpec, facet issueTableFacetSpec) (string, bool) {
	switch facet.Kind {
	case "status":
		return "i.status", len(query.Filters.Statuses) == 0
	case "priority":
		return "i.priority", len(query.Filters.Priorities) == 0
	case "assignee":
		return "CASE WHEN i.assignee_type IS NULL OR i.assignee_id IS NULL THEN '__none__' ELSE i.assignee_type || ':' || i.assignee_id::text END", len(query.Filters.Assignees) == 0 && !query.Filters.IncludeNoAssignee
	case "creator":
		return "i.creator_type || ':' || i.creator_id::text", len(query.Filters.Creators) == 0
	case "project":
		return "COALESCE(i.project_id::text, '__none__')", len(query.Filters.ProjectIDs) == 0 && !query.Filters.IncludeNoProject
	default:
		return "", false
	}
}

// issueTableBaseFacetQuery 在「基础行 WHERE」上一次算完多个可批量基座分面 + 可选总行数：每个
// 分面带一个取值表达式进 GROUPING SETS，由 CASE 给结果行打分面 marker，includeTotal 时再补一个
// 空分组 () 求总行数。它只处理「其维度当前未被查询过滤」的分面——已被过滤的维度在过滤结果里
// 给不出有意义的可选值，必须走 issueTableFacetQuery 的 individual 路径（去掉自身维度重查）。
func (h *Handler) issueTableBaseFacetQuery(
	w http.ResponseWriter,
	r *http.Request,
	base issueTableSQL,
	requestQuery issueTableQuerySpec,
	facets []issueTableFacetSpec,
	includeTotal bool,
) (map[string]issueTableFacetResponse, int64, bool) {
	markerCases := make([]string, 0, len(facets))
	valueCases := make([]string, 0, len(facets))
	groupingSets := make([]string, 0, len(facets)+1)
	responses := make(map[string]issueTableFacetResponse, len(facets))
	for _, facet := range facets {
		expression, ok := issueTableBaseFacetExpression(requestQuery, facet)
		if !ok {
			writeIssueTableQueryFailure(w, r, "failed to batch table facets")
			return nil, 0, false
		}
		identity := issueTableFacetIdentity(facet)
		markerCases = append(markerCases, fmt.Sprintf("WHEN GROUPING(%s) = 0 THEN '%s'", expression, identity))
		valueCases = append(valueCases, fmt.Sprintf("WHEN GROUPING(%s) = 0 THEN (%s)::text", expression, expression))
		groupingSets = append(groupingSets, "("+expression+")")
		responses[identity] = issueTableFacetResponse{
			Kind:       facet.Kind,
			PropertyID: facet.PropertyID,
			Values:     []issueTableFacetValueResponse{},
		}
	}
	if includeTotal {
		groupingSets = append(groupingSets, "()")
	}

	query := fmt.Sprintf(`SELECT CASE %s ELSE '__total__' END,
       CASE %s ELSE '' END,
       COUNT(*)::bigint
FROM issue i
WHERE %s
GROUP BY GROUPING SETS (%s)`, strings.Join(markerCases, " "), strings.Join(valueCases, " "), base.where, strings.Join(groupingSets, ", "))
	rows, err := h.DB.Query(r.Context(), query, base.args...)
	if err != nil {
		slog.Warn("ListIssueTableFacets batch query failed", append(logger.RequestAttrs(r), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to list table facets")
		return nil, 0, false
	}
	defer rows.Close()

	var total int64
	for rows.Next() {
		var identity string
		var value string
		var count int64
		if err := rows.Scan(&identity, &value, &count); err != nil {
			writeIssueTableQueryFailure(w, r, "failed to list table facets")
			return nil, 0, false
		}
		if identity == "__total__" {
			total = count
			continue
		}
		response, ok := responses[identity]
		if !ok {
			writeIssueTableQueryFailure(w, r, "failed to list table facets")
			return nil, 0, false
		}
		response.Values = append(response.Values, issueTableFacetValueResponse{Key: value, Count: count})
		responses[identity] = response
	}
	if err := rows.Err(); err != nil {
		writeIssueTableQueryFailure(w, r, "failed to list table facets")
		return nil, 0, false
	}
	for identity, response := range responses {
		sort.Slice(response.Values, func(i, j int) bool {
			return strings.Compare(response.Values[i].Key, response.Values[j].Key) < 0
		})
		responses[identity] = response
	}
	return responses, total, true
}

// issueTableFacetQuery 为单个分面单独执行计数（individual 路径）：先把查询里该分面自身的维度
// 过滤去掉（issueTableQueryWithoutFacet），再按其 kind 用对应 SQL 分组统计。label / working_agents
// / property 走这类逐条计算；property 分面还须在库里核对定义是否存在、是否归档、类型是否支持。
func (h *Handler) issueTableFacetQuery(w http.ResponseWriter, r *http.Request, requestQuery issueTableQuerySpec, facet issueTableFacetSpec) (issueTableFacetResponse, bool) {
	response := issueTableFacetResponse{Kind: facet.Kind, PropertyID: facet.PropertyID, Values: []issueTableFacetValueResponse{}}
	compiled, ok := h.compileIssueTableQuery(w, r, issueTableQueryWithoutFacet(requestQuery, facet))
	if !ok {
		return response, false
	}

	query := ""
	switch facet.Kind {
	case "status":
		query = fmt.Sprintf(`SELECT i.status, COUNT(*)::bigint FROM issue i WHERE %s GROUP BY i.status`, compiled.where)
	case "priority":
		query = fmt.Sprintf(`SELECT i.priority, COUNT(*)::bigint FROM issue i WHERE %s GROUP BY i.priority`, compiled.where)
	case "assignee":
		query = fmt.Sprintf(`SELECT CASE WHEN i.assignee_type IS NULL OR i.assignee_id IS NULL THEN '__none__' ELSE i.assignee_type || ':' || i.assignee_id::text END, COUNT(*)::bigint FROM issue i WHERE %s GROUP BY 1`, compiled.where)
	case "creator":
		query = fmt.Sprintf(`SELECT i.creator_type || ':' || i.creator_id::text, COUNT(*)::bigint FROM issue i WHERE %s GROUP BY 1`, compiled.where)
	case "project":
		query = fmt.Sprintf(`SELECT COALESCE(i.project_id::text, '__none__'), COUNT(*)::bigint FROM issue i WHERE %s GROUP BY 1`, compiled.where)
	case "label":
		query = fmt.Sprintf(`SELECT itl.label_id::text, COUNT(DISTINCT i.id)::bigint FROM issue i JOIN issue_to_label itl ON itl.issue_id = i.id WHERE %s GROUP BY itl.label_id`, compiled.where)
	case "working_agents":
		// 每个「正在本表格面里跑 issue 工作的 agent」一行，附运行中任务数。判定谓词与
		// ListWorkspaceWorkingAgents type=issue 一致（chat > autopilot > issue 的优先级、仅用户
		// 自建且未归档的 agent），但 issue 集合来自本面自己编译出的 scope + filters，而不是另起
		// 一份独立的全工作区定义——这样才能让头部 chip 的计数与点开后留下的行数对得上（MUL-5525）。
		query = fmt.Sprintf(`SELECT a.id::text, COUNT(*)::bigint
FROM issue i
JOIN agent_task_queue atq ON atq.issue_id = i.id
JOIN agent a ON a.id = atq.agent_id AND a.workspace_id = i.workspace_id
WHERE %s
  AND a.kind = 'user'
  AND a.archived_at IS NULL
  AND atq.status = 'running'
  AND atq.chat_session_id IS NULL
  AND atq.autopilot_run_id IS NULL
GROUP BY a.id`, compiled.where)
	case "property":
		propertyID, err := util.ParseUUID(facet.PropertyID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid facets.property_id")
			return response, false
		}
		property, err := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{
			ID:          propertyID,
			WorkspaceID: compiled.workspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeIssueTableUnsupportedGroup(w, "property_not_found", "The faceted property no longer exists.")
				return response, false
			}
			slog.Warn("resolve table facet property failed", append(logger.RequestAttrs(r), "error", err)...)
			writeIssueTableQueryFailure(w, r, "failed to resolve table facet")
			return response, false
		}
		if property.ArchivedAt.Valid {
			writeIssueTableUnsupportedGroup(w, "property_archived", "The faceted property is archived.")
			return response, false
		}
		propertyKey := "'" + util.UUIDToString(property.ID) + "'"
		switch property.Type {
		case "select", "actor":
			// 未设置该 key 的 issue 统一记进 __none__ 桶，让筛选项菜单里的「无值」带一个真实的
			// 计数。
			query = fmt.Sprintf(`SELECT COALESCE(i.properties ->> %s, '__none__'), COUNT(*)::bigint FROM issue i WHERE %s AND (jsonb_typeof(i.properties -> %s) = 'string' OR NOT (i.properties ? %s)) GROUP BY 1`, propertyKey, compiled.where, propertyKey, propertyKey)
		case "text", "url", "date", "number":
			// 标量值是自由文本，按 distinct 值分组会得到无上界的「每个取值一行」。筛选项菜单对
			// 这种类型只读 __none__ 桶，所以只算已设/未设两个有界桶，而不是拉全量值空间。
			// key 是否存在对「无值」是精确的：存下来的值不可能是 null 或空串，key 在就等于有值。
			query = fmt.Sprintf(`SELECT CASE WHEN i.properties ? %s THEN '__set__' ELSE '__none__' END, COUNT(*)::bigint FROM issue i WHERE %s GROUP BY 1`, propertyKey, compiled.where)
		case "multi_select", "multi_actor":
			query = fmt.Sprintf(`SELECT COALESCE(property_value.value, '__none__'), COUNT(DISTINCT i.id)::bigint FROM issue i JOIN LATERAL (SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(i.properties -> %s) = 'array' THEN i.properties -> %s ELSE '[]'::jsonb END) AS value UNION ALL SELECT NULL WHERE NOT (i.properties ? %s)) property_value(value) ON TRUE WHERE %s GROUP BY 1`, propertyKey, propertyKey, propertyKey, compiled.where)
		case "checkbox":
			query = fmt.Sprintf(`SELECT COALESCE(i.properties ->> %s, '__none__'), COUNT(*)::bigint FROM issue i WHERE %s AND (jsonb_typeof(i.properties -> %s) = 'boolean' OR NOT (i.properties ? %s)) GROUP BY 1`, propertyKey, compiled.where, propertyKey, propertyKey)
		default:
			writeIssueTableUnsupportedGroup(w, "property_type_unsupported", "This property type cannot be used as a filter facet.")
			return response, false
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid facets.kind")
		return response, false
	}

	rows, err := h.DB.Query(r.Context(), query, compiled.args...)
	if err != nil {
		slog.Warn("ListIssueTableFacets query failed", append(logger.RequestAttrs(r), "facet", issueTableFacetIdentity(facet), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to list table facets")
		return response, false
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		var count int64
		if err := rows.Scan(&value, &count); err != nil {
			writeIssueTableQueryFailure(w, r, "failed to list table facets")
			return response, false
		}
		response.Values = append(response.Values, issueTableFacetValueResponse{Key: value, Count: count})
	}
	if err := rows.Err(); err != nil {
		writeIssueTableQueryFailure(w, r, "failed to list table facets")
		return response, false
	}
	if facet.Kind == "working_agents" {
		values, ok := h.filterAccessibleAgentFacetValues(w, r, compiled.workspaceID, response.Values)
		if !ok {
			return response, false
		}
		response.Values = values
	}
	sort.Slice(response.Values, func(i, j int) bool {
		return strings.Compare(response.Values[i].Key, response.Values[j].Key) < 0
	})
	return response, true
}

// filterAccessibleAgentFacetValues 对 working_agents 分面的取值做可见性过滤：该分面的取值键是
// agent id，会暴露 agent 身份，所以必须与其它全工作区 agent 聚合走同一道可见性门槛——private
// 或不在白名单里的 agent，绝不允许通过 id、计数甚至「它是否出现」泄漏出去。
func (h *Handler) filterAccessibleAgentFacetValues(
	w http.ResponseWriter,
	r *http.Request,
	workspaceUUID pgtype.UUID,
	values []issueTableFacetValueResponse,
) ([]issueTableFacetValueResponse, bool) {
	if len(values) == 0 {
		return values, true
	}
	workspaceID := util.UUIDToString(workspaceUUID)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return nil, false
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeIssueTableQueryFailure(w, r, "failed to resolve agent access")
		return nil, false
	}
	filtered := make([]issueTableFacetValueResponse, 0, len(values))
	for _, value := range values {
		if _, permitted := allowed[value.Key]; permitted {
			filtered = append(filtered, value)
		}
	}
	return filtered, true
}

// ListIssueTableFacets 处理 POST /api/issues/table/facets：在只读快照事务里对每个请求的
// facet 统计可筛取值计数（最多 issueTableMaxFacets 个、去重）。
func (h *Handler) ListIssueTableFacets(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		writeError(w, http.StatusInternalServerError, "database is unavailable")
		return
	}
	var request issueTableFacetsRequest
	if !decodeIssueTableJSON(w, r, &request) {
		return
	}
	if len(request.Facets) == 0 || len(request.Facets) > issueTableMaxFacets {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("facets must contain between 1 and %d entries", issueTableMaxFacets))
		return
	}
	r, cancel := withIssueTableQueryTimeout(r)
	defer cancel()
	snapshot, tx, err := h.beginIssueTableSnapshot(r.Context())
	if err != nil {
		slog.Warn("ListIssueTableFacets snapshot failed", append(logger.RequestAttrs(r), "error", err)...)
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

	base, ok := h.compileIssueTableQuery(w, r, request.Query)
	if !ok {
		return
	}

	seen := make(map[string]struct{}, len(request.Facets))
	normalizedFacets := make([]issueTableFacetSpec, len(request.Facets))
	for index, facet := range request.Facets {
		facet.Kind = strings.TrimSpace(facet.Kind)
		facet.PropertyID = strings.TrimSpace(facet.PropertyID)
		identity := issueTableFacetIdentity(facet)
		if _, exists := seen[identity]; exists {
			writeError(w, http.StatusBadRequest, "duplicate table facet")
			return
		}
		seen[identity] = struct{}{}
		normalizedFacets[index] = facet
	}

	includeTotal := request.IncludeTotal == nil || *request.IncludeTotal
	batchFacets := make([]issueTableFacetSpec, 0, len(normalizedFacets))
	individualIndexes := make([]int, 0, len(normalizedFacets))
	for index, facet := range normalizedFacets {
		if _, batchable := issueTableBaseFacetExpression(request.Query, facet); batchable {
			batchFacets = append(batchFacets, facet)
		} else {
			individualIndexes = append(individualIndexes, index)
		}
	}

	responses := make([]issueTableFacetResponse, len(normalizedFacets))
	var total int64
	totalResolved := false
	if len(batchFacets) > 0 {
		batched, batchTotal, ok := h.issueTableBaseFacetQuery(w, r, base, request.Query, batchFacets, includeTotal)
		if !ok {
			return
		}
		for index, facet := range normalizedFacets {
			if response, exists := batched[issueTableFacetIdentity(facet)]; exists {
				responses[index] = response
			}
		}
		if includeTotal {
			total = batchTotal
			totalResolved = true
		}
	}
	if includeTotal && !totalResolved {
		if err := h.DB.QueryRow(r.Context(), fmt.Sprintf("SELECT COUNT(*)::bigint FROM issue i WHERE %s", base.where), base.args...).Scan(&total); err != nil {
			slog.Warn("ListIssueTableFacets total failed", append(logger.RequestAttrs(r), "error", err)...)
			writeIssueTableQueryFailure(w, r, "failed to count table facets")
			return
		}
	}
	for _, index := range individualIndexes {
		resolved, ok := h.issueTableFacetQuery(w, r, request.Query, normalizedFacets[index])
		if !ok {
			return
		}
		responses[index] = resolved
	}

	response := issueTableFacetsResponse{
		QueryFingerprint: base.fingerprint,
		Total:            total,
		Facets:           responses,
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("ListIssueTableFacets snapshot commit failed", append(logger.RequestAttrs(r), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to finish table query")
		return
	}
	committed = true
	writeJSON(w, http.StatusOK, response)
}
