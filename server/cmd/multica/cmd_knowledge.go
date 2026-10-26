// 文件职责：`multica knowledge` 命令树。工作区知识库由服务端代理到
// OpenViking，CLI 只调用 Multica API；鉴权、工作区隔离和 OV 凭据都留在
// 服务端，agent 不需要直接持有 OpenViking 地址或密钥。

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var knowledgeCmd = &cobra.Command{
	Use:   "knowledge",
	Short: "Search and browse the workspace knowledge base",
	Long: "Search and browse the workspace knowledge base. The server proxies " +
		"OpenViking and scopes every request to this workspace.",
}

var knowledgeStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show knowledge base connectivity",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		var out struct {
			Enabled bool   `json:"enabled"`
			Healthy bool   `json:"healthy"`
			BaseURL string `json:"base_url"`
			Error   string `json:"error"`
		}
		if err := client.GetJSON(cmd.Context(), "/api/workspaces/"+client.WorkspaceID+"/knowledge", &out); err != nil {
			return err
		}
		if out.Healthy {
			fmt.Fprintln(cmd.OutOrStdout(), "knowledge: healthy")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "knowledge: unavailable")
		}
		if out.BaseURL != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "base_url:", out.BaseURL)
		}
		if out.Error != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "error:", out.Error)
		}
		return nil
	},
}

var knowledgeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List knowledge resources in a directory",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		uri, _ := cmd.Flags().GetString("uri")
		path := "/api/workspaces/" + client.WorkspaceID + "/knowledge/resources"
		if uri != "" {
			path += "?uri=" + url.QueryEscape(uri)
		}
		var out struct {
			URI    string          `json:"uri"`
			Result json.RawMessage `json:"result"`
		}
		if err := client.GetJSON(cmd.Context(), path, &out); err != nil {
			return err
		}
		return printKnowledgeResult(cmd, out.Result)
	},
}

var knowledgeSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Semantic search in the workspace knowledge base",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		path, _ := cmd.Flags().GetString("path")
		body := map[string]any{"query": args[0], "limit": limit}
		if strings.TrimSpace(path) != "" {
			body["path"] = path
		}
		var out struct {
			Query  string          `json:"query"`
			Result json.RawMessage `json:"result"`
		}
		if err := client.PostJSON(cmd.Context(), "/api/workspaces/"+client.WorkspaceID+"/knowledge/search", body, &out); err != nil {
			return err
		}
		return printKnowledgeResult(cmd, out.Result)
	},
}

func printKnowledgeResult(cmd *cobra.Command, raw json.RawMessage) error {
	if len(raw) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "{}")
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decode knowledge result: %w", err)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
	return nil
}

func init() {
	knowledgeListCmd.Flags().String("uri", "", "directory URI (defaults to this workspace's root)")
	knowledgeSearchCmd.Flags().Int("limit", 10, "maximum number of results")
	knowledgeSearchCmd.Flags().String("path", "", "restrict the search to this workspace URI")
	knowledgeReadCmd.Flags().Int("offset", 0, "starting line number")
	knowledgeReadCmd.Flags().Int("limit", -1, "number of lines to read (-1 reads to the end)")
	knowledgeRemoveCmd.Flags().Bool("recursive", false, "remove a directory recursively")
	knowledgeCmd.AddCommand(knowledgeStatusCmd, knowledgeListCmd, knowledgeSearchCmd, knowledgeReadCmd, knowledgeAddCmd, knowledgeRemoveCmd)
}

var knowledgeReadCmd = &cobra.Command{
	Use:   "read <uri>",
	Short: "Read a knowledge file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		offset, _ := cmd.Flags().GetInt("offset")
		limit, _ := cmd.Flags().GetInt("limit")
		if !strings.HasPrefix(args[0], "viking://") {
			return fmt.Errorf("uri must start with viking://")
		}
		path := fmt.Sprintf("/api/workspaces/%s/knowledge/read?uri=%s&offset=%d&limit=%d",
			client.WorkspaceID, url.QueryEscape(args[0]), offset, limit)
		var out struct {
			URI    string          `json:"uri"`
			Result json.RawMessage `json:"result"`
		}
		if err := client.GetJSON(cmd.Context(), path, &out); err != nil {
			return err
		}
		return printKnowledgeResult(cmd, out.Result)
	},
}

var knowledgeAddCmd = &cobra.Command{
	Use:   "add <file>",
	Short: "Upload a file into the workspace knowledge base",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
		if len(data) > 10<<20 {
			return fmt.Errorf("file exceeds 10 MiB")
		}
		result, err := client.UploadKnowledgeResource(cmd.Context(), data, args[0])
		if err != nil {
			return err
		}
		return printKnowledgeResult(cmd, result)
	},
}

var knowledgeRemoveCmd = &cobra.Command{
	Use:   "rm <uri>",
	Short: "Remove a knowledge file or directory (workspace admins)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(args[0], "viking://") {
			return fmt.Errorf("uri must start with viking://")
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		path := fmt.Sprintf("/api/workspaces/%s/knowledge/resources?uri=%s&recursive=%t",
			client.WorkspaceID, url.QueryEscape(args[0]), recursive)
		if err := client.DeleteJSON(cmd.Context(), path); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "removed:", args[0])
		return nil
	},
}
