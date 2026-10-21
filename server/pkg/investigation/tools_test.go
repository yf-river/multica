package investigation

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSearchHitCanBeReadAtSourceOffset(t *testing.T) {
	root := t.TempDir()
	prefix := strings.Repeat("心跳正常\n", 2000)
	line := strings.Repeat("租", 3000) + "FINAL: mapping failed"
	if err := os.WriteFile(filepath.Join(root, "trace.log"), []byte(prefix+line), 0600); err != nil {
		t.Fatal(err)
	}
	s := Session{Roots: []Root{{ID: "project", Path: root}}}
	hit, err := s.Call(context.Background(), "supervision_search", Request{Root: "project", Path: "trace.log", Query: "FINAL", Reason: "定位业务失败"})
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`source_offset=(\d+)`).FindStringSubmatch(hit.Text)
	if len(match) != 2 {
		t.Fatalf("search hit has no readable source position: %s", hit.Text)
	}
	offset, err := strconv.Atoi(match[1])
	if err != nil || offset != len([]rune(prefix))+2800 {
		t.Fatalf("offset is not the character position of the excerpt: %s", match[1])
	}
	if !strings.Contains(hit.Text, fmt.Sprintf("trace.log:2001: [source_offset=%d]", offset)) {
		t.Fatal(hit.Text)
	}
	page, err := s.Call(context.Background(), "supervision_read", Request{Root: "project", Path: "trace.log", Offset: offset, Reason: "按搜索位置核验上下文"})
	if err != nil || !strings.Contains(page.Text, "FINAL: mapping failed") {
		t.Fatalf("cannot read matched context: %+v %v", page, err)
	}
}

func TestGitQueriesPreserveIndex(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init", "-q")
	file := filepath.Join(root, "source.go")
	if err := os.WriteFile(file, []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.go")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "fixture")
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	revision, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// 模拟复制来源后内容相同、文件时间变化，diff 默认会刷新索引缓存。
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	s := Session{Roots: []Root{{ID: "project", Path: root}}}
	for _, query := range []string{"status", "diff", "history"} {
		_, err := s.Call(context.Background(), "supervision_git", Request{Root: "project", Path: "source.go", Query: query, Revision: strings.TrimSpace(string(revision)), Reason: "核对不适用的历史参数不会被忽略"})
		if err == nil || !strings.Contains(err.Error(), "revision仅用于show") {
			t.Fatalf("%s silently accepted an unused revision: %v", query, err)
		}
	}
	for _, query := range []string{"status", "diff", "history", "show"} {
		req := Request{Root: "project", Path: "source.go", Query: query, Reason: "核验来源且不改动索引"}
		if query == "show" {
			req.Revision = strings.TrimSpace(string(revision))
		}
		_, err := s.Call(context.Background(), "supervision_git", req)
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(index)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%s changed the index", query)
		}
	}
}

