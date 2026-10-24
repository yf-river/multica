package handler

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/review"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// GONG-83：任务级委托授权继承。
//
// 线上故障是「审查 / 系统反馈把协调者唤醒成一条没有人类发起人的运行」，这条运行再
// 想把自己范围内的活交给用户的私有开发 Agent 时，链顶没有人可归因，invoke 门禁
// 失败关闭——即使这位人类前不久刚刚在同一条看板任务上授权过一模一样的委托。
//
// 下面钉住两件事：这条续接在「有证据、同范围、原授权者仍有权限」时真的放行（旧行为
// 是 403），以及它没有被悄悄放大成通用放权——任务范围、目标 Agent、委托者、租户、
// 运行状态、原授权者资格任一不成立都失败关闭，且继承来的授权绝不写进归因。

var taskDelegationFixtureSeq atomic.Int64

// taskDelegationScope 是一次委托发生的场景底座：人类授权人 U、协调者 Agent（Mika
// 形态）、U 拥有的私有开发 Agent，以及一条人类建给协调者的看板任务 S。
type taskDelegationScope struct {
	AuthorizerID  string // 人类授权人 U：任务发起人，也是私有开发 Agent 的 owner
	CoordinatorID string // 协调者 Agent：被系统 / 审查反馈唤醒的那一个
	WorkerID      string // U 拥有的私有开发 Agent：真正的执行者
	ScopeIssueID  string // 看板任务 S
	RuntimeID     string
}

func newTaskDelegationScope(t *testing.T, workerID, authorizerID string) *taskDelegationScope {
	t.Helper()
	runtimeID := handlerTestRuntimeID(t)
	coordinatorID := dbfx.Agent(t,
		fmt.Sprintf("gong83-coordinator-%d", taskDelegationFixtureSeq.Add(1)), runtimeID)
	scopeIssueID := newDelegationIssue(t, "GONG-83 board task "+t.Name(), testutil.Cols{
		"creator_type": "member",
		"creator_id":   authorizerID,
	})
	return &taskDelegationScope{
		AuthorizerID:  authorizerID,
		CoordinatorID: coordinatorID,
		WorkerID:      workerID,
		ScopeIssueID:  scopeIssueID,
		RuntimeID:     runtimeID,
	}
}

// newDelegationIssue 建一条 issue，并先把工作区编号推进到最新——直接插入行不会
// 推进 workspace.issue_counter，之后经服务创建的 issue 会和它撞号。
func newDelegationIssue(t *testing.T, title string, cols testutil.Cols) string {
	t.Helper()
	nextWorkspaceIssueNumber(t)
	return dbfx.Issue(t, title, cols)
}

// seedTaskOn 排一条落在 issueID 上的任务，over 覆盖状态与归因列。
func seedTaskOn(t *testing.T, agentID, runtimeID, issueID string, over testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{
		"agent_id":   agentID,
		"runtime_id": runtimeID,
		"issue_id":   issueID,
		"status":     "running",
		"priority":   0,
	}
	for k, v := range over {
		cols[k] = v
	}
	return dbfx.Task(t, agentID, cols)
}

// taskDelegationBase 是拒绝矩阵的公共前置：S 下的一条子任务、授权运行、证据任务，
// 以及那条没有人类发起人的唤醒运行。
type taskDelegationBase struct {
	*taskDelegationScope
	ChildIssueID    string // S 的直接子任务：证据任务落在这里
	AuthorizedRunID string // 协调者在 S 上、带人类 originator=U 的运行
	EvidenceTaskID  string // 开发 Agent 在子任务上、delegated_from 指向授权运行的任务
	WakeRunID       string // 系统 / 审查反馈唤醒的运行：originator 为空
}

