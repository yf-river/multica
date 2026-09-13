// 文件职责：实现 `multica life` 命令组——作为被配置的「生活陪伴（life
// companion）」agent 运行时的受控操作面：提交记忆候选（memory-candidate）、管理
// 行动提案（proposal）、记录模型自主决策的前摄检查（check）、解析受治理的证据
// 引用（evidence resolve）、提交持久认知任务的结果（job complete）。配套的 MCP
// 服务端在同名兄弟文件 cmd_life_mcp.go。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// newAPIClientContext 建 HTTP client 与带 10 分钟超时的 ctx（life 的治理端点通常
// 涉及服务端推理，给足时间）。
func newAPIClientContext(cmd *cobra.Command) (*cli.APIClient, context.Context, context.CancelFunc, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	return client, ctx, cancel, nil
}

// lifeCmd 是 `multica life` 父命令——agent 以 life companion 身份运行时的工具面。
var lifeCmd = &cobra.Command{
	Use:   "life",
	Short: "Work as the configured life companion",
}

var lifeMemoryCandidateCmd = &cobra.Command{
	Use:   "memory-candidate",
	Short: "Submit a revisable memory candidate with evidence",
	RunE:  runLifeMemoryCandidate,
}

var lifeProposalCmd = &cobra.Command{
	Use:   "proposal",
	Short: "Manage companion action proposals",
}

var lifeProposalCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an internal or user-visible experiment proposal",
	RunE:  runLifeProposalCreate,
}

var lifeProposalPresentCmd = &cobra.Command{
	Use:   "present <proposal-id>",
	Short: "Present an internal proposal for user confirmation",
	Args:  exactArgs(1),
	RunE:  runLifeProposalPresent,
}

var lifeCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Record a model-decided proactive check, including silence",
	RunE:  runLifeCheck,
}

var lifeEvidenceCmd = &cobra.Command{
	Use:   "evidence",
	Short: "Read exact governed evidence for the current life task",
}

var lifeEvidenceResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Resolve governed life evidence references",
	RunE:  runLifeEvidenceResolve,
}

var lifeJobCmd = &cobra.Command{
	Use:   "job",
	Short: "Manage the current durable life cognition job",
}

var lifeJobCompleteCmd = &cobra.Command{
	Use:   "complete",
	Short: "Complete a durable life cognition job with structured output",
	RunE:  runLifeJobComplete,
}

func init() {
	lifeCmd.AddCommand(lifeMemoryCandidateCmd, lifeProposalCmd, lifeCheckCmd, lifeEvidenceCmd, lifeJobCmd)
	lifeProposalCmd.AddCommand(lifeProposalCreateCmd, lifeProposalPresentCmd)
	lifeEvidenceCmd.AddCommand(lifeEvidenceResolveCmd)
	lifeJobCmd.AddCommand(lifeJobCompleteCmd)

	lifeMemoryCandidateCmd.Flags().String("kind", "", "Candidate kind (required)")
	lifeMemoryCandidateCmd.Flags().String("content", "", "Candidate understanding (required)")
	lifeMemoryCandidateCmd.Flags().Float64("confidence", 0, "Confidence from 0 to 1")
	lifeMemoryCandidateCmd.Flags().Float64("urgency", 0, "Urgency from 0 to 1")
	lifeMemoryCandidateCmd.Flags().String("uncertainty", "", "What remains uncertain")
	lifeMemoryCandidateCmd.Flags().String("evidence-type", "chat_message", "Evidence source type")
	lifeMemoryCandidateCmd.Flags().String("evidence-id", "", "Evidence source UUID (required)")
	lifeMemoryCandidateCmd.Flags().String("evidence-excerpt", "", "Optional evidence excerpt")

	lifeProposalCreateCmd.Flags().String("type", "experiment_start", "Proposal type")
	lifeProposalCreateCmd.Flags().String("status", "internal_draft", "internal_draft or pending_confirmation")
	lifeProposalCreateCmd.Flags().String("title", "", "Proposal title (required)")
	lifeProposalCreateCmd.Flags().String("summary", "", "Short user-facing summary")
	lifeProposalCreateCmd.Flags().String("payload-json", "", "Proposal payload as a JSON object")
	lifeProposalCreateCmd.Flags().String("payload-file", "", "Read proposal payload from a JSON file")
	lifeProposalCreateCmd.Flags().String("expires-at", "", "Optional RFC3339 confirmation deadline")

	lifeCheckCmd.Flags().String("status", "", "silent or spoke (required)")
	lifeCheckCmd.Flags().String("trigger", "manual", "schedule, commitment, risk, or manual")
	lifeCheckCmd.Flags().String("reason", "", "Model judgment explaining the decision (required)")
	lifeCheckCmd.Flags().String("context-json", "{}", "Context snapshot as a JSON object")
	lifeEvidenceResolveCmd.Flags().StringSlice("ref", nil, "Evidence reference as material:<uuid>, chronicle:<uuid>, memory:<uuid>, or observer_knowledge:<uuid> (repeatable)")

	lifeJobCompleteCmd.Flags().String("job-id", "", "Life cognition job ID (required)")
	lifeJobCompleteCmd.Flags().String("output-json", "{}", "Structured result as a JSON object")
	lifeJobCompleteCmd.Flags().String("output-file", "", "Read structured result from a JSON file")
}

