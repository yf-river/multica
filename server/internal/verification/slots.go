// Package verification 管理固定验收槽位的占用与迟到回执隔离。
package verification

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrBusy = errors.New("没有可用验收槽位")
var ErrStale = errors.New("验收占用已失效或身份不匹配")

type DB interface {
	Begin(context.Context) (pgx.Tx, error)
}
type Store struct{ DB DB }
type Lease struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
	Workspace  string `json:"workspace_id"`
	Delivery   string `json:"delivery_id"`
	Token      string `json:"worker_token"`
	Source     string `json:"source_commit"`
	Candidate  string `json:"candidate_id"`
	State      string `json:"state"`
}

// Claim 在交付行锁内分配槽位；重复请求复用占用，过期占用不能自动抢占。
func (s Store) Claim(ctx context.Context, workspace, delivery, token, source string) (Lease, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(ctx)
	var valid bool
	err = tx.QueryRow(ctx, `SELECT phase='testing' AND worker_token::text=$3 AND source_commit=$4 FROM issue_delivery WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, delivery, token, source).Scan(&valid)
	if err != nil || !valid {
		return Lease{}, ErrStale
	}
	l := Lease{Workspace: workspace, Delivery: delivery, Token: token, Source: source}
	var oldToken, oldSource string
	err = tx.QueryRow(ctx, `SELECT name,generation,state,candidate_id,worker_token::text,source_commit FROM verification_slot WHERE delivery_id=$1 FOR UPDATE`, delivery).Scan(&l.Name, &l.Generation, &l.State, &l.Candidate, &oldToken, &oldSource)
	if err == nil {
		if token != oldToken || source != oldSource {
			return Lease{}, ErrStale
		}
		return l, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, err
	}
	// 已完成构建的交付按进入等待队列的时间领取，构建耗时不占槽位。
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('verification-slot-claim',0))`); err != nil {
		return Lease{}, err
	}
	var first string
	err = tx.QueryRow(ctx, `SELECT d.id::text FROM issue_delivery d JOIN LATERAL (SELECT stage,created_at FROM verification_event e WHERE e.delivery_id=d.id ORDER BY created_at DESC LIMIT 1) e ON true WHERE d.phase='testing' AND e.stage='waiting_slot' AND NOT EXISTS(SELECT 1 FROM verification_slot s WHERE s.delivery_id=d.id) ORDER BY e.created_at,d.created_at LIMIT 1`).Scan(&first)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, err
	}
	if err == nil && first != delivery {
		return Lease{}, ErrBusy
	}
	err = tx.QueryRow(ctx, `UPDATE verification_slot SET state='preparing',generation=generation+1,workspace_id=$1,delivery_id=$2,worker_token=$3,source_commit=$4,candidate_id='',error='',lease_expires_at=now()+interval '2 minutes',updated_at=now() WHERE name=(SELECT name FROM verification_slot WHERE enabled AND state='idle' AND NOT EXISTS(SELECT 1 FROM verification_slot_seed seed WHERE seed.name=verification_slot.name AND NOT seed.verified) ORDER BY name FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING name,generation,state`, workspace, delivery, token, source).Scan(&l.Name, &l.Generation, &l.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrBusy
	}
	if err != nil {
		return Lease{}, err
	}
	if err = recordEvent(ctx, tx, l, "claimed", ""); err != nil {
		return Lease{}, err
	}
	return l, tx.Commit(ctx)
}

