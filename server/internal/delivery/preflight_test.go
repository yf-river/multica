package delivery_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/delivery"
)

// newRepo builds a throwaway Multica-shaped checkout so preflight can be
// exercised without touching the real working tree.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for preflight tests")
	}
	dir := t.TempDir()
	for _, path := range []string{"server/cmd/server", "apps/web"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run(t, dir, "git", "init", "-q", "-b", "feature/preflight")
	run(t, dir, "git", "config", "user.email", "preflight@test.local")
	run(t, dir, "git", "config", "user.name", "preflight")
	writeFile(t, dir, "README.md", "test\n")
	commit(t, dir, "init")
	return dir
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, message string) string {
	t.Helper()
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-q", "-m", message)
	return strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
}

func baseRequest(repo, commit string) delivery.Request {
	return delivery.Request{
		Repo:       repo,
		Commit:     commit,
		BaseCommit: commit,
		Branch:     "feature/preflight",
		RequiredChecks: []delivery.Check{
			{Name: "业务功能", Stage: "verification"},
			{Name: "组合验收", Stage: "integration"},
		},
		Evidence: []delivery.Evidence{{Name: "本地检查", Status: "passed", Evidence: "实际通过"}},
	}
}

func codes(report delivery.Report) map[string]bool {
	result := map[string]bool{}
	for _, finding := range report.Findings {
		result[finding.Code] = finding.Blocking
	}
	return result
}

func TestRunAcceptsCleanCommit(t *testing.T) {
	repo := newRepo(t)
	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	report, err := delivery.Run(context.Background(), baseRequest(repo, head))
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("expected clean checkout to pass, findings: %+v", report.Findings)
	}
	if report.Commit != head {
		t.Fatalf("commit = %q, want %q", report.Commit, head)
	}
}

func TestRunRejectsDirtyWorktree(t *testing.T) {
	repo := newRepo(t)
	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	writeFile(t, repo, "README.md", "changed\n")
	report, err := delivery.Run(context.Background(), baseRequest(repo, head))
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || !codes(report)["dirty_worktree"] {
		t.Fatalf("expected blocking dirty_worktree, findings: %+v", report.Findings)
	}
}

func TestRunRejectsLocalEnvFile(t *testing.T) {
	repo := newRepo(t)
	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	writeFile(t, repo, ".env.local", "SECRET=1\n")
	report, err := delivery.Run(context.Background(), baseRequest(repo, head))
	if err != nil {
		t.Fatal(err)
	}
	found := codes(report)
	if report.OK || !found["local_env_file"] || !found["dirty_worktree"] {
		t.Fatalf("expected local_env_file to block, findings: %+v", report.Findings)
	}
}

func TestRunRejectsIncompleteChecksAndEvidence(t *testing.T) {
	repo := newRepo(t)
	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	request := baseRequest(repo, head)
	request.RequiredChecks = []delivery.Check{{Name: "只有功能", Stage: "verification"}}
	request.Evidence = nil
	report, err := delivery.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	found := codes(report)
	if found["checks_incomplete"] != true || found["evidence_missing"] != true {
		t.Fatalf("expected checks and evidence to block, findings: %+v", report.Findings)
	}
}

func TestRunRejectsBranchOutsideDeliveryPattern(t *testing.T) {
	repo := newRepo(t)
	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	request := baseRequest(repo, head)
	request.Branch = "codex/scratch"
	report, err := delivery.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !codes(report)["branch_invalid"] {
		t.Fatalf("expected branch_invalid, findings: %+v", report.Findings)
	}
	for _, branch := range []string{"feature/a", "feat/a", "fix/a"} {
		request.Branch = branch
		report, err = delivery.Run(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if report.OK != true {
			t.Fatalf("branch %s should pass, findings: %+v", branch, report.Findings)
		}
	}
}

func TestRunWarnsAboutNonAdditiveMigration(t *testing.T) {
	repo := newRepo(t)
	base := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	writeFile(t, repo, "server/migrations/900_drop_column.up.sql", "-- drop\nALTER TABLE issue DROP COLUMN title;\n")
	head := commit(t, repo, "migration")
	request := baseRequest(repo, head)
	request.BaseCommit = base
	report, err := delivery.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK != true {
		t.Fatalf("migration review stays advisory, findings: %+v", report.Findings)
	}
	if blocking, ok := codes(report)["migration_needs_review"]; !ok || blocking {
		t.Fatalf("expected non-blocking migration_needs_review, findings: %+v", report.Findings)
	}
}

func TestRunAcceptsAdditiveMigration(t *testing.T) {
	repo := newRepo(t)
	base := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	writeFile(t, repo, "server/migrations/901_add_table.up.sql", "CREATE TABLE IF NOT EXISTS demo (id uuid NOT NULL);\n")
	head := commit(t, repo, "additive migration")
	request := baseRequest(repo, head)
	request.BaseCommit = base
	report, err := delivery.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := codes(report)["migration_needs_review"]; ok {
		t.Fatalf("additive migration must not warn, findings: %+v", report.Findings)
	}
}

func TestRunRejectsNonCheckout(t *testing.T) {
	report, err := delivery.Run(context.Background(), delivery.Request{Repo: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK != false || !codes(report)["not_a_checkout"] {
		t.Fatalf("expected not_a_checkout, findings: %+v", report.Findings)
	}
}
