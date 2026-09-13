// retired_builtins.go
//
// 职责：清理"已退休的内置（built-in）"生成内容——历史上的 PM 系列与
// "multica 编码小队"系列 squad、它们派生出的 agent、以及整张依赖图（任务、
// Issue、聊天、autopilot 运行、Life 数据、渠道安装等），一次事务全部硬删除。
// 之所以能硬删：这些模板是系统生成的，没有可恢复的源记录。文件还含 OpenClaw
// （旧实现专用 runtime）退役的清理，以及供迁移 456/488 复用的事务版本。删除
// 前会做多道护栏（仍在职的 squad leader、被 Life 系统收养的 agent 等），有
// 歧义就中止，绝不误删。
package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// removeRetiredBuiltinsHook 在迁移 456 记为已应用之前移除两族历史内置 squad。
// 先把完整的"目标依赖图"物化到临时表里，然后在同一个事务内删除所有相关行。
// 这里刻意用硬删除：这些模板是生成的数据，没有任何源记录能用来恢复。
func removeRetiredBuiltinsHook(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin retired built-in cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := removeRetiredBuiltinsWithTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit retired built-in cleanup: %w", err)
	}
	return nil
}

// removeRetiredBuiltinsWithTx 具体执行"退休内置清理"：匹配 PM 系列与 multica
// 编码小队的 squad/agent（按名称、描述/指令里的特征串、system_key、runtime 配置
// 文本兜底），交给共享的依赖序清理器 removeRetiredGraphWithTx 完成整张图的删除。
func removeRetiredBuiltinsWithTx(ctx context.Context, tx pgx.Tx) error {
	return removeRetiredGraphWithTx(ctx, tx,
		`INSERT INTO retired_builtin_squads (id)
		 SELECT id
		 FROM squad
		 WHERE lower(btrim(name)) IN ('pm', 'multica 编码小队')
		    OR lower(coalesce(description, '')) LIKE ANY (ARRAY[
			   '%generic-project-sop-flow-v2%', '%multica-coding%',
			   '%user-center-sop-flow%', '%user-center-sop-flow-v2%'])
		    OR lower(coalesce(instructions, '')) LIKE ANY (ARRAY[
				'%generic-project-sop-flow-v2%', '%multica-coding%',
				'%user-center-sop-flow%', '%user-center-sop-flow-v2%'])`,
		`INSERT INTO retired_builtin_agents (id)
		 SELECT member_id FROM squad_member
		  WHERE member_type = 'agent' AND squad_id IN (SELECT id FROM retired_builtin_squads)
		   AND member_id IS NOT NULL
		 UNION
		 SELECT leader_id FROM squad
		  WHERE id IN (SELECT id FROM retired_builtin_squads)
		    AND leader_id IS NOT NULL
		 UNION
		 SELECT id FROM agent
		  WHERE lower(btrim(name)) LIKE 'pm-v2%'
		     OR lower(name) LIKE 'multica 编码小队 ·%'
		     OR lower(coalesce(system_key, '')) IN ('pm', 'pm-v2', 'multica-coding', 'user-center-sop-flow', 'user-center-sop-flow-v2')
		     OR lower(coalesce(runtime_config::text, '')) LIKE ANY (ARRAY[
				'%generic-project-sop-flow-v2%', '%multica-coding%',
				'%user-center-sop-flow%', '%user-center-sop-flow-v2%'])`,
		"",
	)
}

