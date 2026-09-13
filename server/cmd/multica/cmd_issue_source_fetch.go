// 文件职责：定义 `issue source-fetch` 命令（runIssueSourceFetch，命令对象在
// cmd_issue.go 中声明）以及两个被该命令用到的通用小工具：
// newResolvedAPIClientContext（建 client+ctx 并顺带解析一个 ID）与
// wantsJSONOutput（判断 --output 是否为 json）。source-fetch 用于在任务/issue 与
// 外部系统（tapd / gongfeng）之间同步「外部来源」元数据——agent 从外部平台把
// 需求抓到本地 issue 后，调用它记录抓取结果。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// newResolvedAPIClientContext 是「建 HTTP client + 带超时 ctx + 用 resolver 把用户
// 输入解析成规范 ID」的一次性封装，避免每个 RunE 里重复这三行样板。任何一步失败
// 都会先 cancel 掉 ctx 再返回错误。value 是要解析的原始输入（id 或 key），resolver
// 是具体实体（issue/project/...）对应的解析函数。
func newResolvedAPIClientContext(
	cmd *cobra.Command,
	value string,
	_ string,
	resolver func(context.Context, *cli.APIClient, string) (resolvedID, error),
) (*cli.APIClient, context.Context, context.CancelFunc, resolvedID, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, resolvedID{}, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	resolved, err := resolver(ctx, client, value)
	if err != nil {
		cancel()
		return nil, nil, nil, resolvedID{}, err
	}
	return client, ctx, cancel, resolved, nil
}

// wantsJSONOutput 判断当前命令是否要求 JSON 输出（--output json）。
func wantsJSONOutput(cmd *cobra.Command) bool {
	value, _ := cmd.Flags().GetString("output")
	return value == "json"
}

// runIssueSourceFetch 执行 `issue source-fetch <issue-id>`：把一次外部来源抓取
// 的结果记录到 issue 上（POST /api/issues/{id}/source-fetch）。--status 记录抓取
// 成败（fetched/fetch_failed），或改用 --auto-fetch 让 server 用已配置的账号凭据
// 代为抓取。请求带随机幂等键，重试不会产生重复记录；返回的 metadata 以表格展示。
func runIssueSourceFetch(cmd *cobra.Command, args []string) error {
	status, _ := cmd.Flags().GetString("status")
	autoFetch, _ := cmd.Flags().GetBool("auto-fetch")
	if status == "" && !autoFetch {
		return fmt.Errorf("--status is required")
	}
	provider, _ := cmd.Flags().GetString("provider")
	fetchProvider, _ := cmd.Flags().GetString("fetch-provider")
	url, _ := cmd.Flags().GetString("url")
	sourceWorkspaceID, _ := cmd.Flags().GetString("source-workspace-id")
	resourceType, _ := cmd.Flags().GetString("resource-type")
	resourceID, _ := cmd.Flags().GetString("resource-id")
	title, _ := cmd.Flags().GetString("title")
	summary, _ := cmd.Flags().GetString("summary")
	bodyExcerpt, _ := cmd.Flags().GetString("body-excerpt")
	version, _ := cmd.Flags().GetString("version")
	fetchErr, _ := cmd.Flags().GetString("error")
	durationMs, _ := cmd.Flags().GetInt64("duration-ms")

	client, ctx, cancel, issueRef, err := newResolvedAPIClientContext(cmd, args[0], "issue", resolveIssueRef)
	if err != nil {
		return err
	}
	defer cancel()

	body := map[string]any{
		"provider":       provider,
		"fetch_provider": fetchProvider,
		"status":         status,
		"url":            url,
		"workspace_id":   sourceWorkspaceID,
		"resource_type":  resourceType,
		"resource_id":    resourceID,
		"title":          title,
		"summary":        summary,
		"body_excerpt":   bodyExcerpt,
		"version":        version,
		"error":          fetchErr,
		"duration_ms":    durationMs,
		"auto_fetch":     autoFetch,
	}
	var result map[string]any
	if err := client.PostJSONWithIdempotencyKey(ctx, "/api/issues/"+issueRef.ID+"/source-fetch", body, uuid.NewString(), &result); err != nil {
		return fmt.Errorf("record source fetch: %w", err)
	}

	if wantsJSONOutput(cmd) {
		return cli.PrintJSON(os.Stdout, result)
	}
	metadata, _ := result["metadata"].(map[string]any)
	printMetadataTable(metadata)
	return nil
}
