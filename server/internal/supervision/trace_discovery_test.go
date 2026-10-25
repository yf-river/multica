package supervision

import (
	"encoding/json"
	"testing"
	"time"
)

// 1. Supervision performed days later still queries the execution's original time range.
func TestDelayedTraceQueryRange(t *testing.T) {
	started := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	completed := started.Add(20 * time.Minute)
	start, end := traceQueryRange(started, completed)
	if !start.Equal(started.Add(-5*time.Minute)) || !end.Equal(completed.Add(5*time.Minute)) {
		t.Fatalf("unexpected range %s/%s", start, end)
	}
	if !end.Before(completed.Add(2 * time.Hour)) {
		t.Fatal("query slid to supervision time")
	}
}

// 2. Every page must be acknowledged; ignored parameters and short incomplete pages remain pending.
func TestLensCandidatePages(t *testing.T) {
	traces := make([]map[string]string, 50)
	for i := range traces {
		traces[i] = map[string]string{"traceID": time.Unix(int64(i), 0).String()}
	}
	body := func(page, size, total int, rows any) []byte {
		b, _ := json.Marshal(map[string]any{"list": map[string]any{"page_num": page, "page_size": size, "total": total, "traces": rows}})
		return b
	}
	ids, complete, valid := lensCandidatePage(body(1, 50, 51, traces), 1)
	if !valid || complete || len(ids) != 50 {
		t.Fatalf("first page %d/%v/%v", len(ids), complete, valid)
	}
	ids, complete, valid = lensCandidatePage(body(2, 50, 51, traces[:1]), 2)
	if !valid || !complete || len(ids) != 1 {
		t.Fatal("last page rejected")
	}
	for _, b := range [][]byte{body(1, 50, 51, traces), body(2, 50, 151, traces[:1]), body(1, 50, 2, traces[:1]), []byte(`{"list":{}}`)} {
		if _, _, valid = lensCandidatePage(b, 2); valid {
			t.Fatal("unproven pagination accepted")
		}
	}
}

// 3. The live filtered API can report total=2 but return one row even at page_size=50.
func TestLensInconsistentTotalRemainsPending(t *testing.T) {
	ids, complete, valid := lensCandidatePage([]byte(`{"list":{"page_num":1,"page_size":50,"total":2,"traces":[{"traceID":"available"}]}}`), 1)
	if valid || complete || len(ids) != 1 {
		t.Fatalf("inconsistent live shape lost partial evidence or claimed completeness: %v/%v/%v", ids, complete, valid)
	}
}
