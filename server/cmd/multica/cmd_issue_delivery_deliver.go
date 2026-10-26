package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/delivery"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/spf13/cobra"
)

var deliveryCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// The delivery flow used to be a dozen hand-built JSON payloads: plan, confirm,
// submit, then sleep loops that re-read the whole transcript to see whether the
// candidate was ready. Each of those steps costs a full context re-read, so the
// paperwork dominated the actual change. These commands keep the same server
// semantics but collapse the caller's side into one call that either finishes
// the registration or stops at the first actionable error.
func addDeliveryFlowCommands(group *cobra.Command) {
	deliver := &cobra.Command{
		Use:   "deliver <issue-id>",
		Short: "Preflight, register the plan, delegate the user's decision, submit and optionally wait",
		Long: "Preflight the checkout, register the plan comment, record the delegated user decision, submit the delivery and optionally wait for the integration candidate. " +
			"A delegated comment is the Agent recording a decision the user already expressed in the conversation; it carries the user's own words and never replaces a decision the user has not made.",
		Args: exactArgs(1),
		RunE: runDeliveryDeliver,
	}
	deliver.Flags().String("plan-file", "", "Markdown file with the confirmed plan (required)")
	deliver.Flags().String("checks-file", "", "JSON file with [{name,stage}] business checks (required)")
	deliver.Flags().String("evidence-file", "", "JSON file with [{name,status,evidence}] local results (required)")
	deliver.Flags().String("delegate", "", "The user's own words authorizing this delivery (required)")
	deliver.Flags().Bool("reuse-plan", false, "Reuse the registered plan instead of posting a new plan comment")
	deliver.Flags().String("scope", "", "Agent's scope statement for the authorization")
	deliver.Flags().Bool("release-after-acceptance", false, "Release to production after acceptance passes")
	deliver.Flags().String("source-commit", "", "Full commit hash (defaults to HEAD)")
	deliver.Flags().String("source-branch", "", "Task branch (defaults to the current branch)")
	deliver.Flags().String("base-commit", "", "Baseline commit (defaults to the merge base with origin/personal-studio-upstream)")
	deliver.Flags().String("repo", "", "Checkout root for preflight (defaults to the working directory)")
	deliver.Flags().Bool("wait", false, "Wait until the integration candidate is ready")
	deliver.Flags().Duration("timeout", 20*time.Minute, "Wait timeout")
	deliver.Flags().String("execution", "", "Stable execution id for this session (defaults to MULTICA_TASK_ID or CODEX_THREAD_ID)")
	deliver.Flags().String("output", "json", "Output format")

	preflight := &cobra.Command{
		Use:   "preflight <issue-id>",
		Short: "Check a checkout against the delivery API and publisher gates without writing",
		Args:  exactArgs(1),
		RunE:  runDeliveryPreflight,
	}
	preflight.Flags().String("checks-file", "", "JSON file with [{name,stage}] business checks")
	preflight.Flags().String("evidence-file", "", "JSON file with [{name,status,evidence}] local results")
	preflight.Flags().String("source-commit", "", "Full commit hash (defaults to HEAD)")
	preflight.Flags().String("source-branch", "", "Task branch (defaults to the current branch)")
	preflight.Flags().String("base-commit", "", "Baseline commit")
	preflight.Flags().String("repo", "", "Checkout root (defaults to the working directory)")
	preflight.Flags().String("execution", "", "Stable execution id for this session")
	preflight.Flags().String("output", "json", "Output format")

	wait := &cobra.Command{
		Use:   "wait <issue-id>",
		Short: "Wait for the delivery phase to change instead of polling with sleeps",
		Args:  exactArgs(1),
		RunE:  runDeliveryWait,
	}
	wait.Flags().String("until", "prepared,needs_changes", "Comma separated phases that end the wait; `prepared` means the candidate exists")
	wait.Flags().Duration("timeout", 20*time.Minute, "Wait timeout")
	wait.Flags().Duration("interval", 5*time.Second, "Poll interval")
	wait.Flags().String("execution", "", "Stable execution id for this session")
	wait.Flags().String("output", "json", "Output format")

	group.AddCommand(deliver)
	group.AddCommand(preflight)
	group.AddCommand(wait)
}

