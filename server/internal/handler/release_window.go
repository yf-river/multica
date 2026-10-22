package handler

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// ReleaseWindow 保存本环境暂停清单；发布器通过同一窗口幂等恢复。
func (h *Handler) ReleaseWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.deliveryOperator(w, r); !ok {
		return
	}
	env := os.Getenv("MULTICA_ENV_ID")
	if env == "" {
		writeError(w, 409, "缺少服务环境身份")
		return
	}
	if r.Method == http.MethodGet {
		var raw json.RawMessage
		err := h.DB.QueryRow(r.Context(), `SELECT COALESCE((SELECT to_jsonb(x) FROM (SELECT w.*,(SELECT COALESCE(jsonb_agg(to_jsonb(p)||jsonb_build_object('task_status',q.status,'failure_reason',q.failure_reason,'task_started_at',q.started_at)),'[]') FROM release_paused_task p LEFT JOIN agent_task_queue q ON q.id=p.task_id WHERE p.window_id=w.id) AS tasks FROM release_window w WHERE w.environment_id=$1 AND w.state<>'finished' AND NOT EXISTS(SELECT 1 FROM agent_runtime a WHERE a.daemon_id=w.daemon_id::text AND NOT EXISTS(SELECT 1 FROM member m WHERE m.workspace_id=a.workspace_id AND m.user_id=$2 AND m.role IN('owner','admin'))))x),'{}')`, env, requestUserID(r)).Scan(&raw)
		if err != nil {
			writeError(w, 500, "读取发布窗口失败")
			return
		}
		writeJSON(w, 200, raw)
		return
	}
	var b struct {
		ID      string `json:"id"`
		Action  string `json:"action"`
		Daemon  string `json:"daemon_id"`
		Release string `json:"release_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&b) != nil {
		writeError(w, 400, "窗口请求格式错误")
		return
	}
	if _, ok := parseUUIDOrBadRequest(w, b.ID, "id"); !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "更新发布窗口失败")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('release-window',0))`)
	if err != nil {
		writeError(w, 500, "锁定发布窗口失败")
		return
	}

	if b.Action != "begin" {
		var allowed bool
		err = tx.QueryRow(r.Context(), `SELECT NOT EXISTS(SELECT 1 FROM agent_runtime a JOIN release_window w ON w.daemon_id::text=a.daemon_id WHERE w.id=$1 AND NOT EXISTS(SELECT 1 FROM member m WHERE m.workspace_id=a.workspace_id AND m.user_id=$2 AND m.role IN('owner','admin')))`, b.ID, requestUserID(r)).Scan(&allowed)
		if err != nil || !allowed {
			writeError(w, 403, "发布窗口需要环境管理员权限")
			return
		}
	}
	if b.Action == "begin" {
		if _, ok := parseUUIDOrBadRequest(w, b.Daemon, "daemon_id"); !ok {
			return
		}
		if b.Release == "" {
			writeError(w, 400, "缺少候选版本")
			return
		}
		var owned bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runtime WHERE daemon_id=$1)`, b.Daemon).Scan(&owned)
		var foreign bool
		if err == nil {
			err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runtime a WHERE a.daemon_id=$1 AND NOT EXISTS(SELECT 1 FROM member m WHERE m.workspace_id=a.workspace_id AND m.user_id=$2 AND m.role IN('owner','admin')))`, b.Daemon, requestUserID(r)).Scan(&foreign)
		}
		if foreign {
			writeError(w, 403, "需要目标运行环境所有工作区的管理员权限")
			return
		}
		if err != nil || !owned {
			writeError(w, 409, "运行环境不存在")
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO release_window(id,environment_id,daemon_id,release_id) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, b.ID, env, b.Daemon, b.Release)
	} else {
		var state string
		err = tx.QueryRow(r.Context(), `SELECT state FROM release_window WHERE id=$1 AND environment_id=$2 FOR UPDATE`, b.ID, env).Scan(&state)
		if err != nil {
			writeError(w, 404, "发布窗口不存在")
			return
		}
		switch b.Action {
		case "pause":
			if state != "draining" && state != "pausing" {
				writeError(w, 409, "当前窗口不能请求暂停")
				return
			}
			// 先关认领并等在途认领退出，再请求暂停；已自然结束的任务不进入清单。
			_, err = tx.Exec(r.Context(), `INSERT INTO release_paused_task(window_id,task_id) SELECT $1,q.id FROM agent_task_queue q JOIN agent_runtime a ON a.id=q.runtime_id JOIN release_window w ON w.id=$1 AND w.daemon_id::text=a.daemon_id WHERE q.status IN('dispatched','running','waiting_local_directory') ON CONFLICT DO NOTHING`, b.ID)
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE agent_task_queue q SET status='deferred',fire_at='infinity',context=COALESCE(context,'{}')||jsonb_build_object('release_assignee_id',(SELECT assignee_id FROM issue WHERE id=q.issue_id)),failure_reason='release_pause',wait_reason='发布暂停，等待自动续接',prepare_lease_expires_at=NULL WHERE q.id IN(SELECT task_id FROM release_paused_task WHERE window_id=$1 AND state='requested') AND q.status IN('dispatched','running','waiting_local_directory')`, b.ID)
			}
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE release_window SET state='pausing',updated_at=now() WHERE id=$1`, b.ID)
			}
		case "resume":
			// 未确认停止的旧执行不能与新执行并行。
			var pending bool
			err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM release_paused_task p JOIN agent_task_queue q ON q.id=p.task_id WHERE p.window_id=$1 AND p.state='requested' AND q.status='deferred' AND q.failure_reason='release_pause')`, b.ID).Scan(&pending)
			if err != nil || pending {
				writeError(w, 409, "仍有任务未确认停止")
				return
			}
			rows, readErr := tx.Query(r.Context(), `SELECT p.task_id::text FROM release_paused_task p WHERE p.window_id=$1 AND p.state='stopped' ORDER BY p.created_at`, b.ID)
			if readErr != nil {
				writeError(w, 500, "读取恢复清单失败")
				return
			}
			var ids []string
			for rows.Next() {
				var id string
				err = rows.Scan(&id)
				if err != nil {
					break
				}
				ids = append(ids, id)
			}
			rows.Close()
			if err == nil {
				err = rows.Err()
			}
			if err != nil {
				writeError(w, 500, "读取恢复清单失败")
				return
			}
			for _, id := range ids {
				// 人工取消、改派、自然结束或已有后继时跳过；保持原队列行便于审计。
				var eligible bool
				err = tx.QueryRow(r.Context(), `SELECT q.status='deferred' AND q.failure_reason='release_pause' AND a.runtime_id=q.runtime_id AND a.archived_at IS NULL AND (q.issue_id IS NULL OR (i.status NOT IN('done','cancelled') AND (i.assignee_id IS NOT DISTINCT FROM (q.context->>'release_assignee_id')::uuid OR NOT(q.context ? 'release_assignee_id')))) AND NOT EXISTS(SELECT 1 FROM agent_task_queue n WHERE n.id<>q.id AND n.agent_id=q.agent_id AND ((q.issue_id IS NOT NULL AND n.issue_id=q.issue_id) OR (q.chat_session_id IS NOT NULL AND n.chat_session_id=q.chat_session_id)) AND n.created_at>q.created_at AND n.status IN('dispatched','running','waiting_local_directory')) FROM agent_task_queue q JOIN agent a ON a.id=q.agent_id LEFT JOIN issue i ON i.id=q.issue_id WHERE q.id=$1 FOR UPDATE OF q`, id).Scan(&eligible)
				if err != nil {
					break
				}
				if !eligible {
					_, err = tx.Exec(r.Context(), `UPDATE release_paused_task SET state='skipped',updated_at=now() WHERE window_id=$1 AND task_id=$2`, b.ID, id)
					if err != nil {
						break
					}
					continue
				}
				next := dbid.NewV7().String()
				_, err = tx.Exec(r.Context(), `UPDATE agent_task_queue SET status='cancelled',completed_at=now() WHERE id=$1 AND status='deferred' AND failure_reason='release_pause'`, id)
				if err == nil {
					_, err = tx.Exec(r.Context(), `INSERT INTO agent_task_queue SELECT (jsonb_populate_record(NULL::agent_task_queue,to_jsonb(q)||jsonb_build_object('id',$2::text,'status',CASE WHEN EXISTS(SELECT 1 FROM agent_task_queue n WHERE n.id<>q.id AND n.issue_id=q.issue_id AND n.agent_id=q.agent_id AND n.status='queued') THEN 'deferred' ELSE 'queued' END,'created_at',now(),'dispatched_at',NULL,'started_at',NULL,'completed_at',NULL,'result',NULL,'error',NULL,'failure_reason',NULL,'fire_at',now(),'wait_reason',NULL,'prepare_lease_expires_at',NULL,'rerun_of_task_id',q.id,'force_fresh_session',false,'handoff_note','本次执行因发布暂停后续接。先核对最后操作结果、Git 和交付记录；已完成的提交、部署或外部写入不得重复。继续原任务剩余工作。'))).* FROM agent_task_queue q WHERE q.id=$1`, id, next)
				}
				if err == nil {
					_, err = tx.Exec(r.Context(), `UPDATE release_paused_task SET state='queued',resume_task_id=$3,updated_at=now() WHERE window_id=$1 AND task_id=$2`, b.ID, id, next)
				}
				if err != nil {
					break
				}
			}
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE release_window SET state='finished',updated_at=now() WHERE id=$1`, b.ID)
			}
		default:
			writeError(w, 400, "未知窗口操作")
			return
		}
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 409, "窗口更新失败，读取原窗口后恢复")
		return
	}
	writeJSON(w, 200, map[string]string{"id": b.ID, "action": b.Action})
}

