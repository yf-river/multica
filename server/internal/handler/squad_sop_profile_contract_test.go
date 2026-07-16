package handler

import "testing"

func TestSquadSOPProfileAcceptsOnlyCanonicalStepFields(t *testing.T) {
	canonical := []byte(`{"profile_key":"current","steps":[{"key":"build","name":"Build","role_key":"developer"}]}`)
	if _, err := normalizeSquadSOPProfile(canonical); err != nil {
		t.Fatalf("canonical profile rejected: %v", err)
	}
	steps := sopProfileStepsForHandler(canonical)
	if len(steps) != 1 || steps[0].Key != "build" || steps[0].Name != "Build" || steps[0].RoleKey != "developer" {
		t.Fatalf("canonical steps = %#v", steps)
	}

	for _, retired := range []string{
		`{"steps":[{"step_key":"build"}]}`,
		`{"steps":[{"id":"build"}]}`,
		`{"steps":[{"key":"build","title":"Build"}]}`,
		`{"steps":[{"key":"build","label":"Build"}]}`,
		`{"steps":[{"key":"build","role":"developer"}]}`,
	} {
		if _, err := normalizeSquadSOPProfile([]byte(retired)); err == nil {
			t.Fatalf("retired profile accepted: %s", retired)
		}
	}
}
