package upstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
)

const (
	// DefaultScheduleHour 是未配置时的每日同步整点（北京时间）。
	DefaultScheduleHour = 9
	// MaxInstructionsLength 与监督提示词保持一致的上限，避免把整库写满。
	MaxInstructionsLength = 200_000
)

// DailyCron 返回北京时间每天 hour 点执行的 cron 表达式。
func DailyCron(hour int) string { return fmt.Sprintf("0 %d * * *", hour) }

// NextDailyRun 按北京时间计算严格晚于当前时刻的下一次运行。
func NextDailyRun(now time.Time, hour int) time.Time {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	local := now.In(zone)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, zone)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Settings 是一次设置保存的完整输入。RuntimeID 为空表示沿用已保存的绑定：
// 没有运行时的成员也能保存批量、时间、提示词和暂停状态。
type Settings struct {
	UpstreamURL    string
	UpstreamBranch string
	LocalRef       string
	BatchSize      int
	ScheduleHour   int
	Enabled        bool
	// Instructions 为 nil 表示这次不改提示词；空串表示恢复内置默认。
	Instructions *string
	// DefaultInstructions 是随代码发布的内置提示词，只用于判断是否需要落库。
	DefaultInstructions string
	RuntimeID           string
	Model               string
	ThinkingLevel       string
}

// EffectiveInstructions 返回实际生效的提示词：改写过用改写值，否则用内置默认。
func EffectiveInstructions(override, defaults string) string {
	if strings.TrimSpace(override) == "" {
		return defaults
	}
	return override
}

// normalizeInstructions 把"等同于内置默认"的输入折叠成空值，让平台升级后仍能跟随新默认。
func normalizeInstructions(override, defaults string) string {
	text := strings.TrimSpace(override)
	if text == "" || text == strings.TrimSpace(defaults) {
		return ""
	}
	return override
}