// deliveryStatus mirrors the read-only delivery payload the API returns.
type deliveryStatus struct {
	Plan *struct {
		ID              string `json:"id"`
		CommentRevision int    `json:"comment_revision"`
		ConfirmedAt     string `json:"confirmed_at"`
	} `json:"plan"`
	Deliveries []deliveryRecord `json:"deliveries"`
}

type deliveryRecord struct {
	ID                 string `json:"id"`
	PlanID             string `json:"plan_id"`
	Phase              string `json:"phase"`
	SourceCommit       string `json:"source_commit"`
	SourceBranch       string `json:"source_branch"`
	Revision           int64  `json:"revision"`
	CandidateID        string `json:"candidate_id"`
	Error              string `json:"error"`
	AuthorizationState string `json:"authorization_state"`
}

func readJSONFile(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func readTextFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return string(raw), nil
}

func runDeliveryPreflight(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	if _, err = resolveIssueRef(ctx, client, args[0]); err != nil {
		return err
	}
	request, err := deliveryRequestFromFlags(cmd)
	if err != nil {
		return err
	}
	report, err := delivery.Run(ctx, request)
	if err != nil {
		return err
	}
	return printPreflight(cmd, report)
}

func printPreflight(cmd *cobra.Command, report delivery.Report) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		if err := cli.PrintJSON(os.Stdout, report); err != nil {
			return err
		}
	} else {
		writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, finding := range report.Findings {
			level := "warn"
			if finding.Blocking {
				level = "BLOCK"
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", level, finding.Code, finding.Detail, finding.Fix)
		}
		_ = writer.Flush()
	}
	if !report.OK {
		return fmt.Errorf("交付预检未通过：修正上述 blocking 项后重试")
	}
	return nil
}

func deliveryRequestFromFlags(cmd *cobra.Command) (delivery.Request, error) {
	checksPath, _ := cmd.Flags().GetString("checks-file")
	evidencePath, _ := cmd.Flags().GetString("evidence-file")
	request := delivery.Request{
		Commit:     mustFlag(cmd, "source-commit"),
		BaseCommit: mustFlag(cmd, "base-commit"),
		Branch:     mustFlag(cmd, "source-branch"),
	}
	request.Repo, _ = cmd.Flags().GetString("repo")
	if checksPath != "" {
		if err := readJSONFile(checksPath, &request.RequiredChecks); err != nil {
			return request, fmt.Errorf("read --checks-file: %w", err)
		}
	}
	if evidencePath != "" {
		if err := readJSONFile(evidencePath, &request.Evidence); err != nil {
			return request, fmt.Errorf("read --evidence-file: %w", err)
		}
	}
	return request, nil
}

func mustFlag(cmd *cobra.Command, name string) string {
	value, _ := cmd.Flags().GetString(name)
	return value
}

