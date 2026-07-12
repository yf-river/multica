package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

func TestAppendCommonIssueListFilters(t *testing.T) {
	values := url.Values{
		"assignee_id":      {"11111111-1111-4111-8111-111111111111"},
		"assignee_ids":     {"22222222-2222-4222-8222-222222222222,33333333-3333-4333-8333-333333333333"},
		"creator_id":       {"44444444-4444-4444-8444-444444444444"},
		"project_id":       {"55555555-5555-4555-8555-555555555555"},
		"involves_user_id": {"66666666-6666-4666-8666-666666666666"},
		"metadata":         {`{"source_provider":"tapd"}`},
	}
	arguments := make([]any, 0, 6)
	w := httptest.NewRecorder()
	where, ok := appendCommonIssueListFilters(w, values, []string{"base"}, func(value any) string {
		arguments = append(arguments, value)
		return "$" + strconv.Itoa(len(arguments)+1)
	})
	if !ok {
		t.Fatalf("appendCommonIssueListFilters failed: %s", w.Body.String())
	}
	if len(arguments) != 6 || len(where) != 7 {
		t.Fatalf("arguments=%d clauses=%d, want 6/7: %#v", len(arguments), len(where), where)
	}
	joined := strings.Join(where, "\n")
	for _, required := range []string{
		"i.assignee_id = $2::uuid",
		"i.assignee_id = ANY($3::uuid[])",
		"i.creator_id = $4::uuid",
		"i.project_id = $5::uuid",
		"a.owner_id     = $6::uuid",
		"i.metadata @> $7::jsonb",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("common filters missing %q:\n%s", required, joined)
		}
	}
}

func TestAppendCommonIssueListFiltersRejectsMalformedValues(t *testing.T) {
	tests := []url.Values{
		{"assignee_id": {"bad"}},
		{"assignee_ids": {"bad"}},
		{"creator_id": {"bad"}},
		{"project_id": {"bad"}},
		{"involves_user_id": {"bad"}},
		{"metadata": {"not-json"}},
	}
	for _, values := range tests {
		w := httptest.NewRecorder()
		if _, ok := appendCommonIssueListFilters(w, values, nil, func(any) string { return "$2" }); ok {
			t.Fatalf("malformed values accepted: %v", values)
		}
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for %v", w.Code, values)
		}
	}
}
