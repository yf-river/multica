package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParsePromptEvaluationDimensionFilters(t *testing.T) {
	tests := []struct {
		name       string
		values     url.Values
		wantOK     bool
		wantStatus int
	}{
		{name: "empty", values: url.Values{}, wantOK: true},
		{name: "current values", values: url.Values{
			"asset_id":  {"11111111-1111-4111-8111-111111111111"},
			"prompt_id": {"22222222-2222-4222-8222-222222222222"},
			"status":    {"已评分"},
		}, wantOK: true},
		{name: "invalid asset", values: url.Values{"asset_id": {"bad"}}, wantStatus: http.StatusBadRequest},
		{name: "invalid prompt", values: url.Values{"prompt_id": {"bad"}}, wantStatus: http.StatusBadRequest},
		{name: "invalid status", values: url.Values{"status": {"unknown"}}, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			filters, ok := parsePromptEvaluationDimensionFilters(w, tt.values)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				if w.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
				}
				return
			}
			if tt.name == "current values" {
				if !filters.assetID.Valid || !filters.promptID.Valid || !filters.status.Valid || filters.status.String != "已评分" {
					t.Fatalf("parsed filters = %+v", filters)
				}
			}
		})
	}
}
