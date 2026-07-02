package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestSquadOperatingProtocolWarnsAgainstDualTrigger locks in the rule
// added for #3033: the protocol must tell the squad leader that a `todo`
// child issue with an agent assignee already fires that agent, so they
// must not also @mention the same agent on the parent issue for the
// same work. Asserts behavior, not exact wording — keep the substrings
// narrow so harmless rewording doesn't break the test.
func TestSquadOperatingProtocolWarnsAgainstDualTrigger(t *testing.T) {
	compact := strings.Join(strings.Fields(squadOperatingProtocol), " ")
	for _, want := range []string{
		"--status todo` 创建并指派给 agent 的子 issue 已经会自动触发该 agent",
		"不要对同一项工作两者都做。",
		"子 issue 不是 SOP 阶段节点",
		"不得为了“继续推进下一阶段”创建同项目 child issue",
		"只有用户明确要求同项目子任务",
		"--parent <当前 issue id>",
		"--project <目标 project UUID>",
		"只带 `--parent` 会继承父 issue 的项目",
		"multica project list --output json",
	} {
		if !strings.Contains(compact, want) {
			t.Errorf("expected squad operating protocol to contain %q\n--- protocol ---\n%s", want, squadOperatingProtocol)
		}
	}
}

func TestEnsureInternalSquadTemplateCreatesCodingSquadIdempotently(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()
	cleanup := func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name = 'Multica 编码小队'`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'Multica 编码小队 · %'`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'internal-squad-codex-test-%'`, testWorkspaceID)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex 内部小队测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "internal-squad-codex-daemon-"+randomID()[:8], "internal-squad-codex-test-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create codex runtime: %v", err)
	}

	create := func() InternalSquadTemplateResponse {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", map[string]any{
			"template_key": "multica-coding",
		})
		testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
		if w.Code != http.StatusOK {
			t.Fatalf("ensure internal squad status = %d, body = %s", w.Code, w.Body.String())
		}
		var resp InternalSquadTemplateResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode internal squad response: %v", err)
		}
		return resp
	}
	first := create()
	if first.Squad.Name != "Multica 编码小队" || first.Squad.MemberCount != 6 || len(first.Agents) != 6 {
		t.Fatalf("first internal squad response = %+v", first)
	}
	if stringFromAny(first.Squad.SOPProfile.(map[string]any)["profile_key"]) != "multica-coding" {
		t.Fatalf("sop profile = %#v", first.Squad.SOPProfile)
	}
	second := create()
	if second.Squad.ID != first.Squad.ID || len(second.Agents) != 6 {
		t.Fatalf("second internal squad response = %+v first=%+v", second, first)
	}
	var agentCount, squadCount, memberCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM agent WHERE workspace_id = $1 AND name LIKE 'Multica 编码小队 · %'`, testWorkspaceID).Scan(&agentCount); err != nil {
		t.Fatalf("count coding squad agents: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM squad WHERE workspace_id = $1 AND name = 'Multica 编码小队'`, testWorkspaceID).Scan(&squadCount); err != nil {
		t.Fatalf("count coding squads: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM squad_member sm
		JOIN squad s ON s.id = sm.squad_id
		WHERE s.workspace_id = $1 AND s.name = 'Multica 编码小队'
	`, testWorkspaceID).Scan(&memberCount); err != nil {
		t.Fatalf("count coding squad members: %v", err)
	}
	if agentCount != 6 || squadCount != 1 || memberCount != 6 {
		t.Fatalf("idempotent counts: agents=%d squads=%d members=%d", agentCount, squadCount, memberCount)
	}
	var wrongConcurrencyCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent
		WHERE workspace_id = $1
		  AND name LIKE 'Multica 编码小队 · %'
		  AND max_concurrent_tasks IS DISTINCT FROM $2
	`, testWorkspaceID, defaultAgentMaxConcurrentTasks).Scan(&wrongConcurrencyCount); err != nil {
		t.Fatalf("count coding squad agent concurrency: %v", err)
	}
	if wrongConcurrencyCount != 0 {
		t.Fatalf("internal squad created %d agents without default concurrency %d", wrongConcurrencyCount, defaultAgentMaxConcurrentTasks)
	}
}

// seedSquadForBriefing creates a squad with the seeded test agent as
// leader. Returns the loaded db.Squad and a cleanup-registered ID.
func seedSquadForBriefing(t *testing.T, leaderID string, name, instructions string) db.Squad {
	t.Helper()
	ctx := context.Background()

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, instructions)
		VALUES ($1, $2, '', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, name, leaderID, testUserID, instructions).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM squad WHERE id = $1`, squadID)
	})

	uuid := util.MustParseUUID(squadID)
	squad, err := testHandler.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          uuid,
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load squad: %v", err)
	}
	return squad
}

func addAgentMember(t *testing.T, squadID pgtype.UUID, agentID, role string) {
	t.Helper()
	if _, err := testHandler.Queries.AddSquadMember(context.Background(), db.AddSquadMemberParams{
		SquadID:    squadID,
		MemberType: "agent",
		MemberID:   util.MustParseUUID(agentID),
		Role:       role,
	}); err != nil {
		t.Fatalf("add agent member: %v", err)
	}
}

func addHumanMember(t *testing.T, squadID pgtype.UUID, userID, role string) {
	t.Helper()
	if _, err := testHandler.Queries.AddSquadMember(context.Background(), db.AddSquadMemberParams{
		SquadID:    squadID,
		MemberType: "member",
		MemberID:   util.MustParseUUID(userID),
		Role:       role,
	}); err != nil {
		t.Fatalf("add human member: %v", err)
	}
}

// seededLeaderAgent loads the first seeded agent in the test workspace.
func seededLeaderAgent(t *testing.T) (id, name string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(), `
		SELECT id, name FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&id, &name); err != nil {
		t.Fatalf("load seeded agent: %v", err)
	}
	return id, name
}

