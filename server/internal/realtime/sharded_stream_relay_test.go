package realtime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestShardedStreamRelayShardForScopeIsStableAndBounded(t *testing.T) {
	relay := NewShardedStreamRelay(NewHub(), nil, nil)

	first := relay.shardFor(ScopeWorkspace, "workspace-1")
	second := relay.shardFor(ScopeWorkspace, "workspace-1")
	if first != second {
		t.Fatalf("expected stable shard selection, got %d then %d", first, second)
	}
	if first < 0 || first >= defaultShardedRelayShards {
		t.Fatalf("shard %d out of range [0,%d)", first, defaultShardedRelayShards)
	}
}

func TestShardedStreamRelayDeliverMessageUsesEnvelopeScope(t *testing.T) {
	hub := NewHub()
	client := attachRealtimeTestClient(hub, ScopeTask, "task-1")
	relay := NewShardedStreamRelay(hub, nil, nil)
	ev := envelope{
		EventID:     "event-1",
		Scope:       ScopeTask,
		ScopeID:     "task-1",
		PayloadJSON: `{"type":"task:updated"}`,
	}

	relay.deliverMessage(redis.XMessage{Values: envelopeRedisValues(ev)})

	select {
	case raw := <-client.send:
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("delivered frame is not JSON: %v", err)
		}
		if frame["event_id"] != ev.EventID {
			t.Fatalf("expected event_id %q, got %v", ev.EventID, frame["event_id"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected sharded relay message to be delivered")
	}

	relay.deliverMessage(redis.XMessage{Values: envelopeRedisValues(ev)})
	select {
	case duplicate := <-client.send:
		t.Fatalf("expected duplicate event id to be deduped, got %s", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}
