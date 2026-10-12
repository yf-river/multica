package health

import "testing"

func TestBMIUsesSelectedMeasurementsWithoutChangingReport(t *testing.T) {
	h := Observation{ID: "height", Code: "height", Value: "180", Unit: "cm", ObservedAt: "2025-01-01"}
	w := Observation{ID: "weight", Code: "weight", Value: "81", Unit: "kg", ObservedAt: "2026-01-01"}
	report := Observation{ID: "report-bmi", Code: "bmi", Value: "27.0"}
	bmi := CalculateBMI([]Observation{w, h, report})
	if bmi == nil || bmi.Value != 25 || bmi.Height.ID != h.ID || bmi.Weight.ObservedAt != w.ObservedAt || report.Value != "27.0" {
		t.Fatal("BMI 必须引用当前采用记录，保留原报告结果和来源日期")
	}
	w.Value = "90"
	if result := CalculateBMI([]Observation{w, h}); result == nil || result.Value != 27.8 {
		t.Fatal("体重更新未同步计算 BMI")
	}
}

func TestBMIUnitAndInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		height, hUnit, weight, wUnit string
		valid                        bool
	}{
		{"1.8", "m", "81000", "g", true}, {"180", "", "81", "kg", false}, {"180", "cm", "81", "lb", false},
		{"0", "cm", "81", "kg", false}, {"180", "cm", "-81", "kg", false}, {"NaN", "cm", "81", "kg", false}, {"180", "cm", "待测", "kg", false},
	} {
		result := CalculateBMI([]Observation{{Code: "height", Value: tc.height, Unit: tc.hUnit}, {Code: "weight", Value: tc.weight, Unit: tc.wUnit}})
		if (result != nil) != tc.valid {
			t.Errorf("BMI 单位或数值校验错误: %+v", tc)
		}
	}
	if CalculateBMI(nil) != nil {
		t.Fatal("缺少来源时不能计算")
	}
}

func TestApplyPlacesBatchFirstAndRestoresSnapshotOrder(t *testing.T) {
	a := Observation{ID: "a", Code: "height"}
	b := Observation{ID: "b", Code: "weight"}
	c := Observation{ID: "c", Code: "waist"}
	old := State{ObservationIDs: []string{"a", "b", "c"}, CurrentObservationIDs: []string{"a", "b", "c"}}
	next := Apply(old, []Observation{a, b, c}, []Observation{{ID: "s", Code: "systolic_pressure"}, {ID: "d", Code: "diastolic_pressure"}})
	expected := []string{"s", "d", "a", "b", "c"}
	for i, id := range expected {
		if next.CurrentObservationIDs[i] != id {
			t.Fatal("批内顺序和未修改项目顺序必须保留")
		}
	}
	if Selected([]Observation{a, b, c}, old.CurrentObservationIDs)[0].ID != "a" {
		t.Fatal("快照原顺序被改变")
	}
}
