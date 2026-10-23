package verification

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSlotLifecycle(t *testing.T) {
	raw := os.Getenv("MULTICA_VERIFICATION_TEST_URL")
	if raw == "" {
		t.Skip("requires isolated verification_pool_test database")
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "verification_pool_test" || cfg.ConnConfig.Host != "127.0.0.1" {
		t.Fatal("refusing non-test target")
	}
	ctx := context.Background()
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	base := p
	schema := fmt.Sprintf("verification_%d", time.Now().UnixNano())
	if _, err = p.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	scoped, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		scoped.Close()
		for _, table := range []string{"verification_slot_maintenance", "verification_slot_seed", "verification_event", "verification_slot", "issue_delivery", "issue_delivery_plan"} {
			if _, e := base.Exec(ctx, "DROP TABLE IF EXISTS "+schema+"."+table); e != nil {
				t.Error(e)
			}
		}
		if _, e := base.Exec(ctx, "DROP SCHEMA "+schema); e != nil {
			t.Error(e)
		}
	}()
	p = scoped

	for _, name := range []string{"542_issue_delivery", "571_verification_slot", "572_verification_slot_name", "573_verification_slot_delivery", "627_verification_seed_lifecycle"} {
		sql, e := os.ReadFile(filepath.Join("../../migrations", name+".up.sql"))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.Exec(ctx, string(sql)); e != nil {
			t.Fatal(e)
		}
	}
	if _, err = p.Exec(ctx, "INSERT INTO verification_slot(name) VALUES ('verify-1'),('verify-2'),('verify-3')"); err != nil {
		t.Fatal(err)
	}
	s := Store{DB: p}
	type task struct{ workspace, delivery, token, source string }
	jobs := make([]task, 5)
	for i := range jobs {
		e := p.QueryRow(ctx, `INSERT INTO issue_delivery(id,workspace_id,issue_id,operation_id,plan_id,plan_snapshot,source_commit,source_branch,base_commit,phase,verification_comment_id,release_comment_id,worker_token) VALUES(gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),'{}','abc','feature/test','base','testing',gen_random_uuid(),gen_random_uuid(),gen_random_uuid()) RETURNING workspace_id::text,id::text,worker_token::text,source_commit`).Scan(&jobs[i].workspace, &jobs[i].delivery, &jobs[i].token, &jobs[i].source)
		if e != nil {
			t.Fatal(e)
		}
	}
	claim := func(i int) (Lease, error) {
		j := jobs[i]
		return s.Claim(ctx, j.workspace, j.delivery, j.token, j.source)
	}
	if _, e := claim(0); !errors.Is(e, ErrBusy) {
		t.Fatalf("disabled slots accepted: %v", e)
	}
	if _, e := p.Exec(ctx, `UPDATE verification_slot SET enabled=true`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	leases := make([]Lease, 5)
	errs := make([]error, 5)
	for i := range jobs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); leases[i], errs[i] = claim(i) }(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	active, waiting := []int{}, []int{}
	for i, e := range errs {
		if errors.Is(e, ErrBusy) {
			waiting = append(waiting, i)
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		if seen[leases[i].Name] {
			t.Fatal("duplicate allocation")
		}
		seen[leases[i].Name] = true
		active = append(active, i)
	}
	if len(active) != 3 || len(waiting) != 2 {
		t.Fatalf("active=%d waiting=%d", len(active), len(waiting))
	}
	resumed := leases[active[1]]
	resumed.Candidate = "restart-candidate"
	if e := s.Advance(ctx, resumed, "prepared", ""); e != nil {
		t.Fatal(e)
	}
	old := resumed
	resumed, e := s.Resume(ctx, resumed)
	if e != nil {
		t.Fatal(e)
	}
	if resumed.Generation <= old.Generation {
		t.Fatal("resume did not fence old worker")
	}
	if !errors.Is(s.Advance(ctx, old, "heartbeat", ""), ErrStale) {
		t.Fatal("old worker survived resume")
	}
	if e = s.Advance(ctx, resumed, "heartbeat", ""); e != nil {
		t.Fatal(e)
	}
	cancelled := leases[active[2]]
	if _, e = p.Exec(ctx, `UPDATE issue_delivery SET phase='cancelled' WHERE id=$1`, cancelled.Delivery); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(s.Advance(ctx, cancelled, "heartbeat", ""), ErrStale) {
		t.Fatal("cancelled delivery extended lease")
	}
	l := leases[active[0]]
	if _, e = p.Exec(ctx, `INSERT INTO verification_slot_seed(name,seed_id,seed_sha256,verified) VALUES($1,'seed-current',repeat('a',64),true)`, l.Name); e != nil {
		t.Fatal(e)
	}
	// 同一租约的幂等领取不标脏，也不要求重新恢复。
	again, e := claim(active[0])
	if e != nil || again != l {
		t.Fatalf("idempotency: %v", e)
	}
	wrong := l
	wrong.Token = "00000000-0000-0000-0000-000000000000"
	if !errors.Is(s.Advance(ctx, wrong, "heartbeat", ""), ErrStale) {
		t.Fatal("wrong token accepted")
	}
	l.Candidate = "candidate-one"
	if e = s.Advance(ctx, l, "prepared", ""); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(s.Advance(ctx, l, "release", ""), ErrStale) {
		t.Fatal("released before drain")
	}
	if e = s.Advance(ctx, l, "drain", ""); e != nil {
		t.Fatal(e)
	}
	if e = s.Advance(ctx, l, "release", ""); e != nil {
		t.Fatal(e)
	}
	var verified bool
	if e = p.QueryRow(ctx, `SELECT verified FROM verification_slot_seed WHERE name=$1`, l.Name).Scan(&verified); e != nil || verified {
		t.Fatalf("released task retained clean seed: %v", e)
	}
	if _, e = claim(waiting[0]); !errors.Is(e, ErrBusy) {
		t.Fatalf("new task acquired previous task data: %v", e)
	}
	if _, e = p.Exec(ctx, `UPDATE verification_slot_seed SET verified=true WHERE name=$1`, l.Name); e != nil {
		t.Fatal(e)
	}
	next, e := claim(waiting[0])
	if e != nil {
		t.Fatal(e)
	}
	if next.Name != l.Name || next.Generation <= l.Generation {
		t.Fatal("generation did not advance")
	}
	if !errors.Is(s.Advance(ctx, l, "release", ""), ErrStale) {
		t.Fatal("late release accepted")
	}
	if _, e = p.Exec(ctx, `UPDATE verification_slot SET lease_expires_at=now()-interval '1 second' WHERE name=$1`, next.Name); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(s.Advance(ctx, next, "heartbeat", ""), ErrStale) {
		t.Fatal("expired heartbeat revived lease")
	}
	if _, e = claim(waiting[1]); !errors.Is(e, ErrBusy) {
		t.Fatal("expired slot stolen")
	}
	if e = s.Advance(ctx, next, "quarantine", "lost worker"); e != nil {
		t.Fatal(e)
	}
	if _, e = claim(waiting[1]); !errors.Is(e, ErrBusy) {
		t.Fatal("quarantined slot stolen")
	}
	if _, e = s.Reset(ctx, next.Name, "operator", "故障进程已停止"); e != nil {
		t.Fatal(e)
	}
	if e = p.QueryRow(ctx, `SELECT verified FROM verification_slot_seed WHERE name=$1`, next.Name).Scan(&verified); e != nil || verified {
		t.Fatalf("reset retained used seed: %v", e)
	}
	if _, e = claim(waiting[1]); !errors.Is(e, ErrBusy) {
		t.Fatalf("reset admitted stale task data: %v", e)
	}
	if _, e = p.Exec(ctx, `UPDATE verification_slot_seed SET verified=true WHERE name=$1`, next.Name); e != nil {
		t.Fatal(e)
	}
	if _, e = claim(waiting[1]); e != nil {
		t.Fatal(e)
	}
}
