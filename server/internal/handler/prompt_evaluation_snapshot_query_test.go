package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParsePromptEvaluationAssetSnapshotQuery(t *testing.T) {
	tests := []struct {
		name       string
		values     url.Values
		wantType   string
		wantLimit  int32
		wantOK     bool
		wantStatus int
	}{
		{name: "defaults", values: url.Values{}, wantType: "验收归档", wantLimit: 20, wantOK: true},
		{name: "current values", values: url.Values{"snapshot_type": {"自动归档"}, "limit": {"100"}}, wantType: "自动归档", wantLimit: 100, wantOK: true},
		{name: "invalid type", values: url.Values{"snapshot_type": {"旧归档"}}, wantStatus: http.StatusBadRequest},
		{name: "invalid limit", values: url.Values{"limit": {"0"}}, wantStatus: http.StatusBadRequest},
		{name: "oversized limit", values: url.Values{"limit": {"101"}}, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			snapshotType, limit, ok := parsePromptEvaluationAssetSnapshotQuery(w, tt.values)
			if ok != tt.wantOK || snapshotType != tt.wantType || limit != tt.wantLimit {
				t.Fatalf("got type=%q limit=%d ok=%v, want type=%q limit=%d ok=%v", snapshotType, limit, ok, tt.wantType, tt.wantLimit, tt.wantOK)
			}
			if !ok && w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}
