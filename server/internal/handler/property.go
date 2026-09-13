// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（property.go）实现"自定义 issue 属性"（MUL-4463）：workspace 级定义
// 的带类型属性，加每个 issue 上的一个属性值袋。前半是"定义"的 CRUD 与各类
// 校验（含 select 选项、actor 引用、URL/数值/日期等各类型的值规范化），后半
// 是"列表接口"支持——properties 过滤条件的编译与 property:xxx 排序表达式。
// 值写入用单键原子语义与定义变更串行化（advisory 锁）。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 自定义 issue 属性（MUL-4463）：workspace 级的有类型属性定义 + 每个 issue 上
// 的属性值袋。
//
// 合约要点（在 MUL-4463 上敲定）：
//   - 定义只由人工的 owner/admin 成员管理。即使是 runtime owner 持有该角色，
//     agent 执行者也被拒绝——否则"字段膨胀"会成为 agent 能批量制造的东西。
//   - 值可被任意成员与 agent 写入；按定义做类型校验，报错时尽量枚举合法取值，
//     让 agent 能自我纠正。
//   - 值写入是单键原子（mirror issue metadata 的做法）：两个 agent 各写不同
//     属性时永不互相覆盖。
//   - 定义走"归档"而不是"删除"；已归档定义拒绝新值，但保留既有值可解析。
const (
	maxActivePropertiesPerWorkspace = 20
	maxPropertySelectOptions        = 50
	maxPropertyNameLen              = 32
	maxPropertyIconLen              = 32
	maxPropertyDescriptionLen       = 500
	maxPropertyTextValueLen         = 2000
	maxPropertyURLValueLen          = 2048
	// multi_actor 的上限远低于 select 的上限：整个属性值袋共享一行 16KB 的
	// 预算，一个装了几百个 actor 的属性会把同一 issue 上其他属性全挤没。
	maxPropertyActorValues = 20
)

// validPropertyTypes 列出全部受支持的属性类型。
var validPropertyTypes = []string{"text", "number", "select", "multi_select", "date", "checkbox", "url", "actor", "multi_actor"}

// Property 图标用稳定的目录键，由 Web 客户端映射成 Lucide 图形。在 API 边界
// 保留这份允许名单，可防止任意文本（包括 emoji）渗进每个渲染图标的 issue 面。
var validPropertyIcons = map[string]struct{}{
	"circle-dot": {}, "signal-high": {}, "user-round": {}, "folder-kanban": {},
	"calendar-days": {}, "tag": {}, "milestone": {}, "flag": {}, "bookmark": {},
	"star": {}, "target": {}, "shield": {}, "bug": {}, "zap": {}, "rocket": {},
	"sparkles": {}, "lightbulb": {}, "globe-2": {}, "link": {}, "hash": {},
	"list-checks": {}, "circle-check": {}, "clock-3": {}, "briefcase-business": {},
	"layers-3": {}, "gauge": {}, "database": {}, "code-2": {}, "palette": {},
	"megaphone": {}, "map-pin": {}, "package": {}, "wrench": {}, "heart": {},
	"circle-alert": {}, "lock-keyhole": {},
}

// errClientRejected 标记"锁内闭包里已由调用侧 fail() 转成 HTTP 状态"的失败。
var errClientRejected = errors.New("client rejected")

// reservedPropertyNames 拦住会与内置 issue 字段（"系统属性"）撞名的定义。
// 比较发生在规范化形态上：小写、空格折叠成下划线——所以 "Due Date"、"due
// date" 与 "due_date" 全都会被拒。
var reservedPropertyNames = map[string]struct{}{
	"status": {}, "priority": {}, "assignee": {}, "project": {}, "parent": {},
	"stage": {}, "label": {}, "labels": {}, "start_date": {}, "due_date": {},
	"title": {}, "description": {}, "creator": {}, "created_at": {}, "updated_at": {},
	"metadata": {}, "properties": {},
}

// ---------------------------------------------------------------------------
// Types（类型）
// ---------------------------------------------------------------------------

// PropertyOption 是 select/multi_select 属性的一个选项。
type PropertyOption struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// PropertyConfig 是属性定义的可选配置；目前只有 select 类属性带 Options。
type PropertyConfig struct {
	Options []PropertyOption `json:"options,omitempty"`
}

