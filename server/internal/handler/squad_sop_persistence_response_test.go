package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestSquadSOPResponsesRejectInvalidPersistedObjects(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{"run profile", func() { squadSOPRunToResponse(db.SquadSopRun{Profile: []byte(`[]`)}, nil) }},
		{"event evidence", func() { squadSOPEventToResponse(db.SquadSopStepEvent{Evidence: []byte(`[]`)}) }},
		{"squad profile", func() { internalSquadProfileKey(db.Squad{SopProfile: []byte(`[]`)}) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("invalid persisted SOP object did not panic")
				}
			}()
			test.call()
		})
	}
}
