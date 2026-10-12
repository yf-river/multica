package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/cooking"
	"github.com/multica-ai/multica/server/internal/meals"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type mealScope struct {
	user, workspace string
	agent           bool
}

func (h *Handler) mealScope(w http.ResponseWriter, r *http.Request) (mealScope, bool) {
	user, ok := requireUserID(w, r)
	if !ok {
		return mealScope{}, false
	}
	ws, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return mealScope{}, false
	}
	workspace := uuidToString(ws)
	if _, err := h.getWorkspaceMember(r.Context(), user, workspace); err != nil {
		writeError(w, 404, "饮食内容不存在")
		return mealScope{}, false
	}
	actor, agent := h.resolveActor(r, user, workspace)
	s := mealScope{user: user, workspace: workspace, agent: actor == "agent"}
	if actor == "member" {
		return s, true
	}
	if actor != "agent" {
		writeError(w, 403, "需要本人授权")
		return s, false
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task_id")
	if !ok {
		return s, false
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: ws})
	if err != nil || task.Status != "running" || uuidToString(task.AgentID) != agent {
		writeError(w, 403, "需要本人发起的运行中任务")
		return s, false
	}
	owner := ""
	if task.InitiatorUserID.Valid {
		owner = uuidToString(task.InitiatorUserID)
	} else if task.ChatSessionID.Valid {
		session, e := h.Queries.GetChatSession(r.Context(), task.ChatSessionID)
		if e == nil {
			owner = uuidToString(session.CreatorID)
		}
	} else if task.IssueID.Valid {
		issue, e := h.Queries.GetIssue(r.Context(), task.IssueID)
		if e == nil && issue.CreatorType == "member" {
			owner = uuidToString(issue.CreatorID)
		}
	}
	if owner != user {
		writeError(w, 403, "任务无法归属到本人")
		return s, false
	}
	return s, true
}
func mealRead(ctx context.Context, q dbExecutor, s mealScope, lock bool) (meals.State, int64, error) {
	var state meals.State
	var revision int64
	var data []byte
	sql := `SELECT data,revision FROM meal_workspace WHERE workspace_id=$1 AND user_id=$2`
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, s.workspace, s.user).Scan(&data, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		state.Init()
		return state, 0, nil
	}
	if err != nil {
		return state, 0, err
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, 0, err
	}
	state.Init()
	return state, revision, nil
}
func mealWrite(ctx context.Context, tx pgx.Tx, s mealScope, state meals.State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE meal_workspace SET data=$3,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND user_id=$2`, s.workspace, s.user, data)
	return err
}
func mealFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, meals.ErrConflict) {
		writeError(w, 409, err.Error())
	} else {
		writeError(w, 500, "暂时无法保存饮食内容，请重试")
	}
}
func (h *Handler) mealResponse(w http.ResponseWriter, r *http.Request, s mealScope, state meals.State, revision int64) {
	statuses := map[string]string{}
	for _, link := range state.Tasks {
		var status string
		err := h.DB.QueryRow(r.Context(), `SELECT status FROM issue WHERE id=$1 AND workspace_id=$2`, link.IssueID, s.workspace).Scan(&status)
		if err == nil {
			statuses[link.IssueID] = status
		} else if errors.Is(err, pgx.ErrNoRows) {
			statuses[link.IssueID] = "missing"
		} else {
			mealFailure(w, err)
			return
		}
	}
	var healthRevision int64
	err := h.DB.QueryRow(r.Context(), `SELECT revision FROM health_state WHERE workspace_id=$1 AND user_id=$2`, s.workspace, s.user).Scan(&healthRevision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		mealFailure(w, err)
		return
	}
	state.Receipts = nil
	writeJSON(w, 200, map[string]any{"state": state, "revision": revision, "health_revision": healthRevision, "task_statuses": statuses})
}
func (h *Handler) GetMeals(w http.ResponseWriter, r *http.Request) {
	s, ok := h.mealScope(w, r)
	if !ok {
		return
	}
	state, revision, err := mealRead(r.Context(), h.DB, s, false)
	if err != nil {
		mealFailure(w, err)
		return
	}
	h.mealResponse(w, r, s, state, revision)
}
func (h *Handler) MealAction(w http.ResponseWriter, r *http.Request) {
	s, ok := h.mealScope(w, r)
	if !ok {
		return
	}
	var c meals.Command
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		writeError(w, 400, "饮食请求格式无效")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "饮食请求包含多余内容")
		return
	}
	if s.agent && c.Action != "draft_save" {
		writeError(w, 403, "助手只能提交菜单建议，确认与实际记录需要本人执行")
		return
	}
	if _, ok := parseUUIDOrBadRequest(w, c.RequestID, "request_id"); !ok {
		return
	}
	if c.Action == "task_link" {
		h.linkMealTask(w, r, s, c)
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		mealFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `INSERT INTO meal_workspace(workspace_id,user_id) VALUES($1,$2) ON CONFLICT(workspace_id,user_id) DO NOTHING`, s.workspace, s.user); err != nil {
		mealFailure(w, err)
		return
	}
	state, revision, err := mealRead(r.Context(), tx, s, true)
	if err != nil {
		mealFailure(w, err)
		return
	}
	replayed, err := state.Replayed(c)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if replayed {
		h.mealResponse(w, r, s, state, revision)
		return
	}
	if revision != c.Revision {
		mealFailure(w, meals.ErrConflict)
		return
	}
	requestDigest := c.Digest()
	validateRecipe := func(e *meals.Entry) error {
		if e.RecipeID == "" {
			return nil
		}
		recipe, found := cooking.Default.Get(e.RecipeID)
		if !found || recipe.Kind != "recipe" {
			return errors.New("菜谱已移除，请重新选择")
		}
		if e.Mode == "home" && len(e.Foods) == 0 {
			e.Foods = meals.RecipeFoods(recipe.Ingredients)
		}
		return nil
	}
	if c.Action == "entry_save" {
		if err = validateRecipe(&c.Entry); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	if c.Action == "draft_save" {
		for i := range c.Draft.Entries {
			if err = validateRecipe(&c.Draft.Entries[i]); err != nil {
				writeError(w, 400, err.Error())
				return
			}
		}
		var current int64
		err = tx.QueryRow(r.Context(), `SELECT revision FROM health_state WHERE workspace_id=$1 AND user_id=$2`, s.workspace, s.user).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			mealFailure(w, err)
			return
		}
		if s.agent && c.Draft.HealthRevision != current {
			writeError(w, 409, "身体数据已变化，请重新读取后生成菜单建议")
			return
		}
	}
	if err = state.Apply(c); err != nil {
		if errors.Is(err, meals.ErrConflict) {
			mealFailure(w, err)
		} else {
			writeError(w, 400, err.Error())
		}
		return
	}
	state.Init()
	state.Receipts = append(state.Receipts, meals.Receipt{ID: c.RequestID, Digest: requestDigest})
	if len(state.Receipts) > 2048 {
		state.Receipts = state.Receipts[len(state.Receipts)-2048:]
	}
	if err = mealWrite(r.Context(), tx, s, state); err != nil {
		mealFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		mealFailure(w, err)
		return
	}
	h.mealResponse(w, r, s, state, revision+1)
}

var errMealTaskLinked = errors.New("meal task already linked")

func (h *Handler) linkMealTask(w http.ResponseWriter, r *http.Request, s mealScope, c meals.Command) {
	state, revision, err := mealRead(r.Context(), h.DB, s, false)
	if err != nil {
		mealFailure(w, err)
		return
	}
	key := "shopping:" + c.Week
	title := "采购食材 · " + c.Week
	description := "采购清单保存在饮食页。采购状态请在清单中记录，任务状态在任务面板维护。"
	if c.ID != "" {
		key = "prep:" + c.ID
		found := false
		for _, p := range state.Plans {
			if p.ID == c.ID {
				title = "集中备餐 · " + p.Name
				description = "制作完成后，在饮食页记录实际入库。"
				found = true
			}
		}
		if !found {
			writeError(w, 404, "备餐计划不存在")
			return
		}
	} else if !meals.ValidDate(c.Week) || meals.Week(c.Week) != c.Week {
		writeError(w, 400, "采购周无效")
		return
	}
	for _, link := range state.Tasks {
		if link.Key == key {
			var exists bool
			if err = h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1 AND workspace_id=$2)`, link.IssueID, s.workspace).Scan(&exists); err != nil {
				mealFailure(w, err)
				return
			}
			if exists {
				h.mealResponse(w, r, s, state, revision)
				return
			}
		}
	}
	related := func(ctx context.Context, tx pgx.Tx, issue db.Issue) error {
		if _, e := tx.Exec(ctx, `INSERT INTO meal_workspace(workspace_id,user_id) VALUES($1,$2) ON CONFLICT(workspace_id,user_id) DO NOTHING`, s.workspace, s.user); e != nil {
			return e
		}
		latest, rev, e := mealRead(ctx, tx, s, true)
		if e != nil {
			return e
		}
		for i, l := range latest.Tasks {
			if l.Key == key {
				var exists bool
				if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue WHERE id=$1 AND workspace_id=$2)`, l.IssueID, s.workspace).Scan(&exists); e != nil {
					return e
				}
				if exists {
					return errMealTaskLinked
				}
				latest.Tasks = append(latest.Tasks[:i], latest.Tasks[i+1:]...)
				break
			}
		}
		if rev != c.Revision {
			return meals.ErrConflict
		}
		latest.Tasks = append(latest.Tasks, meals.TaskLink{Key: key, IssueID: uuidToString(issue.ID)})
		return mealWrite(ctx, tx, s, latest)
	}
	if c.IssueID != "" {
		issue, ok := h.loadIssueForUser(w, r, c.IssueID)
		if !ok {
			return
		}
		if uuidToString(issue.WorkspaceID) != s.workspace {
			writeError(w, 404, "任务不存在")
			return
		}
		tx, e := h.TxStarter.Begin(r.Context())
		if e != nil {
			mealFailure(w, e)
			return
		}
		defer tx.Rollback(r.Context())
		err = related(r.Context(), tx, issue)
		if err == nil {
			err = tx.Commit(r.Context())
		}
	} else {
		_, err = h.IssueService.Create(r.Context(), service.IssueCreateParams{WorkspaceID: parseUUID(s.workspace), CreatorID: parseUUID(s.user), CreatorType: "member", Title: title, Description: pgtype.Text{String: description, Valid: true}, Status: "todo", Priority: "important_not_urgent", AllowDuplicate: true, AssigneeType: pgtype.Text{String: "member", Valid: true}, AssigneeID: parseUUID(s.user)}, service.IssueCreateOpts{ActorID: s.user, RelatedRecord: related})
	}
	if err != nil && !errors.Is(err, errMealTaskLinked) {
		if strings.Contains(err.Error(), "status") {
			writeError(w, 400, "任务状态暂不可用，请在任务面板检查设置")
			return
		}
		mealFailure(w, err)
		return
	}
	state, revision, err = mealRead(r.Context(), h.DB, s, false)
	if err != nil {
		mealFailure(w, err)
		return
	}
	h.mealResponse(w, r, s, state, revision)
}