// PropertyResponse 是单个属性定义的对外形态，含使用量计数与归档信息。
type PropertyResponse struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Description string         `json:"description"`
	Icon        string         `json:"icon"`
	Config      PropertyConfig `json:"config"`
	Position    float64        `json:"position"`
	Archived    bool           `json:"archived"`
	ArchivedAt  *string        `json:"archived_at"`
	UsageCount  int64          `json:"usage_count"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

// parsePropertyConfig 尽力把库里存的 JSON 配置解成 PropertyConfig；空或损坏都
// 兜底为空配置（不影响调用方，空配置意味着没有选项）。
func parsePropertyConfig(raw []byte) PropertyConfig {
	var cfg PropertyConfig
	if len(raw) == 0 {
		return cfg
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return PropertyConfig{}
	}
	return cfg
}

// propertyToResponse 把属性库行 + 使用量计数转成对外响应。
func propertyToResponse(p db.IssueProperty, usageCount int64) PropertyResponse {
	resp := PropertyResponse{
		ID:          uuidToString(p.ID),
		WorkspaceID: uuidToString(p.WorkspaceID),
		Name:        p.Name,
		Type:        p.Type,
		Description: p.Description,
		Icon:        p.Icon,
		Config:      parsePropertyConfig(p.Config),
		Position:    p.Position,
		Archived:    p.ArchivedAt.Valid,
		UsageCount:  usageCount,
		CreatedAt:   timestampToString(p.CreatedAt),
		UpdatedAt:   timestampToString(p.UpdatedAt),
	}
	if p.ArchivedAt.Valid {
		s := timestampToString(p.ArchivedAt)
		resp.ArchivedAt = &s
	}
	return resp
}

// propertyListRowToResponse 把"列表联查"行转成对外响应（行里已带 UsageCount）。
func propertyListRowToResponse(row db.ListIssuePropertiesRow) PropertyResponse {
	return propertyToResponse(db.IssueProperty{
		ID:          row.ID,
		WorkspaceID: row.WorkspaceID,
		Name:        row.Name,
		Type:        row.Type,
		Description: row.Description,
		Icon:        row.Icon,
		Config:      row.Config,
		Position:    row.Position,
		ArchivedAt:  row.ArchivedAt,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}, row.UsageCount)
}

// CreatePropertyRequest 是创建属性定义的入参。
type CreatePropertyRequest struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Config      *PropertyConfig `json:"config"`
}

// UpdatePropertyRequest 是更新属性定义的入参；指针字段区分"未传"与"传空"，
// Archived 控制归档/取消归档。
type UpdatePropertyRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Icon        *string         `json:"icon"`
	Config      *PropertyConfig `json:"config"`
	Archived    *bool           `json:"archived"`
}

// ---------------------------------------------------------------------------
// Validation（校验）
// ---------------------------------------------------------------------------

// normalizePropertyName 把名字规整成"比较形态"：小写、去首尾空格、空格折叠为
// 下划线。
func normalizePropertyName(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), " ", "_")
}

// validatePropertyName 校验属性名：不能含控制字符、去空格后非空、长度不超过
// maxPropertyNameLen、不与内置 issue 字段的保留名冲突。返回干净后的名字。
func validatePropertyName(raw string) (string, error) {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", errors.New("name cannot contain tabs, newlines, or control characters")
		}
	}
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("name is required")
	}
	if utf8.RuneCountInString(name) > maxPropertyNameLen {
		return "", fmt.Errorf("name must be %d characters or fewer", maxPropertyNameLen)
	}
	if _, reserved := reservedPropertyNames[normalizePropertyName(name)]; reserved {
		return "", fmt.Errorf("%q is reserved for a built-in issue field", name)
	}
	return name, nil
}

// validatePropertyIcon 校验图标键：不能含控制字符、长度受限；允许留空
// （不带图标）；非空必须在 validPropertyIcons 允许名单里。
func validatePropertyIcon(raw string) (string, error) {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", errors.New("icon cannot contain tabs, newlines, or control characters")
		}
	}
	icon := strings.TrimSpace(raw)
	if utf8.RuneCountInString(icon) > maxPropertyIconLen {
		return "", fmt.Errorf("icon must be %d characters or fewer", maxPropertyIconLen)
	}
	if icon == "" {
		return "", nil
	}
	if _, ok := validPropertyIcons[icon]; !ok {
		return "", errors.New("icon must be a supported icon key")
	}
	return icon, nil
}

// validatePropertyType 校验类型必须在 validPropertyTypes 内。
func validatePropertyType(t string) error {
	for _, v := range validPropertyTypes {
		if t == v {
			return nil
		}
	}
	return fmt.Errorf("invalid type %q; valid types: %s", t, strings.Join(validPropertyTypes, ", "))
}

// propertyTypeHasOptions 判断该类型是否携带选项（仅 select/multi_select）。
func propertyTypeHasOptions(t string) bool {
	return t == "select" || t == "multi_select"
}

// validatePropertyConfig 规范化要落库的配置。select 类属性必须有 1..50 个
// 选项；调用方没给 id 时每个选项获得一个服务端分配的稳定 UUID（issue 上的
// 值引用的是选项 id，所以改选项名永远不动 issue 行）。非 select 类型不得携带
// 选项，统一存成 {}。
func validatePropertyConfig(propType string, cfg *PropertyConfig) ([]byte, error) {
	if !propertyTypeHasOptions(propType) {
		if cfg != nil && len(cfg.Options) > 0 {
			return nil, fmt.Errorf("type %q does not accept options", propType)
		}
		return []byte(`{}`), nil
	}
	if cfg == nil || len(cfg.Options) == 0 {
		return nil, errors.New("select properties require at least one option")
	}
	if len(cfg.Options) > maxPropertySelectOptions {
		return nil, fmt.Errorf("a property cannot have more than %d options", maxPropertySelectOptions)
	}
	seenIDs := make(map[string]struct{}, len(cfg.Options))
	seenNames := make(map[string]struct{}, len(cfg.Options))
	out := PropertyConfig{Options: make([]PropertyOption, 0, len(cfg.Options))}
	for _, opt := range cfg.Options {
		name, err := validateLabelName(opt.Name)
		if err != nil {
			return nil, fmt.Errorf("option %w", err)
		}
		lower := strings.ToLower(name)
		if _, dup := seenNames[lower]; dup {
			return nil, fmt.Errorf("duplicate option name %q", name)
		}
		seenNames[lower] = struct{}{}
		color, err := normalizeColor(opt.Color)
		if err != nil {
			return nil, fmt.Errorf("option %q: %w", name, err)
		}
		id := strings.TrimSpace(opt.ID)
		if id == "" {
			id = uuid.NewString()
		} else if _, err := uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("option %q: id must be a UUID", name)
		}
		if _, dup := seenIDs[id]; dup {
			return nil, fmt.Errorf("duplicate option id %q", id)
		}
		seenIDs[id] = struct{}{}
		out.Options = append(out.Options, PropertyOption{ID: id, Name: name, Color: color})
	}
	return json.Marshal(out)
}

// propertyOptionIDs 建立"选项 id → 在 config 中的下标"映射，供包含关系判断与
// 排序用。
func propertyOptionIDs(cfg PropertyConfig) map[string]int {
	ids := make(map[string]int, len(cfg.Options))
	for i, opt := range cfg.Options {
		ids[opt.ID] = i
	}
	return ids
}

// selectOptionsHint 拼出"合法选项 id（选项名）"的人读提示，用于错误文案。
func selectOptionsHint(cfg PropertyConfig) string {
	parts := make([]string, len(cfg.Options))
	for i, opt := range cfg.Options {
		parts[i] = fmt.Sprintf("%s (%s)", opt.ID, opt.Name)
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Actor 值（MUL-6286）
// ---------------------------------------------------------------------------

// actorPropertyKinds 是 actor 属性的 V1 取值域：仅限 workspace 成员。issue 的
// assignee 还接受 "agent" 与 "squad"，但被动的引用型属性两者都不该进——引用
// agent 会拖进整个 agent 可见性问题（私有/未进 allow-list 的 agent 不能被按
// id 反向发现），目前又没有用例支撑；而 squad 是路由目标，不是一个人。
//
// 存储形态是 "<kind>:<uuid>"，所以扩这个名单只改一行：不需要迁移、不需要新
// 属性类型，既有定义原地获得新 kind。任何加进来的东西若非对每个 workspace
// 成员都普遍可见（例如 agent），写路径与表格 facet 读路径都必须补回可见性
// 闸门。
var actorPropertyKinds = []string{"member"}

// actorRef 是一条已解析的 "<kind>:<uuid>" 属性值。
type actorRef struct {
	Kind string
	ID   string
}

// String 还原成 "<kind>:<uuid>" 的存储形态。
func (a actorRef) String() string { return a.Kind + ":" + a.ID }

// propertyTypeIsActor 判断类型是否为 actor/multi_actor。
func propertyTypeIsActor(t string) bool {
	return t == "actor" || t == "multi_actor"
}

// actorKindsHint 拼出合法 kind 列表的人读提示。
func actorKindsHint() string {
	return strings.Join(actorPropertyKinds, " / ")
}

// parseActorRef 解析一条存储的 actor 值。成员用 user_id 引用——与 assignee
// 对偶用的同一 id——所以"这人是谁"在产品各处解析结果一致。
func parseActorRef(s string) (actorRef, error) {
	kind, id, found := strings.Cut(s, ":")
	if !found {
		return actorRef{}, fmt.Errorf("value must look like \"<kind>:<uuid>\" where kind is one of: %s", actorKindsHint())
	}
	valid := false
	for _, k := range actorPropertyKinds {
		if kind == k {
			valid = true
			break
		}
	}
	if !valid {
		return actorRef{}, fmt.Errorf("unknown actor kind %q; valid kinds: %s", kind, actorKindsHint())
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return actorRef{}, fmt.Errorf("actor id in %q must be a UUID", s)
	}
	// 存成规范的小写横线形态。uuid.Parse 也接受大写、花括号与 urn: 前缀；下游
	// 每个消费方（客户端成员目录查询、"= me"过滤器、@> 包含判断）都是按引用串
	// 精确比对，未规范化的 id 能存进库、渲染时却是 Unknown、也永远匹配不上
	// 过滤器。
	return actorRef{Kind: kind, ID: parsed.String()}, nil
}

// parseActorRefList 校验 multi_actor 数组：每个元素都必须能解析、去掉重复项、
// 保持调用方顺序。与 multi_select 不同，这里没有 config 顺序可供规范化，按 id
// 排序会让头像行每次编辑都重排。@> 包含判断与顺序无关，过滤结果不受影响。
func parseActorRefList(items []any) ([]actorRef, error) {
	if len(items) == 0 {
		return nil, errors.New("value must be a non-empty array of actor references")
	}
	if len(items) > maxPropertyActorValues {
		return nil, fmt.Errorf("value cannot list more than %d actors", maxPropertyActorValues)
	}
	seen := make(map[string]struct{}, len(items))
	refs := make([]actorRef, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, errors.New("value must be an array of actor reference strings")
		}
		ref, err := parseActorRef(s)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[ref.String()]; dup {
			continue
		}
		seen[ref.String()] = struct{}{}
		refs = append(refs, ref)
	}
	return refs, nil
}

// actorRefsInValue 重新读一份 actor 属性已规范化的存储 JSON，抽出其中的引用。
// SetIssueProperty 在纯形状校验跑完后用它把引用拿去 workspace 里解析。
func actorRefsInValue(propType string, stored []byte) ([]actorRef, error) {
	if propType == "actor" {
		var s string
		if err := json.Unmarshal(stored, &s); err != nil {
			return nil, err
		}
		ref, err := parseActorRef(s)
		if err != nil {
			return nil, err
		}
		return []actorRef{ref}, nil
	}
	var list []string
	if err := json.Unmarshal(stored, &list); err != nil {
		return nil, err
	}
	refs := make([]actorRef, 0, len(list))
	for _, s := range list {
		ref, err := parseActorRef(s)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// resolveActorRefs 确认每条引用都指向本 workspace 的真实成员。只要 kind 只有
// member 一种就不需要可见性闸门：workspace 成员关系对每个成员本来就可见。
// 若将来加入不普遍可见的 kind（如 agent），必须把这个闸门补回来——见
// actorPropertyKinds。
func (h *Handler) resolveActorRefs(r *http.Request, workspaceID string, refs []actorRef) (int, string) {
	ctx := r.Context()
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return http.StatusBadRequest, "invalid workspace_id"
	}
	for _, ref := range refs {
		refUUID, err := util.ParseUUID(ref.ID)
		if err != nil {
			return http.StatusBadRequest, fmt.Sprintf("actor id in %q must be a UUID", ref)
		}
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID:      refUUID,
			WorkspaceID: wsUUID,
		}); err != nil {
			return http.StatusBadRequest, fmt.Sprintf("%q does not refer to a member of this workspace", ref)
		}
	}
	return 0, ""
}

// validatePropertyValue 按定义的类型校验一条原始 JSON 值，返回要落库的规范化
// JSON。错误文案在可能处枚举合法取值——agent 直接消费这些文案自我纠正。
func validatePropertyValue(def db.IssueProperty, raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("value is required")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("value must be valid JSON: %w", err)
	}
	if v == nil {
		return nil, errors.New("value cannot be null (use DELETE to unset a property)")
	}

	cfg := parsePropertyConfig(def.Config)
	switch def.Type {
	case "text":
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("value must be a string")
		}
		if strings.TrimSpace(s) == "" {
			return nil, errors.New("value cannot be empty (use DELETE to unset a property)")
		}
		if utf8.RuneCountInString(s) > maxPropertyTextValueLen {
			return nil, fmt.Errorf("value must be %d characters or fewer", maxPropertyTextValueLen)
		}
		return json.Marshal(sanitizeNullBytes(s))
	case "url":
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("value must be a URL string")
		}
		s = strings.TrimSpace(s)
		if len(s) > maxPropertyURLValueLen {
			return nil, fmt.Errorf("value must be %d characters or fewer", maxPropertyURLValueLen)
		}
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("value must be an http(s) URL")
		}
		return json.Marshal(s)
	case "number":
		if _, ok := v.(float64); !ok {
			return nil, errors.New("value must be a number")
		}
		return json.Marshal(v)
	case "checkbox":
		if _, ok := v.(bool); !ok {
			return nil, errors.New("value must be true or false")
		}
		return json.Marshal(v)
	case "date":
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("value must be a date string in YYYY-MM-DD format")
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return nil, errors.New("value must be a date string in YYYY-MM-DD format")
		}
		return json.Marshal(s)
	case "select":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("value must be one of the option ids: %s", selectOptionsHint(cfg))
		}
		if _, exists := propertyOptionIDs(cfg)[s]; !exists {
			return nil, fmt.Errorf("value must be one of the option ids: %s", selectOptionsHint(cfg))
		}
		return json.Marshal(s)
	case "multi_select":
		items, ok := v.([]any)
		if !ok || len(items) == 0 {
			return nil, fmt.Errorf("value must be a non-empty array of option ids: %s", selectOptionsHint(cfg))
		}
		order := propertyOptionIDs(cfg)
		seen := make(map[string]struct{}, len(items))
		ids := make([]string, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("value must be a non-empty array of option ids: %s", selectOptionsHint(cfg))
			}
			if _, exists := order[s]; !exists {
				return nil, fmt.Errorf("unknown option id %q; valid option ids: %s", s, selectOptionsHint(cfg))
			}
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			ids = append(ids, s)
		}
		// 归一化到 config 顺序：相同的选择序列化出相同结果（稳定的 @> 包含过滤
		// 与变更检测都依赖它）。
		sort.SliceStable(ids, func(a, b int) bool { return order[ids[a]] < order[ids[b]] })
		return json.Marshal(ids)
	case "actor":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("value must be an actor reference string like \"member:<uuid>\" (kinds: %s)", actorKindsHint())
		}
		ref, err := parseActorRef(s)
		if err != nil {
			return nil, err
		}
		return json.Marshal(ref.String())
	case "multi_actor":
		items, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("value must be an array of actor reference strings like \"member:<uuid>\" (kinds: %s)", actorKindsHint())
		}
		refs, err := parseActorRefList(items)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(refs))
		for i, ref := range refs {
			out[i] = ref.String()
		}
		return json.Marshal(out)
	default:
		return nil, fmt.Errorf("unsupported property type %q", def.Type)
	}
}

// removedOptionIDs 返回"库里现有 config 有、传入的替换 config 里没有"的选项 id。
func removedOptionIDs(existingConfig, nextConfig []byte) []string {
	next := propertyOptionIDs(parsePropertyConfig(nextConfig))
	var removed []string
	for _, opt := range parsePropertyConfig(existingConfig).Options {
		if _, kept := next[opt.ID]; !kept {
			removed = append(removed, opt.ID)
		}
	}
	return removed
}

// describeOptionsInUse 渲染"试图移除仍被占用的选项"时的 409 响应文案，例如
// `cannot remove options still in use: "Critical" (3 issues); clear or
// change those values first`。
func describeOptionsInUse(existingConfig []byte, rows []db.CountIssuesUsingPropertyOptionsRow) string {
	names := make(map[string]string)
	for _, opt := range parsePropertyConfig(existingConfig).Options {
		names[opt.ID] = opt.Name
	}
	parts := make([]string, len(rows))
	for i, row := range rows {
		name := names[row.OptionID]
		if name == "" {
			name = row.OptionID
		}
		parts[i] = fmt.Sprintf("%q (%d issues)", name, row.UsageCount)
	}
	sort.Strings(parts)
	return "cannot remove options still in use: " + strings.Join(parts, ", ") + "; clear or change those values first"
}