func newTaskDelegationBase(t *testing.T) *taskDelegationBase {
	t.Helper()
	workerID, authorizerID, _ := privateAgentTestFixture(t)
	sc := newTaskDelegationScope(t, workerID, authorizerID)
	childIssueID := newDelegationIssue(t, "GONG-83 child "+t.Name(), testutil.Cols{
		"creator_type":    "agent",
		"creator_id":      sc.CoordinatorID,
		"parent_issue_id": sc.ScopeIssueID,
	})

	authorizedRunID := seedTaskOn(t, sc.CoordinatorID, sc.RuntimeID, sc.ScopeIssueID, testutil.Cols{
		"originator_user_id":  authorizerID,
		"accountable_user_id": authorizerID,
		"originator_source":   "direct_human",
	})
	evidenceTaskID := seedTaskOn(t, sc.WorkerID, sc.RuntimeID, childIssueID, testutil.Cols{
		"status":                 "completed",
		"delegated_from_task_id": authorizedRunID,
		"originator_user_id":     authorizerID,
		"accountable_user_id":    authorizerID,
		"originator_source":      "delegation",
	})

	// 唤醒运行复刻生产 shape：originator 为空、来源记为 delegation、delegated_from
	// 指向把它唤醒的那条任务。accountable 是被复制下来的人类 U——所以这条运行本身
	// 也不能被当作「人类授权」的证据（查询要求 originator 非空）。
	reviewerTaskID := seedTaskOn(t, sc.CoordinatorID, sc.RuntimeID, sc.ScopeIssueID, testutil.Cols{
		"status":            "completed",
		"originator_source": "unattributed",
	})
	wakeRunID := seedTaskOn(t, sc.CoordinatorID, sc.RuntimeID, sc.ScopeIssueID, testutil.Cols{
		"accountable_user_id":    authorizerID,
		"originator_source":      "delegation",
		"delegated_from_task_id": reviewerTaskID,
	})

	return &taskDelegationBase{
		taskDelegationScope: sc,
		ChildIssueID:        childIssueID,
		AuthorizedRunID:     authorizedRunID,
		EvidenceTaskID:      evidenceTaskID,
		WakeRunID:           wakeRunID,
	}
}

// authority 直接调用判定体，返回 (授权人类, 拒绝原因码)。
func (b *taskDelegationBase) authority(t *testing.T, runID, scopeIssueID, targetID, actorType, actorID, workspaceID string) (string, string) {
	t.Helper()
	ctx := context.Background()
	run, err := testHandler.Queries.GetAgentTask(ctx, util.MustParseUUID(runID))
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(scopeIssueID))
	if err != nil {
		t.Fatalf("load scope issue: %v", err)
	}
	target, err := testHandler.Queries.GetAgent(ctx, util.MustParseUUID(targetID))
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	return testHandler.taskDelegationAuthority(ctx, run, issue, target, actorType, actorID, workspaceID)
}

// wakeAuthority 是矩阵的默认输入：系统唤醒的那条运行，在 S 上指派私有 worker。
func (b *taskDelegationBase) wakeAuthority(t *testing.T) (string, string) {
	t.Helper()
	return b.authority(t, b.WakeRunID, b.ScopeIssueID, b.WorkerID, "agent", b.CoordinatorID, testWorkspaceID)
}

// replayAuthority 是同一个输入走重放形态：服务端重新处理一条已存评论，而不是一条新
// 请求。
func (b *taskDelegationBase) replayAuthority(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	run, err := testHandler.Queries.GetAgentTask(ctx, util.MustParseUUID(b.WakeRunID))
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(b.ScopeIssueID))
	if err != nil {
		t.Fatalf("load scope issue: %v", err)
	}
	target, err := testHandler.Queries.GetAgent(ctx, util.MustParseUUID(b.WorkerID))
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	return testHandler.taskDelegationAuthorityForReplay(ctx, run, issue, target, "agent", b.CoordinatorID, testWorkspaceID)
}

