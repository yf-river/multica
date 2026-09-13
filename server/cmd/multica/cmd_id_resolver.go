// 文件职责：实现 CLI 的「ID 引用解析」公共逻辑——把用户在命令行里给的各种
// 引用（完整 UUID、8 位短前缀、issue 的人类可读 key MUL-123）解析成规范的
// resolvedID。issue 只认 key 与完整 UUID（见 resolveIssueRef 里的历史说明 GH #4701）；
// 其余无人类可读 key 的资源（autopilot/project/label/task run 等）支持短前缀，
// 通过按实体 fetch 候选列表 + resolveIDByPrefix 统一处理。文件末尾的
// actorDisplayLookup 负责把 member/agent/squad 的 UUID 惰性渲染成可读名字，供各
// 命令表格输出共用。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

// minShortIDPrefixLen 是接受短 ID 前缀的最短长度（4 个十六进制字符）。
const minShortIDPrefixLen = 4

// resolverListPageLimit 是候选列表翻页抓取时每页上限（复用 server 允许的页码粒度）。
const resolverListPageLimit = 50

// resolvedID 是一次 ID 解析的成果：ID 是写进请求的规范 UUID。
type resolvedID struct {
	ID string
	// Display 是给人看的尽力而为标签；当解析没从 server 拉展示元数据时它等于 ID。
	Display string
}

// idCandidate 是短前缀解析里的一个候选实体（含展示名与明细，用于歧义报错）。
type idCandidate struct {
	ID      string
	Display string
	Detail  string
}

// displayID 按 --full-id 决定表格里显示完整 ID 还是截短的 ID。
func displayID(id string, full bool) string {
	if full {
		return id
	}
	return truncateID(id)
}

// issueDisplayKey 返回 issue 的人类可读 key（identifier，如 MUL-123）；响应缺
// identifier 时回退到 UUID 并打警告。
func issueDisplayKey(issue map[string]any) string {
	if key := strVal(issue, "identifier"); key != "" {
		return key
	}
	id := strVal(issue, "id")
	if id != "" {
		slog.Warn("issue response missing identifier", "issue_id", id)
	}
	return id
}

// issueCandidate 把 issue 响应包成 idCandidate（Display=identifier，Detail=标题）。
func issueCandidate(issue map[string]any) idCandidate {
	return idCandidate{
		ID:      strVal(issue, "id"),
		Display: issueDisplayKey(issue),
		Detail:  strVal(issue, "title"),
	}
}

// normalizeUUIDPrefix 把输入规整成用于前缀匹配的小写紧凑十六进制串（去横杠、
// 转小写），并校验长度 ≥ minShortIDPrefixLen、纯十六进制。
func normalizeUUIDPrefix(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", fmt.Errorf("id is required")
	}
	prefix := strings.ToLower(strings.ReplaceAll(trimmed, "-", ""))
	if len(prefix) < minShortIDPrefixLen {
		return "", fmt.Errorf("expected a full UUID or at least %d hex characters, got %q", minShortIDPrefixLen, input)
	}
	for _, r := range prefix {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", fmt.Errorf("expected a UUID prefix containing only hex characters, got %q", input)
		}
	}
	return prefix, nil
}

// compactUUID 把 UUID 规整成小写无横杠形态，供前缀比较统一使用。
func compactUUID(id string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
}

// resolveIDByPrefix 是短前缀解析的通用实现：完整 UUID 直接自识别返回；否则把输入
// 当十六进制前缀，拉取全部候选，用紧凑 UUID 前缀匹配，恰好一个命中才返回，多个
// 命中报歧义。fetch 负责按实体类型取候选列表。
func resolveIDByPrefix(ctx context.Context, client *cli.APIClient, kind, input string, fetch func(context.Context, *cli.APIClient) ([]idCandidate, error)) (resolvedID, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return resolvedID{}, fmt.Errorf("%s id is required", kind)
	}
	if uuidRegexp.MatchString(trimmed) {
		return resolvedID{ID: trimmed, Display: trimmed}, nil
	}

	prefix, err := normalizeUUIDPrefix(trimmed)
	if err != nil {
		return resolvedID{}, fmt.Errorf("resolve %s: %w", kind, err)
	}

	candidates, err := fetch(ctx, client)
	if err != nil {
		return resolvedID{}, fmt.Errorf("resolve %s: %w", kind, err)
	}

	matches := make([]idCandidate, 0, 1)
	for _, c := range candidates {
		if c.ID == "" {
			continue
		}
		if strings.HasPrefix(compactUUID(c.ID), prefix) {
			matches = append(matches, c)
		}
	}

	switch len(matches) {
	case 0:
		return resolvedID{}, fmt.Errorf("no %s found matching id prefix %q; run the list command with --full-id to copy the full UUID", kind, input)
	case 1:
		display := matches[0].Display
		if display == "" {
			display = matches[0].ID
		}
		return resolvedID{ID: matches[0].ID, Display: display}, nil
	default:
		return resolvedID{}, ambiguousIDPrefixError(kind, input, matches)
	}
}

