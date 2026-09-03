package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// removeRetiredBuiltinsHook removes the two historical built-in squad
// families before migration 456 is recorded.  The complete target graph is
// materialised in temporary tables first, then every dependent row is
// removed in one transaction.  This is intentionally a hard delete: these
// templates were generated data and have no source record from which they
// could be restored.
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

// removeRetiredGraphWithTx contains the shared dependency-ordered cleanup for
// generated agent graphs. Keeping the seed queries separate lets the one-time
// OpenClaw retirement use the same proven cleanup without teaching the normal
// PM migration about another product concept.
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
		// A runtime row is an ownership boundary even when an old deployment
		// lost its agent link. Include those tasks before deriving their child
		// graph so no runtime-owned work is left behind or deleted out of order.
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

	// A work agent cannot be removed while it is still the leader of a
	// surviving squad, nor when it has been adopted by the Life system.  Such
	// a deployment has ambiguous ownership; abort before deleting anything.
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

	// Events are durable records, so remove deliveries first.  Events that
	// carry a direct task/chat reference belong exclusively to the retired
	// graph; workspace events whose payload embeds one of those ids are
	// redacted to retain the ordering/audit envelope without retaining content.
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
	// Null out references held by surviving rows before removing the target
	// graph. These columns intentionally have no FK because they are
	// polymorphic or cross-version references.
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

	// Remove polymorphic references before deleting their owners.  Rows tied
	// to a target issue/task/chat are intentionally included even if the
	// author or assignee was a human.
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

// removeOpenClawRuntimesWithTx retires runtime rows and the generated agent
// graph attached to them. OpenClaw was an implementation-specific runtime,
// not user-authored Life data; a Life binding is treated as an unsafe upgrade
// and aborts before any row is removed so the user can rebind it explicitly.
func removeOpenClawRuntimesWithTx(ctx context.Context, tx pgx.Tx) error {
	// Include squads led by an OpenClaw agent in the target set. A surviving
	// squad cannot retain a leader row that this migration is about to delete.
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
	// The shared graph cleanup removes rows that are owned by target agents.
	// These runtime/profile references can also be held by otherwise empty
	// rows, so clear them explicitly before deleting the runtime definitions.
	cleanup := []struct {
		name string
		sql  string
	}{
		{"surviving chat runtime references", `UPDATE chat_session SET runtime_id = NULL WHERE runtime_id IN (SELECT id FROM retired_builtin_runtimes)`},
		// A runtime can outlive its agent after a partial upgrade.  Materialise
		// those tasks into the same graph before deleting them so their task
		// messages, usage, channel deliveries and durable events are removed in
		// the dependency order above rather than relying on a runtime FK
		// cascade.  The migration must also be safe on databases where the
		// runtime row has already been removed but the task is still present.
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

// removeRetiredBuiltinsTx is the migration-runner variant. The caller owns
// the transaction and commits it together with migration 456 and its ledger
// row. Keeping the pool wrapper above makes the cleanup easy to exercise in
// isolation while production migration remains atomic.
func removeRetiredBuiltinsTx(ctx context.Context, tx pgx.Tx) error {
	return removeRetiredBuiltinsWithTx(ctx, tx)
}

func execRetiredDelete(ctx context.Context, tx pgx.Tx, name, statement string, update bool) error {
	tag, err := tx.Exec(ctx, statement)
	if err != nil {
		return fmt.Errorf("remove retired built-in %s: %w", name, err)
	}
	slog.Info("retired built-in cleanup", "operation", name, "rows", tag.RowsAffected(), "update", update)
	return nil
}
