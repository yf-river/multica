package main

import (
	"context"
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func fetchMapList(cmd *cobra.Command, path, action string) ([]map[string]any, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var items []map[string]any
	if err := client.GetJSON(ctx, path, &items); err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	return items, nil
}

func printNamedMutationResult(cmd *cobra.Command, entity, action, nameKey string, result map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("%s %s: %s (%s)\n", entity, action, strVal(result, nameKey), strVal(result, "id"))
	return nil
}
