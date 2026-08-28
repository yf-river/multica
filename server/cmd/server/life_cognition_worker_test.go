package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestLifeTextExcerptPreservesShortTextAndBoundsLongText(t *testing.T) {
	if got := lifeTextExcerpt("搭子", 4); got != "搭子" {
		t.Fatalf("short excerpt=%q", got)
	}
	if got := lifeTextExcerpt("一二三四五", 4); got != "一二三四…" {
		t.Fatalf("bounded excerpt=%q", got)
	}
}

func TestPGIntervalDurationUsesSafeFallback(t *testing.T) {
	if got := pgIntervalDuration(pgtype.Interval{}, 9*time.Hour); got != 9*time.Hour {
		t.Fatalf("invalid interval fallback=%s", got)
	}
	if got := pgIntervalDuration(pgtype.Interval{Days: 2, Valid: true}, time.Hour); got != 48*time.Hour {
		t.Fatalf("two days=%s", got)
	}
}

func TestMissingLifeChroniclePeriodsCatchUpInDependencyOrder(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID, workspaceID pgtype.UUID
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, account) VALUES ('Chronicle Catchup', $1) RETURNING id`, "chronicle-catchup-"+suffix).Scan(&userID); err != nil {
		t.Fatalf("create catchup user: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix) VALUES ('Chronicle Catchup', $1, 'CHR') RETURNING id`, "chronicle-catchup-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("create catchup workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	for index, occurredAt := range []time.Time{
		time.Date(2020, 12, 31, 10, 0, 0, 0, time.UTC),
		time.Date(2021, 1, 1, 10, 0, 0, 0, time.UTC),
	} {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO life_material (workspace_id,user_id,source_type,source_key,source_revision,content,occurred_at)
			VALUES ($1,$2,'manual',$3,'1',$4,$5)
		`, workspaceID, userID, fmt.Sprintf("catchup-%s-%d", suffix, index), "跨年材料", occurredAt); err != nil {
			t.Fatalf("create catchup material: %v", err)
		}
	}
	queries := db.New(testPool)
	list := func() []db.ListMissingLifeChroniclePeriodsRow {
		periods, err := queries.ListMissingLifeChroniclePeriods(ctx, db.ListMissingLifeChroniclePeriodsParams{
			WorkspaceID: workspaceID, UserID: userID, MaxPeriods: 32,
			BeforeTime: pgtype.Timestamptz{Time: time.Date(2021, 1, 3, 0, 0, 0, 0, time.UTC), Valid: true},
		})
		if err != nil {
			t.Fatalf("list missing periods: %v", err)
		}
		return periods
	}
	periods := list()
	if len(periods) != 2 || periods[0].PeriodKind != "day" || periods[1].PeriodKind != "day" {
		t.Fatalf("first catchup must contain the two material days: %#v", periods)
	}
	for _, period := range periods {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO life_chronicle_entry (
				workspace_id,user_id,period_start,period_end,facts,period_kind,status,generated_by
			) VALUES ($1,$2,$3,$4,'日记录',$5,'published','companion')
		`, workspaceID, userID, period.PeriodStart, period.PeriodEnd, period.PeriodKind); err != nil {
			t.Fatalf("create daily chronicle: %v", err)
		}
	}
	periods = list()
	if len(periods) == 0 || periods[0].PeriodKind != "month" || periods[0].PeriodStart.Time.Format("2006-01-02") != "2020-12-01" {
		t.Fatalf("month did not become ready after daily catchup: %#v", periods)
	}
	month := periods[0]
	if _, err := testPool.Exec(ctx, `
		INSERT INTO life_chronicle_entry (
			workspace_id,user_id,period_start,period_end,facts,period_kind,status,generated_by
		) VALUES ($1,$2,$3,$4,'月记录','month','published','companion')
	`, workspaceID, userID, month.PeriodStart, month.PeriodEnd); err != nil {
		t.Fatalf("create monthly chronicle: %v", err)
	}
	periods = list()
	foundYear := false
	for _, period := range periods {
		if period.PeriodKind == "year" && period.PeriodStart.Time.Year() == 2020 {
			foundYear = true
		}
	}
	if !foundYear {
		t.Fatalf("year did not become ready after monthly catchup: %#v", periods)
	}
}
