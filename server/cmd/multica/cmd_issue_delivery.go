package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// 交付命令与页面使用同一套版本和确认接口。
func init() {
	group := &cobra.Command{Use: "delivery", Short: "Plan confirmation, integration queue and production review"}
	for _, action := range []string{"diagnose", "status", "plan", "confirm-plan", "revoke-release", "submit", "bind-checks", "accept", "approve", "retest", "cancel", "owner", "claim-owner", "recover-owner", "release-owner"} {
		action := action
		cmd := &cobra.Command{Use: action + " <issue-id>", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAPIClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := cli.APIContext(context.Background())
			defer cancel()
			ref, err := resolveIssueRef(ctx, client, args[0])
			if err != nil {
				return err
			}
			path := "/api/issues/" + ref.ID
			if action == "owner" {
				var result json.RawMessage
				if err = client.GetJSON(ctx, path+"/delivery-owner", &result); err != nil {
					return err
				}
				return cli.PrintJSON(os.Stdout, result)
			}
			if action != "diagnose" && action != "status" && action != "confirm-plan" && action != "revoke-release" {
				if err = deliveryCLIClaim(ctx, cmd, client, path, action); err != nil {
					return err
				}
				if action == "claim-owner" || action == "recover-owner" || action == "release-owner" {
					return cli.PrintJSON(os.Stdout, map[string]any{"status": "ok", "generation": client.DeliveryGeneration})
				}
			}
			var result json.RawMessage
			if action == "diagnose" {
				query, queryErr := deliveryDiagnosticQuery(cmd)
				if queryErr != nil {
					return queryErr
				}
				err = client.GetJSON(ctx, path+"/deliveries/diagnostics?"+query.Encode(), &result)
			} else if action == "status" {
				err = client.GetJSON(ctx, path+"/deliveries", &result)
			} else {
				file, _ := cmd.Flags().GetString("file")
				if file == "" {
					return fmt.Errorf("--file requires a JSON request file")
				}
				raw, readErr := os.ReadFile(file)
				if readErr != nil {
					return readErr
				}
				var body map[string]any
				if err = json.Unmarshal(raw, &body); err != nil {
					return err
				}
				switch action {
				case "plan":
					err = client.PutJSON(ctx, path+"/delivery-plan", body, &result)
				case "confirm-plan":
					err = client.PostJSON(ctx, path+"/delivery-plan/confirm", body, &result)
				case "revoke-release":
					err = client.PostJSON(ctx, path+"/delivery-plan/revoke-release", body, &result)
				case "submit":
					err = client.PostJSON(ctx, path+"/deliveries", body, &result)
				default:
					id, _ := cmd.Flags().GetString("delivery")
					if id == "" {
						return fmt.Errorf("--delivery is required")
					}
					body["action"] = action
					err = client.PostJSON(ctx, path+"/deliveries/"+id+"/action", body, &result)
				}
			}
			if err != nil {
				return err
			}
			return cli.PrintJSON(os.Stdout, result)
		}}
		if action == "bind-checks" {
			cmd.Long = "将原已确认方案的业务验收清单绑定到尚未发布的交付。JSON 包含 revision、plan_id、required_checks（name、stage）；verification 与 integration 各至少一项。相同清单可幂等重试，已绑定项不能删减或替换；不需要重新申请用户发布授权。"
		}
		// 1. Document the same repair payload consumed by the existing delivery action API.
		if action == "retest" {
			cmd.Long = "重新验收待审候选，或修复未批准生产的 needs_changes 原交付。失败修复 JSON 包含 revision、evidence 和 repair：operation_id、source_commit、source_branch、base_commit、environment、generation、seed_id、seed_sha256、compatibility_evidence。保留原槽位和失败记录，不重置数据、不批准生产；repair 只支持尚未部署的隔离候选。已部署且无生产批准的仅 int 集成失败使用 revision、准确 candidate_id 与 evidence（不传 repair），保留失败历史和 verification 证据并回正式集成队列；必须无活动发布占用。相同操作号与完整 repair 请求可幂等重试。"
		}
		if action == "accept" {
			cmd.Long = "提交实际验收证据。组合验收由代表交付提交；JSON 包含 revision、candidate_id 和 evidence，须覆盖当前方案 required_checks 的对应阶段；组合代表须覆盖全部成员的 integration 项，候选编号必须与当前组合一致。相同候选和证据可幂等重试。"
		}
		cmd.Flags().String("file", "", "JSON request file (preserve operation_id on retries)")
		cmd.Flags().String("delivery", "", "Delivery UUID")
		if action == "diagnose" {
			cmd.Short = "Read-only bounded delivery timing and blocker diagnostics"
			cmd.Flags().String("since", "", "RFC3339 incremental cursor time")
			cmd.Flags().String("after", "", "Event UUID from next_cursor (requires --since)")
			cmd.Flags().Int("limit", 50, "Event count, 1–200; latest window unless --since")
		}
		cmd.Flags().String("output", "json", "Output format")
		cmd.Flags().String("execution", "", "Verified task ID for Agent calls; stable session ID for external member calls (defaults to MULTICA_TASK_ID or CODEX_THREAD_ID)")
		cmd.Flags().String("recovery-evidence", "", "Observed old execution and environment state for explicit handoff")
		group.AddCommand(cmd)
	}
	issueCmd.AddCommand(group)
}

