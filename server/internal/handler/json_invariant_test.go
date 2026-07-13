package handler

import (
	"strings"
	"testing"
)

func TestMustJSONBytesRejectsUnsupportedValues(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("unsupported JSON value did not panic")
		}
		if message := recovered.(string); !strings.Contains(message, "marshal required JSON value") {
			t.Fatalf("panic = %q", message)
		}
	}()

	mustJSONBytes(make(chan struct{}))
}
