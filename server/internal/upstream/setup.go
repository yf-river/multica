package upstream

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

const DailyCron = "0 9 * * *"

// NextDailyRun 按北京时间计算严格晚于当前时刻的下一次运行。
func NextDailyRun(now time.Time) time.Time {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	local := now.In(zone)
	next := time.Date(local.Year(), local.Month(), local.Day(), 9, 0, 0, 0, zone)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// SetupExecutor 在配置行锁下建立绑定，切换运行时时停用原调度。
func (s *Service) SetupExecutor(ctx context.Context, c Config, creatorID, instructions string, enabled bool) (string, string, error) {
	runtimeID := c.RuntimeID
	if runtimeID == "" || c.BatchSize < 1 || c.BatchSize > 500 {
		return "", "", ValidationError{Field: "config", Message: "请选择运行时，批量大小应在1到500之间"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_config(singleton) VALUES(1) ON CONFLICT(singleton) DO NOTHING`); err != nil {
		return "", "", err
	}
	var oldPilot string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(autopilot_id::text,'') FROM upstream_sync_config WHERE singleton=1 FOR UPDATE`).Scan(&oldPilot); err != nil {
		return "", "", err
	}
	var currentURL, currentBranch, currentRef, base string
	if err = tx.QueryRow(ctx, `SELECT upstream_url,upstream_branch,local_ref,bootstrap_base_sha FROM upstream_sync_config WHERE singleton=1`).Scan(&currentURL, &currentBranch, &currentRef, &base); err != nil {
		return "", "", err
	}
	if base != "" && (currentURL != c.UpstreamURL || currentBranch != c.UpstreamBranch || currentRef != c.LocalRef) {
		return "", "", ValidationError{Field: "repository", Message: "已有同步历史，不能直接更换仓库或比较基线"}
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_sync_run WHERE status IN ('preparing','running') AND lease_expires_at>now())`).Scan(&busy); err != nil {
		return "", "", err
	}
	if busy {
		return "", "", ErrBusy
	}
	var wsID, mode string
	if err = tx.QueryRow(ctx, `SELECT workspace_id::text,runtime_mode FROM agent_runtime WHERE id=$1`, runtimeID).Scan(&wsID, &mode); err != nil {
		return "", "", err
	}
	var agent, owner string
	err = tx.QueryRow(ctx, `SELECT id::text,COALESCE(owner_id::text,'') FROM agent WHERE workspace_id=$1 AND runtime_id=$2 AND kind='system' AND system_key=$3`, wsID, runtimeID, ExecutorSystemKeyPrefix+runtimeID).Scan(&agent, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		agent = newID()
		_, err = tx.Exec(ctx, `INSERT INTO agent(id,workspace_id,name,description,avatar_url,runtime_mode,runtime_id,instructions,visibility,permission_mode,max_concurrent_tasks,kind,system_key,status,owner_id) VALUES($1,$2,'上游观察员 · '||$4::uuid::text,'每天汇总上游改动，供人工选择','🛰',$3,$4,$5,'private','private',1,'system',$6,'offline',$7)`, agent, wsID, mode, runtimeID, instructions, ExecutorSystemKeyPrefix+runtimeID, creatorID)
	} else if err == nil {
		if owner != "" && owner != creatorID {
			return "", "", ValidationError{Field: "runtime_id", Message: "该执行体由另一位管理员管理，请原配置者变更"}
		}
		_, err = tx.Exec(ctx, `UPDATE agent SET instructions=$2,owner_id=$3,updated_at=now() WHERE id=$1`, agent, instructions, creatorID)
	}
	if err != nil {
		return "", "", err
	}
	var pilot string
	err = tx.QueryRow(ctx, `SELECT id::text FROM autopilot WHERE workspace_id=$1 AND assignee_id=$2 AND status<>'archived' AND title='上游同步（系统）' ORDER BY created_at LIMIT 1`, wsID, agent).Scan(&pilot)
	if errors.Is(err, pgx.ErrNoRows) {
		pilot = newID()
		_, err = tx.Exec(ctx, `INSERT INTO autopilot(id,workspace_id,title,description,assignee_id,assignee_type,status,execution_mode,created_by_type,created_by_id) VALUES($1,$2,'上游同步（系统）',$3,$4,'agent','active','run_only','member',$5)`, pilot, wsID, DailyPrompt, agent, creatorID)
	}
	if err != nil {
		return "", "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE autopilot SET description=$2,updated_at=now() WHERE id=$1`, pilot, DailyPrompt); err != nil {
		return "", "", err
	}
	if oldPilot != "" && oldPilot != pilot {
		if _, err = tx.Exec(ctx, `UPDATE autopilot_trigger SET enabled=false,updated_at=now() WHERE autopilot_id=$1 AND kind='schedule'`, oldPilot); err != nil {
			return "", "", err
		}
	}
	var trigger string
	err = tx.QueryRow(ctx, `SELECT id::text FROM autopilot_trigger WHERE autopilot_id=$1 AND kind='schedule' LIMIT 1`, pilot).Scan(&trigger)
	next := NextDailyRun(time.Now())
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO autopilot_trigger(id,autopilot_id,kind,enabled,cron_expression,timezone,next_run_at,label) VALUES($1,$2,'schedule',$3,$4,'Asia/Shanghai',$5,'每日同步')`, newID(), pilot, enabled, DailyCron, next)
	} else if err == nil {
		_, err = tx.Exec(ctx, `UPDATE autopilot_trigger SET enabled=$2,cron_expression=$3,timezone='Asia/Shanghai',next_run_at=$4,updated_at=now() WHERE id=$1`, trigger, enabled, DailyCron, next)
	}
	if err != nil {
		return "", "", err
	}
	state := "bootstrap"
	if !enabled {
		state = "paused"
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET upstream_url=$5,upstream_branch=$6,local_ref=$7,batch_size=$8,created_by=COALESCE(created_by,$9),runtime_id=$1,agent_id=$2,autopilot_id=$3,sync_state=CASE WHEN $4='paused' THEN 'paused' WHEN sync_state IN ('idle','paused') THEN CASE WHEN cursor_sha<>'' AND NOT COALESCE((SELECT has_more FROM upstream_sync_run WHERE status='completed' ORDER BY completed_at DESC LIMIT 1),true) THEN 'incremental' ELSE 'bootstrap' END ELSE sync_state END,updated_at=now() WHERE singleton=1`, runtimeID, agent, pilot, state, c.UpstreamURL, c.UpstreamBranch, c.LocalRef, c.BatchSize, creatorID); err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return agent, pilot, nil
}

const DailyPrompt = "执行一批上游同步：运行 multica upstream start，按返回的冻结提交清单处理并回传，最后运行 multica upstream finish。历史未追平也每天继续。409 表示本次未领取到工作，直接结束，不调用其他批次的 fail。长时间分析期间用 multica upstream heartbeat --run <本批ID> 续租。不要修改或合入代码。"