func runDeliveryDeliver(cmd *cobra.Command, args []string) error {
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

	planPath, _ := cmd.Flags().GetString("plan-file")
	checksPath, _ := cmd.Flags().GetString("checks-file")
	evidencePath, _ := cmd.Flags().GetString("evidence-file")
	delegate, _ := cmd.Flags().GetString("delegate")
	if planPath == "" || checksPath == "" || evidencePath == "" || strings.TrimSpace(delegate) == "" {
		return fmt.Errorf("--plan-file, --checks-file, --evidence-file and --delegate are required")
	}

	request, err := deliveryRequestFromFlags(cmd)
	if err != nil {
		return err
	}
	report, err := delivery.Run(ctx, request)
	if err != nil {
		return err
	}
	for _, finding := range report.Findings {
		if !finding.Blocking {
			fmt.Fprintf(os.Stderr, "warn: %s %s\n", finding.Code, finding.Detail)
		}
	}
	if !report.OK {
		return printPreflight(cmd, report)
	}
	commit := report.Commit
	branch := report.Branch
	base := request.BaseCommit
	if base == "" {
		base = defaultBaseCommit(cmd, commit)
	}
	if !deliveryCommitPattern.MatchString(base) {
		return fmt.Errorf("需要 --base-commit（40 位基线提交），无法从 origin/personal-studio-upstream 推断")
	}
	// Claim ownership before any write; the server fences every delivery write
	// on it, and keeping the credential local makes retries resumable.
	if err = deliveryCLIClaim(ctx, cmd, client, path, "submit"); err != nil {
		return err
	}

	reusePlan, _ := cmd.Flags().GetBool("reuse-plan")
	if reusePlan {
		if _, _, planErr := currentPlan(ctx, client, path); planErr != nil {
			return fmt.Errorf("--reuse-plan: %w", planErr)
		}
	} else {
		planBody, readErr := readTextFile(planPath)
		if readErr != nil {
			return readErr
		}
		var planComment struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		}
		if err = client.PostJSON(ctx, path+"/comments", map[string]any{"content": planBody}, &planComment); err != nil {
			return fmt.Errorf("register plan comment: %w", err)
		}
		var afterPlan json.RawMessage
		if err = client.PutJSON(ctx, path+"/delivery-plan", map[string]any{"comment_id": planComment.ID, "revision": planComment.Revision}, &afterPlan); err != nil {
			return fmt.Errorf("register plan: %w", err)
		}
	}
	planID, planRevision, err := currentPlan(ctx, client, path)
	if err != nil {
		return err
	}

	scope, _ := cmd.Flags().GetString("scope")
	release, _ := cmd.Flags().GetBool("release-after-acceptance")
	if strings.TrimSpace(scope) == "" {
		scope = "按确认方案交付本次固定提交"
		if release {
			scope += "；int 验收通过后直接发布生产"
		}
	}
	decision := strings.TrimSpace(delegate)
	content := fmt.Sprintf("【委托登记】用户原话：「%s」\n\n登记范围：%s\n登记方式：Agent 代表用户登记本次决定，平台按真实成员评论核对来源与版本。", decision, scope)
	var decisionComment struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
	}
	if err = client.PostJSON(ctx, path+"/comments", map[string]any{"content": content}, &decisionComment); err != nil {
		return fmt.Errorf("register delegated decision: %w", err)
	}
	confirmBody := map[string]any{
		"plan_id":          planID,
		"revision":         planRevision,
		"comment_id":       decisionComment.ID,
		"comment_revision": decisionComment.Revision,
		"scope":            scope,
	}
	if release {
		confirmBody["release_after_acceptance"] = true
	}
	var afterConfirm json.RawMessage
	if err = client.PostJSON(ctx, path+"/delivery-plan/confirm", confirmBody, &afterConfirm); err != nil {
		return fmt.Errorf("confirm plan: %w", err)
	}

	status, err := readStatus(ctx, client, path)
	if err != nil {
		return err
	}
	submit := map[string]any{
		"operation_id":    dbid.NewV7().String(),
		"source_commit":   commit,
		"source_branch":   branch,
		"base_commit":     base,
		"evidence":        request.Evidence,
		"required_checks": request.RequiredChecks,
	}
	if active := activeDelivery(status); active != nil && active.SourceCommit != commit {
		// The server requires an explicit replacement; the CLI owns the
		// bookkeeping so the caller does not have to read revision numbers.
		submit["replace_delivery_id"] = active.ID
		submit["replace_revision"] = active.Revision
	}
	var submitted json.RawMessage
	if err = client.PostJSON(ctx, path+"/deliveries", submit, &submitted); err != nil {
		return fmt.Errorf("submit delivery: %w", err)
	}
	afterSubmit, err := readStatus(ctx, client, path)
	if err != nil {
		return err
	}
	if submitted, err = json.Marshal(compactDelivery(afterSubmit)); err != nil {
		return err
	}

	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		if err = cli.PrintJSON(os.Stdout, submitted); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(os.Stdout, "delivery registered for %s at %s\n", commit[:12], branch)
	}
	if wait, _ := cmd.Flags().GetBool("wait"); wait {
		timeout, _ := cmd.Flags().GetDuration("timeout")
		return waitForPhase(ctx, client, path, []string{"prepared", "needs_changes"}, timeout, 5*time.Second)
	}
	return nil
}

