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
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "validated")
	return err
}}

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
