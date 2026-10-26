package supervision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 轮次是「一次有效完成」的持久身份。自动监督只处理开放轮次，并以轮次截止时间界定
// 材料范围：完成之后的普通评论、扫描或服务重启都不会产生新的自动批次。
const (
	RoundOpen   = "open"
	RoundClosed = "closed"
	// RoundCloseReopened：任务离开完成状态，旧轮次停止派发。
	RoundCloseReopened = "reopened"
	// RoundCloseRecompleted：任务未记录重开就再次完成，旧轮次让位给新轮次。
	RoundCloseRecompleted = "recompleted"
)

// Round 是一轮完成快照：Sequence 是同任务内的完成序号，Cutoff 是材料截止时间。
type Round struct {
	ID       string
	Sequence int
	Cutoff   time.Time
}

// categoryOf 按工作区状态目录解析行为类别；自定义状态继承其声明的类别，无法解析时
// 退回状态键本身（与 issuestatus.Effective 同向失败）。
func categoryOf(alias string) string {
	return `COALESCE((SELECT st.category FROM issue_status st WHERE st.workspace_id=` + alias +
		`.workspace_id AND st.key=` + alias + `.status AND st.archived_at IS NULL),` + alias + `.status)`
}

// OpenRound 登记一次完成快照。statusFilter 由调用方给出：状态钩子只对真正转入完成的
// 请求开轮，扫描补齐时要求任务当前仍处于完成状态。重复调用返回已有开放轮次。
func (s *Service) OpenRound(ctx context.Context, ws, issue string, requireDone bool) (Round, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback(ctx)
	// 1. 轮次与 subject 行在同一事务建立，扫描和状态钩子并发时由唯一索引裁决，
	// 只有一个请求能开出开放轮次。
	// 只有启用了监督设置的工作区才建立轮次：未使用该功能的工作区不产生长期台账。
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue i JOIN supervision_config c ON c.workspace_id=i.workspace_id
      WHERE i.workspace_id=$1 AND i.id=$2 AND COALESCE(i.origin_type,'')<>'health_report' AND (NOT $3 OR `+categoryOf("i")+`='done'))`, ws, issue, requireDone).Scan(&eligible); err != nil {
		return Round{}, err
	}
	if !eligible {
		return Round{}, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO supervision_subject(workspace_id,issue_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, ws, issue); err != nil {
		return Round{}, err
	}
	var existing Round
	err = tx.QueryRow(ctx, `SELECT id::text,sequence,material_cutoff FROM supervision_round
      WHERE workspace_id=$1 AND issue_id=$2 AND status='open' FOR UPDATE`, ws, issue).Scan(&existing.ID, &existing.Sequence, &existing.Cutoff)
	if err == nil {
		if err := freezeRoundMaterials(ctx, tx, ws, issue, existing); err != nil {
			return Round{}, err
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Round{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO supervision_round(id,workspace_id,issue_id,sequence,status,material_cutoff)
      SELECT gen_random_uuid(),$1,$2,COALESCE((SELECT max(sequence) FROM supervision_round WHERE workspace_id=$1 AND issue_id=$2),0)+1,'open',clock_timestamp()
      ON CONFLICT DO NOTHING RETURNING id::text,sequence,material_cutoff`, ws, issue).Scan(&existing.ID, &existing.Sequence, &existing.Cutoff)
	if errors.Is(err, pgx.ErrNoRows) {
		// 并发请求已开出同一轮次，读取它而不是再建一轮。
		err = tx.QueryRow(ctx, `SELECT id::text,sequence,material_cutoff FROM supervision_round WHERE workspace_id=$1 AND issue_id=$2 AND status='open'`, ws, issue).Scan(&existing.ID, &existing.Sequence, &existing.Cutoff)
	}
	if err != nil {
		return Round{}, err
	}
	if err := freezeRoundMaterials(ctx, tx, ws, issue, existing); err != nil {
		return Round{}, err
	}
	return existing, tx.Commit(ctx)
}

