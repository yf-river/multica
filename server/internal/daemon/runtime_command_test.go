package daemon

import "testing"

func TestRuntimeCommandOnly(t *testing.T) {
	for _, test := range []struct {
		command, input string
		want           bool
	}{
		{"/plan", "/plan", true},
		{"/plan", "analyze the repository", false},
		{"/plan analyze", "/plan analyze", false},
		{"/goal pause", "/goal pause", true},
		{"/goal implement", "/goal implement", false},
		{"/goal resume", "/goal resume", false},
	} {
		if got := runtimeCommandOnly(test.command, test.input); got != test.want {
			t.Errorf("%q %q: got %v", test.command, test.input, got)
		}
	}
}
