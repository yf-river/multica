package metrics_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/metrics"
)

func TestEveryAnalyticsEventHasPrometheusCounter(t *testing.T) {
	t.Parallel()

	declared := analyticsEventNames(t)

	m := metrics.NewBusinessMetrics()
	for name := range declared {
		// Build a minimal event with the required label properties that the
		// dispatcher reads. Since IncForEvent reads via stringProp helpers,
		// a nil Properties map is acceptable for events with empty label
		// sets and is normalised by the helpers for the others.
		ev := analytics.Event{
			Name:       name,
			DistinctID: "test",
			Properties: defaultPropsForEvent(name),
		}
		ok := dispatchIncrementsCounter(m, ev)
		if !ok {
			t.Errorf("analytics event %q is not paired with a Prometheus counter via metrics.IncForEvent", name)
		}
	}
}

// TestNoNakedAnalyticsCaptureInHandlersOrServices walks every Go file under
// server/internal/handler and server/internal/service and asserts that every
// `<x>.Analytics.Capture(analytics.<Helper>(...))` call goes through
// metrics.RecordEvent. There are no exceptions: every server-side PostHog
// event must flow through RecordEvent so the Prometheus and PostHog sides
// cannot drift.
func TestNoNakedAnalyticsCaptureInHandlersOrServices(t *testing.T) {
	t.Parallel()

	roots := []string{
		filepath.Join(repoRoot(t), "internal", "handler"),
		filepath.Join(repoRoot(t), "internal", "service"),
		filepath.Join(repoRoot(t), "cmd", "server"),
	}
	var offenders []string
	fset := token.NewFileSet()
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", root, err)
		}
		for _, file := range matches {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if !isAnalyticsCapture(call) {
						return true
					}
					offenders = append(offenders, fset.Position(call.Pos()).String()+" (in "+fn.Name.Name+")")
					return true
				})
			}
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("found %d naked Analytics.Capture(...) calls — wrap them in metrics.RecordEvent so the Prometheus and PostHog sides cannot drift:\n  %s", len(offenders), strings.Join(offenders, "\n  "))
	}
}

// ---- helpers --------------------------------------------------------------

// repoRoot returns the absolute path to server/. The test sources live in
// server/internal/metrics/ so two parents up is the server root.
func repoRoot(t *testing.T) string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	// .../server/internal/metrics/business_pairing_test.go → .../server
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

// analyticsEventNames parses analytics/events.go and returns every Event*
// constant value (the literal string passed to PostHog).
func analyticsEventNames(t *testing.T) map[string]struct{} {
	t.Helper()

	path := filepath.Join(repoRoot(t), "internal", "analytics", "events.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	out := map[string]struct{}{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Values) == 0 {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Event") {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out[strings.Trim(lit.Value, "\"")] = struct{}{}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no Event* constants found in %s", path)
	}
	return out
}

// dispatchIncrementsCounter sends ev through RecordEvent (with a noop
// PostHog client) and returns true when at least one Prometheus counter
// receives a non-zero increment. We use a fresh BusinessMetrics per event
// so a leftover prewarm value from another counter cannot mask a missing
// dispatch case.
func dispatchIncrementsCounter(m *metrics.BusinessMetrics, ev analytics.Event) bool {
	before := sumAllCounters(m)
	metrics.RecordEvent(analytics.NoopClient{}, m, ev)
	after := sumAllCounters(m)
	return after > before
}

// defaultPropsForEvent returns a properties map populated with the label
// values the dispatcher reads, so the synthetic test event lights up its
// matching counter without relying on the analytics helper plumbing.
func defaultPropsForEvent(name string) map[string]any {
	switch name {
	case analytics.EventSignup:
		return nil
	case analytics.EventWorkspaceCreated:
		return map[string]any{"source": "manual"}
	case analytics.EventIssueCreated:
		return map[string]any{"source": "manual", "platform": "web"}
	case analytics.EventChatMessageSent:
		return map[string]any{"platform": "web"}
	case analytics.EventAgentCreated:
		return map[string]any{"runtime_mode": "local", "source": "manual"}
	case analytics.EventAutopilotCreated:
		return map[string]any{"cadence": "manual"}
	case analytics.EventIssueExecuted:
		return map[string]any{"source": "manual"}
	case analytics.EventRuntimeRegistered, analytics.EventRuntimeReady, analytics.EventRuntimeOffline:
		return map[string]any{"runtime_mode": "local", "provider": "claude"}
	case analytics.EventRuntimeFailed:
		return map[string]any{"runtime_mode": "local", "provider": "claude", "failure_reason": "unknown", "recoverable": false}
	case analytics.EventAutopilotRunStarted, analytics.EventAutopilotRunCompleted, analytics.EventAutopilotRunFailed:
		return map[string]any{"cadence": "manual", "trigger_kind": "manual"}
	case analytics.EventFeedbackSubmitted:
		return map[string]any{"kind": "general", "platform": "web"}
	}
	return map[string]any{}
}

func isAnalyticsCapture(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel == nil || sel.Sel.Name != "Capture" {
		return false
	}
	// Receiver must be a selector ending in `.Analytics`.
	rec, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if rec.Sel == nil || rec.Sel.Name != "Analytics" {
		return false
	}
	return true
}
