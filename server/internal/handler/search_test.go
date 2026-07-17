package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseSearchRequestOptions(t *testing.T) {
	tests := []struct {
		name              string
		query             string
		wantLimit         int
		wantOffset        int
		wantIncludeClosed bool
	}{
		{name: "defaults", wantLimit: 20},
		{name: "current values", query: "limit=12&offset=3&include_closed=true", wantLimit: 12, wantOffset: 3, wantIncludeClosed: true},
		{name: "limit is capped", query: "limit=200", wantLimit: 50},
		{name: "invalid values keep defaults", query: "limit=bad&offset=-1&include_closed=TRUE", wantLimit: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestURL := "/api/search?workspace_id=" + testWorkspaceID + "&q=current"
			if tt.query != "" {
				requestURL += "&" + tt.query
			}
			w := httptest.NewRecorder()
			_, _, got, ok := (&Handler{}).parseSearchRequest(w, newRequest(http.MethodGet, requestURL, nil))
			if !ok {
				t.Fatalf("parseSearchRequest() status = %d, body = %s", w.Code, w.Body.String())
			}
			if got.limit != tt.wantLimit || got.offset != tt.wantOffset || got.includeClosed != tt.wantIncludeClosed {
				t.Fatalf("parseSearchRequest() options = %+v, want limit=%d offset=%d includeClosed=%t", got, tt.wantLimit, tt.wantOffset, tt.wantIncludeClosed)
			}
		})
	}
}

func TestSearchIssuesReturnsCommentMatch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}

	keyword := "current-comment-snippet-" + uuid.NewString()
	issue := createHandlerCommentIssueFixture(t, "Search response contract")
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'member', $3, $4)
	`, testWorkspaceID, issue.ID, testUserID, "Discussion contains "+keyword+" for search"); err != nil {
		t.Fatalf("insert searchable comment: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodGet, "/api/issues/search?q="+url.QueryEscape(keyword), nil)
	testHandler.SearchIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SearchIssues status=%d body=%s", w.Code, w.Body.String())
	}

	var payload struct {
		Issues []map[string]any `json:"issues"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(payload.Issues) != 1 {
		t.Fatalf("search issues=%d, want 1: %#v", len(payload.Issues), payload.Issues)
	}
	result := payload.Issues[0]
	if result["match_source"] != "comment" || !strings.Contains(result["matched_comment_snippet"].(string), keyword) {
		t.Fatalf("current comment snippet missing: %#v", result)
	}
}

func TestBuildSearchQuery_SingleTerm(t *testing.T) {
	query, args := buildSearchQuery("Hello", []string{"Hello"}, 0, false, false)

	if args[0] != "hello" {
		t.Errorf("expected phrase arg to be lowercased, got %q", args[0])
	}

	if strings.Contains(query, "ILIKE") {
		t.Error("query should not contain ILIKE")
	}
	if !strings.Contains(query, "LOWER(i.title) LIKE") {
		t.Error("query should contain LOWER(i.title) LIKE")
	}
	if !strings.Contains(query, "LOWER(COALESCE(i.description, '')) LIKE") {
		t.Error("query should contain LOWER(COALESCE(i.description, '')) LIKE")
	}
	if !strings.Contains(query, "LOWER(c.content) LIKE") {
		t.Error("query should contain LOWER(c.content) LIKE")
	}

	if strings.Contains(query, "LOWER(i.title) = LOWER(") {
		t.Error("exact title rank should not wrap pattern in LOWER (already lowercased in Go)")
	}
	if !strings.Contains(query, "LOWER(i.title) = $1") {
		t.Error("exact title rank should compare LOWER(i.title) = $1 directly")
	}

	if !strings.Contains(query, "NOT IN ('done', 'cancelled')") {
		t.Error("query should exclude done/cancelled when includeClosed=false")
	}
}

func TestBuildSearchQuery_MultiTerm(t *testing.T) {
	query, args := buildSearchQuery("Foo Bar", []string{"Foo", "Bar"}, 0, false, false)
	assertMultiTermSearchQuery(t, query, args)
}

func TestBuildSearchQuery_WithNumber(t *testing.T) {
	query, args := buildSearchQuery("MUL-42", []string{"MUL-42"}, 42, true, false)

	_ = args
	if !strings.Contains(query, "i.number = ") {
		t.Error("query should contain number match in WHERE clause")
	}
	if !strings.Contains(query, "THEN 0") {
		t.Error("query should contain tier 0 rank for identifier match")
	}
}

func TestParseQueryNumber_IgnoresInt4Overflow(t *testing.T) {
	if n, ok := parseQueryNumber("1782266512025"); ok {
		t.Fatalf("expected oversized numeric query to stay text-only, got %d", n)
	}
	if n, ok := parseQueryNumber("MUL-1782266512025"); ok {
		t.Fatalf("expected oversized identifier query to stay text-only, got %d", n)
	}
}

func TestBuildSearchQuery_IncludeClosed(t *testing.T) {
	query, _ := buildSearchQuery("test", []string{"test"}, 0, false, true)

	if strings.Contains(query, "NOT IN ('done', 'cancelled')") {
		t.Error("query should not exclude done/cancelled when includeClosed=true")
	}
}