// seededHumanMember returns the (member_row_id, user_id, user_name) of the
// test fixture's human member in the workspace.
func seededHumanMember(t *testing.T) (memberID, userID, userName string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(), `
		SELECT m.id, u.id, u.name
		FROM member m JOIN "user" u ON u.id = m.user_id
		WHERE m.workspace_id = $1 ORDER BY m.created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&memberID, &userID, &userName); err != nil {
		t.Fatalf("load seeded member: %v", err)
	}
	return
}

func TestBuildSquadLeaderBriefing_FullSquad(t *testing.T) {
	ctx := context.Background()
	leaderID, leaderName := seededLeaderAgent(t)

	squad := seedSquadForBriefing(t, leaderID, "Full Squad", "Always write tests.")
	squad.SopProfile = []byte(`{
		"project":"user-center",
		"repo":"/data/ida/user-center",
		"mode":"stage_chain",
		"steps":[
			{"key":"pm","name":"PM 调度","role_key":"pm"},
			{"key":"01-clarify","name":"01 需求澄清","role_key":"01-clarify","skill":"user-center/01-clarify"},
			{"key":"02-design","name":"02 方案设计","role_key":"02-design","skill":"user-center/02-design"},
			{"key":"03-task-split","name":"03 任务拆分","role_key":"03-task-split","skill":"user-center/03-task-split"},
			{"key":"04-implement","name":"04 代码开发","role_key":"04-implement","skill":"user-center/04-implement"},
			{"key":"05-verify","name":"05 测试验证","role_key":"05-verify","skill":"user-center/05-verify"}
		],
		"stage_skills":["user-center/01-clarify","user-center/02-design","user-center/03-task-split","user-center/04-implement","user-center/05-verify"],
		"operation_skills":["user-center/add-api"],
		"cross_project_child_issues":[
			{"target_project":"gateway","trigger":"需要网关路由","assignee":"gateway 项目负责人","title":"补充 gateway 接入信息","body":"说明 API 路径、方法和鉴权要求"},
			{"target_project":"ida-deployment","trigger":"需要部署配置","assignee":"ida-deployment 项目负责人","title":"补充部署配置","body":"说明配置键和环境差异"}
		],
		"acceptance":["阶段产物完整","测试证据完整","交接说明明确"],
		"archive_policy":"06-archive 不作为必跑阶段；最终结论、证据摘要和 handoff 状态由 05-verify 输出。"
	}`)

	helper1 := createHandlerTestAgent(t, "Helper One", []byte("[]"))
	helper2 := createHandlerTestAgent(t, "Helper Two", []byte("[]"))
	addAgentMember(t, squad.ID, helper1, "implementer")
	addAgentMember(t, squad.ID, helper2, "")

	memberRowID, userID, userName := seededHumanMember(t)
	_ = memberRowID
	addHumanMember(t, squad.ID, userID, "reviewer")

	out := buildSquadLeaderBriefing(ctx, testHandler.Queries, squad)

	for _, want := range []string{
		"## 小队负责人操作协议",
		"## 小队名单",
		"负责人（你）：",
		leaderName,
		"## 小队说明 (Full Squad)",
		"Always write tests.",
		"`[@Helper One](mention://agent/" + helper1 + ")`",
		"`[@Helper Two](mention://agent/" + helper2 + ")`",
		`role: "implementer"`,
		`role: "reviewer"`,
		"`[@" + userName + "](mention://member/" + userID + ")`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected briefing to contain %q\n--- briefing ---\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"## 项目 SOP 配置",
		"- 项目：user-center",
		"- 仓库：`/data/ida/user-center`",
		"SOP 步骤链：PM 调度",
		"可调用操作技能：user-center/add-api",
		"目标项目=gateway",
		"目标项目=ida-deployment",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("briefing must not expose sop_profile field %q\n--- briefing ---\n%s", unwanted, out)
		}
	}

	// Helper Two has no role — must NOT render an empty role: "" segment.
	if strings.Contains(out, `Helper Two — agent, role: ""`) {
		t.Errorf("expected empty role to be omitted, got: %s", out)
	}
}

