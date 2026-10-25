// 盘点并按不可变清单修正可确定的历史证据；输出不包含补丁或凭据。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/codediff"
)

type repair struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Before string `json:"before_sha256"`
	After  string `json:"after_sha256"`
	body   []byte
}

func hash(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
func cleanResult(raw []byte) ([]byte, bool) {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil {
		return raw, false
	}
	var diff map[string]any
	changed := false
	var walk func(map[string]any)
	walk = func(root map[string]any) {
		files, _ := root["files"].([]any)
		for _, item := range files {
			file, ok := item.(map[string]any)
			if !ok {
				continue
			}
			patch, _ := file["patch"].(string)
			path, _ := file["path"].(string)
			capture := &codediff.Diff{Files: []codediff.File{{Path: path, Patch: patch}}}
			codediff.RedactDiff(capture)
			if capture.Files[0].Patch != patch {
				file["patch"] = capture.Files[0].Patch
				file["redacted"] = true
				changed = true
			}
		}
		roots, _ := root["roots"].([]any)
		for _, item := range roots {
			if child, ok := item.(map[string]any); ok {
				walk(child)
			}
		}
	}
	if json.Unmarshal(result["code_diff"], &diff) == nil {
		walk(diff)
		if changed {
			result["code_diff"], _ = json.Marshal(diff)
		}
	}
	var text []map[string]any
	if json.Unmarshal(result["text_changes"], &text) == nil {
		touched := false
		for _, item := range text {
			before, _ := item["before"].(string)
			after, _ := item["after"].(string)
			if codediff.ContainsSecret(before) || codediff.ContainsSecret(after) {
				item["before"] = ""
				item["after"] = ""
				item["redacted"] = true
				touched = true
			}
		}
		if touched {
			result["text_changes"], _ = json.Marshal(text)
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(result)
	if err != nil {
		return raw, false
	}
	return out, true
}
func run() error {
	workspace := flag.String("workspace", "", "工作区 UUID")
	expected := flag.String("apply-inventory", "", "仅应用与此盘点摘要一致的清单")
	flag.Parse()
	if *workspace == "" || os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("必须显式指定工作区和 DATABASE_URL")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	access := pgx.ReadOnly
	if *expected != "" {
		access = pgx.ReadWrite
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: access})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='3s'"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout='60s'"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT q.id::text,q.result FROM agent_task_queue q JOIN issue i ON i.id=q.issue_id WHERE i.workspace_id=$1 AND q.status IN ('completed','failed','cancelled') AND (q.result ? 'code_diff' OR q.result ? 'text_changes') ORDER BY q.id`, *workspace)
	if err != nil {
		return err
	}
	repairs := []repair{}
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		if after, changed := cleanResult(raw); changed {
			repairs = append(repairs, repair{Kind: "result", ID: id, Before: hash(raw), After: hash(after), body: after})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT c.id::text,c.author_type FROM comment c WHERE c.workspace_id=$1 AND c.author_type='member' AND EXISTS(SELECT 1 FROM delivery_notice n WHERE n.comment_id=c.id) AND NOT EXISTS(SELECT 1 FROM issue_delivery_authorization a WHERE a.comment_id=c.id) ORDER BY c.id`, *workspace)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, author string
		if err = rows.Scan(&id, &author); err != nil {
			rows.Close()
			return err
		}
		repairs = append(repairs, repair{Kind: "platform_notice", ID: id, Before: hash([]byte(author)), After: hash([]byte("system"))})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	inventory, _ := json.Marshal(repairs)
	digest := hash(inventory)
	if *expected != "" {
		if !strings.EqualFold(*expected, digest) {
			return fmt.Errorf("盘点已变化，未执行修正；重新盘点后核对")
		}
		for _, item := range repairs {
			if item.Kind == "result" {
				_, err = tx.Exec(ctx, `UPDATE agent_task_queue SET result=$2 WHERE id=$1`, item.ID, item.body)
			} else {
				_, err = tx.Exec(ctx, `UPDATE comment SET author_type='system' WHERE id=$1 AND author_type='member'`, item.ID)
			}
			if err != nil {
				return err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"workspace": *workspace, "inventory_sha256": digest, "applied": *expected != "", "repairs": repairs, "unrecoverable": "旧文件基线和完成后已被覆盖的原文不能从当前状态重建，未回填"})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
