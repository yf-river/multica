package main

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/supervision"
	"github.com/multica-ai/multica/server/pkg/investigation"
)

func TestOfflineSupervisionMaterialPaginationAndScope(t *testing.T) {
	body := strings.Repeat("租", 9001)
	materials := []supervision.Material{{Evidence: supervision.Evidence{Kind: "trace", ID: "owned", RunID: "run"}, Text: body, End: 9001, Length: 9001}}
	read := func(path string, offset int) investigation.Result {
		t.Helper()
		out, err := offlineSupervisionMaterial(materials, investigation.Request{Path: path, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	index := read("index", 0)
	if !strings.Contains(index.Text, "owned") || index.End != 1 || index.Truncated {
		t.Fatalf("bad index: %+v", index)
	}
	first := read("trace/owned", 0)
	last := read("trace/owned", first.End)
	if first.End != 8000 || !first.Truncated || last.End != 9001 || last.Truncated || first.SHA256 != last.SHA256 || last.RunID != "run" || first.Text+last.Text != body {
		t.Fatal("replay lost text, pagination, or identity")
	}
	for _, path := range []string{"trace/other", "../private", "/etc/passwd"} {
		if _, err := offlineSupervisionMaterial(materials, investigation.Request{Path: path}); err == nil {
			t.Fatalf("allowed %s", path)
		}
	}
	materials[0].End = 8000
	if _, err := offlineSupervisionMaterial(materials, investigation.Request{Path: "trace/owned", Offset: 8000}); err == nil {
		t.Fatal("invented missing tail")
	}
}

func TestOfflineSupervisionFindingOwnership(t *testing.T) {
	r := supervision.Result{Findings: []supervision.Finding{{ID: "known"}}, Resolutions: []supervision.Resolution{{ID: "known"}}}
	if err := validateSupervisionFindingIDs(r, []string{"known"}); err != nil {
		t.Fatal(err)
	}
	if err := validateSupervisionFindingIDs(r, nil); err == nil {
		t.Fatal("accepted invented existing finding")
	}
	r.Findings = nil
	if err := validateSupervisionFindingIDs(r, nil); err == nil {
		t.Fatal("accepted invented resolution")
	}
}
