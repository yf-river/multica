package health

import "testing"

func TestLastImportSelectsCurrentAndKeepsHistory(t *testing.T) {
	a := Observation{ID: "a", Code: "weight", Unit: "kg", Value: "80", ObservedAt: "2026-10-01", Status: "accepted"}
	b := a
	b.ID = "b"
	b.Value = "75"
	b.ObservedAt = "2025-07-26"
	c := Observation{ID: "c", Code: "height", Unit: "cm", Value: "180", Status: "accepted"}
	old := State{ObservationIDs: []string{"a", "c"}, CurrentObservationIDs: []string{"a", "c"}}
	next := Apply(old, []Observation{a, c}, []Observation{b})
	values := Selected([]Observation{a, b, c}, next.CurrentObservationIDs)
	if len(values) != 2 || values[0].ID != "b" || values[1].ID != "c" || len(next.ObservationIDs) != 3 {
		t.Fatal("覆盖必须保留未涉及指标与旧测量记录")
	}
	if old.CurrentObservationIDs[0] != "a" {
		t.Fatal("历史快照被覆盖")
	}
	legacy := State{ObservationIDs: []string{"a", "b"}}
	ResolveCurrent(&legacy, []Observation{a, b})
	if len(legacy.CurrentObservationIDs) != 1 || legacy.CurrentObservationIDs[0] != "a" {
		t.Fatal("旧快照原有采用规则丢失")
	}
}

func TestCurrentSeparatesMethodButNotResultUnit(t *testing.T) {
	a := Observation{ID: "a", Code: "rate", Unit: "bpm", Method: "静息"}
	b := a
	b.ID = "b"
	b.Unit = "次/分"
	c := b
	c.ID = "c"
	c.Method = "运动"
	state := State{ObservationIDs: []string{"a"}, CurrentObservationIDs: []string{"a"}}
	next := Apply(state, []Observation{a}, []Observation{b, c})
	if len(next.CurrentObservationIDs) != 2 {
		t.Fatal("不同方法混为同一指标，或单位改变未覆盖")
	}
}

func TestApplyPairedBloodPressureUsesOneBatchAndKeepsHistory(t *testing.T) {
	oldSystolic := Observation{ID: "old-s", Code: "systolic_pressure", Unit: "mmHg", Value: "120", ObservedAt: "2026-10-01", Status: "accepted"}
	oldDiastolic := Observation{ID: "old-d", Code: "diastolic_pressure", Unit: "mmHg", Value: "80", ObservedAt: "2026-10-01", Status: "accepted"}
	newSystolic := Observation{ID: "new-s", Code: "systolic_pressure", Unit: "mmHg", Value: "118", ObservedAt: "2026-10-10", Status: "accepted", MeasurementGroupID: "group"}
	newDiastolic := Observation{ID: "new-d", Code: "diastolic_pressure", Unit: "mmHg", Value: "76", ObservedAt: "2026-10-10", Status: "accepted", MeasurementGroupID: "group"}
	old := State{ObservationIDs: []string{"old-s", "old-d"}, CurrentObservationIDs: []string{"old-s", "old-d"}}
	next := Apply(old, []Observation{oldSystolic, oldDiastolic}, []Observation{newSystolic, newDiastolic})
	if len(next.ObservationIDs) != 4 {
		t.Fatal("成对更新必须保留原有血压历史")
	}
	if len(next.CurrentObservationIDs) != 2 || next.CurrentObservationIDs[0] != "new-s" || next.CurrentObservationIDs[1] != "new-d" {
		t.Fatal("成对更新必须同时替换当前收缩压和舒张压")
	}
}
