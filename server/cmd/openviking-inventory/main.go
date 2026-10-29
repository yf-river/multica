// openviking-inventory 导出迁移源清单，不修改数据库、运行时会话或 OpenViking。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	out := flag.String("out", "", "新的私有输出目录")
	native := flag.String("native-root", "", "OpenViking 数据目录（只读，生成全部文件哈希）")
	uploads := flag.String("upload-root", "", "Multica 本地附件目录（只读）")
	flag.Parse()
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	if err := os.Mkdir(*out, 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var database, snapshot string
	if err = tx.QueryRow(ctx, "SELECT current_database(),pg_current_snapshot()::text").Scan(&database, &snapshot); err != nil {
		return err
	}
	counts := map[string]int{}
	tables := []string{"workspace", "project", "issue_status", "issue", "agent", "agent_runtime", "agent_skill", "agent_task_queue", "chat_session", "chat_message", "comment", "attachment", "skill", "skill_file", "skill_asset"}
	for _, table := range tables {
		query := "SELECT to_jsonb(t) FROM " + pgx.Identifier{table}.Sanitize() + " t ORDER BY to_jsonb(t)->>'id'"
		if table == "agent" {
			query = `SELECT jsonb_build_object('id',id,'workspace_id',workspace_id,'name',name,'kind',kind,'system_key',system_key,'archived_at',archived_at,'runtime_id',runtime_id,'model',model,'thinking_level',thinking_level,'service_tier',service_tier,'instructions',instructions,'openviking_user',COALESCE(NULLIF(custom_env->>'MULTICA_OPENVIKING_USER',''),'agent-'||id::text)) FROM agent ORDER BY id`
		}
		if table == "agent_runtime" {
			query = `SELECT jsonb_build_object('id',id,'workspace_id',workspace_id,'provider',provider,'name',name,'runtime_mode',runtime_mode,'status',status,'profile_id',profile_id,'daemon_id',daemon_id) FROM agent_runtime ORDER BY id`
		}
		rows, err := tx.Query(ctx, query)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(*out, table+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			rows.Close()
			return err
		}
		encoder := json.NewEncoder(file)
		for rows.Next() {
			var raw json.RawMessage
			if err = rows.Scan(&raw); err == nil {
				err = encoder.Encode(raw)
			}
			if err != nil {
				break
			}
			counts[table]++
		}
		rowErr := rows.Err()
		rows.Close()
		syncErr := file.Sync()
		closeErr := file.Close()
		for _, e := range []error{err, rowErr, syncErr, closeErr} {
			if e != nil {
				return e
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	roots := map[string]string{"openviking": *native, "uploads": *uploads}
	for label, root := range roots {
		if root == "" {
			continue
		}
		file, err := os.OpenFile(filepath.Join(*out, label+"-files.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(file)
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				target, err := os.Readlink(path)
				if err != nil {
					return fmt.Errorf("unsupported source %s: %w", rel, err)
				}
				return encoder.Encode(map[string]any{"path": rel, "symlink": target})
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			n, err := io.Copy(h, in)
			in.Close()
			if err != nil {
				return err
			}
			after, err := os.Stat(path)
			if err != nil {
				return err
			}
			counts[label+"_files"]++
			return encoder.Encode(map[string]any{"path": rel, "bytes": n, "sha256": hex.EncodeToString(h.Sum(nil)), "mode": info.Mode().Perm(), "changed_during_read": after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime())})
		})
		syncErr := file.Sync()
		closeErr := file.Close()
		for _, e := range []error{err, syncErr, closeErr} {
			if e != nil {
				return e
			}
		}
	}
	manifest := map[string]any{"created_at": time.Now().UTC(), "database": database, "snapshot": snapshot, "counts": counts, "roots": roots, "consistency": "数据库为只读一致性快照；在线文件单独校验，changed_during_read 必须复核。", "ownership": "未指定目标的历史数据保留原归属，禁止按名称猜测迁移。"}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*out, "inventory.json"), raw, 0600); err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}
