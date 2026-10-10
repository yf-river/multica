package handler

import (
	"encoding/json"
	"testing"
)

type emptyHealthReportRow struct{}

func (emptyHealthReportRow) Scan(...any) error { return nil }

func TestHealthReportArchiveReceiptCollections(t *testing.T) {
	report, err := scanHealthReport(emptyHealthReportRow{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if string(receipt["effective_observations"]) != "[]" {
		t.Fatalf("归档回执必须提供空集合而非 null")
	}
}
