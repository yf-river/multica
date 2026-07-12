package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParsePromptEvaluationCaseFilters(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantOK      bool
		wantStatus  string
		wantSource  string
		wantKeyword string
		wantCode    int
	}{
		{name: "empty", wantOK: true, wantCode: http.StatusOK},
		{name: "current values", query: "status=active&source=trace&keyword=+needle+", wantOK: true, wantStatus: "active", wantSource: "trace", wantKeyword: "needle", wantCode: http.StatusOK},
		{name: "invalid status", query: "status=retired", wantCode: http.StatusBadRequest},
		{name: "invalid source", query: "source=legacy", wantCode: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("ParseQuery() error = %v", err)
			}
			w := httptest.NewRecorder()
			got, ok := parsePromptEvaluationCaseFilters(w, values)
			if ok != tt.wantOK || w.Code != tt.wantCode {
				t.Fatalf("parsePromptEvaluationCaseFilters() ok=%t code=%d, want ok=%t code=%d body=%s", ok, w.Code, tt.wantOK, tt.wantCode, w.Body.String())
			}
			if got.status.String != tt.wantStatus || got.source.String != tt.wantSource || got.keyword.String != tt.wantKeyword {
				t.Fatalf("parsePromptEvaluationCaseFilters() = %+v, want status=%q source=%q keyword=%q", got, tt.wantStatus, tt.wantSource, tt.wantKeyword)
			}
		})
	}
}
