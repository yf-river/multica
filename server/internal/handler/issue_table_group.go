// 职责定位：issue 表格的「分组头」读取（POST .../table/groups）：按分组规格（受理人/状态
// 类别等）聚合出各组，支持游标分页与每组的次级计数/上下文。
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// issueTableGroupValueResponse 是一个分组头的类型化 value：Kind 标明分的是哪个维度，其余字段
// 按 kind 二选一填充——status/status_category 填 Status，assignee 填 Actor，project 填
// ProjectID，parent 填 ParentID + Parent + ValueState，property 填 PropertyID + Value/
// ValueState。前端渲染组头时按 Kind 读对应字段。
type issueTableGroupValueResponse struct {
	Kind       string               `json:"kind"`
	Status     string               `json:"status,omitempty"`
	Actor      *issueTableActorRef  `json:"actor"`
	ProjectID  *string              `json:"project_id,omitempty"`
	ParentID   *string              `json:"parent_id,omitempty"`
	Parent     *issueTableParentRef `json:"parent,omitempty"`
	PropertyID string               `json:"property_id,omitempty"`
	Value      any                  `json:"value,omitempty"`
	ValueState string               `json:"value_state,omitempty"`
}

// issueTableParentRef 是 parent 分组下每个组头附带的父 issue 摘要：由 contextExpression 从父
// issue 行内联出来，省去前端逐组回查。
type issueTableParentRef struct {
	ID         string `json:"id"`
	Number     int32  `json:"number"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
}

// issueTableGroupContext 是分组行里 context JSON 的反序列化目标：目前只有 parent 分组会带
// Parent（见 contextExpression），其余分组恒为空对象。
type issueTableGroupContext struct {
	Parent *issueTableParentRef `json:"parent,omitempty"`
}

// issueTableGroupDescriptorResponse 是一个分组头的完整描述：Key 是稳定分组键——前端选中该组后
// 会把它原样回传给 rows 的 group_key；Value 是类型化取值；Count 是组内行数；compound 分组再
// 附 SecondaryGroups（次级状态列，键由 compoundCellGroupKey 生成）。
type issueTableGroupDescriptorResponse struct {
	Key             string                              `json:"key"`
	Value           issueTableGroupValueResponse        `json:"value"`
	Count           int64                               `json:"count"`
	SecondaryGroups []issueTableGroupDescriptorResponse `json:"secondary_groups,omitempty"`
}

// issueTableGroupsResponse 是 POST /table/groups 的响应体：与行查询同源的指纹、行总数、组头
// 列表与下一页游标。
type issueTableGroupsResponse struct {
	QueryFingerprint string                              `json:"query_fingerprint"`
	Total            int64                               `json:"total"`
	Groups           []issueTableGroupDescriptorResponse `json:"groups"`
	NextCursor       *string                             `json:"next_cursor"`
}

// resolvedIssueTableGroup 是解析并验证过后的分组规格：kind 决定分组语义，groupExpr/groupSortExpr
// 是用于 SQL 的分组与排序表达式，property 分组额外携带属性选项表，compound 分组持有 primary 子
// 规格与次级轴信息。status_category 分组则带类别映射（见 statusCategoryExpr）。
type resolvedIssueTableGroup struct {
	crossWorkspace    bool
	kind              string
	propertyID        string
	propertyType      string
	groupExpr         string
	groupSortExpr     string
	activeOptionOrder []string
	activeOptions     map[string]struct{}
	primary           *resolvedIssueTableGroup
	secondaryValues   []string
	secondaryFiltered bool
	// secondaryCategory 标记复合分组的次级轴是状态「类别」而不是状态 key 本身——泳道契约。
	secondaryCategory bool
	// categoryKeys 把 7 个类别各自映射到属于它的具体状态 key，每个请求只解析一次。类别谓词经
	// 它展开成 `i.status = ANY(...)`，让 (workspace_id, status) 索引保持可用——见
	// issuestatus.ExpandCategories。（MUL-6243）
	categoryKeys map[string][]string
	// statusCustomKeys 是 statusCategoryExpr 背后「自定义 key -> 类别」的映射。没有自定义状态的
	// 工作区为空。
	statusCustomKeys map[string]string
}

// statusCategoryExpr 构造用作 GROUP BY 表达式的标量「状态 key -> 类别」改写。内置 key 落在 ELSE
// 分支原样通过——内置 key 本身就是它的类别——所以没有自定义状态的工作区得到的就是 `i.status`。
// （MUL-6243）
func statusCategoryExpr(customKeys map[string]string, addArg func(any) string) string {
	if len(customKeys) == 0 {
		return "i.status"
	}
	keys := make([]string, 0, len(customKeys))
	for key := range customKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("CASE i.status")
	for _, key := range keys {
		fmt.Fprintf(&b, " WHEN %s::text THEN %s::text", addArg(key), addArg(customKeys[key]))
	}
	b.WriteString(" ELSE i.status END")
	return b.String()
}

// resolveStatusCategoryMaps 用「一次」目录读取同时产出类别分组需要的两张形状：GROUP BY 改写背后
// 的「自定义 key -> 类别」映射，以及 predicate()（自身拿不到 context/Querier）查表的「类别 ->
// 具体 key」展开。
//
// 为什么只读一次：看板把七个泳道列拆成七个独立 HTTP 请求加载，若每调用一次
// `ExpandCategories` + `CustomKeyCategories`，单次看板加载就要多出 21 次目录 SELECT。
func (h *Handler) resolveStatusCategoryMaps(
	ctx context.Context,
	workspaceID pgtype.UUID,
) (customKeys map[string]string, categoryKeys map[string][]string, err error) {
	customKeys, err = issuestatus.CustomKeyCategories(ctx, h.issueStatusCatalog(), workspaceID)
	if err != nil {
		return nil, nil, err
	}
	categoryKeys = make(map[string][]string, len(validIssueStatuses))
	for _, category := range validIssueStatuses {
		// 每个类别至少包含它自己的 canonical key，即使工作区从未播种过目录——内置 key 本身就是
		// 它的类别。
		categoryKeys[category] = []string{category}
	}
	// 排序后展开，保证同一目录下「展开结果、进而 SQL 参数列表」都是确定的。
	keys := make([]string, 0, len(customKeys))
	for key := range customKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		category := customKeys[key]
		categoryKeys[category] = append(categoryKeys[category], key)
	}
	return customKeys, categoryKeys, nil
}

// issueTableGroupIdentity 生成分组规格的稳定身份串（含 property 空值是否纳入、compound 的
// primary/secondary 与可见次级值），作为游标分支身份的一部分——不同分组规格间的游标不通用。
func issueTableGroupIdentity(group issueTableGroupSpec) string {
	if group.Kind == "property" {
		return "group:property:" + group.PropertyID + ":empty=" + strconv.FormatBool(group.IncludeEmpty)
	}
	if group.Kind == "compound" {
		identity := "group:compound:" + group.Primary + ":" + group.Secondary
		if group.SecondaryValues != nil {
			identity += ":visible=" + strings.Join(group.SecondaryValues, ",")
		}
		return identity
	}
	return "group:" + group.Kind
}

// resolveIssueTableGroup 校验并解析一次分组请求，产出可用的 resolvedIssueTableGroup。它会按
// kind 补 SQL 表达式、为 status_category / compound 读类别映射、为 property 读属性定义与选项
// 表。allowNone 允许解析「不分组」（组头端点禁止）。非法请求写 4xx/422 并返回 ok=false。
func (h *Handler) resolveIssueTableGroup(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, group issueTableGroupSpec, allowNone bool, crossWorkspace ...bool) (resolvedIssueTableGroup, bool) {
	if group.Kind != "compound" && group.SecondaryValues != nil {
		writeError(w, http.StatusBadRequest, "group.secondary_values requires group.kind=compound")
		return resolvedIssueTableGroup{}, false
	}
	across := len(crossWorkspace) > 0 && crossWorkspace[0]
	switch group.Kind {
	case "none":
		if !allowNone {
			writeError(w, http.StatusBadRequest, "group.kind=none is not valid for group headers")
			return resolvedIssueTableGroup{}, false
		}
		return resolvedIssueTableGroup{kind: "none"}, true
	case "status":
		return resolvedIssueTableGroup{kind: "status", groupExpr: "i.status"}, true
	case "status_category":
		if across {
			return resolvedIssueTableGroup{kind: "status_category", crossWorkspace: true}, true
		}
		// 看板 / 列表 / 泳道列是按「类别」分的：自定义状态归到「它表现为哪个类别」的那一列，
		// 而不是给它单开一列——这样列数上限才钉在 7。（MUL-6243）
		customKeys, categoryKeys, err := h.resolveStatusCategoryMaps(r.Context(), workspaceID)
		if err != nil {
			slog.Warn("resolve status category group failed", append(logger.RequestAttrs(r), "error", err)...)
			writeIssueTableQueryFailure(w, r, "failed to resolve table group")
			return resolvedIssueTableGroup{}, false
		}
		return resolvedIssueTableGroup{
			kind:             "status_category",
			categoryKeys:     categoryKeys,
			statusCustomKeys: customKeys,
		}, true
	case "assignee":
		return resolvedIssueTableGroup{
			kind:      "assignee",
			groupExpr: "CASE WHEN i.assignee_type IS NULL OR i.assignee_id IS NULL THEN '__unassigned__' ELSE i.assignee_type || ':' || i.assignee_id::text END",
			// groupSortExpr 在 issue 已归并为「每受理人一行」之后再跑：显示名若在 GROUP BY 之前
			// 逐行解析，大受理人分组会退化成 N+1 查询。
			groupSortExpr: `LOWER(COALESCE(CASE split_part(group_value, ':', 1)
  WHEN 'member' THEN (SELECT u.name FROM "user" u WHERE u.id = split_part(group_value, ':', 2)::uuid)
  WHEN 'agent' THEN (SELECT a.name FROM agent a WHERE a.workspace_id = $1 AND a.id = split_part(group_value, ':', 2)::uuid)
  WHEN 'squad' THEN (SELECT s.name FROM squad s WHERE s.workspace_id = $1 AND s.id = split_part(group_value, ':', 2)::uuid)
END, ''))`,
		}, true
	case "project":
		return resolvedIssueTableGroup{
			kind:      "project",
			groupExpr: "COALESCE(i.project_id::text, '__no_project__')",
			groupSortExpr: `CASE WHEN group_value = '__no_project__' THEN '' ELSE LOWER(COALESCE(
  (SELECT p.title FROM project p WHERE p.workspace_id = $1 AND p.id = group_value::uuid),
  ''
)) END`,
		}, true
	case "parent":
		return resolvedIssueTableGroup{
			kind:           "parent",
			crossWorkspace: across,
			groupExpr:      "COALESCE(i.parent_issue_id::text, '__no_parent__')",
			groupSortExpr: `CASE WHEN group_value = '__no_parent__' THEN '' ELSE LOWER(COALESCE(
  (SELECT p.title FROM issue p WHERE p.workspace_id = $1 AND p.id = group_value::uuid),
  ''
)) END`,
		}, true
	case "compound":
		if group.Secondary != "status" && group.Secondary != "status_category" {
			writeIssueTableUnsupportedGroup(w, "secondary_group_unsupported", "Only status is supported as a secondary group.")
			return resolvedIssueTableGroup{}, false
		}
		secondaryCategory := group.Secondary == "status_category"
		if group.Primary != "assignee" && group.Primary != "project" && group.Primary != "parent" {
			writeIssueTableUnsupportedGroup(w, "primary_group_unsupported", "This primary group is not supported.")
			return resolvedIssueTableGroup{}, false
		}
		seenSecondaryValues := make(map[string]struct{}, len(group.SecondaryValues))
		for _, value := range group.SecondaryValues {
			if !issueTableContainsString(validIssueStatuses, value) {
				writeError(w, http.StatusBadRequest, "invalid group.secondary_values")
				return resolvedIssueTableGroup{}, false
			}
			if _, exists := seenSecondaryValues[value]; exists {
				writeError(w, http.StatusBadRequest, "duplicate group.secondary_values")
				return resolvedIssueTableGroup{}, false
			}
			seenSecondaryValues[value] = struct{}{}
		}
		primary, ok := h.resolveIssueTableGroup(w, r, workspaceID, issueTableGroupSpec{Kind: group.Primary}, false, across)
		if !ok {
			return resolvedIssueTableGroup{}, false
		}
		resolved := resolvedIssueTableGroup{
			kind:              "compound",
			primary:           &primary,
			secondaryValues:   append([]string(nil), group.SecondaryValues...),
			secondaryFiltered: group.SecondaryValues != nil,
			secondaryCategory: secondaryCategory,
			crossWorkspace:    across,
		}
		if secondaryCategory && !across {
			customKeys, categoryKeys, err := h.resolveStatusCategoryMaps(r.Context(), workspaceID)
			if err != nil {
				slog.Warn("resolve compound status category group failed", append(logger.RequestAttrs(r), "error", err)...)
				writeIssueTableQueryFailure(w, r, "failed to resolve table group")
				return resolvedIssueTableGroup{}, false
			}
			resolved.statusCustomKeys = customKeys
			resolved.categoryKeys = categoryKeys
		}
		return resolved, true
	case "property":
		propertyUUID, err := util.ParseUUID(group.PropertyID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid group.property_id")
			return resolvedIssueTableGroup{}, false
		}
		property, err := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{
			ID:          propertyUUID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeIssueTableUnsupportedGroup(w, "property_not_found", "The grouped property no longer exists.")
				return resolvedIssueTableGroup{}, false
			}
			slog.Warn("resolve table group property failed", append(logger.RequestAttrs(r), "error", err)...)
			writeIssueTableQueryFailure(w, r, "failed to resolve table group")
			return resolvedIssueTableGroup{}, false
		}
		if property.ArchivedAt.Valid {
			writeIssueTableUnsupportedGroup(w, "property_archived", "The grouped property is archived.")
			return resolvedIssueTableGroup{}, false
		}
		propertyID := util.UUIDToString(property.ID)
		quotedKey := "'" + propertyID + "'"
		resolved := resolvedIssueTableGroup{
			kind:          "property",
			propertyID:    propertyID,
			propertyType:  property.Type,
			activeOptions: map[string]struct{}{},
		}
		switch property.Type {
		case "select":
			config := parsePropertyConfig(property.Config)
			resolved.activeOptionOrder = make([]string, 0, len(config.Options))
			for _, option := range config.Options {
				resolved.activeOptions[option.ID] = struct{}{}
				resolved.activeOptionOrder = append(resolved.activeOptionOrder, "value:"+option.ID)
			}
			resolved.groupExpr = fmt.Sprintf(`CASE
  WHEN NOT (i.properties ? %s) THEN 'unset:'
  WHEN jsonb_typeof(i.properties -> %s) = 'string' AND i.properties ->> %s = ANY(%%s::text[]) THEN 'value:' || (i.properties ->> %s)
  WHEN jsonb_typeof(i.properties -> %s) = 'string' THEN 'unavailable:' || (i.properties ->> %s)
  ELSE 'unavailable:'
END`, quotedKey, quotedKey, quotedKey, quotedKey, quotedKey, quotedKey)
		case "checkbox":
			resolved.groupExpr = fmt.Sprintf(`CASE
  WHEN NOT (i.properties ? %s) THEN 'unset:'
  WHEN jsonb_typeof(i.properties -> %s) = 'boolean' THEN 'value:' || (i.properties ->> %s)
  ELSE 'unavailable:'
END`, quotedKey, quotedKey, quotedKey)
		default:
			writeIssueTableUnsupportedGroup(w, "property_type_unsupported", "This property type cannot be used for grouping.")
			return resolvedIssueTableGroup{}, false
		}
		return resolved, true
	default:
		writeIssueTableUnsupportedGroup(w, "group_kind_unsupported", "This group type is not supported.")
		return resolvedIssueTableGroup{}, false
	}
}

// writeIssueTableUnsupportedGroup 以 422 + unsupported_group 错误码写回「该分组方式不受支持」
// 类错误，调用方（前端）据 code 决定降级提示。
func writeIssueTableUnsupportedGroup(w http.ResponseWriter, code, message string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":   "unsupported_group",
		"code":    code,
		"message": message,
	})
}

// expression 返回分组用的 SQL 取值表达式。compound 委托给主分组；status_category 走类别改写；
// select 型 property 需要把激活选项数组参数化后填入 groupExpr。
func (group resolvedIssueTableGroup) expression(addArg func(any) string) string {
	if group.kind == "compound" && group.primary != nil {
		return group.primary.expression(addArg)
	}
	if group.kind == "status_category" {
		return group.categoryExpression(addArg)
	}
	if group.kind == "property" && group.propertyType == "select" {
		active := make([]string, 0, len(group.activeOptions))
		for value := range group.activeOptions {
			active = append(active, value)
		}
		return fmt.Sprintf(group.groupExpr, addArg(active))
	}
	return group.groupExpr
}

// 跨工作区目录中的同名状态可以属于不同类别，按任务所属工作区解析。
const issueStatusCategorySQL = "COALESCE((SELECT st.category FROM issue_status st WHERE st.workspace_id = i.workspace_id AND st.key = i.status), i.status)"

func (group resolvedIssueTableGroup) categoryExpression(addArg func(any) string) string {
	if group.crossWorkspace {
		return issueStatusCategorySQL
	}
	return statusCategoryExpr(group.statusCustomKeys, addArg)
}

// sortExpression 返回组间排序的 SQL 表达式（排序在归并成组之后执行，可用别名 group_value）；
// compound 委托给主分组；没有显式排序表达式的回落到 group_value 本身。
func (group resolvedIssueTableGroup) sortExpression() string {
	if group.kind == "compound" && group.primary != nil {
		return group.primary.sortExpression()
	}
	if group.groupSortExpr != "" {
		return group.groupSortExpr
	}
	return "group_value"
}

// orderExpression 返回组间的优先级排序表达式（如状态类别列的顺序、受理人类型顺序、属性选项的
// 声明顺序）。表达式以 group_value 为输入；prop 分组还需要把选项顺序参数化后嵌入。
func (group resolvedIssueTableGroup) orderExpression(addArg func(any) string) string {
	if group.kind == "compound" && group.primary != nil {
		return group.primary.orderExpression(addArg)
	}
	switch group.kind {
	case "status", "status_category":
		return "CASE group_value WHEN 'backlog' THEN 0 WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'in_review' THEN 3 WHEN 'done' THEN 4 WHEN 'blocked' THEN 5 WHEN 'cancelled' THEN 6 ELSE 7 END"
	case "assignee":
		return "CASE split_part(group_value, ':', 1) WHEN 'member' THEN 0 WHEN 'agent' THEN 1 WHEN 'squad' THEN 2 ELSE 3 END"
	case "project":
		return "CASE WHEN group_value = '__no_project__' THEN 0 ELSE 1 END"
	case "parent":
		return "CASE WHEN group_value = '__no_parent__' THEN 0 ELSE 1 END"
	case "property":
		if group.propertyType == "select" {
			ref := addArg(group.activeOptionOrder)
			return fmt.Sprintf("CASE WHEN group_value LIKE 'value:%%' THEN COALESCE(array_position(%s::text[], group_value), 100000) WHEN group_value LIKE 'unavailable:%%' THEN 100001 ELSE 100002 END", ref)
		}
		return "CASE group_value WHEN 'value:false' THEN 0 WHEN 'value:true' THEN 1 WHEN 'unavailable:' THEN 2 ELSE 3 END"
	default:
		return "0"
	}
}

// contextExpression 返回组头附带上下文 JSON 的 SQL（目前只有 parent 分组要给每组带上父 issue
// 的摘要：id/编号/标识/标题/状态；其余分组恒为空对象）。
func (group resolvedIssueTableGroup) contextExpression(addArg func(any) string, issuePrefix string) string {
	if group.kind == "compound" && group.primary != nil {
		return group.primary.contextExpression(addArg, issuePrefix)
	}
	if group.kind != "parent" {
		return "'{}'::jsonb"
	}
	if group.crossWorkspace {
		return "CASE WHEN group_value = '__no_parent__' THEN '{}'::jsonb ELSE COALESCE((SELECT jsonb_build_object('parent', jsonb_build_object('id', p.id::text, 'number', p.number, 'identifier', w.issue_prefix || '-' || p.number::text, 'title', p.title, 'status', p.status)) FROM issue p JOIN workspace w ON w.id = p.workspace_id WHERE p.id = group_value::uuid ), '{}'::jsonb) END"
	}
	prefixRef := addArg(issuePrefix)
	return fmt.Sprintf(`CASE WHEN group_value = '__no_parent__' THEN '{}'::jsonb ELSE COALESCE((
  SELECT jsonb_build_object('parent', jsonb_build_object(
    'id', p.id::text,
    'number', p.number,
    'identifier', %s::text || '-' || p.number::text,
    'title', p.title,
    'status', p.status
  ))
  FROM issue p
  WHERE p.workspace_id = $1 AND p.id = group_value::uuid
), '{}'::jsonb) END`, prefixRef)
}

// statusCategoryGroupKeyPrefix 是状态类别分组键的前缀，用于把类别写进 Key 里与「按 key 分组」的
// status 键区分开。
const statusCategoryGroupKeyPrefix = "status_category:"

// statusCategoryGroupKey 生成某类别对应的分组键（<前缀> + 类别名）。
func statusCategoryGroupKey(category string) string {
	return statusCategoryGroupKeyPrefix + category
}

// parseStatusCategoryGroupKey 从分组键反解出类别名；前缀不符或类别不在内置白名单里返回 ok=false。
func parseStatusCategoryGroupKey(key string) (string, bool) {
	if !strings.HasPrefix(key, statusCategoryGroupKeyPrefix) {
		return "", false
	}
	category := strings.TrimPrefix(key, statusCategoryGroupKeyPrefix)
	if !issueTableContainsString(validIssueStatuses, category) {
		return "", false
	}
	return category, true
}

// categoryKeysFor 返回某类别下的具体状态 key。查不到时退回类别本身——这永远正确，因为内置 key
// 就是它自己的类别；目录读取失败也因此降级成特性上线前的行为，而不是空结果集。
func (group resolvedIssueTableGroup) categoryKeysFor(category string) []string {
	if keys := group.categoryKeys[category]; len(keys) > 0 {
		return keys
	}
	return []string{category}
}

// compoundCellGroupKey 生成复合分组「主组 × 次级状态列」某个 cell 的分组键：主分组 key base64
// 编码后拼上次级轴（:status: 或 :status_category:）与状态值。主 key 含不可安全拼串的字符，编码
// 是为保证键可回解、无歧义。
func compoundCellGroupKey(primaryKey, status string, category bool) string {
	axis := ":status:"
	if category {
		axis = ":status_category:"
	}
	return "compound:" + base64.RawURLEncoding.EncodeToString([]byte(primaryKey)) + axis + status
}

// descriptor 把一行分组统计（raw 取值 + count + 上下文 + 次级计数）翻译成对外组头描述：按 kind
// 把 raw 解析成类型化 Value 与稳定 Key（无受理人/无项目等哨兵、base64 编码的属性值等），compound
// 时还要套一层次级组列表。raw 与 kind 对不上属内部不一致，返回 error 由调用方按 500 上报。
func (group resolvedIssueTableGroup) descriptor(raw string, count int64, context issueTableGroupContext, secondaryCounts map[string]int64) (issueTableGroupDescriptorResponse, error) {
	if group.kind == "compound" && group.primary != nil {
		descriptor, err := group.primary.descriptor(raw, count, context, nil)
		if err != nil {
			return descriptor, err
		}
		descriptor.SecondaryGroups = make([]issueTableGroupDescriptorResponse, 0, len(secondaryCounts))
		for _, status := range validIssueStatuses {
			statusCount := secondaryCounts[status]
			descriptor.SecondaryGroups = append(descriptor.SecondaryGroups, issueTableGroupDescriptorResponse{
				Key: compoundCellGroupKey(descriptor.Key, status, group.secondaryCategory),
				Value: issueTableGroupValueResponse{
					Kind:   "status",
					Status: status,
				},
				Count: statusCount,
			})
		}
		return descriptor, nil
	}
	descriptor := issueTableGroupDescriptorResponse{Count: count}
	switch group.kind {
	case "status":
		// 任意非空的状态 KEY 都合法，不只那 7 个内置。自 MUL-6243 起工作区可以有自定义状态，
		// 若在这里拒绝它们会让「按状态分组」整组响应 500——一条自定义状态就能让整个工作区的
		// 按状态分组不可用。
		if raw == "" {
			return descriptor, fmt.Errorf("unexpected status group value %q", raw)
		}
		descriptor.Key = "status:" + raw
		descriptor.Value = issueTableGroupValueResponse{Kind: "status", Status: raw}
	case "status_category":
		if !issueTableContainsString(validIssueStatuses, raw) {
			return descriptor, fmt.Errorf("unexpected status category group value %q", raw)
		}
		descriptor.Key = statusCategoryGroupKey(raw)
		// value.kind 保持 "status"：类别的 value 就是它的 canonical 状态 key，所以这是精确映射而
		// 非兼容垫片，所有既有 status 分组消费方无需改动仍能工作。真正区分两类契约的是 KEY。
		// （MUL-6243）
		descriptor.Value = issueTableGroupValueResponse{Kind: "status", Status: raw}
	case "assignee":
		descriptor.Value.Kind = "assignee"
		if raw == "__unassigned__" {
			descriptor.Key = "assignee:unassigned"
			return descriptor, nil
		}
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 || !isIssueActorType(parts[0]) {
			return descriptor, fmt.Errorf("unexpected assignee group value %q", raw)
		}
		if _, err := util.ParseUUID(parts[1]); err != nil {
			return descriptor, fmt.Errorf("unexpected assignee group value %q", raw)
		}
		descriptor.Key = "assignee:" + raw
		descriptor.Value.Actor = &issueTableActorRef{Type: parts[0], ID: parts[1]}
	case "project":
		descriptor.Value.Kind = "project"
		if raw == "__no_project__" {
			descriptor.Key = "project:none"
			return descriptor, nil
		}
		if _, err := util.ParseUUID(raw); err != nil {
			return descriptor, fmt.Errorf("unexpected project group value %q", raw)
		}
		descriptor.Key = "project:" + raw
		descriptor.Value.ProjectID = &raw
	case "parent":
		descriptor.Value.Kind = "parent"
		if raw == "__no_parent__" {
			descriptor.Key = "parent:none"
			descriptor.Value.ValueState = "unset"
			return descriptor, nil
		}
		if _, err := util.ParseUUID(raw); err != nil {
			return descriptor, fmt.Errorf("unexpected parent group value %q", raw)
		}
		descriptor.Key = "parent:" + raw
		descriptor.Value.ParentID = &raw
		descriptor.Value.Parent = context.Parent
		if context.Parent == nil {
			descriptor.Value.ValueState = "unavailable"
		} else {
			descriptor.Value.ValueState = "value"
		}
	case "property":
		state, rawValue, ok := strings.Cut(raw, ":")
		if !ok {
			return descriptor, fmt.Errorf("unexpected property group value %q", raw)
		}
		encoded := base64.RawURLEncoding.EncodeToString([]byte(rawValue))
		descriptor.Key = "property:" + group.propertyID + ":" + state + ":" + encoded
		descriptor.Value = issueTableGroupValueResponse{
			Kind:       "property",
			PropertyID: group.propertyID,
		}
		switch state {
		case "unset":
			descriptor.Value.ValueState = "unset"
		case "unavailable":
			descriptor.Value.ValueState = "unavailable"
			if rawValue != "" {
				descriptor.Value.Value = rawValue
			}
		case "value":
			descriptor.Value.ValueState = "value"
			if group.propertyType == "checkbox" {
				value, err := strconv.ParseBool(rawValue)
				if err != nil {
					return descriptor, fmt.Errorf("unexpected checkbox group value %q", rawValue)
				}
				descriptor.Value.Value = value
			} else {
				descriptor.Value.Value = rawValue
			}
		default:
			return descriptor, fmt.Errorf("unexpected property group state %q", state)
		}
	default:
		return descriptor, fmt.Errorf("unsupported group kind %q", group.kind)
	}
	return descriptor, nil
}

// predicate 把客户端回传的 group_key 反解成一条可直接并入 WHERE 的 SQL 谓词（经 addArg 参数化，
// 不拼接用户输入）。compound 键先解码出主分组键 + 次级状态再组合；property 键反解出状态与
// 值；非法键一律 400。
func (group resolvedIssueTableGroup) predicate(w http.ResponseWriter, key string, addArg func(any) string) (string, bool) {
	if group.kind == "compound" && group.primary != nil {
		const prefix = "compound:"
		if !strings.HasPrefix(key, prefix) {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		encodedAndStatus := strings.TrimPrefix(key, prefix)
		axis := ":status:"
		if group.secondaryCategory {
			axis = ":status_category:"
		}
		encoded, status, ok := strings.Cut(encodedAndStatus, axis)
		if !ok || !issueTableContainsString(validIssueStatuses, status) {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		primaryPredicate, ok := group.primary.predicate(w, string(decoded), addArg)
		if !ok {
			return "", false
		}
		if group.secondaryCategory {
			if group.crossWorkspace {
				return fmt.Sprintf("(%s) AND (%s) = %s::text", primaryPredicate, group.categoryExpression(addArg), addArg(status)), true
			}
			return fmt.Sprintf("(%s) AND i.status = ANY(%s::text[])", primaryPredicate, addArg(group.categoryKeysFor(status))), true
		}
		return fmt.Sprintf("(%s) AND i.status = %s::text", primaryPredicate, addArg(status)), true
	}
	switch group.kind {
	case "none":
		if key != "" {
			writeError(w, http.StatusBadRequest, "group_key must be empty when group.kind=none")
			return "", false
		}
		return "TRUE", true
	case "status":
		const prefix = "status:"
		status, found := strings.CutPrefix(key, prefix)
		// 自定义状态在这里同样是合法分组；无论内置还是自定义，谓词都是精确 key 匹配。长度受限，
		// 防止任意大段文本冒充状态 key 混进来。
		if !found || status == "" || len(status) > 64 {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		return fmt.Sprintf("i.status = %s::text", addArg(status)), true
	case "status_category":
		category, ok := parseStatusCategoryGroupKey(key)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		if group.crossWorkspace {
			return fmt.Sprintf("(%s) = %s::text", group.categoryExpression(addArg), addArg(category)), true
		}
		// 展开成具体 key 而不是把列包进类别函数：`status = ANY(...)` 能继续用 (workspace_id,
		// status) 索引，函数包裹会退化成全工作区扫描。
		return fmt.Sprintf("i.status = ANY(%s::text[])", addArg(group.categoryKeysFor(category))), true
	case "assignee":
		const prefix = "assignee:"
		if !strings.HasPrefix(key, prefix) {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		raw := strings.TrimPrefix(key, prefix)
		if raw == "unassigned" {
			return "i.assignee_type IS NULL AND i.assignee_id IS NULL", true
		}
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 || !isIssueActorType(parts[0]) {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		id, err := util.ParseUUID(parts[1])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		return fmt.Sprintf("i.assignee_type = %s::text AND i.assignee_id = %s::uuid", addArg(parts[0]), addArg(id)), true
	case "project":
		const prefix = "project:"
		if !strings.HasPrefix(key, prefix) {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		raw := strings.TrimPrefix(key, prefix)
		if raw == "none" {
			return "i.project_id IS NULL", true
		}
		id, err := util.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		return fmt.Sprintf("i.project_id = %s::uuid", addArg(id)), true
	case "parent":
		const prefix = "parent:"
		if !strings.HasPrefix(key, prefix) {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		raw := strings.TrimPrefix(key, prefix)
		if raw == "none" {
			return "i.parent_issue_id IS NULL", true
		}
		id, err := util.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		return fmt.Sprintf("i.parent_issue_id = %s::uuid", addArg(id)), true
	case "property":
		prefix := "property:" + group.propertyID + ":"
		if !strings.HasPrefix(key, prefix) {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		rest := strings.TrimPrefix(key, prefix)
		state, encoded, ok := strings.Cut(rest, ":")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
		value := string(decoded)
		keySQL := "'" + group.propertyID + "'"
		switch state {
		case "unset":
			if value != "" {
				writeError(w, http.StatusBadRequest, "invalid group_key")
				return "", false
			}
			return fmt.Sprintf("NOT (i.properties ? %s)", keySQL), true
		case "value":
			if group.propertyType == "select" {
				if _, exists := group.activeOptions[value]; !exists {
					writeError(w, http.StatusBadRequest, "invalid group_key")
					return "", false
				}
				return fmt.Sprintf("jsonb_typeof(i.properties -> %s) = 'string' AND i.properties ->> %s = %s::text", keySQL, keySQL, addArg(value)), true
			}
			if value != "true" && value != "false" {
				writeError(w, http.StatusBadRequest, "invalid group_key")
				return "", false
			}
			return fmt.Sprintf("jsonb_typeof(i.properties -> %s) = 'boolean' AND i.properties ->> %s = %s::text", keySQL, keySQL, addArg(value)), true
		case "unavailable":
			if group.propertyType == "select" && value != "" {
				return fmt.Sprintf("jsonb_typeof(i.properties -> %s) = 'string' AND i.properties ->> %s = %s::text", keySQL, keySQL, addArg(value)), true
			}
			return fmt.Sprintf("i.properties ? %s AND jsonb_typeof(i.properties -> %s) <> %s::text", keySQL, keySQL, addArg(map[string]string{"select": "string", "checkbox": "boolean"}[group.propertyType])), true
		default:
			writeError(w, http.StatusBadRequest, "invalid group_key")
			return "", false
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid group_key")
		return "", false
	}
}

// ListIssueTableGroups 处理 POST /api/issues/table/groups：在只读快照事务里按分组规格返回
// 一组组头描述（含每组的行数/次级计数等），用于表格分组视图的渲染。
func (h *Handler) ListIssueTableGroups(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		writeError(w, http.StatusInternalServerError, "database is unavailable")
		return
	}
	var request issueTableGroupsRequest
	if !decodeIssueTableJSON(w, r, &request) {
		return
	}
	r, cancel := withIssueTableQueryTimeout(r)
	defer cancel()
	snapshot, tx, err := h.beginIssueTableSnapshot(r.Context())
	if err != nil {
		slog.Warn("ListIssueTableGroups snapshot failed", append(logger.RequestAttrs(r), "error", err)...)
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
	compiled, ok := h.compileIssueTableQuery(w, r, request.Query)
	if !ok {
		return
	}
	group, ok := h.resolveIssueTableGroup(w, r, compiled.workspaceID, request.Group, false, compiled.crossWorkspace)
	if !ok {
		return
	}
	groupIdentity := issueTableGroupIdentity(request.Group)
	if !issueTableCursorMatches(w, cursor, compiled.fingerprint, &groupIdentity, nil) {
		return
	}

	args := append([]any(nil), compiled.args...)
	addArg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	groupExpr := group.expression(addArg)
	groupSortExpr := group.sortExpression()
	orderExpr := group.orderExpression(addArg)
	contextExpr := group.contextExpression(addArg, h.getIssuePrefix(r.Context(), compiled.workspaceID))
	cursorPredicate := "TRUE"
	if cursor != nil {
		if cursor.GroupOrder == nil || cursor.GroupSortKey == nil || cursor.GroupCursorKey == nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		orderRef := addArg(*cursor.GroupOrder)
		sortRef := addArg(*cursor.GroupSortKey)
		keyRef := addArg(*cursor.GroupCursorKey)
		cursorPredicate = fmt.Sprintf(`(group_order > %[1]s::int OR (
  group_order = %[1]s::int AND (
    group_sort > %[2]s::text OR (group_sort = %[2]s::text AND group_value > %[3]s::text)
  )
))`, orderRef, sortRef, keyRef)
	}
	limitRef := addArg(limit + 1)
	groupedCTE := fmt.Sprintf(`grouped AS (
  SELECT %s AS group_value, COUNT(*)::bigint AS issue_count,
         COUNT(*)::bigint AS visible_count, '{}'::jsonb AS secondary_counts
  FROM issue i
  WHERE %s
  GROUP BY 1
)`, groupExpr, compiled.where)
	if request.Group.IncludeEmpty && group.kind == "property" {
		expectedValues := []string{"unset:"}
		if group.propertyType == "select" {
			expectedValues = append(append([]string(nil), group.activeOptionOrder...), "unset:")
		} else {
			expectedValues = []string{"value:false", "value:true", "unset:"}
		}
		expectedRef := addArg(expectedValues)
		groupedCTE = fmt.Sprintf(`actual AS (
  SELECT %s AS group_value, COUNT(*)::bigint AS issue_count
  FROM issue i
  WHERE %s
  GROUP BY 1
), expected AS (
  SELECT unnest(%s::text[]) AS group_value
), grouped AS (
  SELECT e.group_value, COALESCE(a.issue_count, 0)::bigint AS issue_count,
         COALESCE(a.issue_count, 0)::bigint AS visible_count,
         '{}'::jsonb AS secondary_counts
  FROM expected e
  LEFT JOIN actual a USING (group_value)
  UNION ALL
  SELECT a.group_value, a.issue_count, a.issue_count AS visible_count,
         '{}'::jsonb AS secondary_counts
  FROM actual a
  WHERE NOT (a.group_value = ANY(%s::text[]))
)`, groupExpr, compiled.where, expectedRef, expectedRef)
	}
	if group.kind == "compound" {
		// 次级轴要么是状态 key 本身、要么是它表现所属的「类别」。按类别时，自定义状态计入它
		// 渲染所在列的那个 cell，绝不会单开一格。（MUL-6243）
		secondaryExpr := "i.status"
		if group.secondaryCategory {
			secondaryExpr = group.categoryExpression(addArg)
		}
		groupedCTE = fmt.Sprintf(`cells AS (
  SELECT %s AS group_value, %s AS secondary_value, COUNT(*)::bigint AS cell_count
  FROM issue i
  WHERE %s
  GROUP BY 1, 2
), grouped AS (
  SELECT group_value,
         SUM(cell_count)::bigint AS issue_count,
         SUM(cell_count)::bigint AS visible_count,
         jsonb_object_agg(secondary_value, cell_count)::jsonb AS secondary_counts
  FROM cells
  GROUP BY group_value
)`, groupExpr, secondaryExpr, compiled.where)
		if group.secondaryFiltered {
			visibleRef := addArg(group.secondaryValues)
			// promoted_parents 匹配的是原始 `child.status`，所以按类别分轴时这里要把可见类别
			// 展开回具体 key；cell 过滤器则直接比较已是类别的 secondary_value。
			visibleKeysRef := visibleRef
			if group.secondaryCategory {
				expanded := make([]string, 0, len(group.secondaryValues))
				for _, category := range group.secondaryValues {
					expanded = append(expanded, group.categoryKeysFor(category)...)
				}
				visibleKeysRef = addArg(expanded)
			}
			headerPredicate := "TRUE"
			promotedParentsCTE := ""
			if group.primary != nil && group.primary.kind == "parent" {
				headerPredicate = `NOT (
    i.parent_issue_id IS NULL AND
    EXISTS (SELECT 1 FROM promoted_parents p WHERE p.id = i.id)
  )`
				promotedParentsCTE = fmt.Sprintf(`, promoted_parents AS (
  SELECT DISTINCT child.parent_issue_id AS id
  FROM membership child
  WHERE child.parent_issue_id IS NOT NULL
    AND child.status = ANY(%s::text[])
)`, visibleKeysRef)
			}
			groupedCTE = fmt.Sprintf(`membership AS NOT MATERIALIZED (
  SELECT i.*
  FROM issue i
  WHERE %s
)%s, cells AS (
  SELECT %s AS group_value, %s AS secondary_value,
         COUNT(*) FILTER (WHERE %s)::bigint AS cell_count
  FROM membership i
  GROUP BY 1, 2
), grouped AS (
  SELECT group_value,
         SUM(cell_count)::bigint AS issue_count,
         COALESCE(
           SUM(cell_count) FILTER (WHERE secondary_value = ANY(%s::text[])),
           0
         )::bigint AS visible_count,
         jsonb_object_agg(secondary_value, cell_count)::jsonb AS secondary_counts
  FROM cells
  GROUP BY group_value
  HAVING COALESCE(
    SUM(cell_count) FILTER (WHERE secondary_value = ANY(%s::text[])),
    0
  ) > 0
)`, compiled.where, promotedParentsCTE, groupExpr, secondaryExpr, headerPredicate, visibleRef, visibleRef)
		}
	}
	query := fmt.Sprintf(`WITH %s, sorted AS (
	  SELECT group_value, issue_count, visible_count, secondary_counts, (%s)::text AS group_sort,
	         (%s)::jsonb AS group_context
	  FROM grouped
	), ranked AS (
	  SELECT group_value, issue_count, visible_count, secondary_counts, group_sort, group_context, (%s)::int AS group_order,
	         SUM(visible_count) OVER ()::bigint AS total
	  FROM sorted
	)
	SELECT group_value, issue_count, secondary_counts, group_sort, group_context, group_order, total
	FROM ranked
	WHERE %s
	ORDER BY group_order ASC, group_sort ASC, group_value ASC
	LIMIT %s`, groupedCTE, groupSortExpr, contextExpr, orderExpr, cursorPredicate, limitRef)

	rows, err := h.DB.Query(r.Context(), query, args...)
	if err != nil {
		slog.Warn("ListIssueTableGroups query failed", append(logger.RequestAttrs(r), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to list table groups")
		return
	}
	defer rows.Close()

	groups := make([]issueTableGroupDescriptorResponse, 0, limit+1)
	orders := make([]int, 0, limit+1)
	sortValues := make([]string, 0, limit+1)
	values := make([]string, 0, limit+1)
	var total int64
	for rows.Next() {
		var raw string
		var count int64
		var secondaryJSON []byte
		var sortValue string
		var contextJSON []byte
		var order int
		if err := rows.Scan(&raw, &count, &secondaryJSON, &sortValue, &contextJSON, &order, &total); err != nil {
			writeIssueTableQueryFailure(w, r, "failed to list table groups")
			return
		}
		var context issueTableGroupContext
		if len(contextJSON) > 0 {
			if err := json.Unmarshal(contextJSON, &context); err != nil {
				writeIssueTableQueryFailure(w, r, "failed to resolve table group")
				return
			}
		}
		secondaryCounts := map[string]int64{}
		if len(secondaryJSON) > 0 {
			if err := json.Unmarshal(secondaryJSON, &secondaryCounts); err != nil {
				writeIssueTableQueryFailure(w, r, "failed to resolve table group")
				return
			}
		}
		descriptor, err := group.descriptor(raw, count, context, secondaryCounts)
		if err != nil {
			slog.Warn("ListIssueTableGroups descriptor failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve table group")
			return
		}
		groups = append(groups, descriptor)
		orders = append(orders, order)
		sortValues = append(sortValues, sortValue)
		values = append(values, raw)
	}
	if err := rows.Err(); err != nil {
		writeIssueTableQueryFailure(w, r, "failed to list table groups")
		return
	}
	rows.Close()

	var nextCursor *string
	if len(groups) > limit {
		groups = groups[:limit]
		lastOrder := orders[limit-1]
		lastSort := sortValues[limit-1]
		lastKey := values[limit-1]
		nextCursor = encodeIssueTableCursor(issueTableCursor{
			Version:          1,
			QueryFingerprint: compiled.fingerprint,
			GroupKey:         &groupIdentity,
			GroupOrder:       &lastOrder,
			GroupSortKey:     &lastSort,
			GroupCursorKey:   &lastKey,
		})
	}
	response := issueTableGroupsResponse{
		QueryFingerprint: compiled.fingerprint,
		Total:            total,
		Groups:           groups,
		NextCursor:       nextCursor,
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("ListIssueTableGroups snapshot commit failed", append(logger.RequestAttrs(r), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to finish table query")
		return
	}
	committed = true
	writeJSON(w, http.StatusOK, response)
}

// issueStatusCatalog 是分组/类别映射读目录要经过的 querier：默认用 h.Queries；测试注入一个计数
// 包装器来断言目录读取次数。
func (h *Handler) issueStatusCatalog() issuestatus.Querier {
	if h.IssueStatusCatalog != nil {
		return h.IssueStatusCatalog
	}
	return h.Queries
}
