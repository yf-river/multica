package upstream

import "testing"

func TestEffectiveInstructionsFollowsDefaultUntilOverridden(t *testing.T) {
	defaults := "内置提示词\n第二行"
	if got := EffectiveInstructions("", defaults); got != defaults {
		t.Fatalf("empty override should follow default, got %q", got)
	}
	if got := EffectiveInstructions("自定义", defaults); got != "自定义" {
		t.Fatalf("override should win, got %q", got)
	}
}

func TestNormalizeInstructionsDropsTextEqualToDefault(t *testing.T) {
	defaults := "内置提示词\n第二行"
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"内置提示词\n第二行  ", ""},
		{"改过的提示词", "改过的提示词"},
	} {
		if got := normalizeInstructions(tc.in, defaults); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestDailyCronUsesGivenHour(t *testing.T) {
	if got := DailyCron(9); got != "0 9 * * *" {
		t.Fatalf("got %q", got)
	}
	if got := DailyCron(0); got != "0 0 * * *" {
		t.Fatalf("got %q", got)
	}
}