func runDeliveryWait(cmd *cobra.Command, args []string) error {
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
	untilFlag, _ := cmd.Flags().GetString("until")
	until := []string{}
	for _, part := range strings.Split(untilFlag, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			until = append(until, trimmed)
		}
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	interval, _ := cmd.Flags().GetDuration("interval")
	return waitForPhase(ctx, client, "/api/issues/"+ref.ID, until, timeout, interval)
}

func waitForPhase(ctx context.Context, client *cli.APIClient, path string, until []string, timeout, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	targets := map[string]bool{}
	for _, phase := range until {
		targets[phase] = true
	}
	last := ""
	for {
		status, err := readStatus(ctx, client, path)
		if err != nil {
			return err
		}
		current := activeDelivery(status)
		phase := "none"
		if current != nil {
			phase = current.Phase
		}
		marker := phase + "|" + valueOr(current, func(r *deliveryRecord) string { return r.CandidateID }) + "|" + valueOr(current, func(r *deliveryRecord) string { return r.Error })
		if marker != last {
			fmt.Fprintf(os.Stderr, "phase=%s candidate=%s error=%s\n", phase, valueOr(current, func(r *deliveryRecord) string { return r.CandidateID }), valueOr(current, func(r *deliveryRecord) string { return r.Error }))
			last = marker
		}
		if targets[phase] || (targets["prepared"] && current != nil && current.CandidateID != "") {
			if printErr := cli.PrintJSON(os.Stdout, compactDelivery(status)); printErr != nil {
				return printErr
			}
			if current != nil && current.Error != "" {
				return fmt.Errorf("交付失败：%s", current.Error)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待超时：当前阶段 %s", phase)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func valueOr(record *deliveryRecord, pick func(*deliveryRecord) string) string {
	if record == nil {
		return ""
	}
	return pick(record)
}

func readStatus(ctx context.Context, client *cli.APIClient, path string) (deliveryStatus, error) {
	var status deliveryStatus
	if err := client.GetJSON(ctx, path+"/deliveries", &status); err != nil {
		return status, err
	}
	return status, nil
}

func currentPlan(ctx context.Context, client *cli.APIClient, path string) (string, int, error) {
	status, err := readStatus(ctx, client, path)
	if err != nil {
		return "", 0, err
	}
	if status.Plan == nil || status.Plan.ID == "" {
		return "", 0, fmt.Errorf("方案登记后仍未读到当前方案")
	}
	return status.Plan.ID, status.Plan.CommentRevision, nil
}

func activeDelivery(status deliveryStatus) *deliveryRecord {
	for index := range status.Deliveries {
		record := &status.Deliveries[index]
		switch record.Phase {
		case "published", "cancelled":
			continue
		}
		return record
	}
	return nil
}

func compactDelivery(status deliveryStatus) map[string]any {
	record := activeDelivery(status)
	result := map[string]any{"phase": "none"}
	if record == nil {
		return result
	}
	result["phase"] = record.Phase
	result["delivery_id"] = record.ID
	result["candidate_id"] = record.CandidateID
	result["source_commit"] = record.SourceCommit
	result["source_branch"] = record.SourceBranch
	result["authorization_state"] = record.AuthorizationState
	if record.Error != "" {
		result["error"] = record.Error
	}
	return result
}

func defaultBaseCommit(cmd *cobra.Command, commit string) string {
	repo, _ := cmd.Flags().GetString("repo")
	if strings.TrimSpace(repo) == "" {
		if wd, err := os.Getwd(); err == nil {
			repo = wd
		}
	}
	if strings.TrimSpace(repo) == "" {
		return ""
	}
	if base, err := delivery.MergeBase(context.Background(), repo, commit); err == nil {
		return base
	}
	return ""
}