// deliveryCLIClaim 将会话凭据保存在私有配置目录，独立 worktree 不共享写入身份。
func deliveryCLIClaim(ctx context.Context, cmd *cobra.Command, client *cli.APIClient, path, action string) error {
	execution, _ := cmd.Flags().GetString("execution")
	if execution == "" {
		execution = os.Getenv("MULTICA_TASK_ID")
	}
	if execution == "" {
		execution = os.Getenv("CODEX_THREAD_ID")
	}
	if execution == "" {
		return fmt.Errorf("delivery writes require --execution <stable-session-id>")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(client.BaseURL+client.WorkspaceID+path+execution)))
	dir := filepath.Join(home, ".multica", "delivery-owners")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file := filepath.Join(dir, key+".json")
	var saved struct {
		Token      string `json:"token"`
		Generation int64  `json:"generation"`
	}
	if raw, e := os.ReadFile(file); e == nil {
		if e = json.Unmarshal(raw, &saved); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	operation := "claim"
	if action == "recover-owner" {
		operation = "recover"
	}
	if action == "release-owner" {
		operation = "release"
	}
	evidence, _ := cmd.Flags().GetString("recovery-evidence")
	body := map[string]any{"action": operation, "execution_id": execution, "token": saved.Token, "generation": saved.Generation, "recovery_evidence": evidence}
	if err = client.PostJSON(ctx, path+"/delivery-owner", body, &saved); err != nil {
		return err
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err = os.WriteFile(file, raw, 0600); err != nil {
		return err
	}
	client.DeliveryToken = saved.Token
	client.DeliveryGeneration = saved.Generation
	return nil
}

// deliveryDiagnosticQuery 不领取负责人或发布租约；增量游标来自上一页的 next_cursor。
func deliveryDiagnosticQuery(cmd *cobra.Command) (url.Values, error) {
	q := url.Values{}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("--limit requires 1–200")
	}
	q.Set("limit", strconv.Itoa(limit))
	since, _ := cmd.Flags().GetString("since")
	after, _ := cmd.Flags().GetString("after")
	delivery, _ := cmd.Flags().GetString("delivery")
	if since != "" {
		if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
			return nil, fmt.Errorf("--since requires RFC3339 time")
		}
		q.Set("since", since)
	}
	if after != "" {
		if since == "" {
			return nil, fmt.Errorf("--after requires --since")
		}
		q.Set("after", after)
	}
	if delivery != "" {
		q.Set("delivery_id", delivery)
	}
	return q, nil
}
