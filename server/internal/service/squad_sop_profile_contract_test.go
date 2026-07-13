package service

import "testing"

func TestSquadSOPProfileReadersRejectInvalidPersistedObjects(t *testing.T) {
	if _, err := normalizeSquadSOPProfile([]byte(`[]`)); err == nil {
		t.Fatal("normalizeSquadSOPProfile accepted array")
	}
	if _, err := parseSquadSOPProfileSteps([]byte(`not-json`)); err == nil {
		t.Fatal("parseSquadSOPProfileSteps accepted malformed JSON")
	}
}
