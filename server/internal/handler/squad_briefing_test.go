package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex 内部小队测试运行时', '{}'::jsonb, $4, 'private', now())
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
		"stage_skills":["user-center/01-clarify","user-center/02-design","user-center/03-task-split","user-center/04-implement","user-center/05-verify","user-center/06-archive"],
		"operation_skills":["user-center/add-api"],
		"cross_project_child_issues":[
			{"target_project":"gateway","trigger":"需要网关路由","assignee":"gateway 项目负责人","title":"补充 gateway 接入信息","body":"说明 API 路径、方法和鉴权要求"},
			{"target_project":"config","trigger":"需要配置项","assignee":"config 项目负责人","title":"补充配置项","body":"说明配置键和环境差异"}
		],
		"acceptance":["阶段产物完整","测试证据完整","交接说明明确"]
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
		"## 项目 SOP 配置",
		"- 项目：user-center",
		"- 仓库：`/data/ida/user-center`",
		"user-center/01-clarify → user-center/02-design → user-center/03-task-split",
		"记录当前阶段、验收要求和证据",
		"- 可调用操作技能：user-center/add-api",
		"阶段产物完整；测试证据完整；交接说明明确",
		"先按 SOP 阶段链推进",
		"跨项目子任务规则",
		"--parent <当前 issue id>",
		"--project <目标项目 id>",
		"不要额外传 `--assignee` 或 `--assignee-id`",
		"自动交给项目负责人",
		"目标项目=gateway",
		"目标项目=config",
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

	// Helper Two has no role — must NOT render an empty role: "" segment.
	if strings.Contains(out, `Helper Two — agent, role: ""`) {
		t.Errorf("expected empty role to be omitted, got: %s", out)
	}
}

func TestBuildSquadSOPProfile_MulticaCodingFields(t *testing.T) {
	out := buildSquadSOPProfile([]byte(`{
		"profile_key":"multica-coding",
		"project":"multica",
		"repo":"/data/ida/goal-test",
		"mode":"coding_squad",
		"roles":[{"key":"captain","name":"队长","responsibility":"接需求、判断流程、拆任务。"}],
		"steps":[{"key":"receive","name":"接收需求","role_key":"captain"},{"key":"design_review","name":"方案设计与确认","role_key":"designer"}],
		"model_policy":{"默认提供方":"codex","默认模型":"gpt-5.3-codex-spark","降级模型":"gpt-5.4-mini","代码测试复杂审查":"Codex/gpt 类模型"},
		"acceptance":["验收者独立给结论"],
		"forbidden_actions":["泄露密钥","未独立验收就完成"]
	}`))
	for _, want := range []string{
		"模板：multica-coding",
		"项目：multica",
		"仓库：`/data/ida/goal-test`",
		"SOP 步骤链：接收需求 → 方案设计与确认",
		"当前默认阶段：接收需求",
		"角色分工：队长：接需求、判断流程、拆任务。",
		"默认提供方=codex",
		"默认模型=gpt-5.3-codex-spark",
		"降级模型=gpt-5.4-mini",
		"代码测试复杂审查=Codex/gpt 类模型",
		"禁止事项：泄露密钥；未独立验收就完成",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected SOP briefing to contain %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestInternalUserCenterTemplateIncludesCrossProjectChildIssuePlan(t *testing.T) {
	template, ok := internalSquadTemplateByKey("user-center")
	if !ok {
		t.Fatal("user-center internal squad template missing")
	}
	raw, err := json.Marshal(template.Profile)
	if err != nil {
		t.Fatalf("marshal template profile: %v", err)
	}
	out := buildSquadSOPProfile(raw)
	for _, want := range []string{
		"模板：user-center-sop-flow",
		"跨项目子任务规则",
		"目标项目=gateway",
		"目标项目=config",
		"--parent <当前 issue id>",
		"--project <目标项目 id>",
		"multica project list --output json",
		"不要额外传 `--assignee` 或 `--assignee-id`",
		"自动交给项目负责人",
		"不要再为同一项工作 @mention 同一个负责人",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected user-center SOP briefing to contain %q\n--- output ---\n%s", want, out)
		}
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
