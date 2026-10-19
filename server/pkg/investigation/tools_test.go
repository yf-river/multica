package investigation

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	for _, query := range []string{"status", "diff", "history", "show"} {
		_, err := s.Call(context.Background(), "supervision_git", Request{Root: "project", Path: "source.go", Query: query, Revision: strings.TrimSpace(string(revision)), Reason: "核验来源且不改动索引"})
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