// CloseOpenRound 关闭当前开放轮次：排队中的批次立即取消，运行中的批次在提交时归档，
// 已完成的结论、问题和阅读进度都保留。没有开放轮次时是幂等空操作。
func (s *Service) CloseOpenRound(ctx context.Context, ws, issue, reason string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var round string
	err = tx.QueryRow(ctx, `UPDATE supervision_round SET status='closed',closed_at=now(),close_reason=NULLIF($3,'')
      WHERE workspace_id=$1 AND issue_id=$2 AND status='open' RETURNING id::text`, ws, issue, reason).Scan(&round)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE supervision_batch b SET status='cancelled',completed_at=now(),
      error='任务重新打开，本轮监督已停止；已完成的结论和证据保留。'
      WHERE b.workspace_id=$1 AND b.status='preparing'
      AND EXISTS(SELECT 1 FROM supervision_batch_round br WHERE br.workspace_id=b.workspace_id AND br.batch_id=b.id AND br.scope='completion' AND br.round_id=$2)`, ws, round); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// reconcileRounds 让轮次与任务当前状态一致：离开完成状态的开放轮次关闭，已完成任务
// 补建缺失的开放轮次。它是状态钩子漏触发（服务停机、状态由平台内部推进）时的兜底，
// 同一轮次身份由唯一索引保证不会重复创建。
func (s *Service) reconcileRounds(ctx context.Context, ws string) error {
	if _, err := s.Pool.Exec(ctx, `WITH stale AS (
      SELECT r.id FROM supervision_round r JOIN issue i ON i.id=r.issue_id AND i.workspace_id=r.workspace_id
      WHERE r.workspace_id=$1 AND r.status='open' AND `+categoryOf("i")+`<>'done' LIMIT 200
    ), closed AS (
      UPDATE supervision_round r SET status='closed',closed_at=now(),close_reason='reopened'
      FROM stale WHERE r.id=stale.id RETURNING r.id
    ) SELECT count(*) FROM closed`, ws); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE supervision_batch b SET status='cancelled',completed_at=now(),
      error='任务重新打开，本轮监督已停止；已完成的结论和证据保留。'
      WHERE b.workspace_id=$1 AND b.status='preparing'
      AND EXISTS(SELECT 1 FROM supervision_batch_round br JOIN supervision_round r ON r.id=br.round_id AND r.workspace_id=br.workspace_id
        WHERE br.workspace_id=b.workspace_id AND br.batch_id=b.id AND br.scope='completion' AND r.status='closed')`, ws); err != nil {
		return err
	}
	// 2. 升级前登记的批次没有轮次归属；任务未完成时不再派发，避免旧队列在收敛后
	// 继续审查开发中任务。运行中的旧批次由提交路径按无归属历史处理。
	if _, err := s.Pool.Exec(ctx, `UPDATE supervision_batch b SET status='cancelled',completed_at=now(),
      error='自动监督只审查已完成任务；该任务未完成，本轮不再派发，已记录的材料保留。'
      WHERE b.workspace_id=$1 AND b.status='preparing'
      AND NOT EXISTS(SELECT 1 FROM supervision_batch_round br WHERE br.workspace_id=b.workspace_id AND br.batch_id=b.id)
      AND EXISTS(SELECT 1 FROM issue i WHERE i.id=b.issue_id AND i.workspace_id=b.workspace_id AND `+categoryOf("i")+`<>'done')`, ws); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO supervision_subject(workspace_id,issue_id)
      SELECT $1,i.id FROM issue i WHERE i.workspace_id=$1 AND COALESCE(i.origin_type,'')<>'health_report' AND `+categoryOf("i")+`='done'
      AND NOT EXISTS(SELECT 1 FROM supervision_subject s WHERE s.workspace_id=$1 AND s.issue_id=i.id)
      ORDER BY i.id LIMIT 200 ON CONFLICT DO NOTHING`, ws); err != nil {
		return err
	}
	// 3. 补齐历史完成轮次：旧记录没有可靠的完成时刻，截止时间取首次登记时刻，把升级前
	// 已存在的材料全部纳入本轮；已经完整覆盖的材料不会重读，后续评论也不会追加重审。
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO supervision_round(id,workspace_id,issue_id,sequence,status,material_cutoff)
      SELECT gen_random_uuid(),$1,i.id,
        COALESCE((SELECT max(sequence) FROM supervision_round r WHERE r.workspace_id=$1 AND r.issue_id=i.id),0)+1,
        'open',clock_timestamp()
      FROM issue i
      WHERE i.workspace_id=$1 AND COALESCE(i.origin_type,'')<>'health_report' AND `+categoryOf("i")+`='done'
      AND NOT EXISTS(SELECT 1 FROM supervision_round r WHERE r.workspace_id=$1 AND r.issue_id=i.id AND r.status='open')
      ORDER BY i.id LIMIT 500 ON CONFLICT DO NOTHING`, ws)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.issue_id::text,r.sequence,r.material_cutoff FROM supervision_round r WHERE r.workspace_id=$1 AND r.status='open' AND NOT EXISTS(SELECT 1 FROM supervision_frozen_material m WHERE m.round_id=r.id)`, ws)
	if err != nil {
		return err
	}
	type pendingRound struct {
		issue string
		round Round
	}
	var pending []pendingRound
	for rows.Next() {
		var p pendingRound
		if err = rows.Scan(&p.round.ID, &p.issue, &p.round.Sequence, &p.round.Cutoff); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pending {
		if err = freezeRoundMaterials(ctx, tx, ws, p.issue, p.round); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// openRound 在派发事务内锁定候选轮次；关闭后不再派发。
func openRound(ctx context.Context, tx pgx.Tx, ws, issue string) (Round, bool, error) {
	var r Round
	err := tx.QueryRow(ctx, `SELECT id::text,sequence,material_cutoff FROM supervision_round
      WHERE workspace_id=$1 AND issue_id=$2 AND status='open' FOR UPDATE`, ws, issue).Scan(&r.ID, &r.Sequence, &r.Cutoff)
	if errors.Is(err, pgx.ErrNoRows) {
		return Round{}, false, nil
	}
	if err != nil {
		return Round{}, false, err
	}
	return r, true, nil
}

func (s *Service) issueDone(ctx context.Context, tx pgx.Tx, ws, issue string) (bool, error) {
	var done bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue i WHERE i.workspace_id=$1 AND i.id=$2 AND `+categoryOf("i")+`='done')`, ws, issue).Scan(&done)
	return done, err
}