func TestInternalUserCenterTemplateIncludesCrossProjectChildIssuePlan(t *testing.T) {
	template, ok := internalSquadTemplateByKey("user-center")
	if !ok {
		t.Fatal("user-center internal squad template missing")
	}
	if len(template.Roles) != 6 {
		t.Fatalf("user-center template roles = %d, want 6", len(template.Roles))
	}
	raw, err := json.Marshal(template.Profile)
	if err != nil {
		t.Fatalf("marshal template profile: %v", err)
	}
	if strings.Contains(string(raw), "model_policy") || strings.Contains(string(raw), "默认模型") {
		t.Fatalf("squad SOP profile must not contain agent model policy: %s", string(raw))
	}
	var profile struct {
		StageSkills []string `json:"stage_skills"`
		MCPServers  []string `json:"mcp_servers"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("unmarshal template profile: %v", err)
	}
	if !slices.Contains(profile.MCPServers, "mcp-server-tapd") || !slices.Contains(profile.MCPServers, "gongfeng") {
		t.Fatalf("user-center template mcp_servers = %+v", profile.MCPServers)
	}
	for _, role := range template.Roles {
		if len(role.MCPConfig) == 0 {
			t.Fatalf("role %s missing MCPConfig", role.Key)
		}
		var mcp struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(role.MCPConfig, &mcp); err != nil {
			t.Fatalf("role %s MCPConfig invalid: %v", role.Key, err)
		}
		if _, ok := mcp.MCPServers["mcp-server-tapd"]; !ok {
			t.Fatalf("role %s MCPConfig missing mcp-server-tapd: %s", role.Key, string(role.MCPConfig))
		}
		if _, ok := mcp.MCPServers["gongfeng"]; !ok {
			t.Fatalf("role %s MCPConfig missing gongfeng: %s", role.Key, string(role.MCPConfig))
		}
		if strings.Contains(string(role.MCPConfig), "TAPD_ACCESS_TOKEN") {
			t.Fatalf("role %s MCPConfig must not embed TAPD token env values: %s", role.Key, string(role.MCPConfig))
		}
	}
	for _, skill := range profile.StageSkills {
		if skill == "user-center/06-archive" {
			t.Fatalf("user-center template stage_skills must not include 06-archive: %+v", profile.StageSkills)
		}
	}
	instructions := template.Instructions
	for _, want := range []string{
		"你是项目 SOP 小队的 PM-项目经理",
		"PM -> 01-需求澄清 -> 02-方案设计 -> 03-任务拆分 -> 04-开发 -> 05-验证测试",
		"## 角色矩阵",
		"PM-项目经理",
		"01-需求澄清",
		"02-方案设计",
		"03-任务拆分",
		"04-开发",
		"05-验证测试",
		"## 跨项目 child issue 规则",
		"单项目需求、TAPD 正文抓取后的真实需求、以及 01-05 阶段推进",
		"不得只评论或委派 03 代替 PM 创建跨项目 child issue",
		"## 验收要求",
		"阶段产物完整",
		"测试证据完整",
		"交接说明明确",
		"跨项目 child issue 由 PM 直接创建并可回读",
		"05-验证测试通过且无阻断后",
		"## 禁止事项",
		"PM 首轮只能输出流程判断、下一阶段调度或跳步确认请求",
		"不得 checkout、编辑代码、运行实现测试",
		"等待任务创建者或 workspace owner/admin 明确同意",
		"把 TAPD 正文抓取后的真实需求复制成同项目 child issue",
		"为了进入 01-clarify/02-design/03-task-split/04-implement/05-verify 创建 child issue",
		"01-05 阶段 Agent @mention 下一阶段或任何负责人",
		"05-验证测试通过后只写验收通过但不更新 issue 状态为 done",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("expected user-center SOP instructions to contain %q\n--- instructions ---\n%s", want, instructions)
		}
	}
	if !strings.Contains(template.Instructions, "不得为了进入下一阶段创建同项目 child issue") {
		t.Fatalf("user-center template instructions must forbid same-project stage child issues:\n%s", template.Instructions)
	}
	if !strings.Contains(template.Instructions, "只有 PM 可以 @mention 下一阶段 Agent") {
		t.Fatalf("user-center template instructions must reserve stage routing to pm:\n%s", template.Instructions)
	}
	if !strings.Contains(template.Instructions, "必须在最终收口中把 issue 状态更新为 done") {
		t.Fatalf("user-center template instructions must require final done status:\n%s", template.Instructions)
	}
	pmRoleFound := false
	for _, role := range template.Roles {
		if role.Key == "pm" {
			pmRoleFound = true
			for _, want := range []string{
				"TAPD 正文抓取后得到的真实需求仍属于当前 issue",
				"不得复制成同项目 child issue",
				"不得为了进入 01-clarify、02-design、03-task-split、04-implement 或 05-verify 创建 child issue",
				"只有真实跨项目依赖才创建 child issue",
				"只有 PM 可以 @mention 下一阶段 Agent",
				"05-verify 通过且无阻断时",
			} {
				if !strings.Contains(role.Instruction+role.Description, want) {
					t.Fatalf("pm role must contain %q\n--- role ---\n%+v", want, role)
				}
			}
			continue
		}
		for _, want := range []string{
			"不得 @mention 任何 Agent、Squad、Member 或 all",
			"不得直接触发下一阶段",
			"由 pm 判断通过、返工、推进或收口",
		} {
			if !strings.Contains(role.Instruction, want) {
				t.Fatalf("%s role must contain %q\n--- role ---\n%+v", role.Key, want, role)
			}
		}
	}
	if !pmRoleFound {
		t.Fatal("user-center template missing pm role")
	}
	if strings.Contains(instructions, "user-center/06-archive") {
		t.Fatalf("user-center SOP instructions must not include 06-archive in required stage skills\n--- instructions ---\n%s", instructions)
	}
}

func TestEnsureUserCenterInternalSquadPersistsMCPConfig(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()
	cleanup := func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name IN ('pm', 'user-center 小队')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND (name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify') OR name LIKE 'user-center 小队 · %')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'internal-user-center-codex-test-%'`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'internal-user-center-codebuddy-test-%'`, testWorkspaceID)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy user-center 小队测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "internal-user-center-codebuddy-daemon-"+randomID()[:8], "internal-user-center-codebuddy-test-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create codebuddy runtime: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex user-center 小队测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "internal-user-center-codex-daemon-"+randomID()[:8], "internal-user-center-codex-test-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create codex runtime: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", map[string]any{
		"template_key": "user-center",
	})
	testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("ensure user-center internal squad status = %d, body = %s", w.Code, w.Body.String())
	}
	var ensured InternalSquadTemplateResponse
	if err := json.NewDecoder(w.Body).Decode(&ensured); err != nil {
		t.Fatalf("decode ensured internal squad response: %v", err)
	}
	if !strings.Contains(ensured.Squad.Instructions, "只有 PM 可以 @mention 下一阶段 Agent") {
		t.Fatalf("first-created internal squad must persist routing instructions:\n%s", ensured.Squad.Instructions)
	}

	rows, err := testPool.Query(ctx, `
		SELECT name, instructions, mcp_config
		FROM agent
		WHERE workspace_id = $1 AND name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
		ORDER BY name
	`, testWorkspaceID)
	if err != nil {
		t.Fatalf("query user-center agents: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name string
		var instructions string
		var raw []byte
		if err := rows.Scan(&name, &instructions, &raw); err != nil {
			t.Fatalf("scan user-center agent: %v", err)
		}
		count++
		if name == projectSOPAgentPM {
			for _, want := range []string{
				"只有 PM 可以 @mention 下一阶段 Agent",
				"不得 checkout、编辑代码、运行实现测试",
				"等待任务创建者或 workspace owner/admin 明确同意",
			} {
				if !strings.Contains(instructions, want) {
					t.Fatalf("pm instructions must contain %q:\n%s", want, instructions)
				}
			}
		}
		if name != projectSOPAgentPM && !strings.Contains(instructions, "不得 @mention 任何 Agent、Squad、Member 或 all") {
			t.Fatalf("%s instructions must forbid worker mentions:\n%s", name, instructions)
		}
		var mcp struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(raw, &mcp); err != nil {
			t.Fatalf("%s mcp_config invalid: %v", name, err)
		}
		if _, ok := mcp.MCPServers["mcp-server-tapd"]; !ok {
			t.Fatalf("%s mcp_config missing mcp-server-tapd: %s", name, string(raw))
		}
		if _, ok := mcp.MCPServers["gongfeng"]; !ok {
			t.Fatalf("%s mcp_config missing gongfeng: %s", name, string(raw))
		}
		if strings.Contains(string(raw), "TAPD_ACCESS_TOKEN") {
			t.Fatalf("%s mcp_config must not embed TAPD token env values: %s", name, string(raw))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate user-center agents: %v", err)
	}
	if count != 6 {
		t.Fatalf("user-center agent count = %d, want 6", count)
	}
	var nonDefaultCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent a
		JOIN agent_runtime ar ON ar.id = a.runtime_id
		WHERE a.workspace_id = $1
		  AND a.name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
		  AND (ar.provider IS DISTINCT FROM 'codebuddy' OR coalesce(a.model, '') <> '')
	`, testWorkspaceID).Scan(&nonDefaultCount); err != nil {
		t.Fatalf("count non-default user-center agents: %v", err)
	}
	if nonDefaultCount != 0 {
		t.Fatalf("default ensure left %d user-center agents outside codebuddy/runtime-default", nonDefaultCount)
	}

	if _, err := testPool.Exec(ctx, `
		UPDATE agent
		SET model = 'stale-model-before-template-resync'
		WHERE workspace_id = $1 AND name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
	`, testWorkspaceID); err != nil {
		t.Fatalf("stale user-center agent models: %v", err)
	}
	w = httptest.NewRecorder()
	overrideModel := "gpt-template-resync-test"
	req = newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", map[string]any{
		"template_key": "user-center",
		"model":        overrideModel,
	})
	testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("re-ensure user-center internal squad status = %d, body = %s", w.Code, w.Body.String())
	}
	var staleModelCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent
		WHERE workspace_id = $1
		  AND name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
		  AND model IS DISTINCT FROM $2
	`, testWorkspaceID, overrideModel).Scan(&staleModelCount); err != nil {
		t.Fatalf("count stale user-center agent models: %v", err)
	}
	if staleModelCount != 0 {
		t.Fatalf("re-ensure left %d user-center agents on stale model", staleModelCount)
	}

	codexModel := "codex-template-provider-test"
	w = httptest.NewRecorder()
	req = newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", map[string]any{
		"template_key":     "user-center",
		"runtime_provider": "codex",
		"model":            codexModel,
	})
	testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("re-ensure user-center internal squad with codex status = %d, body = %s", w.Code, w.Body.String())
	}
	var nonCodexCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent a
		JOIN agent_runtime ar ON ar.id = a.runtime_id
		WHERE a.workspace_id = $1
		  AND a.name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
		  AND (ar.provider IS DISTINCT FROM 'codex' OR a.model IS DISTINCT FROM $2)
	`, testWorkspaceID, codexModel).Scan(&nonCodexCount); err != nil {
		t.Fatalf("count non-codex user-center agents: %v", err)
	}
	if nonCodexCount != 0 {
		t.Fatalf("re-ensure left %d user-center agents outside codex/%s", nonCodexCount, codexModel)
	}
}