// parseIssueProperties 镜像 parseIssueMetadata 用于属性值袋：把存 JSONB 解成
// map，空/损坏兜底为空 map。
func parseIssueProperties(raw []byte) map[string]any {
	return util.JSONObjectOrEmpty(raw)
}

// ---------------------------------------------------------------------------
// 定义管理 handler
// ---------------------------------------------------------------------------

// requirePropertyAdmin 把守"改定义"的门槛：只允许人工 owner/admin 成员。
// agent 执行者在角色校验前就被拒绝——agent 继承其 runtime owner 的凭据，没有
// 这层校验，管理员的 agent 就能批量造定义（MUL-4463 决策：agent 通过评论提议，
// 由人来确认）。
func (h *Handler) requirePropertyAdmin(w http.ResponseWriter, r *http.Request) (workspaceID, userID string, ok bool) {
	workspaceID = h.resolveWorkspaceID(r)
	userID, ok = requireUserID(w, r)
	if !ok {
		return "", "", false
	}
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot manage property definitions")
		return "", "", false
	}
	if _, roleOK := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !roleOK {
		return "", "", false
	}
	return workspaceID, userID, true
}

// ListProperties 返回 workspace 的属性定义列表，按默认不带已归档项；带
// ?include_archived=true 时全部列出。需要成员身份；响应带每个定义的 usage_count。
func (h *Handler) ListProperties(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	rows, err := h.Queries.ListIssueProperties(r.Context(), db.ListIssuePropertiesParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		slog.Warn("ListIssueProperties failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list properties")
		return
	}
	resp := make([]PropertyResponse, len(rows))
	for i, row := range rows {
		resp[i] = propertyListRowToResponse(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"properties": resp, "total": len(resp)})
}

