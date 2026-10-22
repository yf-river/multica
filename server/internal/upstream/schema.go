// Package upstream 维护上游仓库同步的批次台账与待吸收清单。
//
// 本包只负责服务端侧的状态机与校验：真正执行 git 的是 daemon 侧 Agent，
// 它通过 multica CLI 领取批次、分片回传条目、推进游标。
package upstream

import (
	"fmt"
	"strings"
	"time"
)

// 单次回传的上限。Agent 可能一次塞进上千条，服务端必须自己兜住，
// 不依赖提示词约束。
const (
	MaxCommitsPerReport = 300
	MaxItemsPerReport   = 60
	MaxCommitSHAsPerItem = 50
	DefaultLease        = 10 * time.Minute
)

// CommitRef 是一条上游提交在本次回传里的自述。
type CommitRef struct {
	SHA            string     `json:"sha"`
	PatchID        string     `json:"patch_id"`
	Absorbed       bool       `json:"absorbed"`
	AbsorbedMethod string     `json:"absorbed_method"`
	Subject        string     `json:"subject"`
	ClusterKey     string     `json:"cluster_key"`
	CommittedAt    *time.Time `json:"committed_at"`
}

// ItemInput 是一条待吸收项（一个主题簇）。
type ItemInput struct {
	ClusterKey    string     `json:"cluster_key"`
	Title         string     `json:"title"`
	Summary       string     `json:"summary"`
	Rationale     string     `json:"rationale"`
	Verdict       string     `json:"verdict"`
	Confidence    string     `json:"confidence"`
	Presence      string     `json:"presence"`
	Paths         []string   `json:"paths"`
	CommitSHAs    []string   `json:"commit_shas"`
	FirstCommitAt *time.Time `json:"first_commit_at"`
	LastCommitAt  *time.Time `json:"last_commit_at"`
}

// Report 是 Agent 一次分片回传的正文。
type Report struct {
	ScannedCommits  int         `json:"scanned_commits"`
	AbsorbedCommits int         `json:"absorbed_commits"`
	Commits         []CommitRef `json:"commits"`
	Items           []ItemInput `json:"items"`
}

// ValidationError 带字段名，好让 Agent 自己改对再提交一次。
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

var (
	validVerdict    = map[string]bool{"absorb": true, "review": true, "skip": true}
	validConfidence = map[string]bool{"high": true, "medium": true, "low": true}
	validPresence   = map[string]bool{"local": true, "partial": true, "absent": true}
	validMethod     = map[string]bool{"": true, "patch_id": true, "subject": true}
)

// ValidateReport 校验一次回传。错误按"先结构、后引用"的顺序给出，
// 一次只报第一条，避免 Agent 被一屏错误淹没。
func ValidateReport(rep Report) error {
	if len(rep.Commits) > MaxCommitsPerReport {
		return ValidationError{Field: "commits", Message: fmt.Sprintf("最多 %d 条，实际 %d 条", MaxCommitsPerReport, len(rep.Commits))}
	}
	if len(rep.Items) > MaxItemsPerReport {
		return ValidationError{Field: "items", Message: fmt.Sprintf("最多 %d 条，实际 %d 条", MaxItemsPerReport, len(rep.Items))}
	}
	if rep.ScannedCommits != len(rep.Commits) {
		return ValidationError{Field: "scanned_commits", Message: fmt.Sprintf("与 commits 条数不一致（声明 %d，实际 %d）", rep.ScannedCommits, len(rep.Commits))}
	}
	if rep.AbsorbedCommits < 0 || rep.AbsorbedCommits > rep.ScannedCommits {
		return ValidationError{Field: "absorbed_commits", Message: "必须介于 0 与 scanned_commits 之间"}
	}

	known := make(map[string]bool, len(rep.Commits))
	for i, c := range rep.Commits {
		sha := strings.TrimSpace(c.SHA)
		if len(sha) < 7 || len(sha) > 64 {
			return ValidationError{Field: fmt.Sprintf("commits[%d].sha", i), Message: "必须是 7-64 位十六进制提交号"}
		}
		if known[sha] {
			return ValidationError{Field: fmt.Sprintf("commits[%d].sha", i), Message: "本批内重复出现"}
		}
		known[sha] = true
		if !validMethod[c.AbsorbedMethod] {
			return ValidationError{Field: fmt.Sprintf("commits[%d].absorbed_method", i), Message: "只允许空、patch_id 或 subject"}
		}
	}

	assigned := make(map[string]string, len(rep.Commits))
	keys := make(map[string]bool, len(rep.Items))
	for i, it := range rep.Items {
		key := strings.TrimSpace(it.ClusterKey)
		if key == "" {
			return ValidationError{Field: fmt.Sprintf("items[%d].cluster_key", i), Message: "不能为空，建议用 PR 号或归一化主题"}
		}
		if len(key) > 200 {
			return ValidationError{Field: fmt.Sprintf("items[%d].cluster_key", i), Message: "最长 200 字符"}
		}
		if keys[key] {
			return ValidationError{Field: fmt.Sprintf("items[%d].cluster_key", i), Message: "本批内重复"}
		}
		keys[key] = true
		if !validVerdict[it.Verdict] {
			return ValidationError{Field: fmt.Sprintf("items[%d].verdict", i), Message: "只允许 absorb、review 或 skip"}
		}
		if !validConfidence[it.Confidence] {
			return ValidationError{Field: fmt.Sprintf("items[%d].confidence", i), Message: "只允许 high、medium 或 low"}
		}
		if !validPresence[it.Presence] {
			return ValidationError{Field: fmt.Sprintf("items[%d].presence", i), Message: "只允许 local、partial 或 absent"}
		}
		if len(it.CommitSHAs) > MaxCommitSHAsPerItem {
			return ValidationError{Field: fmt.Sprintf("items[%d].commit_shas", i), Message: fmt.Sprintf("最多 %d 条", MaxCommitSHAsPerItem)}
		}
		for _, sha := range it.CommitSHAs {
			sha = strings.TrimSpace(sha)
			if !known[sha] {
				return ValidationError{Field: fmt.Sprintf("items[%d].commit_shas", i), Message: fmt.Sprintf("引用了本批未声明的提交 %s", sha)}
			}
			if owner, ok := assigned[sha]; ok && owner != key {
				return ValidationError{Field: fmt.Sprintf("items[%d].commit_shas", i), Message: fmt.Sprintf("提交 %s 已归属 %s，一个提交只能进一个簇", sha, owner)}
			}
			assigned[sha] = key
		}
	}
	return nil
}