// ambiguousIDPrefixError 把多个前缀命中排成可读的歧义报错（按 ID 排序、列出全部
// 匹配 UUID，提示多给几位或改用 --full-id）。
func ambiguousIDPrefixError(kind, input string, matches []idCandidate) error {
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ID < matches[j].ID
	})
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, "  "+m.ID)
	}
	return fmt.Errorf("ambiguous %s id prefix %q; matches:\n%s\nUse more characters or run the list command with --full-id", kind, input, strings.Join(parts, "\n"))
}

// resolveIssueRef 只接受两种 issue 规范引用：
//
//   - 人类可读的 issue key，如 "MUL-1852"（由 looksLikeIssueIdentifier 校验、
//     服务端解析）；
//   - 虚线规范形态的完整 UUID（由 uuidRegexp 校验）。
//
// 短 UUID 前缀（如 "1881abcd"）一度支持但已移除——在大工作区里 CLI 得在客户端
// 翻完整个 issue 列表来消歧，造成 14–35s 的超时（GH #4701）。既然 `MUL-123` 已
// 覆盖 issue 引用的所有人肉使用场景，短前缀路径被直接删除而非挪到服务端。没有
// 人类可读 key 的其它资源（autopilot、project、label、task run、workspace……）
// 仍接受短 UUID 前缀，见 resolveIDByPrefix。
func resolveIssueRef(ctx context.Context, client *cli.APIClient, input string) (resolvedID, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return resolvedID{}, fmt.Errorf("issue id is required")
	}

	if looksLikeIssueIdentifier(trimmed) {
		return fetchIssueRef(ctx, client, trimmed)
	}
	if uuidRegexp.MatchString(trimmed) {
		// 完整 UUID 自识别即可：解析器为此 GET 一次纯属往返、毫无信息增益，而 agent
		// 在引导阶段每轮要付好几次这个代价（GH #7017）。统一转小写成 server 输出的
		// 规范形态。没有 issue 响应的消息可能显示此 UUID 而非 key；失败会由命令下一
		// 个面向 issue 的端点暴露。所有写路径都在服务端二次校验（如 create 的
		// parent_issue_id），不会仅凭这个 ref 存下什么。
		id := strings.ToLower(trimmed)
		return resolvedID{ID: id, Display: id}, nil
	}

	// 识别常见的「复制了截断 UUID」场景并给出针对性提示。normalizeUUIDPrefix 对
	// 任意 ≥4 位十六进制（去横杠后）的输入都能通过，正好覆盖旧解析器当年
	// 当作前缀接受的范围。
	if _, err := normalizeUUIDPrefix(trimmed); err == nil {
		return resolvedID{}, fmt.Errorf(
			"issue ref %q looks like a short UUID prefix; short prefixes are no longer supported for issues. "+
				"Use the issue key (e.g. MUL-123) shown by `multica issue list`, or pass the full UUID (run a list command with --full-id to copy it)",
			input,
		)
	}
	return resolvedID{}, fmt.Errorf(
		"issue ref %q is not a recognized issue reference; use the issue key (e.g. MUL-123) shown by `multica issue list`, or pass the full UUID",
		input,
	)
}

// fetchIssueRef 按 key 向 server 解析 issue：GET /api/issues/{key} 拿回真实 UUID 与
// identifier。
func fetchIssueRef(ctx context.Context, client *cli.APIClient, ref string) (resolvedID, error) {
	var issue map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(ref), &issue); err != nil {
		return resolvedID{}, err
	}
	c := issueCandidate(issue)
	if c.Display == "" {
		c.Display = c.ID
	}
	return resolvedID{ID: c.ID, Display: c.Display}, nil
}

