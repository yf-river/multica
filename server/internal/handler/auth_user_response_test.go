package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestUserResponsePreservesPersistedProfileFields(t *testing.T) {
	response := userToResponse(db.User{
		OnboardingQuestionnaire: []byte(`{}`),
	})

	body, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal user response: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode user response: %v", err)
	}

	if value, ok := payload["onboarded_at"]; !ok || value != nil {
		t.Fatalf("onboarded_at = %#v, want explicit null", value)
	}
	if value, ok := payload["starter_content_state"]; !ok || value != nil {
		t.Fatalf("starter_content_state = %#v, want explicit null", value)
	}
	questionnaire, ok := payload["onboarding_questionnaire"].(map[string]any)
	if !ok || len(questionnaire) != 0 {
		t.Fatalf("onboarding_questionnaire = %#v, want empty object", payload["onboarding_questionnaire"])
	}
}
