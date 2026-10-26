// Package delivery holds client-side delivery preflight checks.
//
// The publisher rejects a candidate only after it has claimed an environment
// and spent a full build on it. Every rejection we have seen in practice is
// decidable from the checkout alone: a dirty worktree, local env files, a
// branch the delivery API refuses, an incomplete check list, or a migration
// that is not additive. Running the same questions before `submit` turns a
// five-minute build failure into a one-second error, so this package keeps
// them out of the publisher and next to the CLI that submits.
package delivery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Check is one confirmed business acceptance item from the plan.
type Check struct {
	Name  string `json:"name"`
	Stage string `json:"stage"`
}

// Evidence is one real local check result submitted with the delivery.
type Evidence struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

// Finding is one preflight result. A blocking finding means the delivery API
// would reject the submit; a non-blocking one means the publisher may still
// refuse it later.
type Finding struct {
	Code     string `json:"code"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
	Blocking bool   `json:"blocking"`
}

// Report is the outcome of a preflight run.
type Report struct {
	OK       bool      `json:"ok"`
	Commit   string    `json:"commit,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Findings []Finding `json:"findings"`
}

// Request describes the checkout and payload about to be submitted.
type Request struct {
	Repo           string
	Commit         string
	BaseCommit     string
	Branch         string
	RequiredChecks []Check
	Evidence       []Evidence
}

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	branchPattern  = regexp.MustCompile(`^(?:feature|feat|fix)/[A-Za-z0-9_./-]+$`)
	additiveSQL    = regexp.MustCompile(`(?i)^CREATE\s+(TABLE\b|(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\b)`)
	localEnvFiles  = []string{".env", ".env.local", ".env.production", ".env.production.local"}
	reservedChecks = map[string]bool{"delivery-api": true, "delivery-runtime": true, "delivery-worker": true, "delivery-publisher": true, "delivery-coordination": true, "batch-closeout-api": true}
)

// Run evaluates the checkout and the pending submit. A returned error means
// preflight could not run at all (for example git is missing); findings are
// the actionable result.
func Run(ctx context.Context, req Request) (Report, error) {
	report := Report{Findings: []Finding{}}
	add := func(f Finding) { report.Findings = append(report.Findings, f) }

	repo := strings.TrimSpace(req.Repo)
	if repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			return report, err
		}
		repo = wd
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return report, err
	}
	repo = abs

	top, err := git(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		add(Finding{Code: "not_a_checkout", Detail: "目录不是 Git checkout: " + repo, Fix: "在 Multica 仓库根目录运行，或用 --repo 指定", Blocking: true})
		return finish(report), nil
	}
	if resolved, resolveErr := filepath.EvalSymlinks(strings.TrimSpace(top)); resolveErr == nil {
		repo = resolved
	} else {
		repo = strings.TrimSpace(top)
	}
	if !isDir(filepath.Join(repo, "server", "cmd", "server")) || !isDir(filepath.Join(repo, "apps", "web")) {
		add(Finding{Code: "not_a_checkout", Detail: "构建来源不是 Multica 仓库: " + repo, Fix: "在 Multica 仓库根目录运行", Blocking: true})
		return finish(report), nil
	}

	status, err := git(ctx, repo, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return report, err
	}
	if strings.TrimSpace(status) != "" {
		add(Finding{Code: "dirty_worktree", Detail: firstLines(status, 5), Fix: "先提交或 stash 改动，交付只接受干净提交", Blocking: true})
	}
	for _, relative := range []string{"", "apps/web"} {
		for _, name := range localEnvFiles {
			candidate := filepath.Join(repo, relative, name)
			if info, statErr := os.Lstat(candidate); statErr == nil && info.Mode().IsRegular() {
				add(Finding{Code: "local_env_file", Detail: "构建目录含本地环境文件: " + candidate, Fix: "把环境配置移到固定运行配置中", Blocking: true})
			}
		}
	}

	commit := strings.TrimSpace(req.Commit)
	if commit == "" {
		commit, err = git(ctx, repo, "rev-parse", "HEAD")
		if err != nil {
			return report, err
		}
	}
	commit = strings.ToLower(strings.TrimSpace(commit))
	report.Commit = commit
	if !commitPattern.MatchString(commit) {
		add(Finding{Code: "commit_invalid", Detail: "交付需要完整的 40 位提交哈希: " + commit, Fix: "用 git rev-parse HEAD 取完整哈希", Blocking: true})
	} else if _, resolveErr := git(ctx, repo, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); resolveErr != nil {
		add(Finding{Code: "commit_unknown", Detail: "本地仓库找不到提交: " + commit, Fix: "先 fetch 并确认提交已推送", Blocking: true})
	}

	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		if current, branchErr := git(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD"); branchErr == nil {
			branch = strings.TrimSpace(current)
		}
	}
	report.Branch = branch
	if branch == "" || branch == "HEAD" || strings.Contains(branch, "..") || !branchPattern.MatchString(branch) {
		add(Finding{Code: "branch_invalid", Detail: "交付分支必须是 feature/、feat/ 或 fix/ 开头的真实分支: " + branch, Fix: "切到任务分支，或用 --source-branch 指定", Blocking: true})
	}

	validateChecks(add, req.RequiredChecks)
	validateEvidence(add, req.Evidence)
	if commitPattern.MatchString(commit) && commitPattern.MatchString(strings.TrimSpace(req.BaseCommit)) {
		validateMigrations(ctx, repo, commit, strings.TrimSpace(req.BaseCommit), add)
	}
	return finish(report), nil
}

