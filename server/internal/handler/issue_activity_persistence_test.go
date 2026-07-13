package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueActivityBriefRejectsInvalidPersistedDetails(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("invalid persisted activity details did not panic")
		}
	}()
	issueActivityBrief(db.ActivityLog{Details: []byte(`[]`)})
}