func TestEnsureUserCenterInternalSquadPersonalCreatesPrivateAgents(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()
	cleanup := func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name IN ('pm', 'user-center 小队')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND (name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify') OR name LIKE 'user-center 小队 · %')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'internal-user-center-private-test-%'`, testWorkspaceID)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy user-center 私有小队测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "internal-user-center-private-daemon-"+randomID()[:8], "internal-user-center-private-test-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create codebuddy runtime: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", map[string]any{
		"template_key": "user-center",
		"scope":        "personal",
	})
	testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("ensure personal user-center internal squad status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp InternalSquadTemplateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode ensure response: %v", err)
	}
	if resp.Squad.Scope != squadScopePersonal {
		t.Fatalf("squad scope = %q, want %q", resp.Squad.Scope, squadScopePersonal)
	}
	if resp.Squad.Name != "pm" || resp.Squad.MemberCount != 6 || len(resp.Agents) != 6 {
		t.Fatalf("ensure response = %+v", resp)
	}

	var nonPrivateAgentCount, nonOwnerAgentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE scope IS DISTINCT FROM 'personal')::int,
			count(*) FILTER (WHERE owner_id IS DISTINCT FROM $2)::int
		FROM agent
		WHERE workspace_id = $1
		  AND name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
	`, testWorkspaceID, testUserID).Scan(&nonPrivateAgentCount, &nonOwnerAgentCount); err != nil {
		t.Fatalf("count private user-center agents: %v", err)
	}
	if nonPrivateAgentCount != 0 {
		t.Fatalf("personal squad created %d non-personal agents", nonPrivateAgentCount)
	}
	if nonOwnerAgentCount != 0 {
		t.Fatalf("personal squad created %d agents not owned by creator", nonOwnerAgentCount)
	}
}

func TestEnsureUserCenterInternalSquadWorkspaceAndPersonalAgentsAreScoped(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()
	cleanup := func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name IN ('pm', 'user-center 小队')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND (name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify') OR name LIKE 'user-center 小队 · %')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'internal-user-center-scope-test-%'`, testWorkspaceID)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy user-center scope 测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "internal-user-center-scope-daemon-"+randomID()[:8], "internal-user-center-scope-test-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create codebuddy runtime: %v", err)
	}

	ensure := func(scope string) InternalSquadTemplateResponse {
		t.Helper()
		body := map[string]any{"template_key": "user-center"}
		if scope != "" {
			body["scope"] = scope
		}
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", body)
		testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
		if w.Code != http.StatusOK {
			t.Fatalf("ensure %s user-center internal squad status = %d, body = %s", scope, w.Code, w.Body.String())
		}
		var resp InternalSquadTemplateResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode ensure response: %v", err)
		}
		return resp
	}

	workspaceResp := ensure(squadScopeWorkspace)
	personalResp := ensure(squadScopePersonal)
	if workspaceResp.Squad.ID == personalResp.Squad.ID {
		t.Fatalf("workspace and personal squads reused the same squad: %s", workspaceResp.Squad.ID)
	}
	if workspaceResp.Squad.Scope != squadScopeWorkspace || personalResp.Squad.Scope != squadScopePersonal {
		t.Fatalf("unexpected squad visibilities: workspace=%q personal=%q", workspaceResp.Squad.Scope, personalResp.Squad.Scope)
	}

	workspaceAgents := map[string]string{}
	for _, agent := range workspaceResp.Agents {
		workspaceAgents[agent.Name] = agent.ID
	}
	for _, agent := range personalResp.Agents {
		if workspaceAgents[agent.Name] == "" {
			t.Fatalf("personal agent %q did not match a workspace role name", agent.Name)
		}
		if workspaceAgents[agent.Name] == agent.ID {
			t.Fatalf("personal agent %q reused workspace agent id %s", agent.Name, agent.ID)
		}
	}

	var workspaceCount, personalCount, wrongOwnerCount int
	if err := testPool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE scope = 'workspace')::int,
			count(*) FILTER (WHERE scope = 'personal' AND owner_id = $2)::int,
			count(*) FILTER (WHERE scope = 'personal' AND owner_id IS DISTINCT FROM $2)::int
		FROM agent
		WHERE workspace_id = $1
		  AND archived_at IS NULL
		  AND name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
	`, testWorkspaceID, testUserID).Scan(&workspaceCount, &personalCount, &wrongOwnerCount); err != nil {
		t.Fatalf("count scoped user-center agents: %v", err)
	}
	if workspaceCount != 6 || personalCount != 6 || wrongOwnerCount != 0 {
		t.Fatalf("scoped agent counts workspace=%d personal=%d wrongOwner=%d, want 6/6/0", workspaceCount, personalCount, wrongOwnerCount)
	}

	archivedPersonalAgentID := parseUUID(personalResp.Agents[0].ID)
	if _, err := testHandler.Queries.ArchiveAgent(ctx, db.ArchiveAgentParams{
		ID:         archivedPersonalAgentID,
		ArchivedBy: parseUUID(testUserID),
	}); err != nil {
		t.Fatalf("archive personal agent: %v", err)
	}
	personalAgain := ensure(squadScopePersonal)
	if personalAgain.Agents[0].ID != personalResp.Agents[0].ID {
		t.Fatalf("personal re-ensure did not restore archived agent: got %s want %s", personalAgain.Agents[0].ID, personalResp.Agents[0].ID)
	}
	var restoredArchivedAtValid bool
	if err := testPool.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM agent WHERE id = $1`, personalResp.Agents[0].ID).Scan(&restoredArchivedAtValid); err != nil {
		t.Fatalf("read restored personal agent: %v", err)
	}
	if restoredArchivedAtValid {
		t.Fatalf("personal re-ensure left agent %s archived", personalResp.Agents[0].ID)
	}

	workspaceAgain := ensure(squadScopeWorkspace)
	for _, agent := range workspaceAgain.Agents {
		if workspaceAgents[agent.Name] != agent.ID {
			t.Fatalf("workspace re-ensure changed agent %q from %s to %s", agent.Name, workspaceAgents[agent.Name], agent.ID)
		}
	}
}

