package util

import "testing"

func TestStringFromAny(t *testing.T) {
	for _, test := range []struct {
		value any
		want  string
	}{
		{value: "text", want: "text"},
		{value: 12, want: "12"},
		{value: int32(13), want: "13"},
		{value: int64(14), want: "14"},
		{value: 1.25, want: "1.25"},
		{value: true, want: "true"},
		{value: nil, want: ""},
	} {
		if got := StringFromAny(test.value); got != test.want {
			t.Errorf("StringFromAny(%v) = %q, want %q", test.value, got, test.want)
		}
	}
}
