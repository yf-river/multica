package realtime

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDualWriteBroadcasterFansOutLocallyBeforePublishing(t *testing.T) {
	hub := NewHub()
	client := attachRealtimeTestClient(hub, ScopeWorkspace, "workspace-1")
	publisher := &localFirstPublisher{t: t, client: client}
	broadcaster := NewDualWriteBroadcaster(hub, publisher)
	message := []byte(`{"type":"issue:updated"}`)

	broadcaster.BroadcastToScope(ScopeWorkspace, "workspace-1", message)

	if !publisher.called {
		t.Fatal("expected relay publish to be invoked")
	}
	if publisher.scopeType != ScopeWorkspace || publisher.scopeID != "workspace-1" {
		t.Fatalf("unexpected relay scope: %s/%s", publisher.scopeType, publisher.scopeID)
	}
	if string(publisher.frame) != string(message) {
		t.Fatalf("expected relay payload to remain unchanged, got %s", publisher.frame)
	}

	var localFrame map[string]any
	if err := json.Unmarshal(publisher.localFrame, &localFrame); err != nil {
		t.Fatalf("local frame is not JSON: %v", err)
	}
	if localFrame["event_id"] != publisher.eventID {
		t.Fatalf("expected local frame event_id %q, got %v", publisher.eventID, localFrame["event_id"])
	}

	hub.BroadcastToScopeDedup(ScopeWorkspace, "workspace-1", injectEventID(message, publisher.eventID), publisher.eventID)
	select {
	case duplicate := <-client.send:
		t.Fatalf("expected redis loopback to dedup, got duplicate %s", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestDualWriteBroadcasterUserExclusionMatchesRelayEnvelope(t *testing.T) {
	hub := NewHub()
	excluded := attachRealtimeTestClient(hub, ScopeUser, "user-1")
	delivered := attachRealtimeTestClient(hub, ScopeUser, "user-1")
	delivered.workspaceID = "workspace-2"
	publisher := &localFirstPublisher{t: t, client: delivered}
	broadcaster := NewDualWriteBroadcaster(hub, publisher)
	message := []byte(`{"type":"member:added"}`)

	broadcaster.BroadcastToUser("user-1", "workspace-1", message)

	if publisher.scopeType != ScopeUser || publisher.scopeID != "user-1" || publisher.exclude != "workspace-1" {
		t.Fatalf("relay envelope = %s/%s exclude=%q", publisher.scopeType, publisher.scopeID, publisher.exclude)
	}
	select {
	case frame := <-excluded.send:
		t.Fatalf("excluded workspace received duplicate frame %s", frame)
	default:
	}
}

func attachRealtimeTestClient(hub *Hub, scopeType, scopeID string) *Client {
	client := &Client{
		send:          make(chan []byte, 2),
		workspaceID:   "workspace-1",
		userID:        "user-1",
		subscriptions: map[scopeKey]bool{},
	}
	key := sk(scopeType, scopeID)
	client.subscriptions[key] = true

	hub.mu.Lock()
	hub.clients[client] = true
	hub.rooms[key] = map[*Client]bool{client: true}
	hub.mu.Unlock()

	return client
}

type localFirstPublisher struct {
	t      *testing.T
	client *Client

	called     bool
	scopeType  string
	scopeID    string
	exclude    string
	frame      []byte
	eventID    string
	localFrame []byte
}

func (p *localFirstPublisher) PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error {
	p.called = true
	p.scopeType = scopeType
	p.scopeID = scopeID
	p.exclude = exclude
	p.frame = append([]byte(nil), frame...)
	p.eventID = id

	select {
	case p.localFrame = <-p.client.send:
	default:
		p.t.Fatal("expected local fanout to happen before relay publish")
	}
	return nil
}