func TestEnsureUserCenterInternalSquadRestoresArchivedSquadWithoutArchivingAgents(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()
	cleanup := func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name IN ('pm', 'user-center 小队')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND (name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify') OR name LIKE 'user-center 小队 · %')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'internal-user-center-restore-test-%'`, testWorkspaceID)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy user-center 归档恢复测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "internal-user-center-restore-daemon-"+randomID()[:8], "internal-user-center-restore-test-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create codebuddy runtime: %v", err)
	}

	ensure := func() InternalSquadTemplateResponse {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", map[string]any{
			"template_key": "user-center",
		})
		testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
		if w.Code != http.StatusOK {
			t.Fatalf("ensure user-center internal squad status = %d, body = %s", w.Code, w.Body.String())
		}
		var resp InternalSquadTemplateResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode ensure response: %v", err)
		}
		return resp
	}

	first := ensure()
	if first.Squad.Name != "pm" || first.Squad.MemberCount != 6 || len(first.Agents) != 6 {
		t.Fatalf("first ensure response = %+v", first)
	}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/squads/"+first.Squad.ID, nil)
	testHandler.DeleteSquad(w, withURLParams(req, "workspaceId", testWorkspaceID, "id", first.Squad.ID))
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive pm squad status = %d, body = %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/squads", nil)
	testHandler.ListSquads(w, withURLParam(req, "workspaceId", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("list active squads status = %d, body = %s", w.Code, w.Body.String())
	}
	var activeList []SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&activeList); err != nil {
		t.Fatalf("decode active squad list: %v", err)
	}
	for _, item := range activeList {
		if item.ID == first.Squad.ID {
			t.Fatalf("archived pm squad appeared in default active list: %+v", item)
		}
	}

	w = httptest.NewRecorder()
	req = newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/squads?include_archived=true", nil)
	testHandler.ListSquads(w, withURLParam(req, "workspaceId", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("list all squads status = %d, body = %s", w.Code, w.Body.String())
	}
	var allList []SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&allList); err != nil {
		t.Fatalf("decode all squad list: %v", err)
	}
	foundArchived := false
	for _, item := range allList {
		if item.ID == first.Squad.ID {
			foundArchived = item.ArchivedAt != nil
			break
		}
	}
	if !foundArchived {
		t.Fatalf("include_archived list did not expose archived pm squad")
	}

	var archivedAgentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent
		WHERE workspace_id = $1
		  AND name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
		  AND archived_at IS NOT NULL
	`, testWorkspaceID).Scan(&archivedAgentCount); err != nil {
		t.Fatalf("count archived user-center agents: %v", err)
	}
	if archivedAgentCount != 0 {
		t.Fatalf("archive squad archived %d user-center agents; want 0", archivedAgentCount)
	}

	second := ensure()
	if second.Squad.ID != first.Squad.ID {
		t.Fatalf("archived pm squad was recreated instead of restored: first=%s second=%s", first.Squad.ID, second.Squad.ID)
	}
	if second.Squad.ArchivedAt != nil {
		t.Fatalf("restored pm squad still archived: %+v", second.Squad)
	}
	if second.Squad.Name != "pm" || second.Squad.MemberCount != 6 || len(second.Agents) != 6 {
		t.Fatalf("second ensure response = %+v", second)
	}

	w = httptest.NewRecorder()
	req = newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/squads/"+second.Squad.ID, nil)
	testHandler.DeleteSquad(w, withURLParams(req, "workspaceId", testWorkspaceID, "id", second.Squad.ID))
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive restored pm squad status = %d, body = %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req = newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/"+second.Squad.ID+"/restore", nil)
	testHandler.RestoreSquad(w, withURLParams(req, "workspaceId", testWorkspaceID, "id", second.Squad.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("restore pm squad status = %d, body = %s", w.Code, w.Body.String())
	}
	var restored SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&restored); err != nil {
		t.Fatalf("decode restored squad: %v", err)
	}
	if restored.ID != second.Squad.ID || restored.ArchivedAt != nil {
		t.Fatalf("restore response = %+v, want same active squad id %s", restored, second.Squad.ID)
	}

	var activePM, archivedPM, userCenterAgentCount, nonDefaultAgentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE archived_at IS NULL)::int,
			count(*) FILTER (WHERE archived_at IS NOT NULL)::int
		FROM squad
		WHERE workspace_id = $1 AND name = 'pm'
	`, testWorkspaceID).Scan(&activePM, &archivedPM); err != nil {
		t.Fatalf("count pm squads: %v", err)
	}
	if activePM != 1 || archivedPM != 0 {
		t.Fatalf("pm squad counts active=%d archived=%d, want active=1 archived=0", activePM, archivedPM)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent
		WHERE workspace_id = $1
		  AND name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
	`, testWorkspaceID).Scan(&userCenterAgentCount); err != nil {
		t.Fatalf("count user-center agents: %v", err)
	}
	if userCenterAgentCount != 6 {
		t.Fatalf("user-center agent count = %d, want 6", userCenterAgentCount)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent a
		JOIN agent_runtime ar ON ar.id = a.runtime_id
		WHERE a.workspace_id = $1
		  AND a.name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify')
		  AND (ar.provider IS DISTINCT FROM 'codebuddy' OR coalesce(a.model, '') <> '')
	`, testWorkspaceID).Scan(&nonDefaultAgentCount); err != nil {
		t.Fatalf("count non-default user-center agents: %v", err)
	}
	if nonDefaultAgentCount != 0 {
		t.Fatalf("restored pm squad left %d agents outside codebuddy/runtime-default", nonDefaultAgentCount)
	}
}

