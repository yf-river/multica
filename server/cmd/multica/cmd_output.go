package main

import (
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func printNamedMutationResult(cmd *cobra.Command, entity, action, nameKey string, result map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("%s %s: %s (%s)\n", entity, action, strVal(result, nameKey), strVal(result, "id"))
	return nil
}
