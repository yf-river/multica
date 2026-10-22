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
	if _, err = tx.Exec(ctx, `UPDATE autopilot SET description=$2,status=CASE WHEN $3 THEN 'active' ELSE 'paused' END,pause_reason=NULL,updated_at=now() WHERE id=$1`, pilot, DailyPrompt, enabled); err != nil {
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

const DailyPrompt = `你是上游变更观察员，不是合并执行者。

你的唯一目标是找出真正值得我们投入工程成本的上游改动。不要为了让清单变长而推荐，也不要把提交标题直接当成价值判断。对每组变更必须回答：解决了什么真实问题、对我们的当前版本是否适用、收益是否足以覆盖迁移/依赖/兼容成本、有哪些证据仍缺失，以及建议如何在联调中验证。纯重命名、无关重构、仅上游内部适配、无法证明收益或会明显增加维护成本的改动，应明确建议跳过并说明理由。拿不准时使用低置信度和“待评估”，不要包装成推荐。

观察阶段只读取上游专用镜像和冻结提交清单。运行 multica upstream start，按返回的完整清单逐项分析，再运行 multica upstream report --run <本批ID> --file <报告文件>，最后运行 multica upstream finish --run <本批ID>。不要访问、扫描或修改当前工作区，不要寻找其他本地仓库，不要修改代码、提交、推送、合并、创建分支或发布。用户标记“已采纳”只代表选择进入后续研发，不代表代码已经吸收。

历史未追平时每天继续处理下一批；409 表示没有领取到工作，直接结束，不调用其他批次的 fail；410 表示本任务租约已过期，说明原因后结束，由下一次运行重新领取。仓库、镜像、基线或网络不可用时保留原始错误并运行 multica upstream fail --run <本批ID> --error <原始错误>。长时间分析期间每隔不超过五分钟运行 multica upstream heartbeat --run <本批ID> 续租。

最终回复用通俗短句说明本批处理数量、真正值得关注的建议、明确跳过的类别、证据边界和剩余进度，不粘贴大量 JSON。`