func TestUserCenterSquadAssignmentDoesNotPrecreateStageTasks(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()
	cleanup := func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND title LIKE 'user-center stage task test%')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE workspace_id = $1 AND title LIKE 'user-center stage task test%'`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name IN ('pm', 'user-center 小队')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND (name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify') OR name LIKE 'user-center 小队 · %')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'internal-user-center-stage-test-%'`, testWorkspaceID)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex user-center stage 测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "internal-user-center-stage-daemon-"+randomID()[:8], "internal-user-center-stage-test-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create codex runtime: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", map[string]any{
		"template_key": "user-center",
	})
	testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("ensure user-center internal squad status = %d, body = %s", w.Code, w.Body.String())
	}
	var ensured InternalSquadTemplateResponse
	if err := json.NewDecoder(w.Body).Decode(&ensured); err != nil {
		t.Fatalf("decode ensure response: %v", err)
	}

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "user-center stage task test",
		"assignee_type": "squad",
		"assignee_id":   ensured.Squad.ID,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode created issue: %v", err)
	}

	rows, err := testPool.Query(ctx, `
		SELECT e.step_key, COALESCE(e.task_id::text, ''), count(tte.id)::int AS trace_count
		FROM squad_sop_step_event e
		LEFT JOIN task_trace_event tte ON tte.task_id = e.task_id
		WHERE e.issue_id = $1 AND e.event_type = '步骤开始'
		GROUP BY e.step_key, e.task_id
	`, created.ID)
	if err != nil {
		t.Fatalf("query stage task evidence: %v", err)
	}
	defer rows.Close()
	seen := map[string]struct {
		TaskID     string
		TraceCount int
	}{}
	for rows.Next() {
		var stepKey, taskID string
		var traceCount int
		if err := rows.Scan(&stepKey, &taskID, &traceCount); err != nil {
			t.Fatalf("scan stage evidence: %v", err)
		}
		seen[stepKey] = struct {
			TaskID     string
			TraceCount int
		}{TaskID: taskID, TraceCount: traceCount}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate stage evidence: %v", err)
	}
	pm, ok := seen["pm"]
	if !ok {
		t.Fatalf("missing SOP leader start event for PM; seen=%+v", seen)
	}
	if strings.TrimSpace(pm.TaskID) == "" {
		t.Fatalf("PM stage missing task_id")
	}
	if pm.TraceCount == 0 {
		t.Fatalf("PM stage task %s missing trace event", pm.TaskID)
	}
	for _, stepKey := range []string{"01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"} {
		if _, ok := seen[stepKey]; ok {
			t.Fatalf("SOP assignment pre-created %s stage event; seen=%+v", stepKey, seen)
		}
	}

	var leaderTaskID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text
		FROM agent_task_queue
		WHERE issue_id = $1 AND is_leader_task IS TRUE
		ORDER BY created_at ASC
		LIMIT 1
	`, created.ID).Scan(&leaderTaskID); err != nil {
		t.Fatalf("query leader task: %v", err)
	}
	var workerTaskCountBefore int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent_task_queue
		WHERE issue_id = $1
		  AND COALESCE(is_leader_task, false) IS FALSE
	`, created.ID).Scan(&workerTaskCountBefore); err != nil {
		t.Fatalf("count worker tasks after squad assignment: %v", err)
	}
	if workerTaskCountBefore != 0 {
		t.Fatalf("squad assignment pre-created %d worker stage tasks; want 0", workerTaskCountBefore)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'completed', completed_at = now()
		WHERE id = $1
	`, leaderTaskID); err != nil {
		t.Fatalf("complete leader task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE squad_sop_run
		SET leader_task_id = NULL
		WHERE issue_id = $1 AND status IN ('待开始', '进行中', '已阻塞')
	`, created.ID); err != nil {
		t.Fatalf("detach leader task from SOP run: %v", err)
	}
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("load created issue: %v", err)
	}
	_, err = testHandler.TaskService.EnqueueTaskForSquadLeader(ctx, issue, parseUUID(ensured.Squad.LeaderID), pgtype.UUID{})
	if err != nil {
		t.Fatalf("second leader enqueue: %v", err)
	}
	var workerTaskCountAfter int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent_task_queue
		WHERE issue_id = $1
		  AND COALESCE(is_leader_task, false) IS FALSE
	`, created.ID).Scan(&workerTaskCountAfter); err != nil {
		t.Fatalf("count worker tasks after second leader enqueue: %v", err)
	}
	if workerTaskCountAfter != 0 {
		t.Fatalf("second leader enqueue pre-created %d worker stage tasks; want 0", workerTaskCountAfter)
	}
}

func TestSquadLeaderCommentTriggerDoesNotCreateStageTasks(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()
	cleanup := func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND title LIKE 'comment-trigger stage task test%')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE workspace_id = $1 AND title LIKE 'comment-trigger stage task test%')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name IN ('pm', 'user-center 小队')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND (name IN ('PM-项目经理', '01-需求澄清', '02-方案设计', '03-任务拆分', '04-开发', '05-验证测试', 'pm', '01-clarify', '02-design', '03-task-split', '04-implement', '05-verify') OR name LIKE 'user-center 小队 · %')`, testWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND name LIKE 'internal-user-center-comment-trigger-test-%'`, testWorkspaceID)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex user-center comment trigger 测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "internal-user-center-comment-trigger-daemon-"+randomID()[:8], "internal-user-center-comment-trigger-test-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create codex runtime: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/squads/internal-template", map[string]any{
		"template_key": "user-center",
	})
	testHandler.EnsureInternalSquadTemplate(w, withURLParam(req, "workspaceId", testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("ensure user-center internal squad status = %d, body = %s", w.Code, w.Body.String())
	}
	var ensured InternalSquadTemplateResponse
	if err := json.NewDecoder(w.Body).Decode(&ensured); err != nil {
		t.Fatalf("decode ensure response: %v", err)
	}

	var issueID, commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, assignee_type, assignee_id)
		VALUES ($1, 'member', $2, 'comment-trigger stage task test', 'squad', $3)
		RETURNING id
	`, testWorkspaceID, testUserID, ensured.Squad.ID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, '验收通过，请 PM 收口，不要重跑阶段。', 'comment')
		RETURNING id
	`, testWorkspaceID, issueID, testUserID).Scan(&commentID); err != nil {
		t.Fatalf("create trigger comment: %v", err)
	}
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	leaderTask, err := testHandler.TaskService.EnqueueTaskForSquadLeader(ctx, issue, parseUUID(ensured.Squad.LeaderID), parseUUID(commentID))
	if err != nil {
		t.Fatalf("enqueue comment-trigger leader task: %v", err)
	}
	var childTaskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent_task_queue
		WHERE parent_task_id = $1
	`, leaderTask.ID).Scan(&childTaskCount); err != nil {
		t.Fatalf("count comment-trigger child tasks: %v", err)
	}
	if childTaskCount != 0 {
		t.Fatalf("comment-trigger leader task created %d stage child tasks; want 0", childTaskCount)
	}
	var stageStartCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM squad_sop_step_event
		WHERE task_id IN (SELECT id FROM agent_task_queue WHERE parent_task_id = $1)
	`, leaderTask.ID).Scan(&stageStartCount); err != nil {
		t.Fatalf("count comment-trigger stage events: %v", err)
	}
	if stageStartCount != 0 {
		t.Fatalf("comment-trigger leader task created %d child stage events; want 0", stageStartCount)
	}
}

func TestBuildSquadLeaderBriefing_OnlyLeader(t *testing.T) {
	ctx := context.Background()
	leaderID, _ := seededLeaderAgent(t)
	squad := seedSquadForBriefing(t, leaderID, "Solo Squad", "")

	out := buildSquadLeaderBriefing(ctx, testHandler.Queries, squad)
	if !strings.Contains(out, "成员：（无；你是这个 squad 的唯一成员）") {
		t.Errorf("expected lone-leader fallback line, got:\n%s", out)
	}
	// No user instructions → no squad instructions section.
	if strings.Contains(out, "## 小队说明") {
		t.Errorf("expected no squad instructions section when empty, got:\n%s", out)
	}
}

func TestBuildSquadLeaderBriefing_SkipsArchivedAgent(t *testing.T) {
	ctx := context.Background()
	leaderID, _ := seededLeaderAgent(t)
	squad := seedSquadForBriefing(t, leaderID, "Archive Squad", "")

	archived := createHandlerTestAgent(t, "Retired Bot", []byte("[]"))
	addAgentMember(t, squad.ID, archived, "")
	if _, err := testPool.Exec(ctx,
		`UPDATE agent SET archived_at = now(), archived_by = $1 WHERE id = $2`,
		testUserID, archived,
	); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	out := buildSquadLeaderBriefing(ctx, testHandler.Queries, squad)
	if strings.Contains(out, "Retired Bot") {
		t.Errorf("archived agent should not appear in roster:\n%s", out)
	}
	if strings.Contains(out, archived) {
		t.Errorf("archived agent UUID should not appear in roster:\n%s", out)
	}
}

// TestBuildSquadLeaderBriefing_MentionsRoundTrip is the contract test
// guaranteeing every emitted mention markdown string parses back through
// util.ParseMentions to its (type, id). If this ever breaks, the leader's
// dispatch comments will silently fail to trigger anyone.
func TestBuildSquadLeaderBriefing_MentionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	leaderID, _ := seededLeaderAgent(t)
	squad := seedSquadForBriefing(t, leaderID, "Mention Round Trip", "")

	helper := createHandlerTestAgent(t, "Round Trip Bot", []byte("[]"))
	addAgentMember(t, squad.ID, helper, "")

	memberRowID, userID, _ := seededHumanMember(t)
	_ = memberRowID
	addHumanMember(t, squad.ID, userID, "")

	out := buildSquadLeaderBriefing(ctx, testHandler.Queries, squad)
	mentions := util.ParseMentions(out)

	wantIDs := map[string]string{
		leaderID: "agent",
		helper:   "agent",
		userID:   "member",
	}
	got := make(map[string]string, len(mentions))
	for _, m := range mentions {
		got[m.ID] = m.Type
	}
	for id, kind := range wantIDs {
		if got[id] != kind {
			t.Errorf("expected %s mention for id %s, got %q (all parsed: %#v)", kind, id, got[id], mentions)
		}
	}
}

// claimAndDecodeAgent runs ClaimTaskByRuntime for the given runtime and
// returns the agent block of the response. Fails the test on non-200.
func claimAndDecodeAgent(t *testing.T, runtimeID string) *TaskAgentData {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, "test-claim-squad-briefing")
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Task *struct {
			Agent *TaskAgentData `json:"agent"`
		} `json:"task"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Task == nil || resp.Task.Agent == nil {
		t.Fatalf("expected task.agent in response, got: %s", w.Body.String())
	}
	return resp.Task.Agent
}

