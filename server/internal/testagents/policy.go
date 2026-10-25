// Package testagents 为普通与托管测试 Agent 提供同一份环境模型选择。
package testagents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Policy struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Thinking string `json:"thinking_level"`
	Tier     string `json:"service_tier"`
}

func (p Policy) Valid() bool {
	switch p.Provider {
	case "codebuddy":
		return p.Model == "deepseek-v4.1-flash" && p.Thinking == "" && p.Tier == ""
	case "codex":
		return p.Model == "gpt-6.1-sol" && p.Thinking == "low" && p.Tier == "default"
	}
	return false
}
func Environment(name string) bool {
	return name == "int" || name == "verify-1" || name == "verify-2" || name == "verify-3"
}

type Reader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Writer interface {
	Reader
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}
type Selection struct {
	Policy
	RuntimeID   string
	RuntimeMode string
}

func Resolve(ctx context.Context, db Reader, workspace, agentID string) (Selection, bool, error) {
	env := strings.TrimSpace(os.Getenv("MULTICA_ENV_ID"))
	if !Environment(env) {
		return Selection{}, false, nil
	}
	var raw []byte
	var daemon string
	err := db.QueryRow(ctx, `SELECT COALESCE(desired->'overrides'->$3,desired->'policy'),observed->>'daemon_id' FROM test_agent_control WHERE name=$2 ORDER BY (workspace_id=$1) DESC,updated_at DESC LIMIT 1`, workspace, env, agentID).Scan(&raw, &daemon)
	var result Selection
	if err != nil || json.Unmarshal(raw, &result.Policy) != nil || !result.Policy.Valid() {
		return result, true, fmt.Errorf("本环境测试 Agent 默认配置尚未就绪")
	}
	err = db.QueryRow(ctx, `SELECT id::text,runtime_mode FROM agent_runtime WHERE workspace_id=$1 AND provider=$2 AND daemon_id=$3 AND status='online' AND profile_id IS NULL`, workspace, result.Provider, daemon).Scan(&result.RuntimeID, &result.RuntimeMode)
	if err != nil {
		return result, true, fmt.Errorf("本环境测试 runtime 尚未就绪")
	}
	return result, true, nil
}

// Apply 在托管 Agent 保存事务中应用环境选择，保证第一次派发前已使用当前配置。
func Apply(ctx context.Context, db Writer, workspace, agentID string) error {
	selection, enabled, err := Resolve(ctx, db, workspace, agentID)
	if err != nil || !enabled {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE agent SET runtime_id=$3,runtime_mode=$4,model=$5,thinking_level=$6,service_tier=$7,updated_at=now() WHERE workspace_id=$1 AND id=$2`, workspace, agentID, selection.RuntimeID, selection.RuntimeMode, selection.Model, selection.Thinking, selection.Tier)
	if err == nil && tag.RowsAffected() != 1 {
		return fmt.Errorf("测试 Agent 不属于当前环境工作区")
	}
	return err
}
