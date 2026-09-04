package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

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

func wantsJSONOutput(cmd *cobra.Command) bool {
	value, _ := cmd.Flags().GetString("output")
	return value == "json"
}

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