func closeRoundTx(ctx context.Context, tx pgx.Tx, ws, round, reason string) error {
	var id string
	err := tx.QueryRow(ctx, `UPDATE supervision_round SET status='closed',closed_at=now(),close_reason=$3
      WHERE workspace_id=$1 AND id=$2 AND status='open' RETURNING id::text`, ws, round, reason).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("关闭监督轮次：%w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE supervision_batch b SET status='cancelled',completed_at=now(),
      error='任务重新打开，本轮监督已停止；已完成的结论和证据保留。'
      WHERE b.workspace_id=$1 AND b.status='preparing'
      AND EXISTS(SELECT 1 FROM supervision_batch_round br WHERE br.workspace_id=b.workspace_id AND br.batch_id=b.id AND br.scope='completion' AND br.round_id=$2)`, ws, round)
	return err
}

// CategorySQL 供页面查询复用同一套状态类别解析，避免界面与派发对「完成」判断不一致。
func CategorySQL(alias string) string { return categoryOf(alias) }

// 完成事务内保存原文与代次，后续编辑不能改变尚未派发的材料。
func freezeRoundMaterials(ctx context.Context, tx pgx.Tx, ws, issue string, r Round) error {
	for _, kind := range []string{"issue", "comment", "task", "message", "trace", "trace_usage"} {
		source, err := sourceSQL(kind)
		if err != nil {
			return err
		}
		// sourceSQL 使用 $1 为材料身份，$2 为工作区；这里把身份绑定到材料行。
		source = strings.ReplaceAll(source, "$1", "snapshot_material.source_id")
		_, err = tx.Exec(ctx, `INSERT INTO supervision_frozen_material(workspace_id,issue_id,round_id,material_cutoff,kind,source_id,run_id,generation,reviewed_generation,reviewed_offset,changed_at,body)
   SELECT snapshot_material.workspace_id,snapshot_material.issue_id,$4,$5,snapshot_material.kind,snapshot_material.source_id,snapshot_material.run_id,snapshot_material.generation,snapshot_material.reviewed_generation,snapshot_material.reviewed_offset,snapshot_material.changed_at,source.body
   FROM supervision_material snapshot_material CROSS JOIN LATERAL (`+source+`) source(body)
   WHERE snapshot_material.issue_id=$1 AND snapshot_material.workspace_id=$2 AND snapshot_material.kind=$3 AND snapshot_material.changed_at<=$5
   ON CONFLICT DO NOTHING`, issue, ws, kind, r.ID, r.Cutoff)
		if err != nil {
			return err
		}
	}
	return nil
}
