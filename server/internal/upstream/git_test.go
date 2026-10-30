package upstream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestCoversMergedBranchesAndRecognizesEquivalentPatch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	up := filepath.Join(root, "up")
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
	rootSHA := git(up, "rev-parse", "HEAD")
	write("baseline", "existing imported history")
	git(up, "add", ".")
	git(up, "commit", "-m", "imported baseline")
	base := git(up, "rev-parse", "HEAD")
	git(up, "checkout", "-b", "topic")
	write("topic", "new feature")
	git(up, "add", ".")
	git(up, "commit", "-m", "feature")
	git(up, "rev-parse", "HEAD")
	git(up, "checkout", "main")
	write("main", "another change")
	git(up, "add", ".")
	git(up, "commit", "-m", "same title")
	git(up, "merge", "--no-ff", "topic", "-m", "merge feature")
	cfg := Config{UpstreamURL: up, UpstreamBranch: "main", LocalRef: "origin/personal-studio-upstream", BootstrapBaseSHA: base, BatchSize: 2}
	// 新安装没有本地仓库和扫描起点时，应自动覆盖包括根提交在内的上游历史。
	fresh := cfg
	fresh.BootstrapBaseSHA = ""
	fresh.BatchSize = 100
	initial, err := BuildManifest(ctx, filepath.Join(root, "initial-mirror"), fresh, nil)
	if err != nil {
		t.Fatal(err)
	}
	if initial.BaseSHA != rootSHA || len(initial.Commits) != 5 || initial.Commits[0].SHA != rootSHA {
		t.Fatalf("initial scan omitted upstream history: %+v", initial)
	}
	fresh.BootstrapBaseSHA = initial.BaseSHA
	retry, err := BuildManifest(ctx, filepath.Join(root, "initial-mirror"), fresh, nil)
	if err != nil || len(retry.Commits) != 5 || retry.Commits[0].SHA != rootSHA {
		t.Fatalf("root commit lost before report on retry: %+v %v", retry, err)
	}
	first, err := BuildManifest(ctx, mirror, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Commits) != 2 || !first.HasMore {
		t.Fatalf("first batch %+v", first)
	}
	done := []string{}
	for _, c := range first.Commits {
		done = append(done, c.SHA)
	}
	cfg.BootstrapBaseSHA = first.BaseSHA
	cfg.HeadSHA = first.HeadSHA
	next, err := BuildManifest(ctx, mirror, cfg, done)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Commits) != 1 || next.HasMore {
		t.Fatalf("merge not covered exactly once: %+v", next)
	}
	done = append(done, next.Commits[0].SHA)
	empty, err := BuildManifest(ctx, mirror, cfg, done)
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
	delta, err := BuildManifest(ctx, mirror, cfg, done)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Commits) != 1 {
		t.Fatalf("same subject must remain a distinct upstream commit: %+v", delta)
	}
	git(up, "reset", "--hard", first.BaseSHA)
	if _, err = BuildManifest(ctx, mirror, cfg, done); err == nil {
		t.Fatal("rewritten upstream history must require review")
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
