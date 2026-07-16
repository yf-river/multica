package main

import (
	"context"
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func newAPIClientContext(cmd *cobra.Command) (*cli.APIClient, context.Context, context.CancelFunc, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	return client, ctx, cancel, nil
}

func newWorkspaceAPIClientContext(cmd *cobra.Command) (*cli.APIClient, context.Context, context.CancelFunc, string, error) {
	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return nil, nil, nil, "", err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		cancel()
		return nil, nil, nil, "", err
	}
	return client, ctx, cancel, workspaceID, nil
}

func newResolvedAPIClientContext(
	cmd *cobra.Command,
	arg string,
	kind string,
	resolve func(context.Context, *cli.APIClient, string) (resolvedID, error),
) (*cli.APIClient, context.Context, context.CancelFunc, resolvedID, error) {
	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return nil, nil, nil, resolvedID{}, err
	}
	ref, err := resolve(ctx, client, arg)
	if err != nil {
		cancel()
		return nil, nil, nil, resolvedID{}, fmt.Errorf("resolve %s: %w", kind, err)
	}
	return client, ctx, cancel, ref, nil
}

func fetchMapList(cmd *cobra.Command, path, action string) ([]map[string]any, error) {
	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return nil, err
	}
	defer cancel()

	var items []map[string]any
	if err := client.GetJSON(ctx, path, &items); err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	return items, nil
}

func wantsJSONOutput(cmd *cobra.Command) bool {
	output, _ := cmd.Flags().GetString("output")
	return output == "json"
}

func printNamedMutationResult(cmd *cobra.Command, entity, action, nameKey string, result map[string]any) error {
	if wantsJSONOutput(cmd) {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("%s %s: %s (%s)\n", entity, action, strVal(result, nameKey), strVal(result, "id"))
	return nil
}
