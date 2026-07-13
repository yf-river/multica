package service

import "testing"

func TestPromptEvaluationJSONHelpersRejectCorruption(t *testing.T) {
	assertPanics := func(t *testing.T, call func()) {
		t.Helper()
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("corrupt JSON did not panic")
			}
		}()
		call()
	}
	t.Run("persisted object", func(t *testing.T) {
		assertPanics(t, func() { mustDecodePersistedJSONObject([]byte(`[]`), "test") })
	})
	t.Run("optional malformed", func(t *testing.T) {
		assertPanics(t, func() { decodeOptionalPersistedJSON([]byte(`not-json`), map[string]any{}, "test") })
	})
	t.Run("marshal", func(t *testing.T) {
		assertPanics(t, func() { mustJSONBytes(make(chan struct{})) })
	})
	if value := decodeOptionalPersistedJSON(nil, "missing", "test"); value != "missing" {
		t.Fatalf("optional missing value = %#v", value)
	}
}