func TestBuildSearchQuery_SpecialChars(t *testing.T) {
	query, args := buildSearchQuery("100%", []string{"100%"}, 0, false, false)

	_ = query
	if escaped, ok := args[0].(string); !ok || !strings.Contains(escaped, `\%`) {
		t.Errorf("expected %% to be escaped in phrase arg, got %q", args[0])
	}
}

func TestBuildProjectSearchQuery_SingleTerm(t *testing.T) {
	query, args := buildProjectSearchQuery("Hello", []string{"Hello"}, false)

	if args[0] != "hello" {
		t.Errorf("expected phrase arg to be lowercased, got %q", args[0])
	}

	if strings.Contains(query, "ILIKE") {
		t.Error("query should not contain ILIKE")
	}
	if !strings.Contains(query, "LOWER(p.title) LIKE") {
		t.Error("query should contain LOWER(p.title) LIKE")
	}
	if !strings.Contains(query, "LOWER(COALESCE(p.description, '')) LIKE") {
		t.Error("query should contain LOWER(COALESCE(p.description, '')) LIKE")
	}

	if !strings.Contains(query, "NOT IN ('completed', 'cancelled')") {
		t.Error("query should exclude completed/cancelled when includeClosed=false")
	}
}

func TestBuildProjectSearchQuery_MultiTerm(t *testing.T) {
	query, args := buildProjectSearchQuery("Foo Bar", []string{"Foo", "Bar"}, false)
	assertMultiTermSearchQuery(t, query, args)
}

func assertMultiTermSearchQuery(t *testing.T, query string, args []any) {
	t.Helper()
	if args[0] != "foo bar" {
		t.Errorf("expected phrase arg lowercased, got %q", args[0])
	}
	if args[4] != "%foo%" {
		t.Errorf("expected first term arg as contains pattern, got %q", args[4])
	}
	if args[5] != "%bar%" {
		t.Errorf("expected second term arg as contains pattern, got %q", args[5])
	}

	if !strings.Contains(query, " AND ") {
		t.Error("multi-word query should contain AND conditions for per-term matching")
	}
}

func TestBuildProjectSearchQuery_IncludeClosed(t *testing.T) {
	query, _ := buildProjectSearchQuery("test", []string{"test"}, true)

	if strings.Contains(query, "NOT IN ('completed', 'cancelled')") {
		t.Error("query should not exclude completed/cancelled when includeClosed=true")
	}
}

func TestExtractSnippet(t *testing.T) {
	tests := []struct {
		name, content, query, want string
		exact                      bool
	}{
		{name: "phrase", content: "The quick brown fox jumps over the lazy dog near the river bank", query: "brown fox", want: "brown fox"},
		{name: "non-contiguous terms", content: "We need to deploy the new service. The kubernetes cluster is ready for production workloads.", query: "deploy kubernetes", want: "deploy"},
		{name: "short content", content: "short text", query: "missing", want: "short text", exact: true},
		{name: "case insensitive", content: "Error in HTML rendering pipeline", query: "html", want: "HTML"},
		{name: "CJK", content: "这是一段很长的中文内容，包含了搜索关键词测试用例，用来验证多字节字符不会被截断的情况", query: "搜索关键词", want: "搜索关键词"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := extractSnippet(test.content, test.query)
			if (test.exact && got != test.want) || (!test.exact && !strings.Contains(got, test.want)) {
				t.Fatalf("extractSnippet() = %q, want %q (exact=%t)", got, test.want, test.exact)
			}
		})
	}
}

func TestExtractSnippet_FallbackWhenNoMatch(t *testing.T) {
	content := strings.Repeat("a", 200)
	snippet := extractSnippet(content, "zzz")
	if len([]rune(snippet)) > 124 { // 120 + "..."
		t.Errorf("snippet should be truncated to ~120 runes when no match, got len=%d", len([]rune(snippet)))
	}
}

func TestBuildSearchQuery_RankTiers(t *testing.T) {
	query, _ := buildSearchQuery("test phrase", []string{"test", "phrase"}, 0, false, false)
	for _, fragment := range []string{"THEN 5", "THEN 6", "THEN 7", "THEN 8", "ELSE 9"} {
		if !strings.Contains(query, fragment) {
			t.Errorf("query should contain rank fragment %q", fragment)
		}
	}
}

func TestBuildSearchQuery_SingleTermNoAllTermTiers(t *testing.T) {
	query, _ := buildSearchQuery("html", []string{"html"}, 0, false, false)

	rankEnd := strings.Index(query, "ELSE 9 END")
	if rankEnd == -1 {
		t.Fatal("query should contain rank expression with ELSE 9 END")
	}
	rankExpr := query[:rankEnd]

	for _, fragment := range []string{"THEN 4", "THEN 6", "THEN 8"} {
		if strings.Contains(rankExpr, fragment) {
			t.Errorf("single-term rank should not contain %q", fragment)
		}
	}
}
