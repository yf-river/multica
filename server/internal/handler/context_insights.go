package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type insightFrame struct {
	Type            string          `json:"type"`
	SessionID       string          `json:"session_id"`
	DaemonID        string          `json:"daemon_id"`
	ParentSessionID string          `json:"parent_session_id"`
	Seq             int64           `json:"seq"`
	Cwd             string          `json:"cwd"`
	Projections     json.RawMessage `json:"projections"`
	Event           struct {
		Seq  int64  `json:"seq"`
		Time int64  `json:"time"`
		Type string `json:"type"`
	} `json:"event"`
}

type insightSession struct {
	ID              string          `json:"id"`
	SessionID       string          `json:"session_id"`
	DaemonID        string          `json:"daemon_id"`
	ParentSessionID string          `json:"parent_session_id"`
	WorkspaceID     string          `json:"workspace_id"`
	ChatID          string          `json:"chat_session_id"`
	IssueID         string          `json:"issue_id"`
	AgentID         string          `json:"agent_id"`
	Seq             int64           `json:"seq"`
	Cwd             string          `json:"cwd"`
	Projections     json.RawMessage `json:"projections"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Title           string          `json:"title"`
	WorkspaceName   string          `json:"workspace_name"`
	WorkspaceSlug   string          `json:"workspace_slug"`
	Archived        bool            `json:"archived"`
	RunIDs          []string        `json:"run_ids"`
}

func insightKey(daemon, session string) string {
	sum := sha256.Sum256([]byte(daemon + "\x00" + session))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) GetInsightsCaptureConfig(w http.ResponseWriter, r *http.Request) {
	var since pgtype.Timestamptz
	if err := h.DB.QueryRow(r.Context(), `SELECT enabled_since FROM context_insights_config WHERE singleton`).Scan(&since); err != nil {
		writeError(w, 500, "读取洞察配置失败")
		return
	}
	epoch := int64(0)
	if since.Valid {
		epoch = since.Time.UnixMilli()
	}
	writeJSON(w, 200, map[string]any{"enabled_since": epoch, "version": 1})
}

func (h *Handler) ReportContextInsights(w http.ResponseWriter, r *http.Request) {
	task, workspace, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	if !task.IssueID.Valid && !task.ChatSessionID.Valid {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	var body struct {
		Frames []json.RawMessage `json:"frames"`
		Chunk  *struct {
			ID    string `json:"id"`
			Part  int    `json:"part"`
			Total int    `json:"total"`
			Data  string `json:"data"`
		} `json:"chunk"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&body); err != nil || len(body.Frames) > 32 {
		writeError(w, 400, "洞察批次格式无效")
		return
	}
	daemon := middleware.DaemonIDFromContext(r.Context())
	if daemon == "" {
		var value pgtype.Text
		rt, err := h.Queries.GetAgentRuntime(r.Context(), task.RuntimeID)
		if err != nil {
			writeError(w, 403, "运行时不可用")
			return
		}
		value = rt.DaemonID
		daemon = value.String
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "洞察写入失败")
		return
	}
	defer tx.Rollback(r.Context())
	var parent string
	if task.ChatSessionID.Valid {
		err = tx.QueryRow(r.Context(), `SELECT id::text FROM chat_session WHERE id=$1 FOR SHARE`, task.ChatSessionID).Scan(&parent)
	} else {
		err = tx.QueryRow(r.Context(), `SELECT id::text FROM issue WHERE id=$1 FOR SHARE`, task.IssueID).Scan(&parent)
	}
	if err != nil {
		writeError(w, 404, "所属会话或任务已删除")
		return
	}
	var activation pgtype.Timestamptz
	if err = tx.QueryRow(r.Context(), `SELECT enabled_since FROM context_insights_config WHERE singleton`).Scan(&activation); err != nil {
		writeError(w, 500, "读取洞察配置失败")
		return
	}
	if !activation.Valid {
		writeError(w, 409, "洞察采集尚未正式启用")
		return
	}
	since := activation.Time
	if body.Chunk != nil {
		c := body.Chunk
		data, e := base64.StdEncoding.DecodeString(c.Data)
		if e != nil || len(data) > 256*1024 || len(c.ID) != 64 || c.Part < 0 || c.Part >= c.Total || c.Total > 4096 {
			writeError(w, 400, "洞察分块无效")
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO context_insights_chunk(task_id,workspace_id,chat_session_id,issue_id,object_id,part,total,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, task.ID, workspace, task.ChatSessionID, task.IssueID, c.ID, c.Part, c.Total, data); err != nil {
			writeError(w, 500, "分块保存失败")
			return
		}
		if c.Part < c.Total-1 {
			if tx.Commit(r.Context()) != nil {
				writeError(w, 500, "分块未确认")
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		rows, e := tx.Query(r.Context(), `SELECT data FROM context_insights_chunk WHERE task_id=$1 AND object_id=$2 ORDER BY part`, task.ID, c.ID)
		if e != nil {
			writeError(w, 500, "分块读取失败")
			return
		}
		var assembled bytes.Buffer
		parts := 0
		for rows.Next() {
			var part []byte
			if e = rows.Scan(&part); e != nil {
				rows.Close()
				writeError(w, 500, "分块读取失败")
				return
			}
			assembled.Write(part)
			parts++
		}
		e = rows.Err()
		rows.Close()
		sum := sha256.Sum256(assembled.Bytes())
		if e != nil || parts != c.Total || hex.EncodeToString(sum[:]) != c.ID || !json.Valid(assembled.Bytes()) {
			writeError(w, 409, "分块未完整接收，请重传")
			return
		}
		body.Frames = []json.RawMessage{assembled.Bytes()}
		if _, err = tx.Exec(r.Context(), `DELETE FROM context_insights_chunk WHERE task_id=$1 AND object_id=$2`, task.ID, c.ID); err != nil {
			writeError(w, 500, "分块确认失败")
			return
		}
	}
	for _, raw := range body.Frames {
		var frame insightFrame
		if json.Unmarshal(raw, &frame) != nil || frame.SessionID == "" || len(frame.SessionID) > 1024 || (frame.Type != "archive" && frame.Type != "insights" && frame.Type != "insight_attachment" && frame.Type != "insight_context") {
			writeError(w, 400, "洞察事件格式无效")
			return
		}
		key := insightKey(daemon, frame.SessionID)
		_, err = tx.Exec(r.Context(), `INSERT INTO context_insights_session(id,daemon_id,session_id,parent_session_id,workspace_id,chat_session_id,issue_id,agent_id,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(id) DO NOTHING`, key, daemon, frame.SessionID, frame.ParentSessionID, workspace, task.ChatSessionID, task.IssueID, task.AgentID, task.CreatedAt)
		if err != nil {
			break
		}
		var storedWorkspace, storedChat, storedIssue string
		if err = tx.QueryRow(r.Context(), `SELECT workspace_id::text,COALESCE(chat_session_id::text,''),COALESCE(issue_id::text,'') FROM context_insights_session WHERE id=$1 FOR UPDATE`, key).Scan(&storedWorkspace, &storedChat, &storedIssue); err != nil {
			break
		}
		if storedWorkspace != workspace || storedChat != uuidToString(task.ChatSessionID) || storedIssue != uuidToString(task.IssueID) {
			writeError(w, 409, "DSH 会话已经关联其他聊天或任务")
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO context_insights_run(session_key,task_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, key, task.ID)
		if err != nil {
			break
		}
		if frame.Type == "insight_attachment" {
			var attachment struct {
				ID      string `json:"attachment_id"`
				Media   string `json:"media_type"`
				Name    string `json:"filename"`
				Content []byte `json:"content"`
			}
			if json.Unmarshal(raw, &attachment) != nil || len(attachment.ID) != 71 || !strings.HasPrefix(attachment.ID, "sha256:") {
				writeError(w, 400, "附件身份无效")
				return
			}
			sum := sha256.Sum256(attachment.Content)
			if hex.EncodeToString(sum[:]) != attachment.ID[7:] {
				writeError(w, 400, "附件原件校验失败")
				return
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO context_insights_attachment(session_key,attachment_id,media_type,filename,content) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, key, attachment.ID, attachment.Media, filepath.Base(attachment.Name), attachment.Content)
		} else if frame.Type == "archive" || frame.Type == "insight_context" {
			if frame.Type == "archive" && frame.Event.Time < since.UnixMilli() {
				continue
			}
			if frame.Event.Seq < 0 || frame.Event.Type == "" {
				writeError(w, 400, "洞察原始事件缺少身份")
				return
			}
			tag, insertErr := tx.Exec(r.Context(), `INSERT INTO context_insights_event(session_key,seq,task_id,occurred_at,kind,content) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, key, frame.Event.Seq, task.ID, time.UnixMilli(frame.Event.Time), frame.Event.Type, []byte(raw))
			err = insertErr
			if err == nil && tag.RowsAffected() == 0 {
				var previous []byte
				if err = tx.QueryRow(r.Context(), `SELECT content FROM context_insights_event WHERE session_key=$1 AND seq=$2`, key, frame.Event.Seq).Scan(&previous); err == nil && !sameInsightEvent(previous, raw) {
					writeError(w, 409, "同一 DSH 事件身份对应了不同原件")
					return
				}
			}
			if err == nil && frame.Type == "archive" {
				_, err = tx.Exec(r.Context(), `UPDATE context_insights_session SET updated_at=GREATEST(updated_at,$2) WHERE id=$1`, key, time.UnixMilli(frame.Event.Time))
			}
		} else {
			if frame.Seq < 0 || !json.Valid(frame.Projections) {
				writeError(w, 400, "洞察投影格式无效")
				return
			}
			head, headErr := insightHead(frame.Projections)
			if headErr != nil {
				writeError(w, 400, "投影必须是完整 JSON 对象")
				return
			}
			var values map[string]json.RawMessage
			if json.Unmarshal(frame.Projections, &values) == nil && len(values["multicaRunCost"]) > 0 {
				_, err = tx.Exec(r.Context(), `UPDATE context_insights_run SET cost=$3,seq=$4 WHERE session_key=$1 AND task_id=$2 AND seq<$4`, key, task.ID, []byte(values["multicaRunCost"]), frame.Seq)
				if err != nil {
					break
				}
			}
			_, err = tx.Exec(r.Context(), `UPDATE context_insights_session SET seq=$2,projections=$3,parent_session_id=$4,projection_head=$5,cwd=$6 WHERE id=$1 AND seq<$2`, key, frame.Seq, []byte(frame.Projections), frame.ParentSessionID, head, frame.Cwd)
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "Context Insights batch rejected", "task_id", uuidToString(task.ID), "error", err)
		writeError(w, 500, "洞察批次未保存，请重试")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "洞察批次未确认，请重试")
		return
	}
	if h.Hub != nil {
		message, _ := json.Marshal(map[string]any{"type": "insights:updated", "payload": map[string]any{"workspace_id": workspace, "chat_session_id": uuidToString(task.ChatSessionID), "issue_id": uuidToString(task.IssueID), "task_id": uuidToString(task.ID), "version": time.Now().UnixMilli()}})
		if task.ChatSessionID.Valid {
			if session, e := h.Queries.GetChatSession(r.Context(), task.ChatSessionID); e == nil {
				h.Hub.SendToUser(uuidToString(session.CreatorID), message)
			}
		} else if issue, e := h.Queries.GetIssue(r.Context(), task.IssueID); e == nil {
			if issue.OriginType.Valid && issue.OriginType.String == "health_report" {
				h.Hub.SendToUser(uuidToString(issue.CreatorID), message)
			} else {
				h.Hub.BroadcastToWorkspace(workspace, message)
			}
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// 与聊天和任务的现有可见性一致；平台查询逐项使用所属工作区权限。
func (h *Handler) insightVisible(r *http.Request, s insightSession) bool {
	user := requestUserID(r)
	if _, err := h.getWorkspaceMember(r.Context(), user, s.WorkspaceID); err != nil {
		return false
	}
	if s.IssueID != "" {
		issue, err := h.Queries.GetIssue(r.Context(), parseUUID(s.IssueID))
		return err == nil && healthIssueVisible(issue, user)
	}
	if s.ChatID != "" {
		session, err := h.Queries.GetPublicChatSessionInWorkspace(r.Context(), db.GetPublicChatSessionInWorkspaceParams{ID: parseUUID(s.ChatID), WorkspaceID: parseUUID(s.WorkspaceID)})
		if err != nil || uuidToString(session.CreatorID) != user {
			return false
		}
		agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
		return err == nil && h.canAccessPrivateAgent(r.Context(), agent, "member", user, s.WorkspaceID)
	}
	return false
}

const insightSelect = `SELECT s.id,s.session_id,s.daemon_id,s.parent_session_id,s.workspace_id::text,COALESCE(s.chat_session_id::text,''),COALESCE(s.issue_id::text,''),s.agent_id::text,s.seq,s.projections,s.updated_at,s.cwd,
 COALESCE(i.title,c.title,s.session_id),w.name,w.slug,COALESCE(c.status='archived',false),ARRAY(SELECT r.task_id::text FROM context_insights_run r WHERE r.session_key=s.id ORDER BY r.created_at)
 FROM context_insights_session s JOIN workspace w ON w.id=s.workspace_id
 LEFT JOIN issue i ON i.id=s.issue_id LEFT JOIN chat_session c ON c.id=s.chat_session_id`

func scanInsight(row interface{ Scan(...any) error }) (insightSession, error) {
	var s insightSession
	err := row.Scan(&s.ID, &s.SessionID, &s.DaemonID, &s.ParentSessionID, &s.WorkspaceID, &s.ChatID, &s.IssueID, &s.AgentID, &s.Seq, &s.Projections, &s.UpdatedAt, &s.Cwd, &s.Title, &s.WorkspaceName, &s.WorkspaceSlug, &s.Archived, &s.RunIDs)
	return s, err
}

func (h *Handler) ListContextInsights(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	scope := r.URL.Query().Get("workspace_id")
	// 总览只传头部、完整账本和每日汇总；请求正文和图表明细按实际会话读取。
	selectHeads := strings.Replace(insightSelect, "s.projections,", "s.projection_head,", 1)
	rows, err := h.DB.Query(r.Context(), selectHeads+` WHERE EXISTS(SELECT 1 FROM member m WHERE m.workspace_id=s.workspace_id AND m.user_id=$1) AND ($2='' OR s.workspace_id::text=$2) ORDER BY s.updated_at DESC,s.id`, user, scope)
	if err != nil {
		writeError(w, 500, "读取洞察失败")
		return
	}
	sessions := []insightSession{}
	for rows.Next() {
		s, e := scanInsight(rows)
		if e != nil {
			rows.Close()
			writeError(w, 500, "读取洞察失败")
			return
		}
		sessions = append(sessions, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "读取洞察失败")
		return
	}
	visible := []insightSession{}
	for _, s := range sessions {
		if h.insightVisible(r, s) {
			visible = append(visible, s)
		}
	}
	total := len(visible)
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 100
	}
	// 原版总览需要完整头部账本；一次读取避免实时排序在跨页请求间移动记录。
	if r.URL.Query().Get("all") == "true" {
		offset = 0
		limit = total
	}
	start := min(offset, total)
	end := min(start+limit, total)
	visible = visible[start:end]
	writeJSON(w, 200, map[string]any{"sessions": visible, "total": total, "source_commit": "402742a885a7aaf030e17b280627871cd5238de6"})
}

func (h *Handler) loadInsight(w http.ResponseWriter, r *http.Request) (insightSession, bool) {
	s, err := scanInsight(h.DB.QueryRow(r.Context(), insightSelect+` WHERE s.id=$1`, chi.URLParam(r, "insightId")))
	if err != nil || !h.insightVisible(r, s) {
		writeError(w, 404, "洞察不存在")
		return s, false
	}
	return s, true
}
func (h *Handler) GetContextInsights(w http.ResponseWriter, r *http.Request) {
	s, ok := h.loadInsight(w, r)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT task_id::text FROM context_insights_run WHERE session_key=$1 ORDER BY created_at`, s.ID)
	if err != nil {
		writeError(w, 500, "读取运行失败")
		return
	}
	defer rows.Close()
	runs := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			writeError(w, 500, "读取运行失败")
			return
		}
		runs = append(runs, id)
	}
	writeJSON(w, 200, map[string]any{"session": s, "runs": runs})
}
func (h *Handler) GetContextInsightEvents(w http.ResponseWriter, r *http.Request) {
	s, ok := h.loadInsight(w, r)
	if !ok {
		return
	}
	before := int64(1 << 62)
	if raw := r.URL.Query().Get("before"); raw != "" {
		v, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || v < 0 {
			writeError(w, 400, "无效事件位置")
			return
		}
		before = v
	}
	rows, err := h.DB.Query(r.Context(), `SELECT content FROM context_insights_event WHERE session_key=$1 AND seq<=$2 ORDER BY seq DESC LIMIT 100`, s.ID, before)
	if err != nil {
		writeError(w, 500, "读取原始内容失败")
		return
	}
	defer rows.Close()
	events := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			writeError(w, 500, "读取原始内容失败")
			return
		}
		var frame struct {
			Event json.RawMessage `json:"event"`
		}
		if json.Unmarshal(raw, &frame) == nil {
			events = append(events, frame.Event)
		}
	}
	writeJSON(w, 200, map[string]any{"events": events})
}

func (h *Handler) ContextInsightPreferences(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodPut {
		var value map[string]string
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&value) != nil {
			writeError(w, 400, "偏好格式无效")
			return
		}
		raw, _ := json.Marshal(value)
		if _, err := h.DB.Exec(r.Context(), `INSERT INTO context_insights_preference(user_id,preferences) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET preferences=EXCLUDED.preferences,updated_at=now()`, user, raw); err != nil {
			writeError(w, 500, "保存偏好失败")
			return
		}
	}
	var raw []byte
	err := h.DB.QueryRow(r.Context(), `SELECT preferences FROM context_insights_preference WHERE user_id=$1`, user).Scan(&raw)
	if err == pgx.ErrNoRows {
		raw = []byte(`{}`)
	} else if err != nil {
		writeError(w, 500, "读取偏好失败")
		return
	}
	writeJSON(w, 200, json.RawMessage(raw))
}

