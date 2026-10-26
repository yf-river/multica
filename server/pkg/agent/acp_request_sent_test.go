package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type failingACPWriter struct{}

func (failingACPWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestACPControlReadinessFollowsSuccessfulPromptWrite(t *testing.T) {
	ready := false
	stdin := &fakeStdinWithHook{}
	c := &hermesClient{stdin: stdin, pending: make(map[int]*pendingRPC), onRequestSent: func(method string) {
		if method == "session/prompt" {
			ready = true
		}
	}}
	stdin.afterWrite = func() {
		if ready {
			t.Fatal("control gate opened before the user prompt reached the transport")
		}
		var request struct {
			ID int `json:"id"`
		}
		lines := stdin.Lines()
		json.Unmarshal([]byte(lines[len(lines)-1]), &request)
		c.handleLine(fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	}
	if _, err := c.request(t.Context(), "session/prompt", map[string]any{}); err != nil || !ready {
		t.Fatalf("successful prompt write did not enable control: %v", err)
	}
	ready = false
	c.stdin = failingACPWriter{}
	if _, err := c.request(t.Context(), "session/prompt", map[string]any{}); err == nil || ready {
		t.Fatal("failed prompt write enabled live controls")
	}
}
