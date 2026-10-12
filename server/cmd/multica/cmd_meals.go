package main

import (
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/meals"
	"github.com/spf13/cobra"
	"net/url"
	"os"
)

func init() {
	root := &cobra.Command{Use: "meals", Short: "Plan private weekly meals and submit suggestions"}
	get := func(cmd *cobra.Command, path string) error {
		client, ctx, cancel, err := healthCLIClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		var out json.RawMessage
		if err = client.GetJSON(ctx, path, &out); err != nil {
			return err
		}
		return cli.PrintJSON(os.Stdout, out)
	}
	root.AddCommand(&cobra.Command{Use: "overview", Short: "Read personal meal state and revisions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return get(cmd, "/api/meals") }})
	var query string
	var favorites bool
	recipes := &cobra.Command{Use: "recipes", Short: "Search the existing recipe catalog", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		section := "recipes"
		if favorites {
			section = "favorites"
		}
		return get(cmd, "/api/cooking/"+section+"?q="+url.QueryEscape(query))
	}}
	recipes.Flags().StringVar(&query, "query", "", "Recipe or ingredient search")
	recipes.Flags().BoolVar(&favorites, "favorites", false, "Only existing personal favorites")
	root.AddCommand(recipes, &cobra.Command{Use: "recipe <id>", Short: "Read recipe ingredients and instructions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return get(cmd, "/api/cooking/recipes/"+url.PathEscape(args[0]))
	}})
	var file, requestID string
	var revision int64
	submit := &cobra.Command{Use: "propose <draft-id>", Short: "Submit a structured menu draft without confirming it", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if len(raw) > 2<<20 {
			return fmt.Errorf("草稿文件过大")
		}
		var draft meals.Draft
		if err = json.Unmarshal(raw, &draft); err != nil {
			return fmt.Errorf("菜单草稿格式无效")
		}
		draft.ID = args[0]
		draft.Status = "proposed"
		if requestID == "" {
			requestID = meals.ID()
		}
		command := meals.Command{Action: "draft_save", Revision: revision, RequestID: requestID, Draft: draft}
		client, ctx, cancel, err := healthCLIClient(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		var out struct {
			Revision int64 `json:"revision"`
		}
		if err = client.PostJSON(ctx, "/api/meals/actions", command, &out); err != nil {
			return err
		}
		return cli.PrintJSON(os.Stdout, map[string]any{"draft_id": draft.ID, "revision": out.Revision, "status": "proposed"})
	}}
	submit.Flags().StringVar(&file, "file", "", "Private structured menu JSON file")
	submit.Flags().Int64Var(&revision, "revision", 0, "Meals revision read before preparing the suggestion")
	submit.Flags().StringVar(&requestID, "request-id", "", "Stable request UUID for retry")
	submit.MarkFlagRequired("file")
	submit.MarkFlagRequired("revision")
	root.AddCommand(submit)
	rootCmd.AddCommand(root)
}