// GetProperty 返回单个属性定义（含已归档的）。找不到回 404。
func (h *Handler) GetProperty(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "property id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	property, err := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{ID: idUUID, WorkspaceID: wsUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "property not found")
			return
		}
		slog.Warn("GetIssueProperty failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get property")
		return
	}
	writeJSON(w, http.StatusOK, propertyToResponse(property, 0))
}

// CreateProperty 新建一个属性定义。需 owner/admin 人工操作。在属性 advisory
// 锁内检查"活跃定义不超过 20 个"上限，成功后发 EventPropertyCreated 事件。
// 重名回 409。
func (h *Handler) CreateProperty(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requirePropertyAdmin(w, r)
	if !ok {
		return
	}
	var req CreatePropertyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := validatePropertyName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePropertyType(req.Type); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if utf8.RuneCountInString(req.Description) > maxPropertyDescriptionLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be %d characters or fewer", maxPropertyDescriptionLen))
		return
	}
	icon, err := validatePropertyIcon(req.Icon)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	configJSON, err := validatePropertyConfig(req.Type, req.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var property db.IssueProperty
	var capErr error
	committedEvents, err := h.withPropertyLock(r, []string{"props:" + workspaceID}, func(q *db.Queries) ([]events.Event, error) {
		active, err := q.CountActiveIssueProperties(r.Context(), wsUUID)
		if err != nil {
			return nil, err
		}
		if active >= maxActivePropertiesPerWorkspace {
			capErr = fmt.Errorf("a workspace cannot have more than %d active properties; archive unused ones first", maxActivePropertiesPerWorkspace)
			return nil, capErr
		}
		property, err = q.CreateIssueProperty(r.Context(), db.CreateIssuePropertyParams{
			WorkspaceID: wsUUID,
			Name:        name,
			Type:        req.Type,
			Description: sanitizeNullBytes(strings.TrimSpace(req.Description)),
			Icon:        icon,
			Config:      configJSON,
		})
		if err != nil {
			return nil, err
		}
		return []events.Event{buildPropertyDomainEvent(protocol.EventPropertyCreated, property, "member", userID)}, nil
	})
	if capErr != nil {
		writeError(w, http.StatusBadRequest, capErr.Error())
		return
	}
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a property with that name already exists")
			return
		}
		slog.Warn("CreateIssueProperty failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create property")
		return
	}
	resp := propertyToResponse(property, 0)
	for _, event := range committedEvents {
		h.publishEvent(event)
	}
	writeJSON(w, http.StatusCreated, resp)
}