// coordinatorMentionComment 让协调者经真实创建路径发一条 @开发 Agent 的评论，返回
// 评论 id 与该提及的按目标结果。
func coordinatorMentionComment(t *testing.T, b *taskDelegationBase, authorTaskID string) (string, *CommentTriggerOutcome) {
	t.Helper()
	r := newRequest(http.MethodPost, "/api/issues/"+b.ScopeIssueID+"/comments", map[string]any{
		"content": "[@Worker](mention://agent/" + b.WorkerID + ") please take this",
	})
	r.Header.Set("X-Agent-ID", b.CoordinatorID)
	r.Header.Set("X-Task-ID", authorTaskID)
	r = withURLParam(r, "id", b.ScopeIssueID)
	var resp CommentResponse
	testutil.Call(t, testHandler.CreateComment, r).Want(http.StatusCreated).JSON(&resp)
	for i := range resp.TriggerOutcomes {
		if resp.TriggerOutcomes[i].TargetID == b.WorkerID {
			return resp.ID, &resp.TriggerOutcomes[i]
		}
	}
	t.Fatalf("no trigger outcome for the mentioned worker, got %+v", resp.TriggerOutcomes)
	return "", nil
}

// queuedWorkerTasks 数 (issue, worker) 上排队的任务。
func queuedWorkerTasks(t *testing.T, issueID, agentID string) int {
	t.Helper()
	return dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'
	`, issueID, agentID)
}

// delegationChildIssueRequest 是协调者「在 S 下建子任务并指派开发 Agent」的请求。
func delegationChildIssueRequest(t *testing.T, parentID, workerID, actorAgentID, taskID string) *http.Request {
	t.Helper()
	r := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":           "GONG-83 delegated child " + t.Name(),
		"status":          "todo",
		"priority":        "not_important_not_urgent",
		"assignee_type":   "agent",
		"assignee_id":     workerID,
		"parent_issue_id": parentID,
		"allow_duplicate": true,
	})
	r.Header.Set("X-Agent-ID", actorAgentID)
	r.Header.Set("X-Task-ID", taskID)
	return r
}

// latestWorkerTask 读回 (issue, agent) 上最近一条任务的归因列。
func latestWorkerTask(t *testing.T, issueID, agentID string) (taskID string, originator, source, accountable, delegatedFrom pgtype.Text) {
	t.Helper()
	dbfx.QueryRow(t, `
		SELECT id,
		       originator_user_id::text,
		       originator_source,
		       accountable_user_id::text,
		       delegated_from_task_id::text
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, issueID, agentID).Scan(&taskID, &originator, &source, &accountable, &delegatedFrom)
	return taskID, originator, source, accountable, delegatedFrom
}

// TestTaskDelegationAuthority_SystemWakeResumesPrivateWorker 是原故障的回归：同一个
// 请求在没有历史委托时 403（旧行为），建立一次人类授权的委托之后由系统唤醒的同一
// 请求 201，且新任务保持「系统来源」的归因。
func TestTaskDelegationAuthority_SystemWakeResumesPrivateWorker(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	workerID, authorizerID, _ := privateAgentTestFixture(t)
	sc := newTaskDelegationScope(t, workerID, authorizerID)

	// 系统 / 审查反馈唤醒：链顶没有人类。
	wakeRunID := seedTaskOn(t, sc.CoordinatorID, sc.RuntimeID, sc.ScopeIssueID, testutil.Cols{
		"accountable_user_id": authorizerID,
		"originator_source":   "delegation",
	})

	// 1. 还没有任何历史委托：与线上故障一致，私有开发 Agent 被拒。
	testutil.Call(t, testHandler.CreateIssue,
		delegationChildIssueRequest(t, sc.ScopeIssueID, workerID, sc.CoordinatorID, wakeRunID),
	).Want(http.StatusForbidden)

	// 2. 人类授权的运行（真实 originator）在同一条任务下建子任务并指派同一个开发
	// Agent——这次是既有行为，成功并把证据写进任务行。
	authorizedRunID := seedTaskOn(t, sc.CoordinatorID, sc.RuntimeID, sc.ScopeIssueID, testutil.Cols{
		"originator_user_id":  authorizerID,
		"accountable_user_id": authorizerID,
		"originator_source":   "direct_human",
	})
	var firstChild IssueResponse
	testutil.Call(t, testHandler.CreateIssue,
		delegationChildIssueRequest(t, sc.ScopeIssueID, workerID, sc.CoordinatorID, authorizedRunID),
	).Want(http.StatusCreated).JSON(&firstChild)
	cleanupAutopilotChildIssue(t, firstChild.ID)

	_, originator, source, accountable, delegatedFrom := latestWorkerTask(t, firstChild.ID, workerID)
	if originator.String != authorizerID || source.String != "delegation" || delegatedFrom.String != authorizedRunID {
		t.Fatalf("human-authorized delegation must record originator=%s source=delegation delegated_from=%s, got originator=%q source=%q delegated_from=%q",
			authorizerID, authorizedRunID, originator.String, source.String, delegatedFrom.String)
	}

	// 3. 同一个系统唤醒的运行再委托一次：旧代码 403，现在放行。
	var secondChild IssueResponse
	testutil.Call(t, testHandler.CreateIssue,
		delegationChildIssueRequest(t, sc.ScopeIssueID, workerID, sc.CoordinatorID, wakeRunID),
	).Want(http.StatusCreated).JSON(&secondChild)
	cleanupAutopilotChildIssue(t, secondChild.ID)

	// 4. 授权只进闸门：新任务仍是「系统来源」，originator 保持为空。
	_, originator, source, accountable, delegatedFrom = latestWorkerTask(t, secondChild.ID, workerID)
	if originator.Valid {
		t.Fatalf("inherited authority must not be written into attribution; originator=%q", originator.String)
	}
	if source.String != "delegation" {
		t.Fatalf("the delegated task must keep its descent label, got originator_source=%q", source.String)
	}
	if accountable.String != authorizerID {
		t.Fatalf("accountable human must stay the copied parent value %s, got %q", authorizerID, accountable.String)
	}
	if delegatedFrom.String != wakeRunID {
		t.Fatalf("delegated_from must stay the waking run %s, got %q", wakeRunID, delegatedFrom.String)
	}
}