// SaveSettings 保存同步范围、执行体与计划。
//
// 执行体是平台托管的单条 Agent：首次选择运行时时创建，之后换运行时只改绑定，
// 自动化同样按配置里的 id 复用，不再产生第二条。调度只在首次创建、改整点或
// 从暂停恢复时重算，避免每次保存都把当天批次推到次日。
func (s *Service) SaveSettings(ctx context.Context, in Settings, actorID string) (string, string, error) {
	if in.BatchSize < 1 || in.BatchSize > 500 {
		return "", "", ValidationError{Field: "batch_size", Message: "批量大小应在1到500之间"}
	}
	if in.ScheduleHour < 0 || in.ScheduleHour > 23 {
		return "", "", ValidationError{Field: "schedule_hour", Message: "自动同步时间应为0到23点之间"}
	}
	if in.Instructions != nil && len(*in.Instructions) > MaxInstructionsLength {
		return "", "", ValidationError{Field: "instructions", Message: "提示词过长，请控制在20万字符以内"}
	}
	runtimeID := strings.TrimSpace(in.RuntimeID)

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_config(singleton) VALUES(1) ON CONFLICT(singleton) DO NOTHING`); err != nil {
		return "", "", err
	}

	var currentURL, currentBranch, currentRef, base, currentAgent, currentPilot, currentRuntime, currentInstructions string
	var currentHour int
	var currentState string
	if err = tx.QueryRow(ctx, `SELECT upstream_url,upstream_branch,local_ref,bootstrap_base_sha,
 COALESCE(agent_id::text,''),COALESCE(autopilot_id::text,''),COALESCE(runtime_id::text,''),schedule_hour,sync_state,instructions
 FROM upstream_sync_config WHERE singleton=1 FOR UPDATE`).
		Scan(&currentURL, &currentBranch, &currentRef, &base, &currentAgent, &currentPilot, &currentRuntime, &currentHour, &currentState, &currentInstructions); err != nil {
		return "", "", err
	}
	if base != "" && (currentURL != in.UpstreamURL || currentBranch != in.UpstreamBranch || currentRef != in.LocalRef) {
		return "", "", ValidationError{Field: "repository", Message: "已有同步历史，不能直接更换仓库或比较基线"}
	}
	// 换仓库会作废正在冻结的批次范围；其余字段在批次运行期间仍可保存。
	repositoryChanged := currentURL != in.UpstreamURL || currentBranch != in.UpstreamBranch || currentRef != in.LocalRef
	if repositoryChanged {
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_sync_run WHERE status IN ('preparing','running') AND lease_expires_at>now())`).Scan(&busy); err != nil {
			return "", "", err
		}
		if busy {
			return "", "", ErrBusy
		}
	}

	// 先定位已有执行体：配置里记录的 id 优先，其次按固定 system_key 查找。
	var agentID, agentOwner, agentRuntime, agentModel, agentThinking, agentWorkspace string
	err = tx.QueryRow(ctx, `SELECT id::text,COALESCE(owner_id::text,''),COALESCE(runtime_id::text,''),
 COALESCE(model,''),COALESCE(thinking_level,''),workspace_id::text
 FROM agent WHERE id=NULLIF($1,'')::uuid AND kind='user' AND system_key=$2`, currentAgent, ExecutorSystemKey).
		Scan(&agentID, &agentOwner, &agentRuntime, &agentModel, &agentThinking, &agentWorkspace)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id::text,COALESCE(owner_id::text,''),COALESCE(runtime_id::text,''),
 COALESCE(model,''),COALESCE(thinking_level,''),workspace_id::text
 FROM agent WHERE system_key=$1 AND kind='user' ORDER BY created_at LIMIT 1`, ExecutorSystemKey).
			Scan(&agentID, &agentOwner, &agentRuntime, &agentModel, &agentThinking, &agentWorkspace)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
			agentID = ""
		}
	}
	if err != nil {
		return "", "", err
	}

	var runtimeMode, provider, workspaceID string
	if runtimeID != "" {
		if err = tx.QueryRow(ctx, `SELECT workspace_id::text,runtime_mode,provider FROM agent_runtime WHERE id=$1`, runtimeID).
			Scan(&workspaceID, &runtimeMode, &provider); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", "", ValidationError{Field: "runtime_id", Message: "运行时不存在"}
			}
			return "", "", err
		}
	}
	if agentID != "" && runtimeID != "" && agentWorkspace != workspaceID {
		return "", "", ValidationError{Field: "runtime_id", Message: "同步执行体属于另一个工作区"}
	}
	// 沿用原绑定时保留原负责人，避免第二个管理员只改批量或提示词就让私有运行时失去调用资格。
	effectiveOwner := agentOwner
	if agentID == "" || (runtimeID != "" && agentRuntime != runtimeID) || effectiveOwner == "" {
		effectiveOwner = actorID
	}

	override := currentInstructions
	if in.Instructions != nil {
		override = normalizeInstructions(*in.Instructions, in.DefaultInstructions)
	}
	effective := EffectiveInstructions(override, in.DefaultInstructions)

	if runtimeID != "" {
		model := strings.TrimSpace(in.Model)
		thinking := strings.TrimSpace(in.ThinkingLevel)
		if agentRuntime != "" && agentRuntime != runtimeID {
			var oldProvider string
			if err := tx.QueryRow(ctx, `SELECT provider FROM agent_runtime WHERE id=$1`, agentRuntime).Scan(&oldProvider); err == nil && oldProvider != provider {
				// 换 provider 后旧型号/档位不再成立，按 Agent 页面同样的规则清掉。
				if model != "" && agentpkg.ModelKnownIncompatibleWithProvider(provider, model) {
					model = ""
				}
				if thinking != "" && !agentpkg.IsKnownThinkingValue(provider, thinking) {
					thinking = ""
				}
			}
		}
		if thinking != "" && !agentpkg.IsKnownThinkingValue(provider, thinking) {
			return "", "", ValidationError{Field: "thinking_level", Message: "思考等级不适用于所选运行时"}
		}
		if agentID == "" {
			agentID = newID()
			if _, err = tx.Exec(ctx, `INSERT INTO agent(id,workspace_id,name,description,avatar_url,runtime_mode,runtime_id,
 instructions,visibility,permission_mode,max_concurrent_tasks,kind,system_key,status,owner_id,model,thinking_level)
 VALUES($1,$2,'上游观察员','由上游同步设置自动维护，请到上游同步页面修改','🛰',$3,$4,$5,'private','private',1,'user',$6,'offline',$7,NULLIF($8,''),NULLIF($9,''))`,
				agentID, workspaceID, runtimeMode, runtimeID, effective, ExecutorSystemKey, effectiveOwner, model, thinking); err != nil {
				return "", "", err
			}
		} else if _, err = tx.Exec(ctx, `UPDATE agent SET workspace_id=$2,runtime_id=$3,runtime_mode=$4,instructions=$5,
 owner_id=$6,system_key=$7,model=NULLIF($8,''),thinking_level=NULLIF($9,''),archived_at=NULL,archived_by=NULL,updated_at=now() WHERE id=$1`,
			agentID, workspaceID, runtimeID, runtimeMode, effective, effectiveOwner, ExecutorSystemKey, model, thinking); err != nil {
			return "", "", err
		}
	} else if agentID != "" {
		// 未改绑定：只同步提示词，暂停状态下也能先把设置保存好。
		if _, err = tx.Exec(ctx, `UPDATE agent SET instructions=$2,system_key=$3,updated_at=now() WHERE id=$1`, agentID, effective, ExecutorSystemKey); err != nil {
			return "", "", err
		}
	}

	boundRuntime := runtimeID
	if boundRuntime == "" {
		boundRuntime = agentRuntime
	}
	if in.Enabled && boundRuntime == "" {
		return "", "", ValidationError{Field: "runtime_id", Message: "请选择同步运行时"}
	}
	var pilot string
	if agentID != "" {
		if agentWorkspace == "" {
			if err = tx.QueryRow(ctx, `SELECT workspace_id::text FROM agent WHERE id=$1`, agentID).Scan(&agentWorkspace); err != nil {
				return "", "", err
			}
		}
		pilot = currentPilot
		if pilot != "" {
			var owner string
			if err = tx.QueryRow(ctx, `SELECT COALESCE(assignee_id::text,'') FROM autopilot WHERE id=$1 AND status<>'archived'`, pilot).Scan(&owner); err != nil || owner != agentID {
				pilot = ""
			}
		}
		if pilot == "" {
			err = tx.QueryRow(ctx, `SELECT id::text FROM autopilot WHERE assignee_id=$1 AND status<>'archived' ORDER BY created_at LIMIT 1`, agentID).Scan(&pilot)
			if errors.Is(err, pgx.ErrNoRows) {
				pilot = ""
			} else if err != nil {
				return "", "", err
			}
		}
		if pilot == "" {
			pilot = newID()
			if _, err = tx.Exec(ctx, `INSERT INTO autopilot(id,workspace_id,title,description,assignee_id,assignee_type,status,execution_mode,created_by_type,created_by_id)
 VALUES($1,$2,'上游同步（系统）',$3,$4,'agent','active','run_only','member',$5)`, pilot, agentWorkspace, effective, agentID, actorID); err != nil {
				return "", "", err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE autopilot SET description=$2,status=CASE WHEN $3 THEN 'active' ELSE 'paused' END,pause_reason=NULL,updated_at=now() WHERE id=$1`, pilot, effective, in.Enabled); err != nil {
			return "", "", err
		}
		if currentPilot != "" && currentPilot != pilot {
			if _, err = tx.Exec(ctx, `UPDATE autopilot_trigger SET enabled=false,updated_at=now() WHERE autopilot_id=$1 AND kind='schedule'`, currentPilot); err != nil {
				return "", "", err
			}
		}
		reschedule := currentHour != in.ScheduleHour || (currentState == "paused" && in.Enabled)
		if err = upsertSchedule(ctx, tx, pilot, in.ScheduleHour, in.Enabled, reschedule); err != nil {
			return "", "", err
		}
	}

	state := "bootstrap"
	if !in.Enabled {
		state = "paused"
	}
	_, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET upstream_url=$1,upstream_branch=$2,local_ref=$3,batch_size=$4,
 schedule_hour=$5,instructions=$6,created_by=COALESCE(created_by,$7),runtime_id=NULLIF($8,'')::uuid,
 agent_id=NULLIF($9,'')::uuid,autopilot_id=NULLIF($10,'')::uuid,
 sync_state=CASE WHEN $11='paused' THEN 'paused' WHEN sync_state IN ('idle','paused') THEN
   CASE WHEN cursor_sha<>'' AND NOT COALESCE((SELECT has_more FROM upstream_sync_run WHERE status='completed' ORDER BY completed_at DESC LIMIT 1),true) THEN 'incremental' ELSE 'bootstrap' END
   ELSE sync_state END,updated_at=now() WHERE singleton=1`,
		in.UpstreamURL, in.UpstreamBranch, in.LocalRef, in.BatchSize, in.ScheduleHour, override, actorID,
		boundRuntime, agentID, pilot, state)
	if err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return agentID, pilot, nil
}

// 修改 1. 上游把 410 租约过期写进了 setup.go 的 DailyPrompt，但本分支已把提示词收敛到
// service.UpstreamSyncInstructions()（含同一条 410 说明），因此保留 upsertSchedule、丢弃重复的常量。
// upsertSchedule 维护唯一的每日调度。reschedule=false 时保留仍在未来的下次运行时间，
// 只有已过期或从未计算过才重新推到下一个整点。
func upsertSchedule(ctx context.Context, tx pgx.Tx, pilot string, hour int, enabled, reschedule bool) error {
	cron := DailyCron(hour)
	var trigger string
	var nextRunAt *time.Time
	err := tx.QueryRow(ctx, `SELECT id::text,next_run_at FROM autopilot_trigger WHERE autopilot_id=$1 AND kind='schedule' LIMIT 1`, pilot).
		Scan(&trigger, &nextRunAt)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO autopilot_trigger(id,autopilot_id,kind,enabled,cron_expression,timezone,next_run_at,label)
 VALUES($1,$2,'schedule',$3,$4,'Asia/Shanghai',$5,'每日同步')`, newID(), pilot, enabled, cron, NextDailyRun(time.Now(), hour))
		return err
	}
	if err != nil {
		return err
	}
	next := time.Now()
	if nextRunAt != nil && !reschedule {
		next = *nextRunAt
	}
	if !next.After(time.Now()) {
		next = NextDailyRun(time.Now(), hour)
	}
	_, err = tx.Exec(ctx, `UPDATE autopilot_trigger SET enabled=$2,cron_expression=$3,timezone='Asia/Shanghai',next_run_at=$4,updated_at=now() WHERE id=$1`,
		trigger, enabled, cron, next)
	return err
}
