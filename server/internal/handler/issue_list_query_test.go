package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestParseIssueOrder(t *testing.T) {
	tests := []struct {
		name       string
		values     url.Values
		want       string
		wantOK     bool
		wantStatus int
	}{
		{name: "default", values: url.Values{}, want: "i.position ASC, i.created_at DESC", wantOK: true},
		{name: "manual ignores direction", values: url.Values{"sort": {"position"}, "direction": {"desc"}}, want: "i.position ASC, i.created_at DESC", wantOK: true},
		{name: "priority descending", values: url.Values{"sort": {"priority"}, "direction": {"desc"}}, want: "CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END DESC, i.created_at DESC", wantOK: true},
		{name: "dates put null last", values: url.Values{"sort": {"due_date"}, "direction": {"asc"}}, want: "i.due_date ASC NULLS LAST, i.created_at DESC", wantOK: true},
		{name: "invalid sort", values: url.Values{"sort": {"unknown"}}, wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "invalid direction", values: url.Values{"sort": {"title"}, "direction": {"sideways"}}, wantOK: false, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			got, ok := parseIssueOrder(w, tt.values)
			if ok != tt.wantOK {
				t.Fatalf("parseIssueOrder ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("parseIssueOrder = %q, want %q", got, tt.want)
			}
			if !tt.wantOK && w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestAppendIssueInvolvesUserFilterKeepsIndirectAssignmentContract(t *testing.T) {
	var argument any
	where := appendIssueInvolvesUserFilter(nil, func(value any) string {
		argument = value
		return "$2"
	}, pgtype.UUID{Bytes: [16]byte{1}, Valid: true})

	if len(where) != 1 {
		t.Fatalf("where clauses = %d, want 1", len(where))
	}
	if argument == nil {
		t.Fatal("involves user was not bound as a query argument")
	}
	clause := where[0]
	for _, required := range []string{
		"a.owner_id     = $2::uuid",
		"sm.member_type = 'member'",
		"sm.member_type = 'agent'",
		"JOIN agent a ON a.id = s.leader_id",
	} {
		if !strings.Contains(clause, required) {
			t.Fatalf("involves clause missing %q:\n%s", required, clause)
		}
	}
	if strings.Contains(clause, "i.assignee_type = 'member'") {
		t.Fatalf("direct member assignment must stay outside involves_user_id:\n%s", clause)
	}
}