// TestTaskDelegationAuthority_RejectionMatrix 逐条钉住「不放宽」的边界。
func TestTaskDelegationAuthority_RejectionMatrix(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	t.Run("happy path resolves the original authorizer", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		got, reason := b.wakeAuthority(t)
		if got != b.AuthorizerID || reason != "" {
			t.Fatalf("expected authorizer %s with no denial reason, got %q / %q", b.AuthorizerID, got, reason)
		}
	})

	t.Run("no historical delegation", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		// 删掉唯一的委托证据：这条任务范围上从未发生过人类授权的委托。
		dbfx.Exec(t, `DELETE FROM agent_task_queue WHERE id = $1`, b.EvidenceTaskID)

		got, reason := b.wakeAuthority(t)
		if got != "" || reason != "no_authorization_evidence" {
			t.Fatalf("a scope with no delegation of its own must not resolve authority, got %q / %q", got, reason)
		}
	})

	t.Run("delegation belongs to another agent", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		strangerID := dbfx.Agent(t, fmt.Sprintf("gong83-stranger-%d", taskDelegationFixtureSeq.Add(1)), b.RuntimeID)
		dbfx.Exec(t, `UPDATE agent_task_queue SET agent_id = $1 WHERE id = $2`, strangerID, b.EvidenceTaskID)

		got, reason := b.wakeAuthority(t)
		if got != "" || reason != "no_authorization_evidence" {
			t.Fatalf("evidence for a different target agent must not authorize this one, got %q / %q", got, reason)
		}
	})

	t.Run("delegation belongs to another task outside the scope", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		outsideIssueID := newDelegationIssue(t, "GONG-83 unrelated "+t.Name(), testutil.Cols{
			"creator_type": "member",
			"creator_id":   b.AuthorizerID,
		})
		dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id = $1 WHERE id = $2`, outsideIssueID, b.EvidenceTaskID)

		got, reason := b.wakeAuthority(t)
		if got != "" || reason != "no_authorization_evidence" {
			t.Fatalf("evidence from an unrelated task must not authorize this scope, got %q / %q", got, reason)
		}
	})

	t.Run("scope in another workspace", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		foreignWorkspaceID := dbfx.Workspace(t, "GONG-83 Foreign", fmt.Sprintf("gong83-foreign-%d", taskDelegationFixtureSeq.Add(1)))

		got, reason := b.authority(t, b.WakeRunID, b.ScopeIssueID, b.WorkerID, "agent", b.CoordinatorID, foreignWorkspaceID)
		if got != "" || reason != "scope_workspace_mismatch" {
			t.Fatalf("the scope task must belong to the requesting workspace, got %q / %q", got, reason)
		}
	})

	for _, finished := range []string{"completed", "cancelled"} {
		t.Run("waking run already "+finished, func(t *testing.T) {
			b := newTaskDelegationBase(t)
			dbfx.Exec(t, `UPDATE agent_task_queue SET status = $1 WHERE id = $2`, finished, b.WakeRunID)

			got, reason := b.wakeAuthority(t)
			if got != "" || reason != "run_not_running" {
				t.Fatalf("a %s run must not keep lending authority, got %q / %q", finished, got, reason)
			}
		})
	}

	t.Run("X-Task-ID belongs to another agent", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		// resolveActor 会把「任务不属于声称的 agent」降级成 member 主体，判定体在这里
		// 也必须失败关闭，而不是把 agent 身份的残留当成委托者在跑。
		got, reason := b.authority(t, b.WakeRunID, b.ScopeIssueID, b.WorkerID, "member", b.AuthorizerID, testWorkspaceID)
		if got != "" || reason != "not_agent_actor" {
			t.Fatalf("a member actor must never inherit task delegation authority, got %q / %q", got, reason)
		}
	})

	t.Run("waking run sits on another issue", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		elsewhereIssueID := newDelegationIssue(t, "GONG-83 elsewhere "+t.Name(), testutil.Cols{
			"creator_type": "member",
			"creator_id":   b.AuthorizerID,
		})
		dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id = $1 WHERE id = $2`, elsewhereIssueID, b.WakeRunID)

		got, reason := b.wakeAuthority(t)
		if got != "" || reason != "scope_mismatch" {
			t.Fatalf("a run working on another issue must not inherit this scope, got %q / %q", got, reason)
		}
	})

	for _, closed := range []string{"done", "cancelled"} {
		t.Run("scope task already "+closed, func(t *testing.T) {
			b := newTaskDelegationBase(t)
			dbfx.Exec(t, `UPDATE issue SET status = $1 WHERE id = $2`, closed, b.ScopeIssueID)

			got, reason := b.wakeAuthority(t)
			if got != "" || reason != "issue_inactive" {
				t.Fatalf("a %s scope task must end the inherited authority, got %q / %q", closed, got, reason)
			}
		})
	}

	t.Run("original authorizer left the workspace", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		dbfx.Exec(t, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, b.AuthorizerID)

		got, reason := b.wakeAuthority(t)
		if got != "" || reason != "authorizer_not_member" {
			t.Fatalf("a removed authorizer must not keep lending authority, got %q / %q", got, reason)
		}
	})

	t.Run("original authorizer lost invoke rights on the target", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		// 私有 Agent 换 owner：原授权者不再能调用它，历史授权随之失效。owner 必须在
		// 新用户的清理之前复原，否则删 user 会被 agent.owner_id 的外键挡住，残留行会
		// 污染下一次运行。
		outsiderID := newDelegationOutsiderMember(t)
		dbfx.Cleanup(t, `UPDATE agent SET owner_id = $1 WHERE id = $2`, b.AuthorizerID, b.WorkerID)
		dbfx.Exec(t, `UPDATE agent SET owner_id = $1 WHERE id = $2`, outsiderID, b.WorkerID)

		got, reason := b.wakeAuthority(t)
		if got != "" || reason != "authorizer_lacks_permission" {
			t.Fatalf("a withdrawn grant must deny, got %q / %q", got, reason)
		}
	})

	t.Run("managed reviewer stays unreachable", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		reviewerID := dbfx.Agent(t,
			fmt.Sprintf("gong83-reviewer-%d", taskDelegationFixtureSeq.Add(1)), b.RuntimeID,
			testutil.Cols{"system_key": review.SystemKey, "permission_mode": "public_to", "visibility": "workspace"})
		// 即使历史上「授权过」把它当目标，canInvokeAgent 的托管审查员闸门仍然先拒。
		dbfx.Exec(t, `UPDATE agent_task_queue SET agent_id = $1 WHERE id = $2`, reviewerID, b.EvidenceTaskID)

		got, reason := b.authority(t, b.WakeRunID, b.ScopeIssueID, reviewerID, "agent", b.CoordinatorID, testWorkspaceID)
		if got != "" || reason != "authorizer_lacks_permission" {
			t.Fatalf("the managed reviewer must stay closed to ordinary calls, got %q / %q", got, reason)
		}
	})
}

