// 文件职责：实现 `multica runtime` 命令组——列出工作区的 agent runtime（list）、
// 查某 runtime 的 token 用量与每小时任务活跃度、改名（rename）、发起 CLI 更新
// （update）、删除 runtime（delete）。runtime 的 profile 管理在
// cmd_runtime_profile.go（`runtime profile ...`）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// runtimeCmd 是 `multica runtime` 父命令，挂 list/usage/activity/update/rename/
// delete 及 profile 子命令组。
var runtimeCmd = &cobra.Command{
	Use:   "runtime",
	Short: "Work with agent runtimes",
}

var runtimeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List runtimes in the workspace",
	RunE:  runRuntimeList,
}

// runtimeUsageCmd 查某 runtime 近 N 天（默认 90）的每日 token 用量。
var runtimeUsageCmd = &cobra.Command{
	Use:   "usage <runtime-id>",
	Short: "Get token usage for a runtime",
	Args:  exactArgs(1),
	RunE:  runRuntimeUsage,
}

// runtimeActivityCmd 查某 runtime 每小时的任务活跃数。
var runtimeActivityCmd = &cobra.Command{
	Use:   "activity <runtime-id>",
	Short: "Get hourly task activity for a runtime",
	Args:  exactArgs(1),
	RunE:  runRuntimeActivity,
}

// runtimeUpdateCmd 让某 runtime 上的 CLI 更新到指定版本。
var runtimeUpdateCmd = &cobra.Command{
	Use:   "update <runtime-id>",
	Short: "Initiate a CLI update on a runtime",
	Args:  exactArgs(1),
	RunE:  runRuntimeUpdate,
}

var runtimeRenameCmd = &cobra.Command{
	Use:   "rename <runtime-id> <name>",
	Short: "Set a custom display name for a runtime",
	Long: "Set (or clear) a runtime's custom display name.\n\n" +
		"Pass an empty name (\"\") to clear the custom name and fall back to the default. " +
		"Use --machine to apply the name to every runtime on the same machine.",
	Args: exactArgs(2),
	RunE: runRuntimeRename,
}

// runtimeDeleteCmd 从工作区删除某 runtime。
var runtimeDeleteCmd = &cobra.Command{
	Use:   "delete <runtime-id>",
	Short: "Delete a runtime from the workspace",
	Long: "Delete a runtime registration from the workspace.\n\n" +
		"By default this refuses when active agents are still bound to the runtime. " +
		"Pass --cascade to unbind those agents, cancel their queued/running tasks, and delete the runtime. " +
		"Unbound agents keep their configuration, chats and task history; bind them to another runtime to run them again.",
	Args: exactArgs(1),
	RunE: runRuntimeDelete,
}

func init() {
	runtimeCmd.AddCommand(runtimeListCmd)
	runtimeCmd.AddCommand(runtimeUsageCmd)
	runtimeCmd.AddCommand(runtimeActivityCmd)
	runtimeCmd.AddCommand(runtimeUpdateCmd)
	runtimeCmd.AddCommand(runtimeRenameCmd)
	runtimeCmd.AddCommand(runtimeDeleteCmd)

	// runtime list
	runtimeListCmd.Flags().String("output", "table", "Output format: table or json")

	// runtime usage
	runtimeUsageCmd.Flags().String("output", "table", "Output format: table or json")
	runtimeUsageCmd.Flags().Int("days", 90, "Number of days of usage data to retrieve (max 365)")

	// runtime activity
	runtimeActivityCmd.Flags().String("output", "table", "Output format: table or json")

	// runtime update
	runtimeUpdateCmd.Flags().String("target-version", "", "Target version to update to (required)")
	runtimeUpdateCmd.Flags().String("output", "json", "Output format: table or json")
	runtimeUpdateCmd.Flags().Bool("wait", false, "Wait for update to complete (poll until done)")

	// runtime rename
	runtimeRenameCmd.Flags().Bool("machine", false, "Apply the name to every runtime on the same machine")
	runtimeRenameCmd.Flags().String("output", "table", "Output format: table or json")

	// runtime delete
	runtimeDeleteCmd.Flags().Bool("cascade", false, "Unbind active agents from the runtime, cancel their tasks, then delete the runtime")
	runtimeDeleteCmd.Flags().String("output", "table", "Output format: table or json")
}