// UpdateProperty 更新一个属性定义（改名/描述/图标/选项/归档）。需 owner/admin。
// 校验、删除选项的占用普查与写库全程在 advisory 锁内执行，防止并发值写入竞态。
// 会拒绝"移除仍被 issue 引用的选项"（409）。
func (h *Handler) UpdateProperty(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requirePropertyAdmin(w, r)
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "property id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var req UpdatePropertyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 整段"读 → 校验 → 占用普查 → 写"在 advisory 锁（先 workspace 后 property）
	// 下执行：对本属性的并发值写入会排在属性锁后面，占用普查就不会与"引用了将
	// 被删选项的值"竞态（TOCTOU，clean-room 评审 F1）；workspace 锁让取消归档的
	// 容量检查相对创建是原子的（F5）。
	var property db.IssueProperty
	var httpStatus int
	var httpMsg string
	fail := func(status int, msg string) error {
		httpStatus, httpMsg = status, msg
		return errClientRejected
	}
	committedEvents, err := h.withPropertyLock(r, []string{"props:" + workspaceID, "prop:" + uuidToString(idUUID)}, func(q *db.Queries) ([]events.Event, error) {
		existing, err := q.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{ID: idUUID, WorkspaceID: wsUUID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fail(http.StatusNotFound, "property not found")
			}
			return nil, err
		}

		params := db.UpdateIssuePropertyParams{ID: idUUID, WorkspaceID: wsUUID}
		if req.Name != nil {
			name, err := validatePropertyName(*req.Name)
			if err != nil {
				return nil, fail(http.StatusBadRequest, err.Error())
			}
			params.Name = pgtype.Text{String: name, Valid: true}
		}
		if req.Description != nil {
			if utf8.RuneCountInString(*req.Description) > maxPropertyDescriptionLen {
				return nil, fail(http.StatusBadRequest, fmt.Sprintf("description must be %d characters or fewer", maxPropertyDescriptionLen))
			}
			params.Description = pgtype.Text{String: sanitizeNullBytes(strings.TrimSpace(*req.Description)), Valid: true}
		}
		if req.Icon != nil {
			icon, err := validatePropertyIcon(*req.Icon)
			if err != nil {
				return nil, fail(http.StatusBadRequest, err.Error())
			}
			params.Icon = pgtype.Text{String: icon, Valid: true}
		}
		if req.Config != nil {
			configJSON, err := validatePropertyConfig(existing.Type, req.Config)
			if err != nil {
				return nil, fail(http.StatusBadRequest, err.Error())
			}
			if removed := removedOptionIDs(existing.Config, configJSON); len(removed) > 0 {
				rows, err := q.CountIssuesUsingPropertyOptions(r.Context(), db.CountIssuesUsingPropertyOptionsParams{
					OptionIds:   removed,
					WorkspaceID: wsUUID,
					PropertyKey: uuidToString(existing.ID),
				})
				if err != nil {
					return nil, err
				}
				if len(rows) > 0 {
					return nil, fail(http.StatusConflict, describeOptionsInUse(existing.Config, rows))
				}
			}
			params.Config = configJSON
		}
		if req.Archived != nil {
			params.ArchivedSet = true
			if *req.Archived {
				params.ArchivedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
			} else if existing.ArchivedAt.Valid {
				active, err := q.CountActiveIssueProperties(r.Context(), wsUUID)
				if err != nil {
					return nil, err
				}
				if active >= maxActivePropertiesPerWorkspace {
					return nil, fail(http.StatusBadRequest, fmt.Sprintf("a workspace cannot have more than %d active properties; archive unused ones first", maxActivePropertiesPerWorkspace))
				}
			}
		}

		property, err = q.UpdateIssueProperty(r.Context(), params)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fail(http.StatusNotFound, "property not found")
			}
			if isUniqueViolation(err) {
				return nil, fail(http.StatusConflict, "a property with that name already exists")
			}
			return nil, err
		}
		return []events.Event{buildPropertyDomainEvent(protocol.EventPropertyUpdated, property, "member", userID)}, nil
	})
	if err != nil {
		if errors.Is(err, errClientRejected) {
			writeError(w, httpStatus, httpMsg)
			return
		}
		slog.Warn("UpdateProperty failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update property")
		return
	}
	resp := propertyToResponse(property, 0)
	for _, event := range committedEvents {
		h.publishEvent(event)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// issue 值 handler
// ---------------------------------------------------------------------------

// SetIssuePropertyRequest 是写入单条属性值的入参。
type SetIssuePropertyRequest struct {
	Value json.RawMessage `json:"value"`
}

// SetIssueProperty 给某个 issue 设置某条属性的值（单键原子）。调用者需能访问
// 该 issue 且已登录。在属性锁内校验定义未归档、按类型校验值，actor 类再解析
// 引用是否落在本 workspace 成员上。成功返回整袋属性与最新 issue_revision。
func (h *Handler) SetIssueProperty(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	propertyID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "propertyId"), "property id")
	if !ok {
		return
	}
	var req SetIssuePropertyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 校验与写入共享属性 advisory 锁（与定义更新同一把）：写进去的值保证能对应
	// 到并发配置编辑的占用普查所见到的定义状态（TOCTOU，clean-room 评审 F1）。
	var updated db.Issue
	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	var httpStatus int
	var httpMsg string
	fail := func(status int, msg string) error {
		httpStatus, httpMsg = status, msg
		return errClientRejected
	}
	committedEvents, err := h.withPropertyLock(r, []string{"prop:" + uuidToString(propertyID)}, func(q *db.Queries) ([]events.Event, error) {
		def, err := q.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{ID: propertyID, WorkspaceID: issue.WorkspaceID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fail(http.StatusNotFound, "property not found")
			}
			return nil, err
		}
		if def.ArchivedAt.Valid {
			return nil, fail(http.StatusBadRequest, fmt.Sprintf("property %q is archived and cannot receive new values", def.Name))
		}
		value, err := validatePropertyValue(def, req.Value)
		if err != nil {
			return nil, fail(http.StatusBadRequest, err.Error())
		}
		// actor 值指向另一个实体，只做形状校验不够：写库前要把每条引用在本
		// workspace 内解析，并拒绝调用者无权看到的引用。
		if propertyTypeIsActor(def.Type) {
			refs, err := actorRefsInValue(def.Type, value)
			if err != nil {
				return nil, fail(http.StatusBadRequest, err.Error())
			}
			if status, msg := h.resolveActorRefs(r, uuidToString(issue.WorkspaceID), refs); status != 0 {
				return nil, fail(status, msg)
			}
		}
		updated, err = q.SetIssuePropertyValue(r.Context(), db.SetIssuePropertyValueParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
			Key:         uuidToString(def.ID),
			Value:       value,
		})
		if err != nil {
			if isCheckViolation(err) {
				return nil, fail(http.StatusBadRequest, "issue properties exceed the 16KB size limit")
			}
			return nil, err
		}
		return []events.Event{buildIssuePropertiesChangedEvent(updated, actorType, actorID, parseIssueProperties(updated.Properties))}, nil
	})
	if err != nil {
		if errors.Is(err, errClientRejected) {
			writeError(w, httpStatus, httpMsg)
			return
		}
		slog.Warn("SetIssueProperty failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to set property")
		return
	}

	properties := parseIssueProperties(updated.Properties)
	for _, event := range committedEvents {
		h.publishEvent(event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"properties": properties, "issue_revision": updated.Revision})
}