// looksLikeIssueIdentifier 粗略判断输入是否形如 "MUL-1852"（横杠前是字母数字前缀、
// 横杠后是正整数）。仅作形态初筛，真实解析走 fetchIssueRef。
func looksLikeIssueIdentifier(input string) bool {
	if input == "" {
		return false
	}
	dash := strings.LastIndex(input, "-")
	if dash <= 0 || dash >= len(input)-1 {
		return false
	}
	prefix := input[:dash]
	for _, r := range prefix {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	_, ok := parsePositiveInt(input[dash+1:])
	return ok
}

// parsePositiveInt 解析严格正整数字符串，失败或非正返回 ok=false。
func parsePositiveInt(input string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// resolveAutopilotID 按完整 UUID 或短前缀解析 autopilot。
func resolveAutopilotID(ctx context.Context, client *cli.APIClient, input string) (resolvedID, error) {
	return resolveIDByPrefix(ctx, client, "autopilot", input, fetchAutopilotCandidates)
}

// fetchAutopilotCandidates 分页拉取工作区全部 autopilot 作为前缀候选，按 id 去重。
func fetchAutopilotCandidates(ctx context.Context, client *cli.APIClient) ([]idCandidate, error) {
	if client.WorkspaceID == "" {
		return nil, fmt.Errorf("workspace_id is required to resolve autopilot id prefixes")
	}
	const limit = resolverListPageLimit
	candidates := []idCandidate{}
	seen := map[string]struct{}{}
	for offset := 0; ; {
		params := url.Values{}
		params.Set("workspace_id", client.WorkspaceID)
		params.Set("limit", strconv.Itoa(limit))
		if offset > 0 {
			params.Set("offset", strconv.Itoa(offset))
		}
		var resp struct {
			Autopilots []map[string]any `json:"autopilots"`
			Total      int              `json:"total"`
			HasMore    bool             `json:"has_more"`
		}
		if err := client.GetJSON(ctx, "/api/autopilots?"+params.Encode(), &resp); err != nil {
			return nil, err
		}
		added := 0
		for _, a := range resp.Autopilots {
			id := strVal(a, "id")
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			added++
			candidates = append(candidates, idCandidate{
				ID:      id,
				Display: strVal(a, "title"),
				Detail:  strVal(a, "status"),
			})
		}
		pageLen := len(resp.Autopilots)
		offset += pageLen
		if pageLen == 0 || added == 0 {
			break
		}
		if pageLen < limit {
			break
		}
		if resp.HasMore {
			continue
		}
		if resp.Total > 0 {
			if offset >= resp.Total {
				break
			}
			continue
		}
	}
	return candidates, nil
}

// resolveTaskRunID 解析 task run 引用：完整 UUID 直接通过；短前缀要求必须带
// --issue 限定范围——task run 只存在于某 issue 之下，不限范围无法消歧。
func resolveTaskRunID(ctx context.Context, client *cli.APIClient, issueID, input string) (resolvedID, error) {
	trimmed := strings.TrimSpace(input)
	if uuidRegexp.MatchString(trimmed) {
		return resolvedID{ID: trimmed, Display: trimmed}, nil
	}
	if strings.TrimSpace(issueID) == "" {
		return resolvedID{}, fmt.Errorf("short task run prefixes require --issue <issue-id>; pass a full task UUID or run `multica issue runs <issue-id> --full-id`")
	}
	fetch := func(ctx context.Context, client *cli.APIClient) ([]idCandidate, error) {
		return fetchTaskRunCandidatesForIssue(ctx, client, issueID)
	}
	return resolveIDByPrefix(ctx, client, "task run", input, fetch)
}

// fetchTaskRunCandidatesForIssue 拉取某 issue 的全部 task run 作为前缀候选。
func fetchTaskRunCandidatesForIssue(ctx context.Context, client *cli.APIClient, issueID string) ([]idCandidate, error) {
	var runs []map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueID)+"/task-runs", &runs); err != nil {
		return nil, err
	}
	candidates := make([]idCandidate, 0, len(runs))
	for _, r := range runs {
		id := strVal(r, "id")
		if id == "" {
			continue
		}
		candidates = append(candidates, idCandidate{
			ID:      id,
			Display: id,
		})
	}
	return candidates, nil
}

