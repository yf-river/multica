// Package testagents 为普通与托管测试 Agent 提供同一份环境模型选择。
package testagents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Policy struct {
	Provider string `json:"provider"`
	// Runtime 是承载该模型的运行时 provider（codebuddy/codex/dsh…）。旧配置没有这个字段，
	// 回落到 Provider，行为与之前一致。
	Runtime  string `json:"runtime,omitempty"`
	Model    string `json:"model"`
	Thinking string `json:"thinking_level"`
	Tier     string `json:"service_tier"`
}

// modelID 允许 provider 前缀（DSH 的 `codebuddy/deepseek-v4.1-flash`）与常见分隔符。
var modelID = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
var runtimeID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// runtime 返回实际承载模型的运行时；未显式选择时与 provider 相同（兼容历史配置）。
func (p Policy) runtime() string {
	if p.Runtime != "" {
		return p.Runtime
	}
	return p.Provider
}

// Valid 只校验形状：运行时是已知 provider 风格的标识、模型 id 合法。模型是否真的被该运行时
// 支持由运行时在派发时判定，设置页则用运行时模型目录限制可选范围。
func (p Policy) Valid() bool {
	return runtimeID.MatchString(p.runtime()) && modelID.MatchString(p.Model)
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
	err = db.QueryRow(ctx, `SELECT id::text,runtime_mode FROM agent_runtime WHERE workspace_id=$1 AND provider=$2 AND daemon_id=$3 AND status='online' AND profile_id IS NULL`, workspace, result.runtime(), daemon).Scan(&result.RuntimeID, &result.RuntimeMode)
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
