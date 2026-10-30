package upstream

import (
	"context"
	"errors"
	"github.com/multica-ai/multica/server/internal/testagents"
	"strings"

	"github.com/jackc/pgx/v5"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
)

const (
	// DefaultScheduleHour 是旧设置协议的缺省值，仅用于兼容读取。
	DefaultScheduleHour = 9
	// MaxInstructionsLength 与监督提示词保持一致的上限，避免把整库写满。
	MaxInstructionsLength = 200_000
)

// Settings 是一次设置保存的完整输入。RuntimeID 为空表示沿用已保存的绑定：
// 没有运行时的成员也能保存批量、时间、提示词和暂停状态。
type Settings struct {
	BootstrapBaseSHA string
	UpstreamURL      string
	UpstreamBranch   string
	LocalRef         string
	BatchSize        int
	ScheduleHour     int
	Enabled          bool
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

// SaveSettings 保存项目的仓库范围、同步执行体、提示词与人工同步状态。
func (s *Service) SaveSettings(ctx context.Context, in Settings, actorID string) (string, string, error) {
	if in.BatchSize < 1 || in.BatchSize > 500 {
		return "", "", ValidationError{Field: "batch_size", Message: "批量大小应在1到500之间"}
	}
	if in.Instructions != nil && len(*in.Instructions) > MaxInstructionsLength {
		return "", "", ValidationError{Field: "instructions", Message: "提示词过长，请控制在20万字符以内"}
	}
	if in.BootstrapBaseSHA != "" && !fullSHA.MatchString(in.BootstrapBaseSHA) {
		return "", "", ValidationError{Field: "bootstrap_base_sha", Message: "分析起点必须是完整提交 SHA"}
	}
	runtimeID := strings.TrimSpace(in.RuntimeID)

	tx, err := s.begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_config(project_id) VALUES(@project) ON CONFLICT(project_id) DO NOTHING`); err != nil {
		return "", "", err
	}

	var currentURL, currentBranch, currentRef, base, currentAgent, currentPilot, currentRuntime, currentInstructions string
	var currentHour int
	var currentState string
	// 提示词独立保存；schedule_hour 兼容旧协议，不参与派发。
	if err = tx.QueryRow(ctx, `SELECT c.upstream_url,c.upstream_branch,c.local_ref,c.bootstrap_base_sha,
 COALESCE(c.agent_id::text,''),COALESCE(c.autopilot_id::text,''),COALESCE(c.runtime_id::text,''),
 COALESCE(ss.schedule_hour,9),c.sync_state,COALESCE(ss.instructions,'')
 FROM upstream_sync_config c LEFT JOIN upstream_sync_settings ss ON ss.project_id=@project
 WHERE c.project_id=@project FOR UPDATE OF c`).
		Scan(&currentURL, &currentBranch, &currentRef, &base, &currentAgent, &currentPilot, &currentRuntime, &currentHour, &currentState, &currentInstructions); err != nil {
		return "", "", err
	}
	if base != "" && in.BootstrapBaseSHA != "" && in.BootstrapBaseSHA != base {
		return "", "", ValidationError{Field: "bootstrap_base_sha", Message: "已有历史不能直接更换分析起点"}
	}
	if base == "" && in.BootstrapBaseSHA != "" {
		if _, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET bootstrap_base_sha=$1 WHERE project_id=@project`, in.BootstrapBaseSHA); err != nil {
			return "", "", err
		}
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

	// 优先沿用项目绑定的执行体及历史身份。
	var agentID, agentOwner, agentRuntime, agentModel, agentThinking, agentWorkspace string
	err = tx.QueryRow(ctx, `SELECT id::text,COALESCE(owner_id::text,''),COALESCE(runtime_id::text,''),
 COALESCE(model,''),COALESCE(thinking_level,''),workspace_id::text
 FROM agent WHERE id=NULLIF($1,'')::uuid AND kind='system' AND (system_key=$2 OR system_key LIKE $3)`,
		currentAgent, ExecutorSystemKey, ExecutorSystemKeyPrefix+"%").
		Scan(&agentID, &agentOwner, &agentRuntime, &agentModel, &agentThinking, &agentWorkspace)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
		agentID = ""
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
 VALUES($1,$2,$10,'由上游同步设置自动维护，请到上游同步页面修改','🛰',$3,$4,$5,'private','private',1,'system',$6,'offline',$7,NULLIF($8,''),NULLIF($9,''))`,
				agentID, workspaceID, runtimeMode, runtimeID, effective, s.executorKey(), effectiveOwner, model, thinking, "上游观察员 · "+s.Project()); err != nil {
				return "", "", err
			}
		} else {
			// 目标运行时可能还留着旧版为它建的另一条执行体。先把它改成孤儿键：否则两条
			// Agent 共用一个键，旧程序按键读取时会拿到不一定是配置绑定的那条。
			if _, err = tx.Exec(ctx, `UPDATE agent SET system_key=$1::text||id::text,updated_at=now()
 WHERE kind='system' AND system_key=$2 AND id<>$3`,
				ExecutorOrphanPrefix, s.executorKey(), agentID); err != nil {
				return "", "", err
			}
			if _, err = tx.Exec(ctx, `UPDATE agent SET workspace_id=$2,runtime_id=$3,runtime_mode=$4,instructions=$5,
 owner_id=$6,system_key=$7,model=NULLIF($8,''),thinking_level=NULLIF($9,''),archived_at=NULL,archived_by=NULL,updated_at=now() WHERE id=$1`,
				agentID, workspaceID, runtimeID, runtimeMode, effective, effectiveOwner, s.executorKey(), model, thinking); err != nil {
				return "", "", err
			}
		}
	} else if agentID != "" {
		// 未改绑定：只同步提示词，暂停状态下也能先把设置保存好。
		if _, err = tx.Exec(ctx, `UPDATE agent SET instructions=$2,updated_at=now() WHERE id=$1`, agentID, effective); err != nil {
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
			// 按配置记住的 id 复用自动化：执行体可能随运行时删除被重建，自动化仍是同一条，
			// 下面统一把它的指派改到当前执行体。
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM autopilot WHERE id=$1 AND workspace_id=$2 AND status<>'archived')`,
				pilot, agentWorkspace).Scan(&exists); err != nil {
				return "", "", err
			}
			if !exists {
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
		if _, err = tx.Exec(ctx, `UPDATE autopilot SET assignee_id=$2,description=$3,status=CASE WHEN $4 THEN 'active' ELSE 'paused' END,pause_reason=NULL,updated_at=now() WHERE id=$1`, pilot, agentID, effective, in.Enabled); err != nil {
			return "", "", err
		}
		if currentPilot != "" && currentPilot != pilot {
			if _, err = tx.Exec(ctx, `UPDATE autopilot_trigger SET enabled=false,updated_at=now() WHERE autopilot_id=$1 AND kind='schedule'`, currentPilot); err != nil {
				return "", "", err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE autopilot_trigger SET enabled=false,updated_at=now() WHERE autopilot_id=$1 AND kind='schedule'`, pilot); err != nil {
			return "", "", err
		}
	} else if currentPilot != "" {
		// 修改 2. 执行体可能已随运行时删除，配置里记着的自动化还在。这时保存（例如点「暂停」）
		//    也要能停掉它，否则 autopilot_id 被写成 NULL，这条自动化在页面上再也找不到，
		//    触发器却继续按天计时；保留 id 让重新绑定运行时后仍能复用它。
		pilot = currentPilot
		if !in.Enabled {
			if _, err = tx.Exec(ctx, `UPDATE autopilot SET status='paused',updated_at=now() WHERE id=$1 AND status='active'`, pilot); err != nil {
				return "", "", err
			}
			if _, err = tx.Exec(ctx, `UPDATE autopilot_trigger SET enabled=false,updated_at=now() WHERE autopilot_id=$1 AND kind='schedule'`, pilot); err != nil {
				return "", "", err
			}
		}
	}

	// 设置页新增的两项单独落表，配置表保持升级前的列，旧程序读写同一行不受影响。
	if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_settings(project_id,instructions,schedule_hour,updated_at)
 VALUES(@project,$1,$2,now()) ON CONFLICT(project_id) DO UPDATE
 SET instructions=EXCLUDED.instructions,schedule_hour=EXCLUDED.schedule_hour,updated_at=now()`, override, in.ScheduleHour); err != nil {
		return "", "", err
	}
	state := "bootstrap"
	if !in.Enabled {
		state = "paused"
	}
	_, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET upstream_url=$1,upstream_branch=$2,local_ref=$3,batch_size=$4,
 created_by=COALESCE(created_by,$5),runtime_id=NULLIF($6,'')::uuid,
 agent_id=NULLIF($7,'')::uuid,autopilot_id=NULLIF($8,'')::uuid,
 sync_state=CASE WHEN $9='paused' THEN 'paused' WHEN sync_state IN ('idle','paused') THEN
   CASE WHEN cursor_sha<>'' AND NOT COALESCE((SELECT has_more FROM upstream_sync_run WHERE project_id=@project AND status='completed' ORDER BY completed_at DESC LIMIT 1),true) THEN 'incremental' ELSE 'bootstrap' END
   ELSE sync_state END,updated_at=now() WHERE project_id=@project`,
		in.UpstreamURL, in.UpstreamBranch, in.LocalRef, in.BatchSize, actorID,
		boundRuntime, agentID, pilot, state)
	if err != nil {
		return "", "", err
	}
	if agentID != "" {
		if err = testagents.Apply(ctx, tx, workspaceID, agentID); err != nil {
			return "", "", err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return agentID, pilot, nil
}