func finish(report Report) Report {
	report.OK = true
	for _, finding := range report.Findings {
		if finding.Blocking {
			report.OK = false
			break
		}
	}
	if report.Findings == nil {
		report.Findings = []Finding{}
	}
	return report
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func firstLines(text string, limit int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > limit {
		lines = append(lines[:limit], fmt.Sprintf("…共 %d 项", len(strings.Split(strings.TrimSpace(text), "\n"))))
	}
	return strings.Join(lines, "; ")
}

func git(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	// GIT_* environment inherited from an outer git command (hooks, rebase)
	// would silently point at another repository, so drop the ones that do.
	cmd.Env = gitEnv(os.Environ())
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// gitEnv removes the variables that redirect git to a different repository.
func gitEnv(env []string) []string {
	drop := map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_PREFIX": true}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if drop[name] {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func validateChecks(add func(Finding), checks []Check) {
	if len(checks) == 0 || len(checks) > 100 {
		add(Finding{Code: "checks_missing", Detail: "缺少方案业务验收清单", Fix: "用 --checks-file 提供 [{name,stage}]，verification 与 integration 各至少一项", Blocking: true})
		return
	}
	stages := map[string]bool{}
	seen := map[string]bool{}
	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" || reservedChecks[name] || (check.Stage != "verification" && check.Stage != "integration") {
			add(Finding{Code: "checks_invalid", Detail: "业务检查须有独立名称和 verification/integration 阶段: " + name, Fix: "六项协调检查不能充当业务清单，改名或换阶段", Blocking: true})
			return
		}
		key := check.Stage + ":" + name
		if seen[key] {
			add(Finding{Code: "checks_duplicate", Detail: "业务验收项重复: " + key, Fix: "删除重复项", Blocking: true})
			return
		}
		seen[key], stages[check.Stage] = true, true
	}
	for _, stage := range []string{"verification", "integration"} {
		if !stages[stage] {
			add(Finding{Code: "checks_incomplete", Detail: "缺少 " + stage + " 阶段的业务验收项", Fix: "至少各补一项", Blocking: true})
		}
	}
}

func validateEvidence(add func(Finding), evidence []Evidence) {
	if len(evidence) == 0 {
		add(Finding{Code: "evidence_missing", Detail: "缺少本地检查证据", Fix: "用 --evidence-file 提供 [{name,status,evidence}]，只写真实执行过的检查", Blocking: true})
		return
	}
	for _, item := range evidence {
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Evidence) == "" || item.Status != "passed" {
			add(Finding{Code: "evidence_invalid", Detail: "证据需要 name、status=passed 和实际结果: " + item.Name, Fix: "补真实结果，未执行的检查不要标通过", Blocking: true})
			return
		}
	}
}

func validateMigrations(ctx context.Context, repo, commit, base string, add func(Finding)) {
	changed, err := git(ctx, repo, "diff", "--name-only", base+".."+commit, "--", "server/migrations")
	if err != nil {
		return
	}
	for _, line := range strings.Split(changed, "\n") {
		path := strings.TrimSpace(line)
		if !strings.HasSuffix(path, ".up.sql") {
			continue
		}
		sql, showErr := git(ctx, repo, "show", commit+":"+path)
		if showErr != nil {
			continue
		}
		for _, statement := range sqlStatements(sql) {
			if additiveSQL.MatchString(statement) {
				continue
			}
			add(Finding{
				Code:     "migration_needs_review",
				Detail:   "迁移 " + filepath.Base(path) + " 含非新增语句，发布器会在构建后拒绝: " + firstLine(statement),
				Fix:      "拆成新增表/并发索引，或按 docs/operations/environment-releases.md 提供兼容与恢复方案",
				Blocking: false,
			})
			break
		}
	}
}

func firstLine(text string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	return line
}

// sqlStatements strips leading comments and splits a migration the same way
// the publisher's gate does, so preflight and the build agree on the unit a
// rule applies to.
func sqlStatements(sql string) []string {
	var out []string
	for _, part := range strings.Split(stripSQLComments(sql), ";") {
		statement := strings.TrimSpace(part)
		if statement != "" {
			out = append(out, statement)
		}
	}
	return out
}

func stripSQLComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if index := strings.Index(line, "--"); index >= 0 {
			line = line[:index]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// MergeBase returns the branch point against the released baseline. Callers
// that do not know the baseline can still give the delivery API a real
// `base_commit` instead of guessing or leaving it blank.
func MergeBase(ctx context.Context, repo, commit string) (string, error) {
	var lastErr error
	for _, ref := range []string{"origin/personal-studio-upstream", "personal-studio-upstream"} {
		out, err := git(ctx, repo, "merge-base", commit, ref)
		if err == nil {
			if base := strings.TrimSpace(out); base != "" {
				return base, nil
			}
			continue
		}
		lastErr = err
	}
	if out, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", commit+"~1"); err == nil {
		return strings.TrimSpace(out), nil
	} else if lastErr == nil {
		lastErr = err
	}
	return "", lastErr
}