// ---------------------------------------------------------------------------
// Runtime commands —— 各 runtime 子命令的 RunE 实现
// ---------------------------------------------------------------------------

// runRuntimeList 执行 `runtime list`：拉取工作区 runtime 列表并按 --output 输出。
func runRuntimeList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var runtimes []map[string]any
	if err := client.GetJSON(ctx, "/api/runtimes", &runtimes); err != nil {
		return fmt.Errorf("list runtimes: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, runtimes)
	}

	headers := []string{"ID", "NAME", "MODE", "PROVIDER", "STATUS", "LAST_SEEN"}
	rows := make([][]string, 0, len(runtimes))
	for _, rt := range runtimes {
		rows = append(rows, []string{
			strVal(rt, "id"),
			strVal(rt, "name"),
			strVal(rt, "runtime_mode"),
			strVal(rt, "provider"),
			strVal(rt, "status"),
			strVal(rt, "last_seen_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runRuntimeUsage 执行 `runtime usage <runtime-id>`：按日取 token 用量明细。
func runRuntimeUsage(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	days, _ := cmd.Flags().GetInt("days")
	if days < 1 || days > 365 {
		return fmt.Errorf("--days must be between 1 and 365")
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var usage []map[string]any
	path := fmt.Sprintf("/api/runtimes/%s/usage?days=%d", args[0], days)
	if err := client.GetJSON(ctx, path, &usage); err != nil {
		return fmt.Errorf("get runtime usage: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, usage)
	}

	headers := []string{"DATE", "PROVIDER", "MODEL", "INPUT_TOKENS", "OUTPUT_TOKENS", "CACHE_READ", "CACHE_WRITE"}
	rows := make([][]string, 0, len(usage))
	for _, u := range usage {
		rows = append(rows, []string{
			strVal(u, "date"),
			strVal(u, "provider"),
			strVal(u, "model"),
			strVal(u, "input_tokens"),
			strVal(u, "output_tokens"),
			strVal(u, "cache_read_tokens"),
			strVal(u, "cache_write_tokens"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runRuntimeActivity 执行 `runtime activity <runtime-id>`：取每小时任务计数。
func runRuntimeActivity(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var activity []map[string]any
	if err := client.GetJSON(ctx, "/api/runtimes/"+args[0]+"/activity", &activity); err != nil {
		return fmt.Errorf("get runtime activity: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, activity)
	}

	headers := []string{"HOUR", "COUNT"}
	rows := make([][]string, 0, len(activity))
	for _, a := range activity {
		rows = append(rows, []string{
			strVal(a, "hour"),
			strVal(a, "count"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runRuntimeDelete 执行 `runtime delete <runtime-id>`：普通删除遇到「仍有活跃 agent
// 绑定」（409）时给出引导；带 --cascade 则走解绑+删除端点（把期望的活跃 agent ID
// 一起带过去，防止竞态删除后又被重绑的 agent）。
func runRuntimeDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	runtimeID := args[0]
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	err = client.DeleteJSON(ctx, "/api/runtimes/"+runtimeID)
	if err == nil {
		return printRuntimeDeleteResult(cmd, map[string]any{
			"id":      runtimeID,
			"deleted": true,
		})
	}

	conflict, ok := runtimeDeleteConflict(err)
	if !ok {
		// Any other 409 is a deliberate, already-explained refusal — most often
		// a profile-backed instance that cannot be deleted on its own. Show the
		// server's guidance instead of a raw HTTP wrapper; --cascade cannot get
		// past these, so there is nothing more for this command to try.
		if _, msg, isConflict := serverConflictMessage(err); isConflict {
			return errors.New(msg)
		}
		return fmt.Errorf("delete runtime: %w", err)
	}

	cascade, _ := cmd.Flags().GetBool("cascade")
	if !cascade {
		return fmt.Errorf(
			"delete runtime: runtime has active agents bound to it (%s); rebind them to another runtime first, or rerun with --cascade to unbind them and delete the runtime (the agents and their history are kept)",
			strings.Join(conflict.AgentDisplays(), ", "),
		)
	}

	body := map[string]any{
		"expected_active_agent_ids": conflict.AgentIDs(),
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/runtimes/"+runtimeID+"/unbind-agents-and-delete", body, &result); err != nil {
		return fmt.Errorf("cascade delete runtime: %w", err)
	}
	result["id"] = runtimeID
	result["deleted"] = true
	return printRuntimeDeleteResult(cmd, result)
}

// runRuntimeRename 执行 `runtime rename <runtime-id> <name>`：设/清自定义显示名；
// --machine 时把名字应用到同机所有 runtime。
func runRuntimeRename(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	machine, _ := cmd.Flags().GetBool("machine")
	body := map[string]any{"custom_name": args[1]}
	if machine {
		body["apply_to_machine"] = true
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var rt map[string]any
	if err := client.PatchJSON(ctx, "/api/runtimes/"+args[0], body, &rt); err != nil {
		return fmt.Errorf("rename runtime: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, rt)
	}

	if strings.TrimSpace(args[1]) == "" {
		fmt.Fprintf(os.Stderr, "Custom name cleared; runtime is now %q.\n", strVal(rt, "name"))
	} else {
		fmt.Fprintf(os.Stderr, "Runtime renamed to %q.\n", strVal(rt, "custom_name"))
	}
	return nil
}

// runRuntimeUpdate 执行 `runtime update <runtime-id> --target-version <v>`：发起远端
// runtime 的 CLI 更新。带 --wait 时每 2 秒轮询一次更新状态直到 completed/failed/
// timeout 或整体超时。
func runRuntimeUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	targetVersion, _ := cmd.Flags().GetString("target-version")
	if targetVersion == "" {
		return fmt.Errorf("--target-version is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(150*time.Second))
	defer cancel()

	body := map[string]any{
		"target_version": targetVersion,
	}

	var update map[string]any
	if err := client.PostJSON(ctx, "/api/runtimes/"+args[0]+"/update", body, &update); err != nil {
		return fmt.Errorf("initiate update: %w", err)
	}

	wait, _ := cmd.Flags().GetBool("wait")
	if !wait {
		output, _ := cmd.Flags().GetString("output")
		if output == "json" {
			return cli.PrintJSON(os.Stdout, update)
		}
		fmt.Printf("Update initiated: %s (status: %s)\n", strVal(update, "id"), strVal(update, "status"))
		return nil
	}

	// 轮询直到 completed/failed/timeout 或整体超时。
	updateID := strVal(update, "id")
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for update (last status: %s)", strVal(update, "status"))
		case <-time.After(2 * time.Second):
		}

		if err := client.GetJSON(ctx, "/api/runtimes/"+args[0]+"/update/"+updateID, &update); err != nil {
			return fmt.Errorf("get update status: %w", err)
		}

		status := strVal(update, "status")
		if status == "completed" || status == "failed" || status == "timeout" {
			output, _ := cmd.Flags().GetString("output")
			if output == "json" {
				return cli.PrintJSON(os.Stdout, update)
			}
			if status == "completed" {
				fmt.Printf("Update completed: %s\n", strVal(update, "output"))
			} else {
				fmt.Printf("Update %s: %s\n", status, strVal(update, "error"))
			}
			return nil
		}
	}
}

// serverConflictMessage pulls the server's own sentence out of a 409 body.
//
// The refusals behind runtime and profile deletion are written to be read by
// the person who ran the command — they name the machine, the profile and what
// to do instead. HTTPError.Error() would bury that sentence inside a raw JSON
// dump of the whole response, which is what a blocked user used to see.
func serverConflictMessage(err error) (code string, message string, ok bool) {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		return "", "", false
	}
	body := strings.TrimSpace(httpErr.Body)
	if body == "" {
		return "", "", false
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		// Not JSON. An older or proxied server can still put a readable
		// sentence here, and swallowing it would be worse than passing it
		// through — there is nothing to leak when the body was never
		// structured in the first place.
		return "", body, true
	}
	if strings.TrimSpace(payload.Error) == "" {
		return payload.Code, "", false
	}
	return payload.Code, payload.Error, true
}

type runtimeDeleteConflictPayload struct {
	Code         string `json:"code"`
	Error        string `json:"error"`
	ActiveAgents []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"active_agents"`
}

// runtimeDeleteConflict 尝试把删除错误解成 runtime_has_active_agents 冲突；
// 非 409、code 不匹配或没有活跃 agent 时返回 ok=false。
func runtimeDeleteConflict(err error) (runtimeDeleteConflictPayload, bool) {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		return runtimeDeleteConflictPayload{}, false
	}
	var payload runtimeDeleteConflictPayload
	if json.Unmarshal([]byte(httpErr.Body), &payload) != nil {
		return runtimeDeleteConflictPayload{}, false
	}
	if payload.Code != "runtime_has_active_agents" || len(payload.ActiveAgents) == 0 {
		return runtimeDeleteConflictPayload{}, false
	}
	return payload, true
}

// AgentIDs 返回冲突里活跃 agent 的 ID 列表（供 cascade 请求体使用）。
func (p runtimeDeleteConflictPayload) AgentIDs() []string {
	ids := make([]string, 0, len(p.ActiveAgents))
	for _, agent := range p.ActiveAgents {
		if agent.ID != "" {
			ids = append(ids, agent.ID)
		}
	}
	return ids
}

// AgentDisplays 把活跃 agent 渲染成 "名字 (ID)" 形式的人类可读清单，供报错信息用。
func (p runtimeDeleteConflictPayload) AgentDisplays() []string {
	displays := make([]string, 0, len(p.ActiveAgents))
	for _, agent := range p.ActiveAgents {
		switch {
		case agent.Name != "" && agent.ID != "":
			displays = append(displays, fmt.Sprintf("%s (%s)", agent.Name, agent.ID))
		case agent.Name != "":
			displays = append(displays, agent.Name)
		case agent.ID != "":
			displays = append(displays, agent.ID)
		}
	}
	return displays
}

// printRuntimeDeleteResult 渲染删除结果：cascade 删除（带 agents_unbound）与普通
// 删除各给不同提示；并对老版本 server 只返回 agents_archived 的响应做兼容（MUL-5559
// 之前的镜像字段）。
func printRuntimeDeleteResult(cmd *cobra.Command, result map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	if agentsUnbound, ok := result["agents_unbound"]; ok {
		fmt.Fprintf(os.Stderr, "Runtime %s deleted; unbound %v agent(s)", strVal(result, "id"), agentsUnbound)
		if paused, ok := result["autopilots_paused"]; ok {
			fmt.Fprintf(os.Stderr, " and paused %v autopilot(s)", paused)
		}
		fmt.Fprintln(os.Stderr, ".")
		return nil
	}
	// 老版本 server 只回 MUL-5559 之前的镜像字段，做个兼容回退。
	if agentsArchived, ok := result["agents_archived"]; ok {
		fmt.Fprintf(os.Stderr, "Runtime %s deleted; processed %v agent(s).\n", strVal(result, "id"), agentsArchived)
		return nil
	}
	fmt.Fprintf(os.Stderr, "Runtime %s deleted.\n", strVal(result, "id"))
	return nil
}
