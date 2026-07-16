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

func TestSquadSOPProfileSummaryUsesCanonicalStepFields(t *testing.T) {
	profileKey, stepKey, stepName, roleKey := squadSOPProfileSummary([]byte(`{
		"profile_key":" current ",
		"steps":[{"key":" build ","name":" Build ","role_key":" developer "}]
	}`))
	if profileKey != "current" || stepKey != "build" || stepName != "Build" || roleKey != "developer" {
		t.Fatalf("unexpected summary: %q %q %q %q", profileKey, stepKey, stepName, roleKey)
	}

	_, oldStepKey, oldStepName, oldRoleKey := squadSOPProfileSummary([]byte(`{
		"steps":[{"step_key":"old","title":"Old","role":"legacy"}]
	}`))
	if oldStepKey != "" || oldStepName != "" || oldRoleKey != "" {
		t.Fatalf("retired fields remained readable: %q %q %q", oldStepKey, oldStepName, oldRoleKey)
	}
}
