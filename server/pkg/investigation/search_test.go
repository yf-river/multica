package investigation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchCursorContinuesFilesAndLines(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 205; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.txt", i)), []byte("needle\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session := Session{Roots: []Root{{ID: "p", Path: root}}}
	req := Request{Root: "p", Query: "needle", Reason: "分页找到末尾"}
	first, err := session.Call(context.Background(), "supervision_search", req)
	if err != nil || !first.ScopeTruncated || first.NextCursor == "" {
		t.Fatalf("missing continuation: %+v %v", first, err)
	}
	req.Cursor = first.NextCursor
	second, err := session.Call(context.Background(), "supervision_search", req)
	if err != nil || second.ScopeTruncated || !strings.Contains(second.Text, "204.txt:") || strings.Contains(second.Text, "199.txt:") {
		t.Fatalf("bad continuation: %+v %v", second, err)
	}
	req.Query = "different"
	if _, err = session.Call(context.Background(), "supervision_search", req); err == nil {
		t.Fatal("cursor accepted for another query")
	}
	if err = os.WriteFile(filepath.Join(root, "many.txt"), []byte(strings.Repeat("needle\n", 450)), 0600); err != nil {
		t.Fatal(err)
	}
	req = Request{Root: "p", Path: "many.txt", Query: "needle", Reason: "行级续查"}
	for page := 0; page < 3; page++ {
		out, e := session.Call(context.Background(), "supervision_search", req)
		if e != nil || !strings.Contains(out.Text, fmt.Sprintf("many.txt:%d:", page*200+1)) {
			t.Fatalf("lost lines: %+v %v", out, e)
		}
		if page < 2 && out.NextCursor == "" {
			t.Fatal("lost next page")
		}
		req.Cursor = out.NextCursor
	}
	if req.Cursor != "" {
		t.Fatal("last page still has cursor")
	}
}

func TestSearchCursorContinuesByteBudgetAndRejectsMissingSource(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat("x", 9*1024*1024)+"\nneedle"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session := Session{Roots: []Root{{ID: "p", Path: root}}}
	req := Request{Root: "p", Query: "needle", Reason: "跨扫描字节预算"}
	first, err := session.Call(context.Background(), "supervision_search", req)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("no cursor: %+v %v", first, err)
	}
	req.Cursor = first.NextCursor
	out, err := session.Call(context.Background(), "supervision_search", req)
	if err != nil || !strings.Contains(out.Text, "b.txt:") || out.ScopeTruncated {
		t.Fatalf("lost file: %+v %v", out, err)
	}
	if err = os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = session.Call(context.Background(), "supervision_search", req); err == nil {
		t.Fatal("removed cursor target treated as complete")
	}
}

func TestWrongMaterialToolExplainsCorrection(t *testing.T) {
	saved := false
	session := Session{Save: func(_ context.Context, _ Request, _ *Result) error { saved = true; return nil }}
	out, err := session.Call(context.Background(), "supervision_read", Request{Root: "materials", Path: "message/uuid", Reason: "补读"})
	if err == nil || !strings.Contains(out.Error, "supervision_material") || !saved {
		t.Fatalf("wrong tool failure unhelpful or lost: %+v %v", out, err)
	}
}
