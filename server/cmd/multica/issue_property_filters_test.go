package main

import (
	"encoding/json"
	"testing"
)

func TestPropertyComparison(t *testing.T) {
	cases := []struct {
		kind, op, value, want string
		invalid               bool
	}{
		{"number", ">=", "3", `{"op":"gte","value":"3"}`, false},
		{"date", "<", "2026-01-01", `{"op":"before","value":"2026-01-01"}`, false},
		{"text", "~=", "  审查  ", `{"op":"contains","value":"  审查  "}`, false},
		{"select", "=", "__none__", `"__none__"`, false},
		{"number", ">", "NaN", "", true}, {"number", ">", "+Inf", "", true},
		{"date", ">=", "2026-01-01", "", true}, {"date", "<", "2026-02-30", "", true},
		{"select", ">", "High", "", true}, {"text", "!=", "test", "", true},
	}
	for _, c := range cases {
		t.Run(c.kind+c.op+c.value, func(t *testing.T) {
			v, err := propertyComparison(c.kind, c.op, c.value)
			if c.invalid {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(v)
			if err != nil || string(b) != c.want {
				t.Fatalf("got %s / %v", b, err)
			}
		})
	}
}
