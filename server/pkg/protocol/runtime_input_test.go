package protocol

import (
	"strings"
	"testing"
)

func TestRuntimeInputBoundary(t *testing.T) {
	for _, tc := range []struct {
		kind, content string
		valid         bool
	}{
		{"text", "/goal clear", true},
		{"command", " /goal pause\n", true},
		{"command", "/plan off", true},
		{"command", "/plan unsupported", false},
		{"command", "/goal " + strings.Repeat("界", 4000), true},
		{"command", "/goal " + strings.Repeat("界", 4001), false},
		{"text", "\x00hidden", false},
	} {
		if err := ValidateRuntimeInput(tc.kind, tc.content); (err == nil) != tc.valid {
			t.Errorf("%s input length %d: valid=%v err=%v", tc.kind, len(tc.content), tc.valid, err)
		}
	}
}
