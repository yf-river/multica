package protocol

import "testing"

func TestNormalizeGOOS(t *testing.T) {
	for input, want := range map[string]string{
		"darwin": "macos", "windows": "windows", "linux": "linux", "freebsd": "freebsd",
	} {
		if got := NormalizeGOOS(input); got != want {
			t.Errorf("NormalizeGOOS(%q) = %q, want %q", input, got, want)
		}
	}
}