// DeleteIssueProperty 清除某 issue 上某条属性的值（相当于 unset）。已归档定义
// 也允许清值——清理永不能被挡住。未知属性 id 只需属于该 workspace；
// `properties - key` 在键不存在时是无害的空操作。
func (h *Handler) DeleteIssueProperty(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	propertyID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "propertyId"), "property id")
	if !ok {
		return
	}

	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 清值对已归档定义也放行——清理动作永远不该被阻塞。未知属性 id 只需属于本
	// workspace；`properties - key` 在键不存在时是无害空操作。
	if _, err := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{ID: propertyID, WorkspaceID: issue.WorkspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "property not found")
			return
		}
		slog.Warn("GetIssueProperty in DeleteIssueProperty failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to unset property")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start property transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.DeleteIssuePropertyValue(r.Context(), db.DeleteIssuePropertyValueParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         uuidToString(propertyID),
	})
	if err != nil {
		slog.Warn("DeleteIssuePropertyValue failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to unset property")
		return
	}

	workspaceID := uuidToString(updated.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	properties := parseIssueProperties(updated.Properties)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildIssuePropertiesChangedEvent(updated, actorType, actorID, properties))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record property event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit property change")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, map[string]any{"properties": properties, "issue_revision": updated.Revision})
}

// withPropertyLock 在一个持有指定 advisory 锁的事务里跑 fn，从而把"定义的变更"
// 与"值的写入"串行化（TOCTOU：一次配置更新的占用普查与并发值写入若交错，会留下
// 永久悬空的选项引用），也让"创建/取消归档"彼此串行（20 个活跃上限与
// MAX(position)+1 都是先读后写）。锁随事务作用域释放。
func (h *Handler) withPropertyLock(r *http.Request, lockKeys []string, fn func(q *db.Queries) ([]events.Event, error)) ([]events.Event, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(r.Context())
	// 调用方按固定的全局顺序传键（workspace 在 property 之前），重叠的锁集合
	// 才不会互相死锁。
	for _, key := range lockKeys {
		if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
			return nil, err
		}
	}
	committedEvents, err := fn(h.Queries.WithTx(tx))
	if err != nil {
		return nil, err
	}
	for i, event := range committedEvents {
		persisted, err := service.RecordDurableEventTx(r.Context(), h.Queries.WithTx(tx), event)
		if err != nil {
			return nil, err
		}
		committedEvents[i] = persisted
	}
	if err := tx.Commit(r.Context()); err != nil {
		return nil, err
	}
	return committedEvents, nil
}

// ---------------------------------------------------------------------------
// 列表接口支持：properties 过滤条件 + 属性排序表达式
// ---------------------------------------------------------------------------

const (
	maxPropertiesFilterDefinitions = 20
	maxPropertiesFilterValues      = 50
	// noPropertyValue 是代表"未设置"的过滤值——它编译成"键不存在"谓词而非 jsonb
	// 包含模式。该串不会与真实选项 id 撞车（select 选项 id 是 UUID，checkbox 用
	// "true"/"false"）。
	noPropertyValue = "__none__"
	// operatorPatternKey 标记一条已编译的"操作符"分支（见
	// parsePropertiesFilterParam）。它和它兄弟键都不长成 UUID 的样子，所以该
	// 标记永远不可能与"单个键就是定义 id"的包含模式撞车。
	operatorPatternKey = "__op__"
)

// propertyFilterOperator 是 properties 过滤器里一条结构化成员——
// {"op": "contains", "value": "foo"}——与表示精确相等的普通字符串成员并列。
// 操作符语义：
//
//   - contains：对值文本形态（text/url）做大小写不敏感的子串匹配。
//   - gt / gte / lt / lte：数值比较，只匹配存成 jsonb 数字的值（number）。
//   - before / after：对存储串做字典序比较；date 属性存的是 "YYYY-MM-DD" 纯日期
//     串，字典序即时间序（date）。
type propertyFilterOperator struct {
	Op    string `json:"op"`
	Value string `json:"value"`
}

// propertyOperatorPattern 是编译后的操作符分支：JSON 对象
// {"__op__": "<op>", "def": "<definitionId>", "value": "<value>"}，properties
// FilterPredicate 与静态 ListOpenIssues 的展开都能识别。`contains` 的值在解析时
// 已做 ILIKE 转义，两个消费方都能直接拼进模式。
type propertyOperatorPattern struct {
	Op    string `json:"__op__"`
	Def   string `json:"def"`
	Value string `json:"value"`
}

// propertyFilterOps 把操作符名映射成 SQL 比较符；空串给 contains 占位。
var propertyFilterOps = map[string]string{
	"contains": "",
	"gt":       ">",
	"gte":      ">=",
	"lt":       "<",
	"lte":      "<=",
	"before":   "<",
	"after":    ">",
}