// resolveAutopilotTriggerID 在给定 autopilot 下解析其 trigger 引用（短前缀候选即
// 该 autopilot 的全部 trigger）。
func resolveAutopilotTriggerID(ctx context.Context, client *cli.APIClient, autopilotID, input string) (resolvedID, error) {
	trimmed := strings.TrimSpace(input)
	if uuidRegexp.MatchString(trimmed) {
		return resolvedID{ID: trimmed, Display: trimmed}, nil
	}
	fetch := func(ctx context.Context, client *cli.APIClient) ([]idCandidate, error) {
		var resp map[string]any
		if err := client.GetJSON(ctx, "/api/autopilots/"+url.PathEscape(autopilotID), &resp); err != nil {
			return nil, err
		}
		triggersRaw, _ := resp["triggers"].([]any)
		candidates := make([]idCandidate, 0, len(triggersRaw))
		for _, raw := range triggersRaw {
			t, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			detail := strVal(t, "kind")
			if label := strVal(t, "label"); label != "" {
				detail = label
			}
			candidates = append(candidates, idCandidate{
				ID:      strVal(t, "id"),
				Display: strVal(t, "id"),
				Detail:  detail,
			})
		}
		return candidates, nil
	}
	return resolveIDByPrefix(ctx, client, "autopilot trigger", input, fetch)
}

// resolveProjectID 按完整 UUID 或短前缀解析 project。
func resolveProjectID(ctx context.Context, client *cli.APIClient, input string) (resolvedID, error) {
	return resolveIDByPrefix(ctx, client, "project", input, fetchProjectCandidates)
}

// fetchProjectCandidates 拉取工作区全部 project 作为前缀候选。
func fetchProjectCandidates(ctx context.Context, client *cli.APIClient) ([]idCandidate, error) {
	if client.WorkspaceID == "" {
		return nil, fmt.Errorf("workspace_id is required to resolve project id prefixes")
	}
	params := url.Values{"workspace_id": {client.WorkspaceID}}
	var result map[string]any
	if err := client.GetJSON(ctx, "/api/projects?"+params.Encode(), &result); err != nil {
		return nil, err
	}
	projectsRaw, _ := result["projects"].([]any)
	candidates := make([]idCandidate, 0, len(projectsRaw))
	for _, raw := range projectsRaw {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		candidates = append(candidates, idCandidate{
			ID:      strVal(p, "id"),
			Display: strVal(p, "title"),
			Detail:  strVal(p, "status"),
		})
	}
	return candidates, nil
}

// resolveProjectResourceID 在给定 project 下解析其 resource 引用（短前缀候选为
// 该项目全部资源）。
func resolveProjectResourceID(ctx context.Context, client *cli.APIClient, projectID, input string) (resolvedID, error) {
	fetch := func(ctx context.Context, client *cli.APIClient) ([]idCandidate, error) {
		var result map[string]any
		if err := client.GetJSON(ctx, "/api/projects/"+url.PathEscape(projectID)+"/resources", &result); err != nil {
			return nil, err
		}
		resourcesRaw, _ := result["resources"].([]any)
		candidates := make([]idCandidate, 0, len(resourcesRaw))
		for _, raw := range resourcesRaw {
			r, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			display := strVal(r, "label")
			if display == "" {
				display = strVal(r, "resource_type")
			}
			candidates = append(candidates, idCandidate{
				ID:      strVal(r, "id"),
				Display: display,
				Detail:  summarizeResourceRef(r["resource_ref"]),
			})
		}
		return candidates, nil
	}
	return resolveIDByPrefix(ctx, client, "project resource", input, fetch)
}

// resolveLabelID 按完整 UUID 或短前缀解析 label。
func resolveLabelID(ctx context.Context, client *cli.APIClient, input string) (resolvedID, error) {
	return resolveIDByPrefix(ctx, client, "label", input, fetchLabelCandidates)
}

// fetchLabelCandidates 拉取工作区全部 label 作为前缀候选。
func fetchLabelCandidates(ctx context.Context, client *cli.APIClient) ([]idCandidate, error) {
	if client.WorkspaceID == "" {
		return nil, fmt.Errorf("workspace_id is required to resolve label id prefixes")
	}
	params := url.Values{"workspace_id": {client.WorkspaceID}}
	var result map[string]any
	if err := client.GetJSON(ctx, "/api/labels?"+params.Encode(), &result); err != nil {
		return nil, err
	}
	labelsRaw, _ := result["labels"].([]any)
	candidates := make([]idCandidate, 0, len(labelsRaw))
	for _, raw := range labelsRaw {
		l, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		candidates = append(candidates, idCandidate{
			ID:      strVal(l, "id"),
			Display: strVal(l, "name"),
			Detail:  strVal(l, "color"),
		})
	}
	return candidates, nil
}

