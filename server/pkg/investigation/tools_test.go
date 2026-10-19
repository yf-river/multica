package investigation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
