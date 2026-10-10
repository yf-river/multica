// 身体数据命令使用当前身份，报告内容仅通过私有健康接口传递。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/health"
	"github.com/spf13/cobra"
)

func init() {
	parent := &cobra.Command{Use: "health", Short: "Manage private body data and checkup reports"}
	reports := &cobra.Command{Use: "report", Short: "Archive and process checkup reports"}
	overview := &cobra.Command{Use: "overview", Short: "Read current body data", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return healthCLIGet(cmd, "/api/health/overview") }}
	get := &cobra.Command{Use: "get <report-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return healthCLIGet(cmd, "/api/health/reports/"+args[0])
	}}
	prepare := &cobra.Command{Use: "prepare <report-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, cancel, err := healthCLIClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		var out any
		if err := client.PostJSON(ctx, "/api/health/reports/"+args[0]+"/prepare", map[string]any{}, &out); err != nil {
			return err
		}
		return cli.PrintJSON(os.Stdout, out)
	}}
	pages := &cobra.Command{Use: "pages <report-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		from, _ := cmd.Flags().GetInt("from")
		to, _ := cmd.Flags().GetInt("to")
		return healthCLIGet(cmd, fmt.Sprintf("/api/health/reports/%s/pages?from=%d&to=%d", args[0], from, to))
	}}
	pages.Flags().Int("from", 1, "First page")
	pages.Flags().Int("to", 4, "Last page, at most four pages")
	process := &cobra.Command{Use: "process <report-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, cancel, err := healthCLIClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		agent, _ := cmd.Flags().GetString("agent-id")
		again, _ := cmd.Flags().GetBool("reprocess")
		var out any
		if err := client.PostJSON(ctx, "/api/health/reports/"+args[0]+"/process", map[string]any{"agent_id": agent, "reprocess": again}, &out); err != nil {
			return err
		}
		return cli.PrintJSON(os.Stdout, out)
	}}
	process.Flags().String("agent-id", "", "Assistant agent ID, defaults to current chat agent")
	process.Flags().Bool("reprocess", false, "Create a new extraction attempt")
	apply := &cobra.Command{Use: "apply <report-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, cancel, err := healthCLIClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		revision, _ := cmd.Flags().GetInt64("revision")
		var out any
		if err := client.PostJSON(ctx, "/api/health/reports/"+args[0]+"/apply", map[string]any{"revision": revision}, &out); err != nil {
			return err
		}
		return cli.PrintJSON(os.Stdout, out)
	}}
	apply.Flags().Int64("revision", 0, "Expected body data revision")
	apply.MarkFlagRequired("revision")
	upload := &cobra.Command{Use: "upload <local-pdf>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, cancel, err := healthCLIClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		file, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer file.Close()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", filepath.Base(args[0]))
		if err != nil {
			return err
		}
		if _, err = io.Copy(part, io.LimitReader(file, 100<<20)); err != nil {
			return err
		}
		if err = writer.Close(); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, "POST", client.BaseURL+"/api/health/reports", &body)
		if err != nil {
			return err
		}
		healthCLIHeaders(request, client)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response, err := client.HTTPClient.Do(request)
		if err != nil {
			return fmt.Errorf("报告上传失败")
		}
		defer response.Body.Close()
		if response.StatusCode >= 400 {
			return fmt.Errorf("报告上传失败，HTTP %d", response.StatusCode)
		}
		var out any
		if err = json.NewDecoder(response.Body).Decode(&out); err != nil {
			return err
		}
		return cli.PrintJSON(os.Stdout, out)
	}}
	image := &cobra.Command{Use: "image <report-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, cancel, err := healthCLIClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		page, _ := cmd.Flags().GetInt("page")
		dir, _ := cmd.Flags().GetString("output-dir")
		request, err := http.NewRequestWithContext(ctx, "GET", client.BaseURL+"/api/health/reports/"+args[0]+"/images/"+strconv.Itoa(page), nil)
		if err != nil {
			return err
		}
		healthCLIHeaders(request, client)
		response, err := client.HTTPClient.Do(request)
		if err != nil {
			return fmt.Errorf("页面下载失败")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("页面下载失败，HTTP %d", response.StatusCode)
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		path := filepath.Join(dir, fmt.Sprintf("health-page-%d.png", page))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, response.Body)
		f.Close()
		if err != nil {
			return err
		}
		return cli.PrintJSON(os.Stdout, map[string]any{"path": path, "page": page})
	}}
	image.Flags().Int("page", 1, "PDF page number")
	image.Flags().String("output-dir", "./health-pages", "Private working directory")
	submit := &cobra.Command{Use: "submit <report-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, cancel, err := healthCLIClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		path, _ := cmd.Flags().GetString("file")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var result json.RawMessage
		if err := validateHealthResultJSON(data); err != nil {
			return err
		}
		result = data
		attempt, _ := cmd.Flags().GetInt("attempt")
		epoch, _ := cmd.Flags().GetInt64("epoch")
		phase, _ := cmd.Flags().GetString("phase")
		var out map[string]any
		if err := client.PostJSON(ctx, "/api/health/reports/"+args[0]+"/submit", map[string]any{"attempt": attempt, "epoch": epoch, "result": result, "phase": phase}, &out); err != nil {
			return err
		}
		var report struct {
			IssueID *string `json:"issue_id"`
		}
		if err := client.GetJSON(ctx, "/api/health/reports/"+args[0], &report); err != nil {
			return err
		}
		if status, ok := out["issue_status"].(string); ok && report.IssueID != nil {
			if err := client.PutJSON(ctx, "/api/issues/"+*report.IssueID, map[string]any{"status": status}, nil); err != nil {
				return fmt.Errorf("数据已保存，但任务状态更新失败：%w", err)
			}
		}
		return cli.PrintJSON(os.Stdout, out)
	}}
	submit.Flags().String("file", "", "Structured JSON result file")
	submit.Flags().Int("attempt", 0, "Processing attempt")
	submit.Flags().Int64("epoch", 0, "Body data epoch")
	submit.Flags().String("phase", "complete", "extraction, analysis or complete")
	submit.MarkFlagRequired("file")
	submit.MarkFlagRequired("attempt")
	submit.MarkFlagRequired("epoch")
	reports.AddCommand(get, prepare, pages, process, apply, upload, image, submit)
	parent.AddCommand(overview, reports)
	rootCmd.AddCommand(parent)
}
func validateHealthResultJSON(data []byte) error {
	var result health.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("结果格式无效：%w；指标各字段、conclusions、uncertainty 均为字符串，page 和 context_revision 为整数，observations 和 pages 为数组", err)
	}
	return nil
}

func healthCLIClient(cmd *cobra.Command) (*cli.APIClient, context.Context, context.CancelFunc, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	client.HTTPClient.Timeout = 10 * time.Minute
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	return client, ctx, cancel, nil
}
func healthCLIGet(cmd *cobra.Command, path string) error {
	client, ctx, cancel, err := healthCLIClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var out any
	if err := client.GetJSON(ctx, path, &out); err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, out)
}
func healthCLIHeaders(request *http.Request, c *cli.APIClient) {
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("X-Workspace-ID", c.WorkspaceID)
	request.Header.Set("X-Client-Platform", "cli")
	if c.AgentID != "" {
		request.Header.Set("X-Agent-ID", c.AgentID)
	}
	if c.TaskID != "" {
		request.Header.Set("X-Task-ID", c.TaskID)
	}
}
