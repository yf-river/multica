package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/supervision"
	"github.com/spf13/cobra"
)

// 评测使用生产解析器和证据校验，不把另写的JSON检查当成产品契约通过。
var supervisionValidateCmd = &cobra.Command{Use: "supervision-validate <bundle>", Hidden: true, Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var bundle struct {
		Output         string                 `json:"output"`
		Materials      []supervision.Material `json:"materials"`
		Investigations []supervision.Material `json:"investigations"`
		KnownFindings  []string               `json:"known_findings"`
	}
	if err = json.Unmarshal(data, &bundle); err != nil {
		return err
	}
	result, err := supervision.ParseResult(bundle.Output)
	if err != nil {
		return err
	}
	if err = supervision.ValidateResult(result, bundle.Materials, bundle.Investigations...); err != nil {
		return err
	}
	// 线上提交还会校验问题归属；离线回放只能引用启动器保存的本任务问题。
	if err = validateSupervisionFindingIDs(result, bundle.KnownFindings); err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
}}

func validateSupervisionFindingIDs(result supervision.Result, known []string) error {
	ids := make(map[string]bool, len(known))
	for _, id := range known {
		ids[id] = true
	}
	for _, finding := range result.Findings {
		if finding.ID != "" && !ids[finding.ID] {
			return fmt.Errorf("复查问题未指向已有记录")
		}
	}
	for _, resolution := range result.Resolutions {
		if !ids[resolution.ID] {
			return fmt.Errorf("解决记录未指向已有问题")
		}
	}
	return nil
}

func init() { rootCmd.AddCommand(supervisionValidateCmd) }

var supervisionPromptCmd = &cobra.Command{Use: "supervision-prompt <bundle>", Hidden: true, Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var b struct {
		Batch         string                 `json:"batch"`
		Current       string                 `json:"current"`
		Summary       string                 `json:"summary"`
		Open          string                 `json:"open"`
		Materials     []supervision.Material `json:"materials"`
		Investigation bool                   `json:"investigation"`
	}
	if err = json.Unmarshal(data, &b); err != nil {
		return err
	}
	if b.Open == "" {
		b.Open = "[]"
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), supervision.BuildPrompt(b.Batch, b.Current, b.Summary, b.Open, b.Materials, b.Investigation))
	return err
}}

func init() { rootCmd.AddCommand(supervisionPromptCmd) }