// queueSquadIssueTaskFor creates an issue assigned to the squad and a queued
// task for the given (agentID, runtimeID). Returns the issue + task IDs.
func queueSquadIssueTaskFor(t *testing.T, squadID, agentID, runtimeID string, issueNumber int) (issueID, taskID string) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `
INSERT INTO issue (
workspace_id, title, status, priority, creator_id, creator_type,
assignee_type, assignee_id, number, position
) VALUES ($1, 'Squad briefing claim test', 'todo', 'medium', $2, 'member',
'squad', $3, $4, 0)
RETURNING id
`, testWorkspaceID, testUserID, squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create squad-assigned issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	if err := testPool.QueryRow(ctx, `
INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
VALUES ($1, $2, $3, 'queued', 0)
RETURNING id
`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("queue task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
	return
}

// TestClaimTask_LeaderGetsBriefing — when the squad leader claims a task on
// a squad-assigned issue, the response's agent.instructions must include
// the Operating Protocol + Roster + user instructions.
func TestClaimTask_LeaderGetsBriefing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var leaderID, runtimeID string
	if err := testPool.QueryRow(ctx,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1`,
		testWorkspaceID,
	).Scan(&leaderID, &runtimeID); err != nil {
		t.Fatalf("get leader agent: %v", err)
	}

	squad := seedSquadForBriefing(t, leaderID, "Briefing Claim Squad", "Be terse.")

	helper := createHandlerTestAgent(t, "Briefing Helper", []byte("[]"))
	addAgentMember(t, squad.ID, helper, "implementer")

	queueSquadIssueTaskFor(t, util.UUIDToString(squad.ID), leaderID, runtimeID, 95001)

	agent := claimAndDecodeAgent(t, runtimeID)
	for _, want := range []string{
		"## 小队负责人操作协议",
		"## 小队名单",
		"负责人（你）：",
		"## 小队说明 (Briefing Claim Squad)",
		"Be terse.",
		"`[@Briefing Helper](mention://agent/" + helper + ")`",
	} {
		if !strings.Contains(agent.Instructions, want) {
			t.Errorf("expected agent.instructions to contain %q\n--- instructions ---\n%s", want, agent.Instructions)
		}
	}
}

// TestClaimTask_NonLeaderGetsNoBriefing — when a non-leader squad member
// claims a task on a squad-assigned issue, NO briefing is injected.
func TestClaimTask_NonLeaderGetsNoBriefing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var leaderID string
	if err := testPool.QueryRow(ctx,
		`SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1`,
		testWorkspaceID,
	).Scan(&leaderID); err != nil {
		t.Fatalf("get leader agent: %v", err)
	}

	squad := seedSquadForBriefing(t, leaderID, "Non-Leader Squad", "Squad guidance.")

	// Create a second agent (NOT the leader) with its own runtime so the
	// claim path picks its task without ambiguity.
	helperID := createHandlerTestAgent(t, "Non Leader Helper", []byte("[]"))
	addAgentMember(t, squad.ID, helperID, "")
	var helperRuntime string
	if err := testPool.QueryRow(ctx,
		`SELECT runtime_id FROM agent WHERE id = $1`, helperID,
	).Scan(&helperRuntime); err != nil {
		t.Fatalf("get helper runtime: %v", err)
	}

	queueSquadIssueTaskFor(t, util.UUIDToString(squad.ID), helperID, helperRuntime, 95002)

	agent := claimAndDecodeAgent(t, helperRuntime)
	for _, mustNot := range []string{
		"小队负责人操作协议",
		"小队名单",
		"小队说明 (Non-Leader Squad)",
	} {
		if strings.Contains(agent.Instructions, mustNot) {
			t.Errorf("non-leader claim should NOT contain %q\n--- instructions ---\n%s", mustNot, agent.Instructions)
		}
	}
}

// Avoid "imported and not used: pgtype" if helpers above are the only users.
var _ pgtype.UUID
