package health

import (
	"testing"
	"time"
)

func TestNormalizePreservesOriginalAndSeparatesMethods(t *testing.T) {
	height := Observation{Name: "身高", Value: "1.75", Unit: "m", ObservedAt: "2025-07-26", Source: "manual"}
	if err := Normalize(&height); err != nil {
		t.Fatal(err)
	}
	if height.Value != "175" || height.Unit != "cm" || height.OriginalValue != "1.75" || height.OriginalUnit != "m" {
		t.Fatalf("normalization lost source: %+v", height)
	}
	old := Observation{Name: "体重", Value: "70000", Unit: "g", ObservedAt: "2025-07-26", Source: "manual"}
	if err := Normalize(&old); err != nil {
		t.Fatal(err)
	}
	recent := old
	recent.ID = "recent"
	recent.Value = "72"
	recent.ObservedAt = "2026-10-09"
	conflict := recent
	conflict.ID = "conflict"
	conflict.Value = "73"
	different := recent
	different.Method = "其他方法"
	pending := recent
	pending.Status = "pending"
	pending.ObservedAt = "2026-10-10"
	result := Current([]Observation{recent, old, conflict, different, pending})
	if len(result) != 3 {
		t.Fatalf("latest date, conflict or methods lost: %+v", result)
	}
	for _, o := range result {
		if o.ObservedAt == old.ObservedAt {
			t.Fatal("old report replaced new measure")
		}
	}
}
func TestMissingDateUnitAndQualitativeValues(t *testing.T) {
	qualitative := Observation{Name: "幽门螺杆菌", Value: "阴性", ObservedAt: "2025-07-26", Source: "text"}
	if err := Normalize(&qualitative); err != nil || qualitative.Status != "accepted" {
		t.Fatalf("qualitative result rejected: %+v %v", qualitative, err)
	}
	unknown := Observation{Name: "血糖", Value: "5.2", Source: "text"}
	if err := Normalize(&unknown); err != nil || unknown.Status != "accepted" || unknown.Uncertainty == "" {
		t.Fatalf("识别说明或自动录入丢失: %+v %v", unknown, err)
	}
	manual := unknown
	manual.Source = "manual"
	if Normalize(&manual) == nil {
		t.Fatal("missing manual date accepted")
	}
	threshold := Observation{Name: "检测限", Value: "<0.01", Unit: "ng/ml", ObservedAt: "2025-07-26", Source: "text"}
	if err := Normalize(&threshold); err != nil || threshold.Value != "<0.01" {
		t.Fatal("comparison operator lost")
	}
}
func TestAgeUsesBirthDateNotReportAge(t *testing.T) {
	if Age("", time.Now()) != nil {
		t.Fatal("unknown birthday produced age")
	}
	for _, tt := range []struct {
		today string
		age   int
	}{{"2026-07-25", 25}, {"2026-07-26", 26}} {
		today, _ := time.Parse("2006-01-02", tt.today)
		got := Age("2000-07-26", today)
		if got == nil || *got != tt.age {
			t.Fatalf("age on %s = %v", tt.today, got)
		}
	}
}
func TestEvidenceRequiresSamePageAndValue(t *testing.T) {
	page := PDFPage{Page: 19, Text: "", OCR: "胆碱酯酶 10.76 KU/L", Words: []Word{{Text: "10.76", Confidence: 96}}}
	o := Observation{OriginalValue: "10.76", Evidence: "胆碱酯酶 10.76 KU/L"}
	if !EvidenceMatches(page, o) || !OCRValueReliable(page, "10.76") {
		t.Fatal("source evidence not recognized")
	}
	o.OriginalValue = "10.77"
	if EvidenceMatches(page, o) {
		t.Fatal("invented value accepted")
	}
	o.OriginalValue = "10.76"
	o.Evidence = "其他指标 10.76 KU/L"
	if EvidenceMatches(page, o) {
		t.Fatal("unrelated evidence accepted")
	}
	page.Words[0].Confidence = 45
	if OCRValueReliable(page, "10.76") {
		t.Fatal("low confidence numeric word accepted")
	}
	page.Words[0].Confidence = 96
	if OCRValueReliable(page, "10") {
		t.Fatal("truncated numeric value accepted")
	}
}