// Advance 的所有写操作都绑定代次；释放前必须先进入 draining 并停止实际运行。
func (s Store) Advance(ctx context.Context, l Lease, action, detail string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state, candidate string
	var live bool
	err = tx.QueryRow(ctx, `SELECT state,candidate_id,lease_expires_at>now() FROM verification_slot WHERE name=$1 AND generation=$2 AND workspace_id=$3 AND delivery_id=$4 AND worker_token=$5 AND source_commit=$6 FOR UPDATE`, l.Name, l.Generation, l.Workspace, l.Delivery, l.Token, l.Source).Scan(&state, &candidate, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return err
	}
	if action == "heartbeat" || action == "prepared" {
		var valid bool
		err = tx.QueryRow(ctx, `SELECT phase='testing' AND worker_token::text=$3 AND source_commit=$4 FROM issue_delivery WHERE workspace_id=$1 AND id=$2`, l.Workspace, l.Delivery, l.Token, l.Source).Scan(&valid)
		if err != nil || !valid {
			return ErrStale
		}
	}
	switch action {
	case "heartbeat":
		if !live || (state != "preparing" && state != "verifying") {
			return ErrStale
		}
		_, err = tx.Exec(ctx, `UPDATE verification_slot SET lease_expires_at=now()+interval '2 minutes',updated_at=now() WHERE name=$1`, l.Name)
	case "prepared":
		if !live || state != "preparing" || l.Candidate == "" {
			return ErrStale
		}
		_, err = tx.Exec(ctx, `UPDATE verification_slot SET state='verifying',candidate_id=$2,updated_at=now() WHERE name=$1`, l.Name, l.Candidate)
	case "drain":
		if candidate != l.Candidate {
			return ErrStale
		}
		_, err = tx.Exec(ctx, `UPDATE verification_slot SET state='draining',updated_at=now() WHERE name=$1`, l.Name)
	case "quarantine":
		if detail == "" {
			return fmt.Errorf("缺少故障原因")
		}
		_, err = tx.Exec(ctx, `UPDATE verification_slot SET state='quarantined',error=$2,updated_at=now() WHERE name=$1`, l.Name, detail)
	case "release":
		if state != "draining" || candidate != l.Candidate {
			return ErrStale
		}
		// 独立任务结束后数据可能已被修改；同一租约的续接不经过释放。
		if _, err = tx.Exec(ctx, `UPDATE verification_slot_seed SET verified=false,updated_at=now() WHERE name=$1`, l.Name); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE verification_slot SET state='idle',workspace_id=NULL,delivery_id=NULL,worker_token=NULL,source_commit='',candidate_id='',lease_expires_at=NULL,error='',updated_at=now() WHERE name=$1`, l.Name)
	default:
		return fmt.Errorf("未知槽位操作: %s", action)
	}
	if err != nil {
		return err
	}
	if err = recordEvent(ctx, tx, l, action, detail); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Resume 仅供已核对运行现场的外部接力器恢复；新代次使旧工作进程的回执失效。
func (s Store) Resume(ctx context.Context, l Lease) (Lease, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `UPDATE verification_slot SET generation=generation+1,lease_expires_at=now()+interval '2 minutes',updated_at=now() WHERE name=$1 AND generation=$2 AND workspace_id=$3 AND delivery_id=$4 AND worker_token=$5 AND source_commit=$6 AND candidate_id=$7 AND state IN ('verifying','draining') RETURNING generation,state`, l.Name, l.Generation, l.Workspace, l.Delivery, l.Token, l.Source, l.Candidate).Scan(&l.Generation, &l.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrStale
	}
	if err != nil {
		return Lease{}, err
	}
	if err = recordEvent(ctx, tx, l, "resumed", ""); err != nil {
		return Lease{}, err
	}
	return l, tx.Commit(ctx)
}

func recordEvent(ctx context.Context, tx pgx.Tx, l Lease, stage, detail string) error {
	_, err := tx.Exec(ctx, `INSERT INTO verification_event(workspace_id,delivery_id,environment,generation,candidate_id,stage,detail) VALUES($1,$2,$3,$4,$5,$6,$7)`, l.Workspace, l.Delivery, l.Name, l.Generation, l.Candidate, stage, detail)
	return err
}

// Reset 由管理员收回无法自愈的槽位：隔离态可直接收回，其他非空闲态必须等租约过期。
// 只改槽位归属，不触碰交付记录，原交付再领取会拿到新代次而不是旧现场。
func (s Store) Reset(ctx context.Context, name, actor, reason string) (Lease, error) {
	if name != "verify-1" && name != "verify-2" && name != "verify-3" {
		return Lease{}, fmt.Errorf("未知验收槽位")
	}
	if reason == "" {
		return Lease{}, fmt.Errorf("缺少收回原因")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(ctx)
	var l Lease
	var state string
	var expired bool
	err = tx.QueryRow(ctx, `SELECT state,COALESCE(workspace_id::text,''),COALESCE(delivery_id::text,''),COALESCE(worker_token::text,''),source_commit,candidate_id,generation,COALESCE(lease_expires_at<=now(),true) FROM verification_slot WHERE name=$1 FOR UPDATE`, name).Scan(&state, &l.Workspace, &l.Delivery, &l.Token, &l.Source, &l.Candidate, &l.Generation, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrStale
	}
	if err != nil {
		return Lease{}, err
	}
	l.Name = name
	l.State = state
	if state == "idle" {
		return l, tx.Commit(ctx)
	}
	if state != "quarantined" && !expired {
		return Lease{}, ErrBusy
	}
	if _, err = tx.Exec(ctx, `UPDATE verification_slot SET state='idle',generation=generation+1,workspace_id=NULL,delivery_id=NULL,worker_token=NULL,source_commit='',candidate_id='',lease_expires_at=NULL,error='',updated_at=now() WHERE name=$1`, name); err != nil {
		return Lease{}, err
	}
	l.State = "idle"
	l.Generation++
	// 事件表两列都是 NOT NULL；槽位行被手工清空归属时只能跳过审计写入。
	if l.Workspace != "" && l.Delivery != "" {
		if err = recordEvent(ctx, tx, l, "reset", actor+"："+reason); err != nil {
			return Lease{}, err
		}
	}
	return l, tx.Commit(ctx)
}

func (s Store) Progress(ctx context.Context, l Lease, stage string) error {
	if stage != "building" && stage != "waiting_slot" {
		return fmt.Errorf("非法构建阶段")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var valid bool
	err = tx.QueryRow(ctx, `SELECT phase='testing' AND worker_token::text=$3 AND source_commit=$4 FROM issue_delivery WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, l.Workspace, l.Delivery, l.Token, l.Source).Scan(&valid)
	if err != nil || !valid {
		return ErrStale
	}
	if err = recordEvent(ctx, tx, l, stage, ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
