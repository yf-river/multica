package handler

import (
	"strings"
	"testing"
)

func TestMikaOnboardingOpeningIsComplete(t *testing.T) {
	opening := buildMikaOnboardingOpening("Mika", "Venus")
	for _, want := range []string{"Venus", "Mika", "Multica"} {
		if !strings.Contains(opening, want) {
			t.Errorf("opening dropped %q: %s", want, opening)
		}
	}
	if strings.Contains(opening, "%!") {
		t.Fatalf("opening has a broken format verb: %s", opening)
	}
}

// Owners may rename Mika, so the opening reads the agent's current display
// name. Hardcoding "Mika" would have a renamed agent introduce itself under a
// name the member never chose.
func TestMikaOnboardingOpeningUsesTheCurrentDisplayName(t *testing.T) {
	opening := buildMikaOnboardingOpening("Ada", "Venus")
	if !strings.Contains(opening, "我是 Ada，") {
		t.Fatalf("opening does not introduce the renamed agent:\n%s", opening)
	}
	if strings.Contains(opening, "Mika") {
		t.Fatalf("opening still says Mika after a rename:\n%s", opening)
	}
}

// A blank name is not a state the product should render around: fall back to
// the default rather than emit "I'm , your Chief of Staff".
func TestMikaOnboardingOpeningFallsBackToTheDefaultName(t *testing.T) {
	opening := buildMikaOnboardingOpening("   ", "Venus")
	if !strings.Contains(opening, "我是 Mika，") {
		t.Fatalf("blank name did not fall back to the product default:\n%s", opening)
	}
}

// Workspace names are member-typed and chat renders assistant content as
// markdown, so an unescaped name reformats Mika's first sentence.
func TestMikaOnboardingOpeningEscapesMemberTypedNames(t *testing.T) {
	opening := buildMikaOnboardingOpening("Mika", "**Ops** `prod`")

	if strings.Contains(opening, "**Ops**") {
		t.Errorf("emphasis in the workspace name survived unescaped:\n%s", opening)
	}
	if strings.Contains(opening, "`prod`") {
		t.Errorf("a code span in the workspace name survived unescaped:\n%s", opening)
	}
	for _, want := range []string{`\*\*Ops\*\*`, "\\`prod\\`"} {
		if !strings.Contains(opening, want) {
			t.Errorf("expected %q in the escaped opening:\n%s", want, opening)
		}
	}
}

// The escaper must not double-escape its own output: a name containing a
// literal backslash stays one backslash to the reader.
func TestEscapeMarkdownInlineHandlesBackslashesFirst(t *testing.T) {
	if got := escapeMarkdownInline(`a\*b`); got != `a\\\*b` {
		t.Fatalf("escapeMarkdownInline(`a\\*b`) = %q, want %q", got, `a\\\*b`)
	}
}

// The opening is the member's introduction to the working model, so all four
// beats have to survive a copy edit: what Multica is, who Mika is, what happens
// next, and the handoff to the starter cards below.
func TestMikaOnboardingOpeningKeepsItsFourBeats(t *testing.T) {
	opening := buildMikaOnboardingOpening("Mika", "Venus")

	for _, beat := range []string{
		"Multica 是一个",     // what the product is
		"Chief of Staff",    // who is speaking
		"把它变成一个任务",         // what happens next
		"从下面选一个开始",          // the bridge to the cards
	} {
		if !strings.Contains(opening, beat) {
			t.Errorf("opening lost a required beat (%q):\n%s", beat, opening)
		}
	}

	// The cards below name the options; a written menu duplicates them and
	// costs the member a retype where a click would do.
	if strings.Count(opening, "\n-") > 0 {
		t.Errorf("the opening must not list options — that is the cards' job:\n%s", opening)
	}
}
