// snapshot.go：ghsnapshot 的「取状态」半边——唯一一条 GraphQL 查询、contexts 分页，
// 以及把 GitHub 的联合类型节点归一化成扁平的逐检查快照（PRSnapshot）。快照是每次抓取
// 的最小原子写入单元，完整反映 GitHub API 当时返回的内容，不含任何增量推断。
package ghsnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// CheckContext is one normalized check for a PR head. Both check runs and commit
// statuses are flattened into this shape, so
// downstream storage and aggregation are uniform.
type CheckContext struct {
	Name string
	// Status 是归一化后的生命周期："queued"、"in_progress" 或 "completed"。
	Status string
	// Conclusion 是归一化后的结果："success"、"failure"、"neutral"、"cancelled"、
	// "skipped"、"timed_out"、"action_required"、"startup_failure"、"stale" 或 "error"；
	// 检查仍在跑时为空。
	Conclusion      string
	DetailsURL      string
	IsStatusContext bool
}

// PRSnapshot 是每次抓取写入的原子单元。它精确镜像 API 当时返回的内容——不做增量推断。
type PRSnapshot struct {
	HeadSHA          string
	Mergeable        string // MERGEABLE / CONFLICTING / UNKNOWN（原始枚举）
	MergeStateStatus string // CLEAN / DIRTY / BLOCKED / BEHIND / UNSTABLE / ...（原始枚举）
	// RollupState 是 statusCheckRollup.state（SUCCESS/FAILURE/PENDING/ERROR/EXPECTED）。
	// 仅当 HasChecks 为 false 时才为空。
	RollupState string
	// HasChecks 在 statusCheckRollup 为 null 时为 false——GitHub 报告「该 commit 还没有
	// 任何 check」。这绝不能渲染成「已通过」（验收标准 5）。
	HasChecks bool
	Contexts  []CheckContext
}

// Decided 报告快照是否已尘埃落定：没有 check 还在跑、可合并状态已知。未决断的开放 PR
// 快照会驱动有界 chase-window 重抓（refresh.go）。
func (s *PRSnapshot) Decided() bool {
	if s.Mergeable == "UNKNOWN" || s.Mergeable == "" {
		return false
	}
	if s.HasChecks {
		switch s.RollupState {
		case "PENDING", "EXPECTED", "":
			return false
		}
	}
	for _, c := range s.Contexts {
		if c.Status != "completed" {
			return false
		}
	}
	return true
}

const (
	maxSnapshotContextPages = 100
	snapshotPageSize        = 100
)

type restPullRequest struct {
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
}

type restCheckRun struct {
	ID         int64  `json:"id"`
	HeadSHA    string `json:"head_sha"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"details_url"`
}

