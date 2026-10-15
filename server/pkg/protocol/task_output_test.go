package protocol

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTaskOutputPagesPreserveLongUnicodeEvidence(t *testing.T) {
	output := strings.Repeat("中文证据\n", 25000) + "final-rpc-registration-evidence"
	preview, total, truncated := TaskOutputPreview(output)
	if !truncated || !utf8.ValidString(preview) || total != utf8.RuneCountInString(output) {
		t.Fatal("invalid preview")
	}
	var recovered strings.Builder
	offset := 0
	for {
		page := PageTaskOutput(output, offset, "")
		recovered.WriteString(page.Output)
		if !page.HasMore {
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("pagination did not advance")
		}
		offset = page.NextOffset
	}
	if recovered.String() != output {
		t.Fatal("output was lost across pages")
	}
	page := PageTaskOutput(output, 0, "final-rpc-registration-evidence")
	if !page.Found || !strings.Contains(page.Output, "final-rpc-registration-evidence") {
		t.Fatal("search missed evidence beyond preview")
	}
	if PageTaskOutput(output, 0, "absent-evidence").Found {
		t.Fatal("false search match")
	}
	if PageTaskOutput(output, total+1, "").Output != "" {
		t.Fatal("offset beyond end")
	}
}
