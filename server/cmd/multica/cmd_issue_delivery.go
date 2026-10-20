package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// 交付命令与页面使用同一套版本和确认接口。
func init() {
	group := &cobra.Command{Use: "delivery", Short: "Plan confirmation, integration queue and production review"}
	for _, action := range []string{"status", "plan", "confirm-plan", "submit", "accept", "approve", "cancel"} {
		action := action
		cmd := &cobra.Command{Use: action + " <issue-id>", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAPIClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := cli.APIContext(context.Background())
			defer cancel()
			ref, err := resolveIssueRef(ctx, client, args[0])
			if err != nil {
				return err
			}
			path := "/api/issues/" + ref.ID
			var result json.RawMessage
			if action == "status" {
				err = client.GetJSON(ctx, path+"/deliveries", &result)
			} else {
				file, _ := cmd.Flags().GetString("file")
				if file == "" {
					return fmt.Errorf("--file requires a JSON request file")
				}
				raw, readErr := os.ReadFile(file)
				if readErr != nil {
					return readErr
				}
				var body map[string]any
				if err = json.Unmarshal(raw, &body); err != nil {
					return err
				}
				switch action {
				case "plan":
					err = client.PutJSON(ctx, path+"/delivery-plan", body, &result)
				case "confirm-plan":
					err = client.PostJSON(ctx, path+"/delivery-plan/confirm", body, &result)
				case "submit":
					err = client.PostJSON(ctx, path+"/deliveries", body, &result)
				default:
					id, _ := cmd.Flags().GetString("delivery")
					if id == "" {
						return fmt.Errorf("--delivery is required")
					}
					body["action"] = action
					err = client.PostJSON(ctx, path+"/deliveries/"+id+"/action", body, &result)
				}
			}
			if err != nil {
				return err
			}
			return cli.PrintJSON(os.Stdout, result)
		}}
		cmd.Flags().String("file", "", "JSON request file (preserve operation_id on retries)")
		cmd.Flags().String("delivery", "", "Delivery UUID")
		cmd.Flags().String("output", "json", "Output format")
		group.AddCommand(cmd)
	}
	issueCmd.AddCommand(group)
}