// newDelegationOutsiderMember 建一个与本测试任何 Agent 都无关的普通成员，用于
// 「私有 Agent 换了 owner」这类撤回场景。
func newDelegationOutsiderMember(t *testing.T) string {
	t.Helper()
	memberID := dbfx.User(t, "GONG-83 Plain Member",
		fmt.Sprintf("gong83-plain-%d@multica.test", taskDelegationFixtureSeq.Add(1)))
	dbfx.Member(t, testWorkspaceID, memberID, "member")
	return memberID
}

// TestTaskDelegationAuthority_TopLevelCreateStaysClosed 钉住「新顶层任务没有绑定
// 对象」：同一条被唤醒的运行可以续接 S 范围内的委托，但不能凭空获得新顶层任务的
// 权限。
func TestTaskDelegationAuthority_TopLevelCreateStaysClosed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	b := newTaskDelegationBase(t)

	r := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":           "GONG-83 top level " + t.Name(),
		"status":          "todo",
		"priority":        "not_important_not_urgent",
		"assignee_type":   "agent",
		"assignee_id":     b.WorkerID,
		"allow_duplicate": true,
	})
	r.Header.Set("X-Agent-ID", b.CoordinatorID)
	r.Header.Set("X-Task-ID", b.WakeRunID)
	testutil.Call(t, testHandler.CreateIssue, r).Want(http.StatusForbidden)
}

