package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptLibraryResponsesRejectInvalidPersistedJSONShapes(t *testing.T) {
	tests := map[string]func() error{
		"item variables": func() error {
			_, err := promptLibraryItemToResponse(db.PromptLibraryItem{Variables: []byte(`{}`), Tags: []byte(`[]`)})
			return err
		},
		"item tags": func() error {
			_, err := promptLibraryItemToResponse(db.PromptLibraryItem{Variables: []byte(`[]`), Tags: []byte(`null`)})
			return err
		},
		"version variables": func() error {
			_, err := promptLibraryVersionToResponse(db.PromptLibraryVersion{Variables: []byte(`{}`), Tags: []byte(`[]`)})
			return err
		},
		"version tags": func() error {
			_, err := promptLibraryVersionToResponse(db.PromptLibraryVersion{Variables: []byte(`[]`), Tags: []byte(`{}`)})
			return err
		},
		"trial variables": func() error {
			_, err := promptLibraryTrialToResponse(db.PromptLibraryTrial{Variables: []byte(`[]`)})
			return err
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := test(); err == nil {
				t.Fatal("invalid persisted shape was accepted")
			}
		})
	}
}

func TestPromptLibraryResponsesPreserveCurrentJSONShapes(t *testing.T) {
	item, err := promptLibraryItemToResponse(db.PromptLibraryItem{
		Variables: []byte(`[{"name":"title"}]`),
		Tags:      []byte(`["review"]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Variables) != 1 || len(item.Tags) != 1 {
		t.Fatalf("item shapes were not preserved: %#v", item)
	}
	trial, err := promptLibraryTrialToResponse(db.PromptLibraryTrial{Variables: []byte(`{"title":"Login"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if trial.Variables["title"] != "Login" {
		t.Fatalf("trial variables were not preserved: %#v", trial.Variables)
	}
}