type restStatusContext struct {
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"target_url"`
}

// FetchPRSnapshot uses only Pull requests, Checks and Commit statuses read
// permissions. Every page is pinned to one head; a final PR read detects pushes
// before the guarded atomic write in refresh.go.
func FetchPRSnapshot(ctx context.Context, c *Client, installationID int64, owner, repo string, number int32) (*PRSnapshot, error) {
	if !c.Enabled() {
		return nil, errors.New("ghsnapshot: client not configured")
	}
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	prPath := fmt.Sprintf("%s/pulls/%d", base, number)
	pr, err := fetchSnapshotPR(ctx, c, installationID, prPath)
	if err != nil {
		return nil, err
	}
	commitPath := base + "/commits/" + url.PathEscape(pr.Head.SHA)
	runs, err := fetchSnapshotPages[restCheckRun](ctx, c, installationID, commitPath+"/check-runs?filter=latest", "check_runs")
	if err != nil {
		return nil, err
	}
	statuses, err := fetchSnapshotPages[restStatusContext](ctx, c, installationID, commitPath+"/status", "statuses")
	if err != nil {
		return nil, err
	}

	snap := &PRSnapshot{HeadSHA: pr.Head.SHA}
	seenRuns := make(map[int64]struct{}, len(runs))
	for _, run := range runs {
		if run.ID <= 0 || run.Name == "" || run.HeadSHA != snap.HeadSHA {
			return nil, errors.New("ghsnapshot: invalid check run or mismatched head")
		}
		if _, seen := seenRuns[run.ID]; seen {
			return nil, errors.New("ghsnapshot: duplicate check run during pagination")
		}
		seenRuns[run.ID] = struct{}{}
		status := normalizeRunStatus(run.Status)
		conclusion := strings.ToLower(run.Conclusion)
		if status != "completed" {
			conclusion = ""
		} else if rollupConclusion(conclusion) == "PENDING" {
			// Unknown/empty conclusions must remain undecided in both the
			// refresh queue and the persisted running-check counts.
			status, conclusion = "queued", ""
		}
		snap.Contexts = append(snap.Contexts, CheckContext{Name: run.Name, Status: status, Conclusion: conclusion, DetailsURL: run.DetailsURL})
	}
	seenStatuses := make(map[string]struct{}, len(statuses))
	for _, entry := range statuses {
		if entry.Context == "" {
			return nil, errors.New("ghsnapshot: missing status context")
		}
		if _, seen := seenStatuses[entry.Context]; seen {
			return nil, errors.New("ghsnapshot: duplicate status context during pagination")
		}
		seenStatuses[entry.Context] = struct{}{}
		status, conclusion := normalizeStatusState(entry.State)
		snap.Contexts = append(snap.Contexts, CheckContext{Name: entry.Context, Status: status, Conclusion: conclusion, DetailsURL: entry.TargetURL, IsStatusContext: true})
	}

	latest, err := fetchSnapshotPR(ctx, c, installationID, prPath)
	if err != nil {
		return nil, err
	}
	if latest.Head.SHA != snap.HeadSHA {
		return nil, errors.New("ghsnapshot: pull request head changed during fetch")
	}
	snap.Mergeable = "UNKNOWN"
	if latest.Mergeable != nil {
		if *latest.Mergeable {
			snap.Mergeable = "MERGEABLE"
		} else {
			snap.Mergeable = "CONFLICTING"
		}
	}
	snap.MergeStateStatus = strings.ToUpper(latest.MergeableState)
	if latest.Mergeable == nil || snap.MergeStateStatus == "" {
		snap.MergeStateStatus = "UNKNOWN"
	}
	snap.HasChecks = len(snap.Contexts) > 0
	if snap.HasChecks {
		snap.RollupState = rollupContexts(snap.Contexts)
	}
	return snap, nil
}

func fetchSnapshotPR(ctx context.Context, c *Client, installationID int64, path string) (*restPullRequest, error) {
	data, err := c.restGet(ctx, installationID, path)
	if err != nil {
		return nil, err
	}
	var pr restPullRequest
	if json.Unmarshal(data, &pr) != nil || pr.Head.SHA == "" {
		return nil, errors.New("ghsnapshot: malformed pull request data")
	}
	return &pr, nil
}

// Counts must stay consistent across pages. Never commit a truncated collection
// or follow a pagination URL carrying the installation token to another host.
func fetchSnapshotPages[T any](ctx context.Context, c *Client, installationID int64, path, collection string) ([]T, error) {
	var items []T
	expected := -1
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	for page := 1; page <= maxSnapshotContextPages; page++ {
		data, err := c.restGet(ctx, installationID, fmt.Sprintf("%s%sper_page=%d&page=%d", path, separator, snapshotPageSize, page))
		if err != nil {
			return nil, err
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(data, &envelope) != nil {
			return nil, errors.New("ghsnapshot: malformed check page")
		}
		var count *int
		var entries []T
		if json.Unmarshal(envelope["total_count"], &count) != nil || count == nil || *count < 0 ||
			json.Unmarshal(envelope[collection], &entries) != nil || entries == nil || len(entries) > snapshotPageSize {
			return nil, errors.New("ghsnapshot: malformed check collection")
		}
		if expected == -1 {
			expected = *count
		} else if *count != expected {
			return nil, errors.New("ghsnapshot: check count changed during pagination")
		}
		items = append(items, entries...)
		if len(items) > expected {
			return nil, errors.New("ghsnapshot: check collection exceeds reported count")
		}
		if len(items) == expected {
			return items, nil
		}
		if len(entries) == 0 {
			return nil, errors.New("ghsnapshot: truncated check collection")
		}
	}
	return nil, errors.New("ghsnapshot: check-context pagination exceeds page limit")
}

func rollupContexts(contexts []CheckContext) string {
	pending := false
	for _, cc := range contexts {
		if cc.Status != "completed" {
			pending = true
			continue
		}
		switch rollupConclusion(cc.Conclusion) {
		case "FAILURE":
			return "FAILURE"
		case "PENDING":
			pending = true
		}
	}
	if pending {
		return "PENDING"
	}
	return "SUCCESS"
}

func rollupConclusion(conclusion string) string {
	switch conclusion {
	case "failure", "cancelled", "timed_out", "action_required", "startup_failure", "stale", "error":
		return "FAILURE"
	case "success", "neutral", "skipped":
		return "SUCCESS"
	default:
		return "PENDING"
	}
}

// normalizeRunStatus 把 GraphQL 的 CheckRun.status 枚举映射到我们的生命周期。只有
// COMPLETED 是终态；QUEUED/IN_PROGRESS/WAITING/PENDING/REQUESTED 都还在跑。
func normalizeRunStatus(s string) string {
	switch strings.ToUpper(s) {
	case "COMPLETED":
		return "completed"
	case "IN_PROGRESS":
		return "in_progress"
	default:
		return "queued"
	}
}

// normalizeStatusState 把旧式 StatusContext.state 映射成 (status, conclusion)。
// conclusion 为空表示仍在跑。
func normalizeStatusState(s string) (status, conclusion string) {
	switch strings.ToUpper(s) {
	case "SUCCESS":
		return "completed", "success"
	case "FAILURE":
		return "completed", "failure"
	case "ERROR":
		return "completed", "error"
	case "PENDING":
		return "in_progress", ""
	default: // EXPECTED 与任何未知状态
		return "queued", ""
	}
}