// escapeLikePattern 在默认反斜杠转义符下，为字面子串匹配转义 SQL LIKE 通配符。
func escapeLikePattern(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

// validatePropertyFilterOperator 校验一条操作符成员并返回编译后的模式。值的
// 规则沿用旧的字符串规则：非空、有长度上限、只接受可比较的形态（数值操作符
// 要浮点数，before/after 要真的纯日期串）。
func validatePropertyFilterOperator(definitionID string, op propertyFilterOperator) (propertyOperatorPattern, error) {
	if _, known := propertyFilterOps[op.Op]; !known {
		return propertyOperatorPattern{}, fmt.Errorf("properties filter op %q is not supported", op.Op)
	}
	if op.Value == "" {
		return propertyOperatorPattern{}, errors.New("properties filter operator values cannot be empty")
	}
	pattern := propertyOperatorPattern{Op: op.Op, Def: definitionID, Value: op.Value}
	switch op.Op {
	case "contains":
		if utf8.RuneCountInString(op.Value) > maxPropertyTextValueLen {
			return propertyOperatorPattern{}, fmt.Errorf("properties filter value must be %d characters or fewer", maxPropertyTextValueLen)
		}
		pattern.Value = escapeLikePattern(op.Value)
	case "gt", "gte", "lt", "lte":
		num, err := strconv.ParseFloat(op.Value, 64)
		if err != nil || math.IsNaN(num) || math.IsInf(num, 0) {
			return propertyOperatorPattern{}, fmt.Errorf("properties filter op %q requires a finite number", op.Op)
		}
		// 存库前规范化为普通十进制：ParseFloat 接受 Postgres ::numeric 不接受
		// 的写法（"0x1p4" 十六进制浮点、旧版 PG 上带下划线的 "1_000"），而静态的
		// open_only 展开会在 SQL 内部转这个精确的字符串——未规范化的值会让那条
		// 路径 500，尽管动态路径（绑定已解析的浮点）能成功。
		pattern.Value = strconv.FormatFloat(num, 'f', -1, 64)
	case "before", "after":
		if _, err := time.Parse("2006-01-02", op.Value); err != nil {
			return propertyOperatorPattern{}, fmt.Errorf("properties filter op %q requires a YYYY-MM-DD date", op.Op)
		}
	}
	return pattern, nil
}

// parsePropertiesFilterParam 读 `properties` 查询参数——一个
// {<definitionId>: [<value>, ...]} 的 JSON 对象——并把它编译成"OR 组"：
// 同一定义内是 OR，不同定义之间是 AND。
//
// 字符串成员表示相等；它展开成所有可能命中的包含形态（选项 id 串、数组元素、
// 布尔、jsonb 数字），不可能命中的形态只是永不满足而已。select/multi_select 的
// 值是选项 id，checkbox 是 "true"/"false"。
//
// 对象成员 {"op", "value"} 是标量操作符（见 propertyFilterOperator），编译成
// 一条操作符模式。
//
// 特殊值 noPropertyValue（"__none__"）表示"未设置"：它产出标记对象
// {"__none__": "<definitionId>"}，parseNoPropertyValuePattern 与静态
// ListOpenIssues 的展开都把它识别为"键不存在"检查。
//
// 参数为空时返回 (nil, true)。
func parsePropertiesFilterParam(w http.ResponseWriter, raw string) ([][]json.RawMessage, bool) {
	if raw == "" {
		return nil, true
	}
	var parsed map[string][]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		writeError(w, http.StatusBadRequest, "properties filter must be a JSON object of {definitionId: [values]}")
		return nil, false
	}
	if len(parsed) > maxPropertiesFilterDefinitions {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("properties filter cannot cover more than %d definitions", maxPropertiesFilterDefinitions))
		return nil, false
	}
	groups := make([][]json.RawMessage, 0, len(parsed))
	totalAlternatives := 0
	for definitionID, values := range parsed {
		if _, err := uuid.Parse(definitionID); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("properties filter key %q is not a definition id", definitionID))
			return nil, false
		}
		if len(values) == 0 {
			continue
		}
		if len(values) > maxPropertiesFilterValues {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("properties filter for %s cannot list more than %d values", definitionID, maxPropertiesFilterValues))
			return nil, false
		}
		alternatives := make([]json.RawMessage, 0, len(values)*3)
		appendAlt := func(v any) bool {
			buf, err := json.Marshal(map[string]any{definitionID: v})
			if err != nil {
				writeError(w, http.StatusBadRequest, "properties filter is invalid")
				return false
			}
			alternatives = append(alternatives, buf)
			return true
		}
		hasNoValue := false
		for _, rawValue := range values {
			// 普通字符串成员保持旧的相等语义。
			var stringValue string
			if err := json.Unmarshal(rawValue, &stringValue); err == nil {
				value := stringValue
				if value == "" {
					writeError(w, http.StatusBadRequest, "properties filter values cannot be empty")
					return nil, false
				}
				if value == noPropertyValue {
					if hasNoValue {
						continue
					}
					marker, err := json.Marshal(map[string]string{noPropertyValue: definitionID})
					if err != nil {
						writeError(w, http.StatusBadRequest, "properties filter is invalid")
						return nil, false
					}
					alternatives = append(alternatives, marker)
					hasNoValue = true
					continue
				}
				if !appendAlt(value) || !appendAlt([]string{value}) { // select 字符串 / multi_select 元素
					return nil, false
				}
				if value == "true" || value == "false" {
					if !appendAlt(value == "true") { // checkbox 布尔
						return nil, false
					}
				}
				if num, err := strconv.ParseFloat(value, 64); err == nil &&
					!math.IsNaN(num) && !math.IsInf(num, 0) {
					// number 属性的标量：数字过滤值必须匹配存储的 jsonb 数字，
					// 而不是上面追加的字符串形态。NaN / Infinity 直接跳过：它们
					// 不能表示成 JSON，marshal 会 400 掉整条过滤。
					if !appendAlt(num) {
						return nil, false
					}
				}
				continue
			}
			// 对象成员是标量操作符。
			var op propertyFilterOperator
			if err := json.Unmarshal(rawValue, &op); err != nil || op.Op == "" {
				writeError(w, http.StatusBadRequest, "properties filter values must be strings or {op, value} objects")
				return nil, false
			}
			pattern, err := validatePropertyFilterOperator(definitionID, op)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return nil, false
			}
			buf, marshalErr := json.Marshal(pattern)
			if marshalErr != nil {
				writeError(w, http.StatusBadRequest, "properties filter is invalid")
				return nil, false
			}
			alternatives = append(alternatives, buf)
		}
		totalAlternatives += len(alternatives)
		groups = append(groups, alternatives)
	}
	if len(groups) == 0 {
		return nil, true
	}
	// 限制 OR 扇出：每个分支都会变成下方 SQL 里的一个绑定参数，失控的过滤器会
	// 把语句撑爆。
	if totalAlternatives > 256 {
		writeError(w, http.StatusBadRequest, "properties filter is too large")
		return nil, false
	}
	return groups, true
}

// parseNoPropertyValuePattern 判断某条分支是否为合成的"无值"标记——jsonb 对象
// {"__none__": "<definitionId>"}——若是则返回谓词必须测试"键不存在"的那个定义
// 的 id。
func parseNoPropertyValuePattern(alt json.RawMessage) (string, bool) {
	var marker map[string]string
	if err := json.Unmarshal(alt, &marker); err != nil {
		return "", false
	}
	defID, ok := marker[noPropertyValue]
	return defID, ok
}