// TestTaskDelegationAuthority_CommentMentionPreviewMatchesDispatch 钉住入口一致性：
// 评论 @提及在预览与真实派发上用同一判定——有证据时两边都放行，没有证据时两边都拦截。
func TestTaskDelegationAuthority_CommentMentionPreviewMatchesDispatch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	mentionContent := func(workerID string) string {
		return "[@Worker](mention://agent/" + workerID + ") please take this"
	}
	previewAgents := func(t *testing.T, b *taskDelegationBase) []CommentTriggerAgentResponse {
		t.Helper()
		r := newRequest(http.MethodPost, "/api/issues/"+b.ScopeIssueID+"/comments/trigger-preview",
			map[string]any{"content": mentionContent(b.WorkerID)})
		r.Header.Set("X-Agent-ID", b.CoordinatorID)
		r.Header.Set("X-Task-ID", b.WakeRunID)
		r = withURLParam(r, "id", b.ScopeIssueID)
		var resp CommentTriggerPreviewResponse
		testutil.Call(t, testHandler.PreviewCommentTriggers, r).Want(http.StatusOK).JSON(&resp)
		return resp.Agents
	}
	dispatchMention := func(t *testing.T, b *taskDelegationBase) {
		t.Helper()
		r := newRequest(http.MethodPost, "/api/issues/"+b.ScopeIssueID+"/comments",
			map[string]any{"content": mentionContent(b.WorkerID)})
		r.Header.Set("X-Agent-ID", b.CoordinatorID)
		r.Header.Set("X-Task-ID", b.WakeRunID)
		r = withURLParam(r, "id", b.ScopeIssueID)
		testutil.Call(t, testHandler.CreateComment, r).Want(http.StatusCreated)
	}
	triggersWorker := func(agents []CommentTriggerAgentResponse, workerID string) bool {
		for _, a := range agents {
			if a.ID == workerID {
				return true
			}
		}
		return false
	}
	queuedWorkerTasks := func(t *testing.T, issueID, workerID string) int {
		t.Helper()
		return dbfx.Count(t, `
			SELECT count(*) FROM agent_task_queue
			WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'
		`, issueID, workerID)
	}

	t.Run("with evidence both allow", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		if agents := previewAgents(t, b); !triggersWorker(agents, b.WorkerID) {
			t.Fatalf("preview with a verified in-scope delegation must show the private worker, got %+v", agents)
		}
		dispatchMention(t, b)
		if n := queuedWorkerTasks(t, b.ScopeIssueID, b.WorkerID); n != 1 {
			t.Fatalf("the real dispatch must enqueue the private worker once, got %d", n)
		}
	})

	t.Run("without evidence both block", func(t *testing.T) {
		b := newTaskDelegationBase(t)
		dbfx.Exec(t, `UPDATE agent_task_queue SET originator_user_id = NULL WHERE id = $1`, b.EvidenceTaskID)
		if agents := previewAgents(t, b); triggersWorker(agents, b.WorkerID) {
			t.Fatalf("preview without evidence must not show the private worker, got %+v", agents)
		}
		dispatchMention(t, b)
		if n := queuedWorkerTasks(t, b.ScopeIssueID, b.WorkerID); n != 0 {
			t.Fatalf("without evidence the mention must not enqueue the private worker, got %d", n)
		}
	})
}