func (h *Handler) ContextInsightPrices(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	var raw []byte
	var fetched time.Time
	var version string
	err := h.DB.QueryRow(r.Context(), `SELECT version,catalog,fetched_at FROM context_insights_price ORDER BY fetched_at DESC LIMIT 1`).Scan(&version, &raw, &fetched)
	if err != nil || time.Since(fetched) > time.Hour*24 {
		req, e := http.NewRequestWithContext(r.Context(), "GET", "https://models.dev/api.json", nil)
		if e == nil {
			response, e := (&http.Client{Timeout: 15 * time.Second}).Do(req)
			if e == nil {
				defer response.Body.Close()
				var catalog json.RawMessage
				if response.StatusCode == 200 && json.NewDecoder(http.MaxBytesReader(w, response.Body, 32<<20)).Decode(&catalog) == nil {
					sum := sha256.Sum256(catalog)
					next := hex.EncodeToString(sum[:])
					_, e = h.DB.Exec(r.Context(), `INSERT INTO context_insights_price(version,catalog) VALUES($1,$2) ON CONFLICT(version) DO UPDATE SET fetched_at=now()`, next, catalog)
					if e == nil {
						raw = catalog
						version = next
						err = nil
					}
				}
			}
		}
	}
	if len(raw) == 0 {
		writeError(w, 502, "价格目录暂不可用")
		return
	}
	w.Header().Set("X-Price-Catalog-Version", version)
	writeJSON(w, 200, json.RawMessage(raw))
}