// removeRetiredGraphWithTx 内含共享的"按依赖序清理生成式 agent 图"逻辑。把
// 种子查询独立出来，让一次性的 OpenClaw 退役能复用同一套验证过的清理流程，
// 而不必给普通 PM 迁移再教一个新产品概念。
func removeRetiredGraphWithTx(ctx context.Context, tx pgx.Tx, squadSeed, agentSeed, runtimeSeed string) error {

	statements := []string{
		`CREATE TEMP TABLE retired_builtin_squads (id uuid PRIMARY KEY) ON COMMIT DROP`,
		squadSeed,
		`WITH RECURSIVE nested AS (
			 SELECT sm.member_id AS id
			 FROM squad_member sm
			 WHERE sm.member_type = 'squad'
			   AND sm.squad_id IN (SELECT id FROM retired_builtin_squads)
			 UNION
			 SELECT sm.member_id
			 FROM squad_member sm
			 JOIN nested n ON n.id = sm.squad_id
			 WHERE sm.member_type = 'squad'
		)
			 INSERT INTO retired_builtin_squads (id)
		 SELECT id FROM nested ON CONFLICT DO NOTHING`,
		`CREATE TEMP TABLE retired_builtin_agents (id uuid PRIMARY KEY) ON COMMIT DROP`,
		agentSeed,
		`CREATE TEMP TABLE retired_builtin_runtimes (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_runtimes (id)
		 SELECT DISTINCT runtime_id FROM agent
		  WHERE id IN (SELECT id FROM retired_builtin_agents) AND runtime_id IS NOT NULL`,
		"__retired_builtin_runtime_seed__",
		`CREATE TEMP TABLE retired_builtin_autopilots (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_autopilots (id)
		 SELECT id FROM autopilot
		  WHERE (assignee_type = 'squad' AND assignee_id IN (SELECT id FROM retired_builtin_squads))
		     OR (assignee_type = 'agent' AND assignee_id IN (SELECT id FROM retired_builtin_agents))
		     OR (created_by_type = 'agent' AND created_by_id IN (SELECT id FROM retired_builtin_agents))`,
		`CREATE TEMP TABLE retired_builtin_runs (id uuid PRIMARY KEY, task_id uuid) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_runs (id, task_id)
		 SELECT id, task_id FROM autopilot_run
		  WHERE autopilot_id IN (SELECT id FROM retired_builtin_autopilots)
		     OR squad_id IN (SELECT id FROM retired_builtin_squads)`,
		`CREATE TEMP TABLE retired_builtin_tasks (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`WITH RECURSIVE roots AS (
			 SELECT id FROM agent_task_queue
			  WHERE agent_id IN (SELECT id FROM retired_builtin_agents)
			     OR squad_id IN (SELECT id FROM retired_builtin_squads)
			     OR autopilot_run_id IN (SELECT id FROM retired_builtin_runs)
			     OR id IN (SELECT task_id FROM retired_builtin_runs WHERE task_id IS NOT NULL)
		), related AS (
			 SELECT id FROM roots
			 UNION
			 SELECT child.id FROM agent_task_queue child
			 JOIN related parent ON child.parent_task_id = parent.id
			     OR child.delegated_from_task_id = parent.id
			     OR child.escalation_for_task_id = parent.id
			     OR child.retry_of_task_id = parent.id
			     OR child.rerun_of_task_id = parent.id
			     OR child.chat_input_task_id = parent.id
		)
		 INSERT INTO retired_builtin_tasks (id) SELECT id FROM related`,
		// runtime 行本身就是一条所有权边界——即使旧部署丢了它的 agent 链接。
		// 先把这些任务纳入集合，再去派生它们的子图，保证不会漏掉 runtime 名下
		// 的工作，也不会乱序删除。
		`INSERT INTO retired_builtin_tasks (id)
		 SELECT id FROM agent_task_queue
		  WHERE runtime_id IN (SELECT id FROM retired_builtin_runtimes)
		 ON CONFLICT DO NOTHING`,
		`WITH RECURSIVE related AS (
			 SELECT id FROM retired_builtin_tasks
			 UNION
			 SELECT child.id FROM agent_task_queue child
			 JOIN related parent ON child.parent_task_id = parent.id
			     OR child.delegated_from_task_id = parent.id
			     OR child.escalation_for_task_id = parent.id
			     OR child.retry_of_task_id = parent.id
			     OR child.rerun_of_task_id = parent.id
			     OR child.chat_input_task_id = parent.id
		)
		 INSERT INTO retired_builtin_tasks (id) SELECT id FROM related
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO retired_builtin_runs (id, task_id)
		 SELECT id, task_id FROM autopilot_run
		  WHERE task_id IN (SELECT id FROM retired_builtin_tasks)
		 ON CONFLICT (id) DO NOTHING`,
		`CREATE TEMP TABLE retired_builtin_issues (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`WITH RECURSIVE roots AS (
			 SELECT id FROM issue
			  WHERE (assignee_type = 'squad' AND assignee_id IN (SELECT id FROM retired_builtin_squads))
			     OR (assignee_type = 'agent' AND assignee_id IN (SELECT id FROM retired_builtin_agents))
			     OR (creator_type = 'agent' AND creator_id IN (SELECT id FROM retired_builtin_agents))
			     OR (origin_type = 'autopilot' AND origin_id IN (SELECT id FROM retired_builtin_autopilots))
			     OR id IN (SELECT issue_id FROM agent_task_queue
					       WHERE id IN (SELECT id FROM retired_builtin_tasks) AND issue_id IS NOT NULL)
		), descendants AS (
			 SELECT id FROM roots
			 UNION
			 SELECT child.id FROM issue child
			 JOIN descendants parent ON parent.id = child.parent_issue_id
		)
		 INSERT INTO retired_builtin_issues (id) SELECT id FROM descendants`,
		`CREATE TEMP TABLE retired_builtin_chats (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_chats (id)
		 SELECT id FROM chat_session
		  WHERE agent_id IN (SELECT id FROM retired_builtin_agents)
		     OR id IN (SELECT chat_session_id FROM agent_task_queue
			       WHERE id IN (SELECT id FROM retired_builtin_tasks) AND chat_session_id IS NOT NULL)
		     OR id IN (SELECT chat_session_id FROM chat_message
			       WHERE task_id IN (SELECT id FROM retired_builtin_tasks) AND chat_session_id IS NOT NULL)`,
		`CREATE TEMP TABLE retired_builtin_source_contexts (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_source_contexts (id)
		 SELECT id FROM issue_source_context
		  WHERE issue_id IN (SELECT id FROM retired_builtin_issues)
		     OR origin_task_id IN (SELECT id FROM retired_builtin_tasks)
		     OR source_issue_id IN (SELECT id FROM retired_builtin_issues)`,
		`CREATE TEMP TABLE retired_builtin_experiments (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_experiments (id)
		 SELECT id FROM life_experiment
		  WHERE created_by_type = 'agent' AND created_by_id IN (SELECT id FROM retired_builtin_agents)`,
		`CREATE TEMP TABLE retired_builtin_rounds (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`WITH RECURSIVE rounds AS (
			 SELECT id FROM life_experiment_round WHERE experiment_id IN (SELECT id FROM retired_builtin_experiments)
			 UNION
			 SELECT r.id FROM life_experiment_round r JOIN rounds p ON r.previous_round_id = p.id
		)
		 INSERT INTO retired_builtin_rounds (id) SELECT id FROM rounds`,
		`CREATE TEMP TABLE retired_builtin_modules (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_modules (id)
		 SELECT id FROM life_module WHERE source_experiment_id IN (SELECT id FROM retired_builtin_experiments)`,
		`CREATE TEMP TABLE retired_builtin_quick_actions (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_quick_actions (id)
		 SELECT id FROM quick_action
		  WHERE (assignee_type = 'agent' AND assignee_id IN (SELECT id FROM retired_builtin_agents))
		     OR (created_by_type = 'agent' AND created_by_id IN (SELECT id FROM retired_builtin_agents))`,
		`CREATE TEMP TABLE retired_builtin_comments (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_comments (id)
		 SELECT id FROM comment
		  WHERE issue_id IN (SELECT id FROM retired_builtin_issues)
		     OR (author_type = 'agent' AND author_id IN (SELECT id FROM retired_builtin_agents))
		     OR source_task_id IN (SELECT id FROM retired_builtin_tasks)
		     OR quick_action_id IN (SELECT id FROM retired_builtin_quick_actions)`,
		`CREATE TEMP TABLE retired_builtin_chat_messages (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_chat_messages (id)
		 SELECT id FROM chat_message
		  WHERE chat_session_id IN (SELECT id FROM retired_builtin_chats)
		     OR task_id IN (SELECT id FROM retired_builtin_tasks)`,
		`CREATE TEMP TABLE retired_builtin_triggers (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_triggers (id)
		 SELECT id FROM autopilot_trigger WHERE autopilot_id IN (SELECT id FROM retired_builtin_autopilots)`,
		`CREATE TEMP TABLE retired_builtin_webhook_deliveries (id uuid PRIMARY KEY) ON COMMIT DROP`,
		`INSERT INTO retired_builtin_webhook_deliveries (id)
		 SELECT id FROM webhook_delivery
		  WHERE autopilot_id IN (SELECT id FROM retired_builtin_autopilots)
		     OR autopilot_run_id IN (SELECT id FROM retired_builtin_runs)
		     OR trigger_id IN (SELECT id FROM retired_builtin_triggers)`,
	}
	for _, statement := range statements {
		if statement == "__retired_builtin_runtime_seed__" {
			if runtimeSeed == "" {
				continue
			}
			statement = runtimeSeed
		}
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("materialize retired built-in targets: %w", err)
		}
	}

	for _, target := range []struct {
		name  string
		table string
	}{{"squads", "retired_builtin_squads"}, {"agents", "retired_builtin_agents"}, {"runtimes", "retired_builtin_runtimes"}, {"autopilots", "retired_builtin_autopilots"}, {"runs", "retired_builtin_runs"}, {"tasks", "retired_builtin_tasks"}, {"issues", "retired_builtin_issues"}, {"chats", "retired_builtin_chats"}, {"comments", "retired_builtin_comments"}, {"chat messages", "retired_builtin_chat_messages"}, {"experiments", "retired_builtin_experiments"}, {"rounds", "retired_builtin_rounds"}, {"modules", "retired_builtin_modules"}, {"quick actions", "retired_builtin_quick_actions"}, {"triggers", "retired_builtin_triggers"}, {"webhooks", "retired_builtin_webhook_deliveries"}} {
		var count int64
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+target.table).Scan(&count); err != nil {
			return fmt.Errorf("count retired built-in %s: %w", target.name, err)
		}
		slog.Info("retired built-in cleanup target", "kind", target.name, "count", count)
	}

	// 一个工作 agent 若仍是某个存活 squad 的 leader、或已被 Life 系统收养，就
	// 不能删。这种部署的所有权有歧义：在删除任何东西之前先中止。
	var conflictCount int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM squad
		 WHERE leader_id IN (SELECT id FROM retired_builtin_agents)
		   AND id NOT IN (SELECT id FROM retired_builtin_squads)`).Scan(&conflictCount); err != nil {
		return fmt.Errorf("check surviving squad leaders: %w", err)
	}
	if conflictCount > 0 {
		return fmt.Errorf("retired built-in agent is leader of %d surviving squad(s)", conflictCount)
	}
	if err := tx.QueryRow(ctx, `
		SELECT (
			SELECT count(*) FROM companion_profile
			 WHERE agent_id IN (SELECT id FROM retired_builtin_agents)
		) + (
			SELECT count(*) FROM life_observer
			 WHERE agent_id IN (SELECT id FROM retired_builtin_agents)
		)`).Scan(&conflictCount); err != nil {
		return fmt.Errorf("check Life ownership of retired agents: %w", err)
	}
	if conflictCount > 0 {
		return fmt.Errorf("retired built-in agent is referenced by Life data")
	}

	// 事件是持久记录，所以先删投递。直接携带 task/chat 引用的事件专属于被退休
	// 的图；而那些负载里"嵌了上述 id"的工作区事件则被脱敏——保留排序/审计外壳，
	// 但不再保留内容。
	if err := execRetiredDelete(ctx, tx, "domain event deliveries", `
		DELETE FROM domain_event_delivery d
		 WHERE d.event_id IN (
			 SELECT e.id FROM domain_event_outbox e
			 WHERE e.task_id IN (SELECT id::text FROM retired_builtin_tasks)
			    OR e.chat_session_id IN (SELECT id::text FROM retired_builtin_chats)
		 )`, false); err != nil {
		return err
	}
	if err := execRetiredDelete(ctx, tx, "domain events", `
		DELETE FROM domain_event_outbox
		 WHERE task_id IN (SELECT id::text FROM retired_builtin_tasks)
		    OR chat_session_id IN (SELECT id::text FROM retired_builtin_chats)`, false); err != nil {
		return err
	}
	if err := execRetiredDelete(ctx, tx, "redacted domain events", `
		UPDATE domain_event_outbox e
		 SET payload = jsonb_build_object('redacted', true, 'event_type', e.event_type),
		     last_error = NULL
		 WHERE e.workspace_id IS NOT NULL
		   AND (
			 EXISTS (SELECT 1 FROM retired_builtin_agents a WHERE e.payload::text LIKE '%' || a.id::text || '%')
			 OR EXISTS (SELECT 1 FROM retired_builtin_squads s WHERE e.payload::text LIKE '%' || s.id::text || '%')
			 OR EXISTS (SELECT 1 FROM retired_builtin_issues i WHERE e.payload::text LIKE '%' || i.id::text || '%')
		   )`, true); err != nil {
		return err
	}
	// 删目标图之前，先把"存活行仍持有它的引用"置空。这些列刻意没有外键——
	// 它们是多态引用或跨版本引用。
	updates := []struct {
		name string
		sql  string
	}{
		{"surviving task parent references", `UPDATE agent_task_queue SET parent_task_id = NULL WHERE parent_task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"surviving task delegation references", `UPDATE agent_task_queue SET delegated_from_task_id = NULL WHERE delegated_from_task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"surviving task escalation references", `UPDATE agent_task_queue SET escalation_for_task_id = NULL WHERE escalation_for_task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"surviving task retry references", `UPDATE agent_task_queue SET retry_of_task_id = NULL WHERE retry_of_task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"surviving task rerun references", `UPDATE agent_task_queue SET rerun_of_task_id = NULL WHERE rerun_of_task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"surviving task chat input references", `UPDATE agent_task_queue SET chat_input_task_id = NULL WHERE chat_input_task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"surviving task quick-action references", `UPDATE agent_task_queue SET regenerate_quick_actions_for = NULL WHERE regenerate_quick_actions_for IN (SELECT id FROM retired_builtin_tasks)`},
		{"surviving task autopilot-run references", `UPDATE agent_task_queue SET autopilot_run_id = NULL WHERE autopilot_run_id IN (SELECT id FROM retired_builtin_runs)`},
		{"surviving task chat-session references", `UPDATE agent_task_queue SET chat_session_id = NULL WHERE chat_session_id IN (SELECT id FROM retired_builtin_chats)`},
		{"surviving issue parent references", `UPDATE issue SET parent_issue_id = NULL WHERE parent_issue_id IN (SELECT id FROM retired_builtin_issues)`},
		{"surviving comment parent references", `UPDATE comment SET parent_id = NULL WHERE parent_id IN (SELECT id FROM retired_builtin_comments)`},
		{"surviving source comment references", `UPDATE issue_source_context SET anchor_comment_id = NULL WHERE anchor_comment_id IN (SELECT id FROM retired_builtin_comments)`},
		{"surviving experiment round references", `UPDATE life_experiment_round SET previous_round_id = NULL WHERE previous_round_id IN (SELECT id FROM retired_builtin_rounds)`},
		{"surviving webhook replay references", `UPDATE webhook_delivery SET replayed_from_delivery_id = NULL WHERE replayed_from_delivery_id IN (SELECT id FROM retired_builtin_webhook_deliveries)`},
	}
	for _, item := range updates {
		if err := execRetiredDelete(ctx, tx, item.name, item.sql, true); err != nil {
			return err
		}
	}

	// 在删除它们的 owner 之前先移除多态引用。那些"挂在目标 issue/task/chat 上
	// 的行"即使作者/负责人是人类也刻意一并删除。
	deletes := []struct {
		name string
		sql  string
	}{
		{"webhook deliveries", `DELETE FROM webhook_delivery WHERE id IN (SELECT id FROM retired_builtin_webhook_deliveries)`},
		{"autopilot collaborators", `DELETE FROM autopilot_collaborator WHERE autopilot_id IN (SELECT id FROM retired_builtin_autopilots)`},
		{"autopilot subscribers", `DELETE FROM autopilot_subscriber WHERE autopilot_id IN (SELECT id FROM retired_builtin_autopilots)`},
		{"autopilot rule versions", `DELETE FROM autopilot_rule_version WHERE autopilot_id IN (SELECT id FROM retired_builtin_autopilots)`},
		{"autopilot triggers", `DELETE FROM autopilot_trigger WHERE id IN (SELECT id FROM retired_builtin_triggers)`},
		{"autopilot runs", `DELETE FROM autopilot_run WHERE id IN (SELECT id FROM retired_builtin_runs)`},
		{"system cron executions", `DELETE FROM sys_cron_executions WHERE scope_id IN (SELECT id::text FROM retired_builtin_autopilots) OR scope_id IN (SELECT id::text FROM retired_builtin_runs) OR scope_id IN (SELECT id::text FROM retired_builtin_tasks)`},
		{"channel binding tokens", `DELETE FROM channel_binding_token WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"channel user bindings", `DELETE FROM channel_user_binding WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"channel inbound dedup", `DELETE FROM channel_inbound_message_dedup WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"channel inbound audit", `DELETE FROM channel_inbound_audit WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"channel media pending objects", `DELETE FROM channel_media_pending_object WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"channel session bindings by installation", `DELETE FROM channel_chat_session_binding WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"channel outbound messages by installation", `DELETE FROM channel_outbound_message WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"channel task deliveries by installation", `DELETE FROM channel_task_delivery WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"channel outbound messages", `DELETE FROM channel_outbound_message WHERE task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"channel task deliveries", `DELETE FROM channel_task_delivery WHERE task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"channel outbound cards", `DELETE FROM channel_outbound_card_message WHERE task_id IN (SELECT id FROM retired_builtin_tasks) OR chat_session_id IN (SELECT id FROM retired_builtin_chats)`},
		{"channel chat bindings", `DELETE FROM channel_chat_session_binding WHERE chat_session_id IN (SELECT id FROM retired_builtin_chats)`},
		{"channel context generations", `DELETE FROM channel_chat_context_generation WHERE chat_session_id IN (SELECT id FROM retired_builtin_chats)`},
		{"channel inbound media", `DELETE FROM channel_media_pending_object WHERE chat_message_id IN (SELECT id FROM chat_message WHERE chat_session_id IN (SELECT id FROM retired_builtin_chats))`},
		{"lark chat bindings", `DELETE FROM lark_chat_session_binding WHERE chat_session_id IN (SELECT id FROM retired_builtin_chats)`},
		{"lark outbound cards", `DELETE FROM lark_outbound_card_message WHERE task_id IN (SELECT id FROM retired_builtin_tasks) OR chat_session_id IN (SELECT id FROM retired_builtin_chats)`},
		{"lark inbound audit", `DELETE FROM lark_inbound_audit WHERE installation_id IN (SELECT id FROM lark_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"lark binding tokens", `DELETE FROM lark_binding_token WHERE installation_id IN (SELECT id FROM lark_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"lark user bindings", `DELETE FROM lark_user_binding WHERE installation_id IN (SELECT id FROM lark_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"lark chat bindings by installation", `DELETE FROM lark_chat_session_binding WHERE installation_id IN (SELECT id FROM lark_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"dingtalk bot identities", `DELETE FROM dingtalk_bot_identity WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"dingtalk group presence", `DELETE FROM dingtalk_group_presence WHERE installation_id IN (SELECT id FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"chat drafts", `DELETE FROM chat_draft_restore WHERE chat_session_id IN (SELECT id FROM retired_builtin_chats) OR task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"agent builder drafts", `DELETE FROM agent_builder_draft WHERE chat_session_id IN (SELECT id FROM retired_builtin_chats)`},
		{"chat message attachments", `DELETE FROM attachment WHERE chat_message_id IN (SELECT id FROM retired_builtin_chat_messages)`},
		{"comment attachments", `DELETE FROM attachment WHERE comment_id IN (SELECT id FROM retired_builtin_comments)`},
		{"target attachments", `DELETE FROM attachment WHERE issue_id IN (SELECT id FROM retired_builtin_issues) OR chat_session_id IN (SELECT id FROM retired_builtin_chats) OR task_id IN (SELECT id FROM retired_builtin_tasks) OR source_context_id IN (SELECT id FROM retired_builtin_source_contexts) OR (uploader_type = 'agent' AND uploader_id IN (SELECT id FROM retired_builtin_agents))`},
		{"chat messages", `DELETE FROM chat_message WHERE id IN (SELECT id FROM retired_builtin_chat_messages)`},
		{"inbox items", `DELETE FROM inbox_item WHERE issue_id IN (SELECT id FROM retired_builtin_issues) OR (recipient_type = 'agent' AND recipient_id IN (SELECT id FROM retired_builtin_agents)) OR (actor_type = 'agent' AND actor_id IN (SELECT id FROM retired_builtin_agents))`},
		{"activity logs", `DELETE FROM activity_log WHERE issue_id IN (SELECT id FROM retired_builtin_issues) OR (actor_type = 'agent' AND actor_id IN (SELECT id FROM retired_builtin_agents))`},
		{"issue subscribers", `DELETE FROM issue_subscriber WHERE issue_id IN (SELECT id FROM retired_builtin_issues) OR (user_type = 'agent' AND user_id IN (SELECT id FROM retired_builtin_agents))`},
		{"issue reactions", `DELETE FROM issue_reaction WHERE issue_id IN (SELECT id FROM retired_builtin_issues) OR (actor_type = 'agent' AND actor_id IN (SELECT id FROM retired_builtin_agents))`},
		{"comment reactions", `DELETE FROM comment_reaction WHERE comment_id IN (SELECT id FROM retired_builtin_comments) OR (actor_type = 'agent' AND actor_id IN (SELECT id FROM retired_builtin_agents))`},
		{"pinned items", `DELETE FROM pinned_item WHERE item_type = 'issue' AND item_id IN (SELECT id FROM retired_builtin_issues)`},
		{"comments authored by retired agents", `DELETE FROM comment WHERE id IN (SELECT id FROM retired_builtin_comments)`},
		{"issue source context objects", `DELETE FROM issue_source_context_object_intent WHERE source_context_id IN (SELECT id FROM retired_builtin_source_contexts)`},
		{"issue source contexts", `DELETE FROM issue_source_context WHERE issue_id IN (SELECT id FROM retired_builtin_issues) OR origin_task_id IN (SELECT id FROM retired_builtin_tasks) OR source_issue_id IN (SELECT id FROM retired_builtin_issues)`},
		{"issue dependencies", `DELETE FROM issue_dependency WHERE issue_id IN (SELECT id FROM retired_builtin_issues) OR depends_on_issue_id IN (SELECT id FROM retired_builtin_issues)`},
		{"issue labels", `DELETE FROM issue_to_label WHERE issue_id IN (SELECT id FROM retired_builtin_issues)`},
		{"issue GitHub links", `DELETE FROM issue_pull_request WHERE issue_id IN (SELECT id FROM retired_builtin_issues)`},
		{"issue VCS links", `DELETE FROM issue_vcs_pull_request WHERE issue_id IN (SELECT id FROM retired_builtin_issues)`},
		{"quick action comments", `DELETE FROM comment WHERE quick_action_id IN (SELECT id FROM retired_builtin_quick_actions)`},
		{"quick actions", `DELETE FROM quick_action WHERE id IN (SELECT id FROM retired_builtin_quick_actions)`},
		{"agent invocation targets", `DELETE FROM agent_invocation_target WHERE agent_id IN (SELECT id FROM retired_builtin_agents) OR (target_type IN ('team', 'member') AND target_id IN (SELECT id FROM retired_builtin_agents UNION SELECT id FROM retired_builtin_squads))`},
		{"agent MCP bindings", `DELETE FROM agent_mcp_server WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"agent labels", `DELETE FROM agent_to_label WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"chat pins", `DELETE FROM chat_pinned_agent WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"channel installations", `DELETE FROM channel_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"dingtalk routes", `DELETE FROM dingtalk_group_route WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"lark installations", `DELETE FROM lark_installation WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"daemon connections", `DELETE FROM daemon_connection WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"life derivations", `DELETE FROM life_derivation WHERE job_id IN (SELECT id FROM life_cognition_job WHERE companion_agent_id IN (SELECT id FROM retired_builtin_agents) OR task_id IN (SELECT id FROM retired_builtin_tasks))`},
		{"life cognition jobs", `DELETE FROM life_cognition_job WHERE companion_agent_id IN (SELECT id FROM retired_builtin_agents) OR task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"life internal thoughts", `DELETE FROM life_internal_thought WHERE companion_agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"life proactive checks", `DELETE FROM life_proactive_check WHERE companion_agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"task messages", `DELETE FROM task_message WHERE task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"task tokens", `DELETE FROM task_token WHERE task_id IN (SELECT id FROM retired_builtin_tasks) OR agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"task usage", `DELETE FROM task_usage WHERE task_id IN (SELECT id FROM retired_builtin_tasks)`},
		{"agent tasks", `DELETE FROM agent_task_queue WHERE id IN (SELECT id FROM retired_builtin_tasks)`},
		{"chat sessions", `DELETE FROM chat_session WHERE id IN (SELECT id FROM retired_builtin_chats)`},
		{"task usage hourly", `DELETE FROM task_usage_hourly WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"task usage hourly dirty", `DELETE FROM task_usage_hourly_dirty WHERE agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"life commitments", `DELETE FROM life_commitment WHERE issue_id IN (SELECT id FROM retired_builtin_issues)`},
		{"life experiment observations", `DELETE FROM life_experiment_observation WHERE round_id IN (SELECT id FROM retired_builtin_rounds)`},
		{"life experiment memories", `DELETE FROM life_experiment_memory WHERE round_id IN (SELECT id FROM retired_builtin_rounds)`},
		{"life module versions", `DELETE FROM life_module_version WHERE module_id IN (SELECT id FROM retired_builtin_modules)`},
		{"life experiment rounds", `DELETE FROM life_experiment_round WHERE id IN (SELECT id FROM retired_builtin_rounds) OR issue_id IN (SELECT id FROM retired_builtin_issues) OR proposal_id IN (SELECT id FROM life_action_proposal WHERE companion_agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"life modules", `DELETE FROM life_module WHERE id IN (SELECT id FROM retired_builtin_modules)`},
		{"life experiments", `DELETE FROM life_experiment WHERE id IN (SELECT id FROM retired_builtin_experiments)`},
		{"life relationship events", `DELETE FROM life_relationship_event WHERE relationship_change_proposal_id IN (SELECT id FROM life_action_proposal WHERE companion_agent_id IN (SELECT id FROM retired_builtin_agents))`},
		{"life action proposals", `DELETE FROM life_action_proposal WHERE companion_agent_id IN (SELECT id FROM retired_builtin_agents)`},
		{"autopilots", `DELETE FROM autopilot WHERE id IN (SELECT id FROM retired_builtin_autopilots)`},
		{"issues", `DELETE FROM issue WHERE id IN (SELECT id FROM retired_builtin_issues)`},
		{"squad memberships", `DELETE FROM squad_member WHERE squad_id IN (SELECT id FROM retired_builtin_squads) OR (member_type = 'agent' AND member_id IN (SELECT id FROM retired_builtin_agents)) OR (member_type = 'squad' AND member_id IN (SELECT id FROM retired_builtin_squads))`},
		{"retired squads", `DELETE FROM squad WHERE id IN (SELECT id FROM retired_builtin_squads)`},
		{"retired agents", `DELETE FROM agent WHERE id IN (SELECT id FROM retired_builtin_agents)`},
		{"orphaned runtimes", `DELETE FROM agent_runtime r WHERE r.id IN (SELECT id FROM retired_builtin_runtimes) AND NOT EXISTS (SELECT 1 FROM agent a WHERE a.runtime_id = r.id)`},
	}
	for _, item := range deletes {
		if err := execRetiredDelete(ctx, tx, item.name, item.sql, false); err != nil {
			return err
		}
	}

	return nil
}

// removeOpenClawRuntimesWithTx 退役 runtime 行及其挂着的整棵生成式 agent 图。
// OpenClaw 是实现专属的 runtime，不是用户创作的 Life 数据；若某 agent 被 Life
// 绑定，视为"不安全的升级"，会在删除任何行之前中止，让用户显式重新绑定。
func removeOpenClawRuntimesWithTx(ctx context.Context, tx pgx.Tx) error {
	// 把"由 OpenClaw agent 带领的 squad"也纳入目标集：存活的 squad 不能留着一条
	// 本迁移即将删除的 leader 行。
	squadSeed := `INSERT INTO retired_builtin_squads (id)
		 SELECT s.id FROM squad s
		 LEFT JOIN agent a ON a.id = s.leader_id
		 LEFT JOIN agent_runtime r ON r.id = a.runtime_id
		 LEFT JOIN runtime_profile p ON p.id = r.profile_id
		 WHERE lower(coalesce(r.provider, '')) = 'openclaw'
		    OR lower(coalesce(p.protocol_family, '')) = 'openclaw'`
	agentSeed := `INSERT INTO retired_builtin_agents (id)
		 SELECT a.id
		 FROM agent a
		 JOIN agent_runtime r ON r.id = a.runtime_id
		 LEFT JOIN runtime_profile p ON p.id = r.profile_id
		 WHERE lower(coalesce(r.provider, '')) = 'openclaw'
		    OR lower(coalesce(p.protocol_family, '')) = 'openclaw'`
	runtimeSeed := `INSERT INTO retired_builtin_runtimes (id)
		 SELECT r.id
		 FROM agent_runtime r
		 LEFT JOIN runtime_profile p ON p.id = r.profile_id
		 WHERE lower(coalesce(r.provider, '')) = 'openclaw'
		    OR lower(coalesce(p.protocol_family, '')) = 'openclaw'
		 ON CONFLICT DO NOTHING`
	if err := removeRetiredGraphWithTx(ctx, tx, squadSeed, agentSeed, runtimeSeed); err != nil {
		return fmt.Errorf("remove OpenClaw agent graph: %w", err)
	}
	// 共享的图清理会删掉"目标 agent 拥有"的行。但这些 runtime/profile 引用也
	// 可能被"本应已空"的行持有，所以要在删除 runtime 定义之前显式清一遍。
	cleanup := []struct {
		name string
		sql  string
	}{
		{"surviving chat runtime references", `UPDATE chat_session SET runtime_id = NULL WHERE runtime_id IN (SELECT id FROM retired_builtin_runtimes)`},
		// 部分升级后 runtime 可能比它的 agent 活得久。先把这些任务物化进同一张
		// 图再删，让它们的消息、用量、渠道投递、可靠事件都按上面的依赖序被清掉，
		// 而不是指望 runtime 外键级联。迁移在"runtime 行已被删但任务仍在"的数据库
		// 上也必须安全。
		{"orphan runtime task rows", `DELETE FROM agent_task_queue
			WHERE runtime_id IN (SELECT id FROM retired_builtin_runtimes)
			  AND id NOT IN (SELECT id FROM retired_builtin_tasks)`},
		{"OpenClaw runtime profiles", `DELETE FROM runtime_profile WHERE lower(protocol_family) = 'openclaw'`},
		{"OpenClaw runtime rows", `DELETE FROM agent_runtime WHERE lower(provider) = 'openclaw' OR id IN (SELECT id FROM retired_builtin_runtimes)`},
	}
	for _, item := range cleanup {
		if err := execRetiredDelete(ctx, tx, item.name, item.sql, false); err != nil {
			return err
		}
	}
	return nil
}

// removeRetiredBuiltinsTx 是供迁移 runner 使用的变体：调用方拥有事务，并把
// 它连同迁移 456 及台账行一起提交。保留上面那个连接池包装版本，是为了在隔离
// 环境里方便单独练习清理流程，而生产迁移仍是原子的。
func removeRetiredBuiltinsTx(ctx context.Context, tx pgx.Tx) error {
	return removeRetiredBuiltinsWithTx(ctx, tx)
}

// execRetiredDelete 执行一条清理 SQL（删除或更新）并记日志。
func execRetiredDelete(ctx context.Context, tx pgx.Tx, name, statement string, update bool) error {
	tag, err := tx.Exec(ctx, statement)
	if err != nil {
		return fmt.Errorf("remove retired built-in %s: %w", name, err)
	}
	slog.Info("retired built-in cleanup", "operation", name, "rows", tag.RowsAffected(), "update", update)
	return nil
}