// runLifeEvidenceResolve 执行 `life evidence resolve --ref <type>:<uuid>`：把一条条
// material/chronicle/memory/observer_knowledge 引用解析成确切受治理的原文证据。
func runLifeEvidenceResolve(cmd *cobra.Command, _ []string) error {
	rawReferences, _ := cmd.Flags().GetStringSlice("ref")
	if len(rawReferences) == 0 {
		return errors.New("at least one --ref is required")
	}
	references := make([]map[string]string, 0, len(rawReferences))
	for _, raw := range rawReferences {
		parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("invalid --ref %q: expected <source_type>:<uuid>", raw)
		}
		references = append(references, map[string]string{
			"source_type": strings.TrimSpace(parts[0]),
			"source_id":   strings.TrimSpace(parts[1]),
		})
	}
	result, err := resolveLifeEvidence(cmd, references)
	if err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, result)
}

// resolveLifeEvidence 调 /api/life/agent/evidence/resolve，返回证据解析结果。
func resolveLifeEvidence(cmd *cobra.Command, references []map[string]string) (map[string]any, error) {
	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/life/agent/evidence/resolve", map[string]any{"references": references}, &result); err != nil {
		return nil, fmt.Errorf("resolve life evidence: %w", err)
	}
	return result, nil
}

// runLifeJobComplete 执行 `life job complete --job-id <id>`：把结构化结果（JSON
// 对象，行内或文件）提交给持久认知任务收尾。
func runLifeJobComplete(cmd *cobra.Command, _ []string) error {
	jobID, err := requireFlag(cmd, "job-id")
	if err != nil {
		return err
	}
	outputJSON, _ := cmd.Flags().GetString("output-json")
	outputFile, _ := cmd.Flags().GetString("output-file")
	if strings.TrimSpace(outputFile) != "" && cmd.Flags().Changed("output-json") {
		return errors.New("use only one of --output-json or --output-file")
	}
	raw := []byte(outputJSON)
	if strings.TrimSpace(outputFile) != "" {
		raw, err = os.ReadFile(outputFile)
		if err != nil {
			return fmt.Errorf("read output file: %w", err)
		}
	}
	output, err := decodeJSONObject(raw, "job output")
	if err != nil {
		return err
	}
	result, err := completeLifeJob(cmd, jobID, output)
	if err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, result)
}

// completeLifeJob 调 /api/life/agent/jobs/{jobID}/complete 提交任务结果。
func completeLifeJob(cmd *cobra.Command, jobID string, output map[string]any) (map[string]any, error) {
	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/life/agent/jobs/"+jobID+"/complete", map[string]any{"output": output}, &result); err != nil {
		return nil, fmt.Errorf("complete life cognition job: %w", err)
	}
	return result, nil
}

// requireFlag 读取必填字符串 flag：trim 后为空即报 "--<name> is required"。
func requireFlag(cmd *cobra.Command, name string) (string, error) {
	value, _ := cmd.Flags().GetString(name)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("--%s is required", name)
	}
	return value, nil
}

// decodeJSONObject 把字节解析成 JSON 对象，失败或解析成非对象时报带字段名的错。
func decodeJSONObject(raw []byte, field string) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	if value == nil {
		return nil, fmt.Errorf("%s must be a JSON object", field)
	}
	return value, nil
}

