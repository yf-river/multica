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
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// 交付命令与页面使用同一套版本和确认接口。
func init() {
	group := &cobra.Command{Use: "delivery", Short: "Plan confirmation, integration queue and production review"}
	for _, action := range []string{"diagnose", "status", "plan", "confirm-plan", "revoke-release", "submit", "bind-checks", "accept", "approve", "review-project-comments", "publish", "retest", "cancel", "owner", "claim-owner", "recover-owner", "release-owner"} {
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
				if action == "publish" {
					return deliveryCLIPublish(ctx, cmd, client, path)
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
			cmd.Long = "重新验收待审候选，或修复原交付。失败槽位修复 JSON 包含 revision、evidence 和 repair：operation_id、source_commit、source_branch、base_commit、environment、generation、seed_id、seed_sha256、compatibility_evidence。项目批次合并冲突修复使用同一原交付，repair 提供 operation_id、source_commit、source_branch、base_commit，修复后重新审查和测试。相同操作号与完整请求可幂等重试。"
		}
		if action == "accept" {
			cmd.Long = "提交实际验收证据。JSON 包含 revision、candidate_id 和 evidence，须覆盖本交付 required_checks 的对应阶段。项目批次各负责人提交自己的联调证据；其他批次由代表汇总全部成员的联调项。候选编号必须与当前组合一致。相同候选和证据可幂等重试。"
		}
		if action == "review-project-comments" {
			cmd.Long = "项目待发布后收到用户评论时，先核对最新评论及任务范围；范围未变且用户未要求暂停时，提交 JSON：revision、user_comments_version、scope_review。若需要修改或暂停，不得使用此动作绕过。"
		}
		if action == "publish" {
			cmd.Long = "一次完成审核与组合验收：先提交审核批准，随后在批次进入可验收时用同一份证据提交验收，直到发布完成或超时。JSON 包含 review 与 evidence；--timeout 控制最长等待。"
			cmd.Flags().Duration("timeout", 45*time.Minute, "最长等待时间")
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
	// 领取会在服务端生成令牌，先确认本地可保存，避免丢失刚领取的归属。
	probe, err := os.CreateTemp(dir, ".delivery-owner-*")
	if err != nil {
		return fmt.Errorf("交付归属凭据目录不可写: %w", err)
	}
	probeName := probe.Name()
	if err = probe.Close(); err != nil {
		os.Remove(probeName)
		return err
	}
	if err = os.Remove(probeName); err != nil {
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
		if operation != "release" {
			// 令牌无法持久化时，用刚领取的令牌释放归属，避免留下无法接续的占用。
			var released any
			releaseErr := client.PostJSON(ctx, path+"/delivery-owner", map[string]any{
				"action": "release", "execution_id": execution,
				"token": saved.Token, "generation": saved.Generation,
			}, &released)
			if releaseErr != nil {
				return fmt.Errorf("保存交付归属凭据失败: %w；释放归属也失败: %v", err, releaseErr)
			}
		}
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

// deliveryCLIPublish 把"审核批准 → 批次验收 → 发布完成"合成一次调用：每轮读取最新批次状态，
// 已就绪就提交验收，未就绪就继续等；发布完成、失败或超时即返回，重试幂等。
func deliveryCLIPublish(ctx context.Context, cmd *cobra.Command, client *cli.APIClient, path string) error {
	id, _ := cmd.Flags().GetString("delivery")
	file, _ := cmd.Flags().GetString("file")
	if id == "" || file == "" {
		return fmt.Errorf("publish requires --delivery and --file")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		return err
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	deadline := time.Now().Add(timeout)
	approved := false
	for {
		var status struct {
			Deliveries []struct {
				ID          string `json:"id"`
				Phase       string `json:"phase"`
				Revision    int64  `json:"revision"`
				CandidateID string `json:"candidate_id"`
				Error       string `json:"error"`
			} `json:"deliveries"`
		}
		if err = client.GetJSON(ctx, path+"/deliveries", &status); err != nil {
			return err
		}
		var current *struct {
			ID          string `json:"id"`
			Phase       string `json:"phase"`
			Revision    int64  `json:"revision"`
			CandidateID string `json:"candidate_id"`
			Error       string `json:"error"`
		}
		for i := range status.Deliveries {
			if status.Deliveries[i].ID == id {
				current = &status.Deliveries[i]
				break
			}
		}
		if current == nil {
			return fmt.Errorf("delivery %s not found", id)
		}
		switch current.Phase {
		case "published":
			return cli.PrintJSON(os.Stdout, map[string]any{"status": "published", "delivery": id, "candidate": current.CandidateID})
		case "needs_changes", "cancelled":
			return fmt.Errorf("delivery %s stopped in %s: %s", id, current.Phase, current.Error)
		case "approved":
			if !approved {
				body["action"] = "approve"
				var receipt json.RawMessage
				if err = client.PostJSON(ctx, path+"/deliveries/"+id+"/action", body, &receipt); err != nil {
					return err
				}
				approved = true
				break
			}
			if current.CandidateID != "" {
				accept := map[string]any{"action": "accept", "revision": current.Revision, "candidate_id": current.CandidateID, "evidence": body["evidence"]}
				var receipt json.RawMessage
				if e := client.PostJSON(ctx, path+"/deliveries/"+id+"/action", accept, &receipt); e != nil {
					// 批次尚未进入可验收阶段：等下一轮再试。
					if !strings.Contains(e.Error(), "验收") && !strings.Contains(e.Error(), "409") {
						return e
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("publish timed out in phase %s", current.Phase)
		}
		time.Sleep(10 * time.Second)
	}
}
