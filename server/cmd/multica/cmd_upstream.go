package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/upstream"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// upstream 命令组只给同步 Agent 用：领批次、回传、推进游标。
// 人看清单在 Web 的「配置 → 上游同步」页面，不走命令行。
var upstreamCmd = &cobra.Command{
	Use:   "upstream",
	Short: "Sync the upstream repository (agent-facing)",
}

var upstreamStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Claim the next sync batch and print its range",
	Args:  cobra.NoArgs,
	RunE:  runUpstreamStart,
}

var upstreamReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Report commits and clusters for the claimed batch",
	Args:  cobra.NoArgs,
	RunE:  runUpstreamReport,
}

var upstreamFinishCmd = &cobra.Command{
	Use:   "finish",
	Short: "Close the batch and advance the cursor",
	Args:  cobra.NoArgs,
	RunE:  runUpstreamFinish,
}

var upstreamFailCmd = &cobra.Command{
	Use:   "fail",
	Short: "Mark the batch failed with a reason",
	Args:  cobra.NoArgs,
	RunE:  runUpstreamFail,
}

func init() {
	upstreamCmd.GroupID = groupCore
	upstreamStartCmd.Flags().String("repo", ".", "local repository used only for comparison")
	upstreamStartCmd.Flags().String("run", "", "run id from a previous start (optional; server resolves it from the task)")
	upstreamReportCmd.Flags().String("run", "", "run id returned by 'upstream start'")
	upstreamReportCmd.Flags().String("file", "", "path to the JSON report (required)")
	upstreamFinishCmd.Flags().String("run", "", "run id returned by 'upstream start'")
	upstreamFinishCmd.Flags().String("cursor", "", "commit sha this batch reached (required)")
	upstreamFinishCmd.Flags().Bool("has-more", false, "whether older/remaining commits still need a batch")
	upstreamFailCmd.Flags().String("run", "", "run id returned by 'upstream start'")
	upstreamFailCmd.Flags().String("error", "", "failure reason")
	_ = upstreamReportCmd.MarkFlagRequired("file")

	heartbeat := &cobra.Command{Use: "heartbeat", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, err := upstreamRunPath(cmd, "/heartbeat")
		if err != nil {
			return err
		}
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		var out map[string]any
		return client.PostJSON(ctx, p, map[string]any{}, &out)
	}}
	heartbeat.Flags().String("run", "", "current run id")
	upstreamCmd.AddCommand(upstreamStartCmd, upstreamReportCmd, upstreamFinishCmd, upstreamFailCmd, heartbeat)
}

func upstreamRunPath(cmd *cobra.Command, suffix string) (string, error) {
	runID, _ := cmd.Flags().GetString("run")
	if runID == "" {
		return "", fmt.Errorf("--run is required")
	}
	return "/api/upstream/runs/" + url.PathEscape(runID) + suffix, nil
}

func runUpstreamStart(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var out upstream.StartResult
	if err := client.PostJSON(ctx, "/api/upstream/runs/start", map[string]any{}, &out); err != nil {
		return fmt.Errorf("claim batch: %w", err)
	}
	repo, _ := cmd.Flags().GetString("repo")
	repo, err = filepath.Abs(repo)
	if err != nil {
		return err
	}
	home, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	mirror := filepath.Join(home, "multica-upstream", out.RunID)
	gitCtx, stop := context.WithTimeout(context.Background(), 5*time.Minute)
	defer stop()
	if out.Manifest != nil {
		return cli.PrintJSON(os.Stdout, map[string]any{"run_id": out.RunID, "manifest": out.Manifest, "mirror": mirror})
	}
	m, err := upstream.BuildManifest(gitCtx, repo, mirror, out.Config, out.Processed)
	if err != nil {
		var ignored map[string]any
		_ = client.PostJSON(context.Background(), "/api/upstream/runs/"+out.RunID+"/fail", map[string]any{"error": err.Error()}, &ignored)
		return err
	}
	for i := range m.Commits {
		m.Commits[i].ClusterKey = out.Assignments[m.Commits[i].SHA]
	}
	var ack map[string]any
	prepareCtx, cancelPrepare := cli.APIContext(context.Background())
	defer cancelPrepare()
	if err = client.PostJSON(prepareCtx, "/api/upstream/runs/"+out.RunID+"/prepare", m, &ack); err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, map[string]any{"run_id": out.RunID, "manifest": m, "mirror": mirror})
}

func runUpstreamReport(cmd *cobra.Command, _ []string) error {
	path, err := upstreamRunPath(cmd, "/items")
	if err != nil {
		return err
	}
	file, _ := cmd.Flags().GetString("file")
	// Agent 生成的报告可能很大，但 4MB 已经远超单批 300 条的上限。
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read report file: %w", err)
	}
	if len(raw) > 4<<20 {
		return fmt.Errorf("report file too large (max 4MB)")
	}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("report file is not valid JSON: %w", err)
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var out map[string]any
	if err := client.PostJSON(ctx, path, body, &out); err != nil {
		return fmt.Errorf("report batch: %w", err)
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runUpstreamFinish(cmd *cobra.Command, _ []string) error {
	path, err := upstreamRunPath(cmd, "/finish")
	if err != nil {
		return err
	}
	cursor, _ := cmd.Flags().GetString("cursor")
	hasMore, _ := cmd.Flags().GetBool("has-more")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var out map[string]any
	if err := client.PostJSON(ctx, path, map[string]any{"cursor_sha": cursor, "has_more": hasMore}, &out); err != nil {
		return fmt.Errorf("finish batch: %w", err)
	}
	runID, _ := cmd.Flags().GetString("run")
	if home, e := os.UserCacheDir(); e == nil && len(runID) == 36 && filepath.Base(runID) == runID {
		_ = os.RemoveAll(filepath.Join(home, "multica-upstream", runID))
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runUpstreamFail(cmd *cobra.Command, _ []string) error {
	path, err := upstreamRunPath(cmd, "/fail")
	if err != nil {
		return err
	}
	message, _ := cmd.Flags().GetString("error")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var out map[string]any
	if err := client.PostJSON(ctx, path, map[string]any{"error": message}, &out); err != nil {
		return fmt.Errorf("fail batch: %w", err)
	}
	runID, _ := cmd.Flags().GetString("run")
	if home, e := os.UserCacheDir(); e == nil && len(runID) == 36 && filepath.Base(runID) == runID {
		_ = os.RemoveAll(filepath.Join(home, "multica-upstream", runID))
	}
	return cli.PrintJSON(os.Stdout, out)
}