func deleteContextInsightData(ctx context.Context, tx pgx.Tx, workspace, object string, chat bool) error {
	column := "issue_id"
	if chat {
		column = "chat_session_id"
	}
	scope := `SELECT id FROM context_insights_session WHERE workspace_id=$1 AND ` + column + `=$2`
	for _, query := range []string{`DELETE FROM context_insights_chunk WHERE workspace_id=$1 AND ` + column + `=$2`, `DELETE FROM context_insights_attachment WHERE session_key IN (` + scope + `)`, `DELETE FROM context_insights_event WHERE session_key IN (` + scope + `)`, `DELETE FROM context_insights_run WHERE session_key IN (` + scope + `)`, `DELETE FROM context_insights_session WHERE workspace_id=$1 AND ` + column + `=$2`} {
		if _, err := tx.Exec(ctx, query, workspace, object); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) GetContextInsightAttachment(w http.ResponseWriter, r *http.Request) {
	session, ok := h.loadInsight(w, r)
	if !ok {
		return
	}
	var data []byte
	var media, name string
	if h.DB.QueryRow(r.Context(), `SELECT content,media_type,filename FROM context_insights_attachment WHERE session_key=$1 AND attachment_id=$2`, session.ID, r.URL.Query().Get("id")).Scan(&data, &media, &name) != nil {
		writeError(w, 404, "附件原件尚未采集")
		return
	}
	if media == "" {
		media = "application/octet-stream"
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Write(data)
}

func (h *Handler) ListContextInsightRuns(w http.ResponseWriter, r *http.Request) {
	issue, chat := r.URL.Query().Get("issue_id"), r.URL.Query().Get("chat_session_id")
	if (issue == "") == (chat == "") {
		writeError(w, 400, "请选择一个聊天或任务")
		return
	}
	session := insightSession{IssueID: issue, ChatID: chat}
	var err error
	if issue != "" {
		err = h.DB.QueryRow(r.Context(), `SELECT workspace_id::text FROM issue WHERE id=$1`, issue).Scan(&session.WorkspaceID)
	} else {
		err = h.DB.QueryRow(r.Context(), `SELECT workspace_id::text FROM chat_session WHERE id=$1`, chat).Scan(&session.WorkspaceID)
	}
	if err != nil || !h.insightVisible(r, session) {
		writeError(w, 404, "会话不存在")
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT t.id::text,COALESCE(rt.provider,''),t.status,COALESCE(t.started_at,t.created_at) FROM agent_task_queue t LEFT JOIN agent_runtime rt ON rt.id=t.runtime_id WHERE ($1<>'' AND t.issue_id::text=$1) OR ($2<>'' AND t.chat_session_id::text=$2) ORDER BY t.created_at DESC`, issue, chat)
	if err != nil {
		writeError(w, 500, "读取运行失败")
		return
	}
	defer rows.Close()
	runs := []map[string]any{}
	for rows.Next() {
		var id, provider, status string
		var at time.Time
		if rows.Scan(&id, &provider, &status, &at) != nil {
			writeError(w, 500, "读取运行失败")
			return
		}
		runs = append(runs, map[string]any{"id": id, "provider": provider, "status": status, "started_at": at})
	}
	if rows.Err() != nil {
		writeError(w, 500, "读取运行失败")
		return
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

// PG JSONB 无法表示合法 JSON 中的 \u0000；正文与投影均按原始字节存储。
func insightHead(raw []byte) ([]byte, error) {
	var projections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &projections); err != nil {
		return nil, err
	}
	if projections == nil {
		return nil, fmt.Errorf("projection must be an object")
	}
	var timeline map[string]json.RawMessage
	if source := projections["contextTimeline"]; len(source) > 0 {
		if err := json.Unmarshal(source, &timeline); err != nil {
			return nil, err
		}
		for _, key := range []string{"requests", "events", "nodes", "archive", "fileOps", "commands", "spans"} {
			delete(timeline, key)
		}
		projections["contextTimeline"], _ = json.Marshal(timeline)
	}
	return json.Marshal(projections)
}

func sameInsightEvent(first, second []byte) bool {
	var a, b struct {
		Event any `json:"event"`
	}
	da := json.NewDecoder(bytes.NewReader(first))
	da.UseNumber()
	db := json.NewDecoder(bytes.NewReader(second))
	db.UseNumber()
	return da.Decode(&a) == nil && db.Decode(&b) == nil && reflect.DeepEqual(a.Event, b.Event)
}

// 运行摘要复用原版投影的峰谷费用分桶，子会话各自只计一次。
func (h *Handler) hydrateInsightCosts(ctx context.Context, resp []AgentTaskResponse) {
	ids := make([]string, 0, len(resp))
	for _, task := range resp {
		ids = append(ids, task.ID)
	}
	if len(ids) == 0 {
		return
	}
	rows, err := h.DB.Query(ctx, `SELECT task_id::text,cost FROM context_insights_run WHERE task_id::text=ANY($1) AND cost IS NOT NULL ORDER BY task_id,session_key`, ids)
	if err != nil {
		slog.WarnContext(ctx, "洞察运行费用读取失败", "error", err)
		return
	}
	defer rows.Close()
	byTask := map[string][]json.RawMessage{}
	for rows.Next() {
		var id string
		var cost []byte
		if rows.Scan(&id, &cost) != nil {
			return
		}
		byTask[id] = append(byTask[id], json.RawMessage(cost))
	}
	if rows.Err() != nil {
		return
	}
	for i := range resp {
		resp[i].InsightCosts = byTask[resp[i].ID]
	}
}