// parseOperatorPattern 判断某条分支是否为编译好的标量操作符（见
// parsePropertiesFilterParam）并拆出各部分。包含模式永远不带 "__op__" 键，
// 两种形态不会混淆。
func parseOperatorPattern(alt json.RawMessage) (propertyOperatorPattern, bool) {
	var marker map[string]string
	if err := json.Unmarshal(alt, &marker); err != nil {
		return propertyOperatorPattern{}, false
	}
	op, hasOp := marker[operatorPatternKey]
	def, hasDef := marker["def"]
	if !hasOp || !hasDef {
		return propertyOperatorPattern{}, false
	}
	return propertyOperatorPattern{Op: op, Def: def, Value: marker["value"]}, true
}

// operatorPatternPredicate 把一条操作符分支渲染成 SQL。操作符在解析期已校验过，
// 下面的重解析对编译产物不会失败；畸形模式退化成 FALSE 而不是匹配上。
func operatorPatternPredicate(pattern propertyOperatorPattern, addArg func(any) string) string {
	defArg := addArg(pattern.Def)
	switch pattern.Op {
	case "contains":
		// 值在解析期已做 ILIKE 转义。子串匹配限定在存储的字符串上：->> 也会把
		// 数字、布尔、数组序列化成文本，而客户端匹配器刻意把 contains 当成
		// text/url 操作符。
		return fmt.Sprintf("(jsonb_typeof(i.properties -> %s) = 'string' AND (i.properties ->> %s) ILIKE '%%' || %s || '%%')",
			defArg, defArg, addArg(pattern.Value))
	case "gt", "gte", "lt", "lte":
		// 在每条服务路径上把规范化十进制绑定成 numeric。用 CASE 让 jsonb 类型
		// 守卫是结构性的，而不是依赖 cast 前 SQL 限定符的求值顺序。
		num, err := strconv.ParseFloat(pattern.Value, 64)
		if err != nil || math.IsNaN(num) || math.IsInf(num, 0) {
			return "FALSE"
		}
		canonical := strconv.FormatFloat(num, 'f', -1, 64)
		return fmt.Sprintf("(CASE WHEN jsonb_typeof(i.properties -> %s) = 'number' THEN (i.properties ->> %s)::numeric END %s %s::numeric)",
			defArg, defArg, propertyFilterOps[pattern.Op], addArg(canonical))
	case "before", "after":
		return fmt.Sprintf("(jsonb_typeof(i.properties -> %s) = 'string' AND i.properties ->> %s %s %s)",
			defArg, defArg, propertyFilterOps[pattern.Op], addArg(pattern.Value))
	default:
		return "FALSE"
	}
}

// propertiesFilterPredicate 渲染 AND-of-ORs 的过滤检查，每个分支一个绑定参数。
// 相等分支是朴素的 `i.properties @> $n` 包含析取——操作数恒定正是让规划器能走
// jsonb_path_ops GIN 索引的前提（相关的 jsonb_array_elements 形态会废掉它——
// 评审里用 EXPLAIN 验证过）。
//
// "无值"标记分支渲染成键不存在的析取——`NOT (i.properties ? $m)`——用不上 GIN
// 索引，但对"未设置"状态是精确的（属性值永不为 null；DELETE 才是 unset）。
// 操作符分支渲染成各自的类型化比较（ILIKE / 数值 / 日期串），同样不走索引：
// 标量范围本质就无法使用包含索引。
func propertiesFilterPredicate(groups [][]json.RawMessage, addArg func(any) string) string {
	groupSQL := make([]string, 0, len(groups))
	for _, alternatives := range groups {
		ors := make([]string, 0, len(alternatives))
		for _, alt := range alternatives {
			if defID, ok := parseNoPropertyValuePattern(alt); ok {
				ors = append(ors, fmt.Sprintf("NOT (i.properties ? %s)", addArg(defID)))
				continue
			}
			if pattern, ok := parseOperatorPattern(alt); ok {
				ors = append(ors, operatorPatternPredicate(pattern, addArg))
				continue
			}
			ors = append(ors, fmt.Sprintf("i.properties @> %s::jsonb", addArg(string(alt))))
		}
		groupSQL = append(groupSQL, "("+strings.Join(ors, " OR ")+")")
	}
	return "(" + strings.Join(groupSQL, " AND ") + ")"
}

// propertySortExpr 把一个 `property:<definitionId>` 排序值解析成 SQL ORDER BY
// 表达式。sortValue 不是 property 形态时返回 handled=false（调用方回落到自己的
// 静态白名单）。畸形 id 写 400（ok=false）。未知/已归档的定义，或没有有意义的
// 序的类型，退化成空表达式——调用方保持 position 顺序，与前端"陈旧持久化排序"
// 的回退行为一致，而不是用 400 弄坏已装客户端。
func (h *Handler) propertySortExpr(r *http.Request, workspaceID string, sortValue string) (expr string, handled bool, err error) {
	const prefix = "property:"
	if !strings.HasPrefix(sortValue, prefix) {
		return "", false, nil
	}
	rawID := strings.TrimPrefix(sortValue, prefix)
	parsedID, parseErr := uuid.Parse(rawID)
	if parseErr != nil {
		return "", true, errors.New("invalid sort value")
	}
	wsUUID, wsErr := util.ParseUUID(workspaceID)
	if wsErr != nil {
		return "", true, errors.New("invalid workspace id")
	}
	var defUUID pgtype.UUID
	copy(defUUID.Bytes[:], parsedID[:])
	defUUID.Valid = true
	def, dbErr := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{ID: defUUID, WorkspaceID: wsUUID})
	if dbErr != nil {
		if errors.Is(dbErr, pgx.ErrNoRows) {
			return "", true, nil // stale sort → position order
		}
		return "", true, fmt.Errorf("resolve sort property: %w", dbErr)
	}
	// 已归档定义与未知定义一样退化为 position 顺序——它们的值已从 UI 隐藏，
	// 按它们排序等于按不可见数据排。
	if def.ArchivedAt.Valid {
		return "", true, nil
	}
	// uuidToString 会把解析出的 UUID 重新序列化：只含十六进制与横线，可以安全
	// 内嵌进 ORDER BY 字符串。
	id := uuidToString(def.ID)
	switch def.Type {
	case "number":
		return fmt.Sprintf("CASE WHEN jsonb_typeof(i.properties->'%s') = 'number' THEN (i.properties->>'%s')::numeric END", id, id), true, nil
	case "date", "text", "url", "select":
		return fmt.Sprintf("NULLIF(i.properties->>'%s', '')", id), true, nil
	default: // multi_select、checkbox 及未来类型：没有有意义的序
		return "", true, nil
	}
}
