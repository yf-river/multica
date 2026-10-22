package upstream

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestCoversMergedBranchesAndRecognizesEquivalentPatch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	up := filepath.Join(root, "up")
	local := filepath.Join(root, "local")
	mirror := filepath.Join(root, "mirror")
	if err := os.Mkdir(up, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(repo string, args ...string) string {
		t.Helper()
		s, err := gitOutput(ctx, repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	git(up, "init", "-b", "main")
	git(up, "config", "user.name", "test")
	git(up, "config", "user.email", "test@example.invalid")
	write := func(file, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(up, file), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("base", "base")
	git(up, "add", ".")
	git(up, "commit", "-m", "base")
	cmd := exec.Command("git", "clone", up, local)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, b)
	}
	git(local, "config", "user.name", "test")
	git(local, "config", "user.email", "test@example.invalid")
	git(up, "checkout", "-b", "topic")
	write("topic", "new feature")
	git(up, "add", ".")
	git(up, "commit", "-m", "feature")
	topic := git(up, "rev-parse", "HEAD")
	git(up, "checkout", "main")
	write("main", "another change")
	git(up, "add", ".")
	git(up, "commit", "-m", "same title")
	git(up, "merge", "--no-ff", "topic", "-m", "merge feature")
	git(local, "fetch", "origin", "topic")
	git(local, "cherry-pick", "-x", topic)
	cfg := Config{UpstreamURL: up, UpstreamBranch: "main", LocalRef: "HEAD", BatchSize: 2}
	first, err := BuildManifest(ctx, local, mirror, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Commits) != 2 || !first.HasMore {
		t.Fatalf("first batch %+v", first)
	}
	done := []string{}
	foundEquivalent := false
	for _, c := range first.Commits {
		done = append(done, c.SHA)
		if c.SHA == topic && c.Absorbed && c.AbsorbedMethod == "patch_id" {
			foundEquivalent = true
		}
	}
	if !foundEquivalent {
		t.Fatal("equivalent patch not recognized")
	}
	cfg.BootstrapBaseSHA = first.BaseSHA
	cfg.HeadSHA = first.HeadSHA
	next, err := BuildManifest(ctx, local, mirror, cfg, done)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Commits) != 1 || next.HasMore {
		t.Fatalf("merge not covered exactly once: %+v", next)
	}
	done = append(done, next.Commits[0].SHA)
	empty, err := BuildManifest(ctx, local, mirror, cfg, done)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Commits) != 0 || empty.HasMore {
		t.Fatal("already processed commits repeated")
	}
	// 同标题的新内容仍然进入待处理，不能按标题误认为已吸收。
	write("main", "different content")
	git(up, "add", ".")
	git(up, "commit", "-m", "same title")
	delta, err := BuildManifest(ctx, local, mirror, cfg, done)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Commits) != 1 || delta.Commits[0].Absorbed {
		t.Fatalf("same subject is not patch equivalence: %+v", delta)
	}
	git(up, "reset", "--hard", first.BaseSHA)
	if _, err = BuildManifest(ctx, local, mirror, cfg, done); err == nil {
		t.Fatal("rewritten upstream history must require review")
	}
}

func TestNextDailyRunUsesBeijingNineAM(t *testing.T) {
	for _, tc := range []struct{ now, want string }{{"2026-10-22T00:59:59Z", "2026-10-22T01:00:00Z"}, {"2026-10-22T01:00:00Z", "2026-10-23T01:00:00Z"}, {"2026-10-22T08:00:00Z", "2026-10-23T01:00:00Z"}} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := NextDailyRun(now).UTC().Format(time.RFC3339); got != tc.want {
			t.Fatalf("%s got %s want %s", tc.now, got, tc.want)
		}
	}
}
func TestManifestRejectsDuplicateOrPartialSHA(t *testing.T) {
	a := strings.Repeat("a", 40)
	m := BatchManifest{BaseSHA: a, HeadSHA: a, Commits: []CommitRef{{SHA: a}, {SHA: a}}}
	if ValidateManifest(m, 200) == nil {
		t.Fatal("duplicate accepted")
	}
	m.Commits = []CommitRef{{SHA: "aaaaaaaa"}}
	if ValidateManifest(m, 200) == nil {
		t.Fatal("partial SHA accepted")
	}
}