// actorDisplayLookup 是 member/agent/squad 的惰性名字查找表：首用某一类时一次性
// 拉取该类全部实体，之后表格多行渲染无需重复请求。struct 按值传递共享同一 state
// 指针，避免重复加载。
type actorDisplayLookup struct {
	ctx    context.Context
	client *cli.APIClient
	state  *actorDisplayLookupState
}

// actorDisplayLookupState 保存已加载的实体名映射与各自的加载标记（防止重复拉取）。
type actorDisplayLookupState struct {
	members       map[string]string
	agents        map[string]string
	squads        map[string]string
	membersLoaded bool
	agentsLoaded  bool
	squadsLoaded  bool
}

// loadActorDisplayLookup 创建一张空查找表（实体在首次使用时才加载）。
func loadActorDisplayLookup(ctx context.Context, client *cli.APIClient) actorDisplayLookup {
	return actorDisplayLookup{
		ctx:    ctx,
		client: client,
		state:  &actorDisplayLookupState{},
	}
}

// loadMembers 加载工作区 member 的 user_id → name 映射（只加载一次，失败静默，
// 让调用方回退到 type:id 显示）。
func (l actorDisplayLookup) loadMembers() {
	if l.state == nil || l.state.membersLoaded {
		return
	}
	l.state.membersLoaded = true
	l.state.members = map[string]string{}
	if l.client == nil || l.client.WorkspaceID == "" {
		return
	}
	var members []map[string]any
	if err := l.client.GetJSON(l.ctx, "/api/workspaces/"+url.PathEscape(l.client.WorkspaceID)+"/members", &members); err == nil {
		for _, m := range members {
			if id := strVal(m, "user_id"); id != "" {
				l.state.members[id] = strVal(m, "name")
			}
		}
	}
}

// loadAgents 加载工作区 agent 的 id → name 映射（只加载一次，失败静默）。
func (l actorDisplayLookup) loadAgents() {
	if l.state == nil || l.state.agentsLoaded {
		return
	}
	l.state.agentsLoaded = true
	l.state.agents = map[string]string{}
	if l.client == nil || l.client.WorkspaceID == "" {
		return
	}
	var agents []map[string]any
	agentPath := "/api/agents?" + url.Values{"workspace_id": {l.client.WorkspaceID}}.Encode()
	if err := l.client.GetJSON(l.ctx, agentPath, &agents); err == nil {
		for _, a := range agents {
			if id := strVal(a, "id"); id != "" {
				l.state.agents[id] = strVal(a, "name")
			}
		}
	}
}

// loadSquads 加载 squad 的 id → name 映射（只加载一次，失败静默）。
func (l actorDisplayLookup) loadSquads() {
	if l.state == nil || l.state.squadsLoaded {
		return
	}
	l.state.squadsLoaded = true
	l.state.squads = map[string]string{}
	if l.client == nil || l.client.WorkspaceID == "" {
		return
	}
	var squads []map[string]any
	if err := l.client.GetJSON(l.ctx, "/api/squads", &squads); err == nil {
		for _, s := range squads {
			if id := strVal(s, "id"); id != "" {
				l.state.squads[id] = strVal(s, "name")
			}
		}
	}
}

// actor 把 (actorType, id) 渲染成 "member:Alice" 这类可读名字；查不到名字时回退
// 成 "type:id" 原样返回，绝不因展示失败而报错。
func (l actorDisplayLookup) actor(actorType, id string) string {
	if actorType == "" || id == "" {
		return ""
	}
	switch actorType {
	case "member":
		l.loadMembers()
		if l.state != nil && l.state.members != nil {
			if name := l.state.members[id]; name != "" {
				return "member:" + name
			}
		}
	case "agent":
		l.loadAgents()
		if l.state != nil && l.state.agents != nil {
			if name := l.state.agents[id]; name != "" {
				return "agent:" + name
			}
		}
	case "squad":
		l.loadSquads()
		if l.state != nil && l.state.squads != nil {
			if name := l.state.squads[id]; name != "" {
				return "squad:" + name
			}
		}
	}
	return actorType + ":" + id
}

// agent 把 agent id 渲染成纯名字（不带 "agent:" 前缀）；查不到时原样返回 id。
func (l actorDisplayLookup) agent(id string) string {
	if id == "" {
		return ""
	}
	l.loadAgents()
	if l.state != nil && l.state.agents != nil {
		if name := l.state.agents[id]; name != "" {
			return name
		}
	}
	return id
}