// runLifeMemoryCandidate 执行 `life memory-candidate`：提交一条可修订的记忆候选
// 及其证据（kind/content/evidence-id 必填）。
func runLifeMemoryCandidate(cmd *cobra.Command, _ []string) error {
	kind, err := requireFlag(cmd, "kind")
	if err != nil {
		return err
	}
	content, err := requireFlag(cmd, "content")
	if err != nil {
		return err
	}
	evidenceID, err := requireFlag(cmd, "evidence-id")
	if err != nil {
		return err
	}
	confidence, _ := cmd.Flags().GetFloat64("confidence")
	urgency, _ := cmd.Flags().GetFloat64("urgency")
	uncertainty, _ := cmd.Flags().GetString("uncertainty")
	evidenceType, _ := cmd.Flags().GetString("evidence-type")
	excerpt, _ := cmd.Flags().GetString("evidence-excerpt")

	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var result map[string]any
	err = client.PostJSON(ctx, "/api/life/agent/memory-candidates", map[string]any{
		"kind": kind, "content": content, "confidence": confidence, "urgency": urgency,
		"uncertainty": uncertainty,
		"evidence":    []map[string]any{{"source_type": evidenceType, "source_id": evidenceID, "excerpt": excerpt}},
	}, &result)
	if err != nil {
		return fmt.Errorf("create memory candidate: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}

// runLifeProposalCreate 执行 `life proposal create`：创建实验提案，payload 为
// JSON 对象（行内 --payload-json 或文件 --payload-file，二选一）。
func runLifeProposalCreate(cmd *cobra.Command, _ []string) error {
	title, err := requireFlag(cmd, "title")
	if err != nil {
		return err
	}
	payloadJSON, _ := cmd.Flags().GetString("payload-json")
	payloadFile, _ := cmd.Flags().GetString("payload-file")
	if strings.TrimSpace(payloadJSON) != "" && strings.TrimSpace(payloadFile) != "" {
		return errors.New("use only one of --payload-json or --payload-file")
	}
	var raw []byte
	if strings.TrimSpace(payloadFile) != "" {
		raw, err = os.ReadFile(payloadFile)
		if err != nil {
			return fmt.Errorf("read payload file: %w", err)
		}
	} else if strings.TrimSpace(payloadJSON) != "" {
		raw = []byte(payloadJSON)
	} else {
		return errors.New("--payload-json or --payload-file is required")
	}
	payload, err := decodeJSONObject(raw, "proposal payload")
	if err != nil {
		return err
	}
	proposalType, _ := cmd.Flags().GetString("type")
	status, _ := cmd.Flags().GetString("status")
	summary, _ := cmd.Flags().GetString("summary")
	expiresAt, _ := cmd.Flags().GetString("expires-at")

	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var result map[string]any
	err = client.PostJSON(ctx, "/api/life/agent/proposals", map[string]any{
		"proposal_type": proposalType, "status": status, "title": title,
		"summary": summary, "payload": payload, "expires_at": expiresAt,
	}, &result)
	if err != nil {
		return fmt.Errorf("create life proposal: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}

// runLifeProposalPresent 执行 `life proposal present <id>`：把内部提案提交给用户
// 确认（POST /present）。
func runLifeProposalPresent(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/life/agent/proposals/"+args[0]+"/present", map[string]any{}, &result); err != nil {
		return fmt.Errorf("present life proposal: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}

// runLifeCheck 执行 `life check`：记录一次模型自主决定的前摄检查（含「选择沉默」
// 的情况），status/reason 必填，context 为 JSON 快照。
func runLifeCheck(cmd *cobra.Command, _ []string) error {
	status, err := requireFlag(cmd, "status")
	if err != nil {
		return err
	}
	reason, err := requireFlag(cmd, "reason")
	if err != nil {
		return err
	}
	trigger, _ := cmd.Flags().GetString("trigger")
	contextJSON, _ := cmd.Flags().GetString("context-json")
	contextSnapshot, err := decodeJSONObject([]byte(contextJSON), "context")
	if err != nil {
		return err
	}

	client, ctx, cancel, err := newAPIClientContext(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var result map[string]any
	err = client.PostJSON(ctx, "/api/life/agent/proactive-checks", map[string]any{
		"status": status, "trigger_source": trigger, "reason": reason, "context_snapshot": contextSnapshot,
	}, &result)
	if err != nil {
		return fmt.Errorf("record proactive check: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}