func TestGitDoesNotUseConfiguredExternalWorktree(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("authorized source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "fixture")
	if err := os.WriteFile(filepath.Join(outside, "source.txt"), []byte("outside-only sentinel\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("config", "core.worktree", outside)
	s := Session{Roots: []Root{{ID: "project", Path: root}}}
	result, err := s.Call(context.Background(), "supervision_git", Request{Root: "project", Path: "source.txt", Query: "diff", Reason: "核验Git仅使用授权来源"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "" {
		t.Fatalf("Git read a worktree outside the authorized root: %s", result.Text)
	}
}

func TestGitDoesNotRunRepositoryFilters(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init", "-q")
	for name, body := range map[string]string{"source.txt": "original\n", ".gitattributes": "source.txt filter=fixture\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "fixture")
	marker := filepath.Join(outside, "filter-ran")
	git("config", "filter.fixture.clean", "touch '"+strings.ReplaceAll(marker, "'", "'\\''")+"'; cat")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := Session{Roots: []Root{{ID: "project", Path: root}}}
	for _, query := range []string{"status", "diff"} {
		_, err := s.Call(context.Background(), "supervision_git", Request{Root: "project", Path: "source.txt", Query: query, Reason: "核验只读Git不执行仓库过滤程序"})
		if err == nil || !strings.Contains(err.Error(), "仓库配置了可执行过滤器") {
			t.Fatalf("%s did not report its read-only limitation: %v", query, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Git ran a configured filter: %v", err)
	}
	history, err := s.Call(context.Background(), "supervision_git", Request{Root: "project", Path: "source.txt", Query: "history", Reason: "获取可安全读取的历史版本"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.Call(context.Background(), "supervision_git", Request{Root: "project", Path: "source.txt", Query: "show", Revision: history.Revision, Reason: "核验原始历史文件，不运行过滤器"})
	if err != nil || file.Text != "original\n" {
		t.Fatalf("raw history fallback failed: %+v %v", file, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("history fallback ran a filter: %v", err)
	}
}

func TestReadPaginationAndScope(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte(strings.Repeat("租", 9001)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "private"), []byte("must not expose"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	s := Session{Roots: []Root{{ID: "current", Path: root, RunID: "run-A"}}}
	first, err := s.Call(context.Background(), "supervision_read", Request{Root: "current", Path: "source.go", Reason: "核验映射"})
	if err != nil {
		t.Fatal(err)
	}
	if first.End != 8000 || first.Length != 9001 || !first.Truncated || first.RunID != "run-A" {
		t.Fatalf("bad page: %+v", first)
	}
	last, err := s.Call(context.Background(), "supervision_read", Request{Root: "current", Path: "source.go", Offset: first.End, Reason: "补读"})
	if err != nil {
		t.Fatal(err)
	}
	if last.Truncated || last.End != 9001 || first.SHA256 != last.SHA256 {
		t.Fatal("tail or source hash mismatch")
	}
	for _, p := range []string{"../private", "/etc/passwd", "escape/private", ".env", ".ssh/id_rsa"} {
		if _, err = s.Call(context.Background(), "supervision_read", Request{Root: "current", Path: p, Reason: "测试越界"}); err == nil {
			t.Fatalf("allowed %s", p)
		}
	}
	if _, err = s.Call(context.Background(), "supervision_read", Request{Root: "other", Path: "source.go", Reason: "测试跨源"}); err == nil {
		t.Fatal("allowed another source")
	}
}

func TestSearchHiddenFilesBudgetAndEvidenceFailure(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "normal.go"), []byte("TenantID: input.UserID"), 0600)
	_ = os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600)
	s := Session{Roots: []Root{{ID: "current", Path: root}}}
	r, err := s.Call(context.Background(), "supervision_search", Request{Root: "current", Query: "UserID", Reason: "查找调用链"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Text, "normal.go:1:") {
		t.Fatal(r.Text)
	}
	r, err = s.Call(context.Background(), "supervision_search", Request{Root: "current", Reason: "列出来源"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Text, ".env") {
		t.Fatal("secret listed")
	}
	s.Calls = 40
	if _, err = s.Call(context.Background(), "supervision_read", Request{Root: "current", Path: "normal.go", Reason: "超预算"}); err == nil {
		t.Fatal("budget not enforced")
	}
	s.Calls = 0
	s.Save = func(context.Context, Request, *Result) error { return os.ErrPermission }
	if _, err = s.Call(context.Background(), "supervision_read", Request{Root: "current", Path: "normal.go", Reason: "保存证据"}); err == nil {
		t.Fatal("save failure hidden")
	}
}

func TestSearchLargeLogsAndIncompleteScope(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "trace.log"), []byte(strings.Repeat("heartbeat\n", 220000)+"FINAL tenant_id=u98 expected=t17 passed=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := Session{Roots: []Root{{ID: "project", Path: root}}}
	result, err := s.Call(context.Background(), "supervision_search", Request{Root: "project", Path: "trace.log", Query: "FINAL", Reason: "定位大文件末尾业务结果"})
	if err != nil || !strings.Contains(result.Text, "FINAL tenant_id=u98") || result.Truncated {
		t.Fatalf("large log tail not searched: result=%+v error=%v", result, err)
	}
	if err := os.WriteFile(filepath.Join(root, "oversized.log"), []byte(strings.Repeat("x", 17*1024*1024)), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = s.Call(context.Background(), "supervision_search", Request{Root: "project", Path: "oversized.log", Query: "FINAL", Reason: "超限必须披露未核验范围"})
	if err != nil || !result.Truncated {
		t.Fatalf("oversized file silently treated as no match: %+v %v", result, err)
	}
}

func TestSearchBudgetCanBeNarrowedAndLongMatchRemainsVisible(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.log", "b.log"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat("x", 9*1024*1024)+"\nFINAL\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := Session{Roots: []Root{{ID: "project", Path: root}}}
	result, err := s.Call(context.Background(), "supervision_search", Request{Root: "project", Query: "FINAL", Reason: "有界全目录搜索"})
	if err != nil || !result.Truncated || !strings.Contains(result.Text, "a.log:") || strings.Contains(result.Text, "b.log:") {
		t.Fatalf("budget not disclosed: %+v %v", result, err)
	}
	result, err = s.Call(context.Background(), "supervision_search", Request{Root: "project", Path: "b.log", Query: "FINAL", Reason: "缩小范围核验未读文件"})
	if err != nil || result.Truncated || !strings.Contains(result.Text, "b.log:") {
		t.Fatalf("narrowed search failed: %+v %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(root, "one-line.json"), []byte(strings.Repeat("租", 1000)+"NEEDLE"+strings.Repeat("户", 1000)), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = s.Call(context.Background(), "supervision_search", Request{Root: "project", Path: "one-line.json", Query: "NEEDLE", Reason: "长行片段须显示命中并标记截断"})
	if err != nil || !result.Truncated || !strings.Contains(result.Text, "NEEDLE") {
		t.Fatalf("match cropped out: %+v %v", result, err)
	}
}

func TestEvidenceRootCannotReadAnotherIssueOrRunState(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"gong8-links/report.json", "gong-8-log/output.txt", "gong9-links/report.json"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("验收记录"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session := Session{Roots: []Root{{ID: "evidence", Path: dir, ArtifactPrefix: "GONG-8"}}}
	out, err := session.Call(context.Background(), "supervision_search", Request{Root: "evidence", Path: ".", Reason: "列出本任务验收材料"})
	if err != nil || !strings.Contains(out.Text, "gong8-links/report.json") || strings.Contains(out.Text, "gong9") {
		t.Fatalf("bad scope: %s %v", out.Text, err)
	}
	if _, err = session.Call(context.Background(), "supervision_read", Request{Root: "evidence", Path: "gong8-links/report.json", Reason: "核对验收结果"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"gong9-links/report.json", "../credentials.json", ".env", "gong-80-log/output.txt"} {
		if _, err = session.Call(context.Background(), "supervision_read", Request{Root: "evidence", Path: p, Reason: "越权反例"}); err == nil {
			t.Fatalf("read outside scope: %s", p)
		}
	}
}
