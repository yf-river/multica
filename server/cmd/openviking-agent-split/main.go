// openviking-agent-split 按已盘点的项目归属调整未完成事项，历史负责人不变。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/openviking"
)

type Issue struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace_id"`
	Assignee  string `json:"assignee_id"`
	Status    string `json:"status"`
	Revision  int64  `json:"revision"`
	Target    string `json:"target_user"`
}
type Plan struct {
	Unfinished []Issue `json:"unfinished"`
	Historical []Issue `json:"historical_preserved"`
	Unresolved []Issue `json:"unresolved"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	planPath := flag.String("plan", "", "盘点生成的 agent-plan.json")
	targetsPath := flag.String("targets", "", "OpenViking user 到已有 Multica Agent ID 的显式映射")
	expected := flag.String("expected-database", "", "必须与实际连接数据库一致")
	apply := flag.Bool("apply", false, "提交事务；默认只读校验")
	flag.Parse()
	if *expected == "" {
		return fmt.Errorf("--expected-database required")
	}
	raw, err := os.ReadFile(*planPath)
	if err != nil {
		return err
	}
	var plan Plan
	if err = json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	raw, err = os.ReadFile(*targetsPath)
	if err != nil {
		return err
	}
	targets := map[string]string{}
	if err = json.Unmarshal(raw, &targets); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	mode := pgx.ReadOnly
	if *apply {
		mode = pgx.ReadWrite
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: mode})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var database string
	if err = tx.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		return err
	}
	if database != *expected {
		return fmt.Errorf("actual database differs from explicit expected database")
	}
	users := map[string]bool{}
	for user, id := range targets {
		if _, err = openviking.User(id, user); err != nil {
			return err
		}
		if users[id] {
			return fmt.Errorf("two OpenViking users cannot share one Agent")
		}
		users[id] = true
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent WHERE id=$1 AND archived_at IS NULL)", id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("target Agent not available: %s", id)
		}
	}
	for _, i := range plan.Unfinished {
		target, ok := targets[i.Target]
		if !ok {
			return fmt.Errorf("target user has no explicit Agent: %s", i.Target)
		}
		var valid bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue i JOIN issue_status s ON s.workspace_id=i.workspace_id AND s.key=i.status JOIN agent a ON a.id=$1 AND a.workspace_id=i.workspace_id WHERE i.id=$2 AND i.assignee_type='agent' AND i.assignee_id=$3 AND i.status=$4 AND i.revision=$5 AND s.category NOT IN ('done','completed','cancelled'))`, target, i.ID, i.Assignee, i.Status, i.Revision).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("issue changed or target workspace differs: %s; regenerate preview", i.ID)
		}
	}
	for _, i := range plan.Historical {
		var owner string
		if err = tx.QueryRow(ctx, "SELECT assignee_id::text FROM issue WHERE id=$1", i.ID).Scan(&owner); err != nil {
			return err
		}
		if owner != i.Assignee {
			return fmt.Errorf("historical owner changed: %s", i.ID)
		}
	}
	if *apply {
		for user, id := range targets {
			if _, err = tx.Exec(ctx, `UPDATE agent SET custom_env=jsonb_set(custom_env,'{MULTICA_OPENVIKING_USER}',to_jsonb($1::text),true),updated_at=now() WHERE id=$2`, user, id); err != nil {
				return err
			}
		}
		for _, i := range plan.Unfinished {
			if _, err = tx.Exec(ctx, `UPDATE issue SET assignee_id=$1,revision=revision+1,updated_at=now() WHERE id=$2`, targets[i.Target], i.ID); err != nil {
				return err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	fmt.Printf("database=%s apply=%t unfinished=%d historical_preserved=%d unresolved_untouched=%d\n", database, *apply, len(plan.Unfinished), len(plan.Historical), len(plan.Unresolved))
	return nil
}