// AckReleasePause 仅在本地执行退出并保存工作目录后确认，不终结业务任务。
func (h *Handler) AckReleasePause(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	var b struct {
		Session string `json:"session_id"`
		WorkDir string `json:"work_dir"`
		RootDir string `json:"root_dir"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&b) != nil {
		writeError(w, 400, "暂停结果格式错误")
		return
	}
	b.Session = util.SanitizeTextForPostgres(b.Session)
	b.WorkDir = util.SanitizeTextForPostgres(b.WorkDir)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "记录暂停失败")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `UPDATE agent_task_queue SET session_id=COALESCE(NULLIF($2,''),session_id),work_dir=COALESCE(NULLIF($3,''),work_dir) WHERE id=$1 AND status='deferred' AND failure_reason='release_pause'`, task.ID, b.Session, b.WorkDir)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE release_paused_task SET state='stopped',session_id=COALESCE(NULLIF($2,''),(SELECT session_id FROM agent_task_queue WHERE id=$1),''),work_dir=$3,root_dir=$4,updated_at=now() WHERE task_id=$1 AND state='requested'`, task.ID, b.Session, b.WorkDir, b.RootDir)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE delivery_owner SET expires_at=now(),generation=generation+1 WHERE execution_id=$1`, uuidToString(task.ID))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "记录暂停失败")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "stopped"})
}