// TestCommentTriggerOptions_EffectiveInvokerPrecedence 钉住回退顺序：真实发起人 →
// autopilot 委派 → 任务级委托继承。继承排在最后，前两者永远优先。
func TestCommentTriggerOptions_EffectiveInvokerPrecedence(t *testing.T) {
	target := db.Agent{}

	fromTask := func(db.Agent) string { return "task-authorizer" }

	cases := []struct {
		name string
		opts commentTriggerComputeOptions
		want string
	}{
		{
			name: "real originator wins over everything",
			opts: commentTriggerComputeOptions{
				OriginatorUserID:                   "originator",
				AutopilotDelegationAuthorityUserID: "autopilot-creator",
				TaskDelegationAuthority:            fromTask,
			},
			want: "originator",
		},
		{
			name: "autopilot authority wins over task delegation",
			opts: commentTriggerComputeOptions{
				AutopilotDelegationAuthorityUserID: "autopilot-creator",
				TaskDelegationAuthority:            fromTask,
			},
			want: "autopilot-creator",
		},
		{
			name: "task delegation is the last resort",
			opts: commentTriggerComputeOptions{TaskDelegationAuthority: fromTask},
			want: "task-authorizer",
		},
		{
			name: "no resolver means no authority",
			opts: commentTriggerComputeOptions{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.effectiveInvokerFor(target); got != tc.want {
				t.Fatalf("effectiveInvokerFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTaskDelegationAuthority_ReplayAfterAuthorRunFinished 钉住被审查打回的那条时序：
// 协调者写完评论这一轮就结束，重放发生在目标 Agent 完成之后，作者运行早已不在
// running。新请求必须继续拒绝（已结束的运行不能为一条新请求放权），重放则按同一条
// 已存评论的血缘继续判定，且其余边界一条不少。
func TestTaskDelegationAuthority_ReplayAfterAuthorRunFinished(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	b := newTaskDelegationBase(t)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, b.WakeRunID)

	// 新请求：拒绝的理由仍是运行已结束。
	if got, reason := b.wakeAuthority(t); got != "" || reason != "run_not_running" {
		t.Fatalf("a finished run must not authorize a new request, got %q / %q", got, reason)
	}

	// 重放：同一条评论、同一份证据，放行。
	got, reason := b.replayAuthority(t)
	if got != b.AuthorizerID || reason != "" {
		t.Fatalf("the replay of an accepted comment must keep resolving %s, got %q / %q", b.AuthorizerID, got, reason)
	}

	// 重放不放宽证据：没有历史委托照样拒绝。
	dbfx.Exec(t, `DELETE FROM agent_task_queue WHERE id = $1`, b.EvidenceTaskID)
	if got, reason := b.replayAuthority(t); got != "" || reason != "no_authorization_evidence" {
		t.Fatalf("the replay must still require delegation evidence, got %q / %q", got, reason)
	}
}

// TestReconcileCommentsOnCompletion_TaskDelegationRestoresAuthority 是原故障的完整
// 时序：目标 Agent 正忙 → 创建时只能回报 deferred → 协调者这一轮结束、目标完成后完成
// 调和重放。修复前这条重放用 running 重新判定继承授权，@提及被静默丢弃。
func TestReconcileCommentsOnCompletion_TaskDelegationRestoresAuthority(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	b := newTaskDelegationBase(t)

	busyTaskID := seedTaskOn(t, b.WorkerID, b.RuntimeID, b.ScopeIssueID, testutil.Cols{"status": "dispatched"})
	_, outcome := coordinatorMentionComment(t, b, b.WakeRunID)
	if outcome.Status != DispatchDeferred {
		t.Fatalf("a busy target must report the success shape deferred, got %q/%q", outcome.Status, outcome.ReasonCode)
	}

	// 协调者写完这句就结束，目标 Agent 随后完成——重放时两条运行都不在 running。
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, b.WakeRunID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, busyTaskID)
	workerTask, err := testHandler.Queries.GetAgentTask(ctx, util.MustParseUUID(busyTaskID))
	if err != nil {
		t.Fatalf("load worker task: %v", err)
	}

	testHandler.reconcileCommentsOnCompletion(ctx, &workerTask)

	if n := queuedWorkerTasks(t, b.ScopeIssueID, b.WorkerID); n != 1 {
		t.Fatalf("the deferred mention must be replayed into exactly one follow-up, got %d", n)
	}
	// 继承来的授权只进闸门：续接的任务仍是没有人类发起人的系统来源。
	var originatorValid bool
	var source pgtype.Text
	if err := testPool.QueryRow(ctx, `
		SELECT originator_user_id IS NOT NULL, originator_source
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'
	`, b.ScopeIssueID, b.WorkerID).Scan(&originatorValid, &source); err != nil {
		t.Fatalf("read follow-up attribution: %v", err)
	}
	if originatorValid {
		t.Fatal("the replayed follow-up must stay unattributed; inherited authority is authorization-only")
	}
	if source.String != "delegation" {
		t.Fatalf("the replayed follow-up must keep its delegation descent, got %q", source.String)
	}
}

// TestRetriggerCancelledTaskSurvivors_TaskDelegationRestoresAuthority 覆盖另一条重放：
// 取消批次的幸存者评论在批次被取消后重新派发，此时写下这条评论的运行同样已经结束。
func TestRetriggerCancelledTaskSurvivors_TaskDelegationRestoresAuthority(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	b := newTaskDelegationBase(t)

	busyTaskID := seedTaskOn(t, b.WorkerID, b.RuntimeID, b.ScopeIssueID, testutil.Cols{"status": "dispatched"})
	commentID, outcome := coordinatorMentionComment(t, b, b.WakeRunID)
	if outcome.Status != DispatchDeferred {
		t.Fatalf("a busy target must report the success shape deferred, got %q/%q", outcome.Status, outcome.ReasonCode)
	}
	// 承载这条评论的批次被取消：任务连同它的输入一起交给幸存者重放。
	dbfx.Exec(t, `
		UPDATE agent_task_queue
		SET status = 'cancelled', coalesced_comment_ids = ARRAY[$1]::uuid[]
		WHERE id = $2
	`, commentID, busyTaskID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, b.WakeRunID)

	cancelledTask, err := testHandler.Queries.GetAgentTask(ctx, util.MustParseUUID(busyTaskID))
	if err != nil {
		t.Fatalf("load cancelled task: %v", err)
	}
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(b.ScopeIssueID))
	if err != nil {
		t.Fatalf("load scope issue: %v", err)
	}

	testHandler.retriggerCancelledTaskSurvivors(ctx, issue, []db.AgentTaskQueue{cancelledTask}, pgtype.UUID{})

	if n := queuedWorkerTasks(t, b.ScopeIssueID, b.WorkerID); n != 1 {
		t.Fatalf("the cancelled survivor must be replayed into exactly one task, got %d", n)
	}
}
