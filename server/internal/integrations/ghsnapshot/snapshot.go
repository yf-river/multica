// snapshot.go：ghsnapshot 的「取状态」半边——唯一一条 GraphQL 查询、contexts 分页，
// 以及把 GitHub 的联合类型节点归一化成扁平的逐检查快照（PRSnapshot）。快照是每次抓取
// 的最小原子写入单元，完整反映 GitHub API 当时返回的内容，不含任何增量推断。
package ghsnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// prSnapshotQuery 是整个功能背后的唯一一条 GraphQL 查询。它在单次往返内返回
// （Elon 实测 cost=1）：
//   - headRefOid：快照所描述的 head（钉死防过期写入）；
//   - mergeable：MERGEABLE / CONFLICTING / UNKNOWN——只回答「有没有冲突」；
//   - mergeStateStatus：CLEAN / DIRTY / BLOCKED / BEHIND / UNSTABLE / ...——
//     "Ready to merge" 只能由 CLEAN 推导；
//   - statusCheckRollup：整体 CI 结论，外加每个 check/status context。
//
// $cursor 为 statusCheckRollup.contexts 分页；调用方循环到 hasNextPage 为 false 为止
// （验收标准 2——绝不假设 context 少于 100 个）。
const prSnapshotQuery = `query($owner:String!,$repo:String!,$number:Int!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      headRefOid
      mergeable
      mergeStateStatus
      commits(last:1){nodes{commit{
        statusCheckRollup{
          state
          contexts(first:100,after:$cursor){
            pageInfo{hasNextPage endCursor}
            nodes{
              __typename
              ... on CheckRun{name status conclusion detailsUrl}
              ... on StatusContext{context state targetUrl}
            }
          }
        }
      }}}
    }
  }
}`

// CheckContext 是 PR head 的某一条归一化后的检查。GraphQL 的 CheckRun 与 StatusContext
// 两类 context 都被压平进这个结构（见 normalizeNode），下游的存储与聚合因此是统一的。
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

// graphqlRollup 是 statusCheckRollup 的 JSON 结构：整体 state 加分页的 contexts。
type graphqlRollup struct {
	State    string `json:"state"`
	Contexts struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []json.RawMessage `json:"nodes"`
	} `json:"contexts"`
}

// graphqlPullRequest 是查询返回里 pullRequest 对象的 JSON 结构（只取我们需要的字段）。
type graphqlPullRequest struct {
	HeadRefOid       string `json:"headRefOid"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Commits          struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *graphqlRollup `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// rollup 取最新提交的 statusCheckRollup；没有提交时返回 nil。
func (pr *graphqlPullRequest) rollup() *graphqlRollup {
	if len(pr.Commits.Nodes) == 0 {
		return nil
	}
	return pr.Commits.Nodes[0].Commit.StatusCheckRollup
}

// graphqlPRData 是最外层 data 的 JSON 结构。
type graphqlPRData struct {
	Repository struct {
		PullRequest *graphqlPullRequest `json:"pullRequest"`
	} `json:"repository"`
}

// maxSnapshotContextPages 是对 statusCheckRollup.contexts 分页的安全上限。
const maxSnapshotContextPages = 100

// FetchPRSnapshot 执行 prSnapshotQuery，把 statusCheckRollup.contexts 分页拉完，返回
// 归一化快照。rollup 为 nil 时得到 HasChecks=false 且无 contexts。
func FetchPRSnapshot(ctx context.Context, c *Client, installationID int64, owner, repo string, number int32) (*PRSnapshot, error) {
	if !c.Enabled() {
		return nil, errors.New("ghsnapshot: client not configured")
	}
	snap := &PRSnapshot{}
	cursor := ""
	// 防御病态的游标死循环；100 页 = 1 万条 context，远超任何真实 PR。
	for page := 0; page < maxSnapshotContextPages; page++ {
		vars := map[string]any{"owner": owner, "repo": repo, "number": number}
		if cursor != "" {
			vars["cursor"] = cursor
		} else {
			vars["cursor"] = nil
		}
		data, err := c.graphQL(ctx, installationID, prSnapshotQuery, vars)
		if err != nil {
			return nil, err
		}
		var parsed graphqlPRData
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, errors.New("ghsnapshot: malformed pull request data")
		}
		pr := parsed.Repository.PullRequest
		if pr == nil {
			return nil, errors.New("ghsnapshot: pull request not found")
		}
		if page == 0 {
			snap.HeadSHA = pr.HeadRefOid
			snap.Mergeable = pr.Mergeable
			snap.MergeStateStatus = pr.MergeStateStatus
		} else if pr.HeadRefOid != snap.HeadSHA {
			// 每一页都会重读 PR 的最新 commit。若分页过程中一次 synchronize 事件推进了
			// head，把那些页混在一起会把「新 head 的 contexts」标成旧 head 的。
			return nil, errors.New("ghsnapshot: pull request head changed during pagination")
		}
		rollup := pr.rollup()
		if rollup == nil {
			// statusCheckRollup 为 null → 还没有 check。没有可分页的内容。
			if page > 0 {
				return nil, errors.New("ghsnapshot: check rollup changed during pagination")
			}
			return snap, nil
		}
		snap.HasChecks = true
		snap.RollupState = rollup.State
		for _, raw := range rollup.Contexts.Nodes {
			if cc, ok := normalizeNode(raw); ok {
				snap.Contexts = append(snap.Contexts, cc)
			}
		}
		if !rollup.Contexts.PageInfo.HasNextPage {
			return snap, nil
		}
		nextCursor := rollup.Contexts.PageInfo.EndCursor
		if nextCursor == "" || nextCursor == cursor {
			return nil, errors.New("ghsnapshot: invalid check-context pagination cursor")
		}
		if page == maxSnapshotContextPages-1 {
			return nil, errors.New("ghsnapshot: check-context pagination exceeds page limit")
		}
		cursor = nextCursor
	}
	return nil, errors.New("ghsnapshot: check-context pagination exceeds page limit")
}

// normalizeNode 把一个 GraphQL 联合节点（CheckRun 或 StatusContext）压平成
// CheckContext。CheckRun 的 statuses/conclusions 转小写；旧式 StatusContext 的 state
// 映射到同一生命周期，让聚合口径统一：
//
//	SUCCESS  → completed / success
//	FAILURE  → completed / failure
//	ERROR    → completed / error
//	PENDING  → in_progress / (running)
//	EXPECTED → queued / (running)
func normalizeNode(raw json.RawMessage) (CheckContext, bool) {
	var probe struct {
		Typename string `json:"__typename"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return CheckContext{}, false
	}
	switch probe.Typename {
	case "CheckRun":
		var n struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			DetailsURL string `json:"detailsUrl"`
		}
		if err := json.Unmarshal(raw, &n); err != nil {
			return CheckContext{}, false
		}
		return CheckContext{
			Name:       n.Name,
			Status:     normalizeRunStatus(n.Status),
			Conclusion: strings.ToLower(n.Conclusion),
			DetailsURL: n.DetailsURL,
		}, true
	case "StatusContext":
		var n struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"targetUrl"`
		}
		if err := json.Unmarshal(raw, &n); err != nil {
			return CheckContext{}, false
		}
		status, conclusion := normalizeStatusState(n.State)
		return CheckContext{
			Name:            n.Context,
			Status:          status,
			Conclusion:      conclusion,
			DetailsURL:      n.TargetURL,
			IsStatusContext: true,
		}, true
	default:
		return CheckContext{}, false
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
