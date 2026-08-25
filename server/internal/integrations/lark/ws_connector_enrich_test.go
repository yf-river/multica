package lark

import (
	"context"
	"sync"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// recordingEnricher captures what the connector hands it and rewrites
// the body so the test can prove enrichment ran between decode and emit.
type recordingEnricher struct {
	mu    sync.Mutex
	msgs  []InboundMessage
	creds []InstallationCredentials
}

func (e *recordingEnricher) Enrich(ctx context.Context, msg InboundMessage, creds InstallationCredentials) InboundMessage {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.msgs = append(e.msgs, msg)
	e.creds = append(e.creds, creds)
	msg.Body = "ENRICHED:" + msg.Body
	return msg
}

// TestWSConnectorEnrichesBeforeEmit verifies the connector runs the
// Enricher on a decoded message — with the connection's resolved
// credentials — before emitting it to the dispatcher.
func TestWSConnectorEnrichesBeforeEmit(t *testing.T) {
	t.Parallel()
	conn := newFakeWSConn()
	decoder := func(payload []byte, _ db.LarkInstallation) (InboundMessage, bool, error) {
		return InboundMessage{
			EventID:   string(payload),
			AppID:     "test_app",
			MessageID: "msg-" + string(payload),
			Body:      "raw-" + string(payload),
		}, true, nil
	}
	enr := &recordingEnricher{}

	c := quietConnector(t, conn, decoder, time.Hour, enr.Enrich)

	emitted := &inboundMessageRecorder{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, db.LarkInstallation{AppID: "test_app"}, emitted.emit) }()

	pushDataFrame(conn, []byte("evt-1"), "m1")

	messages := waitForEmits(t, emitted, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if messages[0].Body != "ENRICHED:raw-evt-1" {
		t.Errorf("emit body = %q; enricher did not run before emit", messages[0].Body)
	}

	enr.mu.Lock()
	defer enr.mu.Unlock()
	if len(enr.msgs) != 1 || enr.msgs[0].Body != "raw-evt-1" {
		t.Errorf("enricher received %+v", enr.msgs)
	}
	if len(enr.creds) != 1 || enr.creds[0].AppID != "test_app" || enr.creds[0].AppSecret != "secret" {
		t.Errorf("enricher got wrong creds: %+v", enr.creds)
	}
}
