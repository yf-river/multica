package realtime

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"
)

func HeartbeatKey(nodeID string) string {
	return fmt.Sprintf("ws:node:%s:heartbeat", nodeID)
}

const (
	heartbeatTTL    = 90 * time.Second
	heartbeatPeriod = 30 * time.Second
)

// envelope is what we serialise into each XADD message. It is opaque to the
// hub: the relay decodes payload_json before fanning out.
type envelope struct {
	EventID     string `json:"event_id"`
	EventType   string `json:"event_type"`
	Scope       string `json:"scope"`
	ScopeID     string `json:"scope_id"`
	WorkspaceID string `json:"workspace_id"`
	ActorID     string `json:"actor_id"`
	CreatedAt   string `json:"created_at"`
	NodeID      string `json:"node_id"`
	PayloadJSON string `json:"payload_json"` // raw JSON of the original ws frame
}

func newEnvelope(nodeID, scopeType, scopeID, exclude string, frame []byte, id string) envelope {
	ev := envelope{
		EventID:     id,
		Scope:       scopeType,
		ScopeID:     scopeID,
		NodeID:      nodeID,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		PayloadJSON: string(frame),
	}
	if exclude != "" {
		ev.WorkspaceID = exclude
	}
	if t, a := peekTypeActor(frame); t != "" {
		ev.EventType = t
		ev.ActorID = a
	}
	return ev
}

func envelopeRedisValues(ev envelope) map[string]any {
	return map[string]any{
		"event_id":     ev.EventID,
		"event_type":   ev.EventType,
		"scope":        ev.Scope,
		"scope_id":     ev.ScopeID,
		"workspace_id": ev.WorkspaceID,
		"actor_id":     ev.ActorID,
		"created_at":   ev.CreatedAt,
		"node_id":      ev.NodeID,
		"payload_json": ev.PayloadJSON,
	}
}

func envelopeFromXMessage(msg redis.XMessage) (envelope, bool) {
	ev := envelope{
		EventID:     redisString(msg.Values["event_id"]),
		EventType:   redisString(msg.Values["event_type"]),
		Scope:       redisString(msg.Values["scope"]),
		ScopeID:     redisString(msg.Values["scope_id"]),
		WorkspaceID: redisString(msg.Values["workspace_id"]),
		ActorID:     redisString(msg.Values["actor_id"]),
		CreatedAt:   redisString(msg.Values["created_at"]),
		NodeID:      redisString(msg.Values["node_id"]),
		PayloadJSON: redisString(msg.Values["payload_json"]),
	}
	return ev, ev.PayloadJSON != ""
}

func redisString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return ""
	}
}

func deliverEnvelope(hub *Hub, daemonRuntime DaemonRuntimeDeliverer, ev envelope) {
	if ev.PayloadJSON == "" {
		return
	}
	frame := injectEventID([]byte(ev.PayloadJSON), ev.EventID)
	switch ev.Scope {
	case ScopeDaemonRuntime:
		if daemonRuntime != nil {
			daemonRuntime.DeliverDaemonRuntime(ev.ScopeID, frame, ev.EventID)
		}
	case "global":
		hub.fanoutAllDedup(frame, "", ev.EventID)
	case ScopeUser:
		hub.fanoutUser(ev.ScopeID, frame, ev.WorkspaceID, ev.EventID)
	default:
		hub.BroadcastToScopeDedup(ev.Scope, ev.ScopeID, frame, ev.EventID)
	}
}

// peekTypeActor parses the WS frame just enough to lift event_type / actor_id
// for the envelope. Failures yield empty strings — the envelope still works.
func peekTypeActor(frame []byte) (string, string) {
	var probe struct {
		Type    string `json:"type"`
		ActorID string `json:"actor_id"`
	}
	_ = json.Unmarshal(frame, &probe)
	return probe.Type, probe.ActorID
}

// injectEventID inserts the event_id field into an existing JSON object frame
// without re-encoding the payload. The frame must be a JSON object.
func injectEventID(frame []byte, eventID string) []byte {
	if eventID == "" || len(frame) == 0 || frame[0] != '{' {
		return frame
	}
	// Decode-encode round-trip is simplest and avoids edge cases with
	// trailing whitespace / nested escapes. A few extra allocations per
	// message are fine relative to the network cost.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(frame, &obj); err != nil {
		return frame
	}
	if _, exists := obj["event_id"]; exists {
		return frame
	}
	idJSON, _ := json.Marshal(eventID)
	obj["event_id"] = idJSON
	out, err := json.Marshal(obj)
	if err != nil {
		return frame
	}
	return out
}

// DualWriteBroadcaster delivers each message both to the local hub (immediate
// fanout) AND to the Redis relay (cross-node fanout). It dedups via
// Client.markSeen so the same client doesn't see the same event twice when
// the Redis relay loops the message back.
type DualWriteBroadcaster struct {
	local *Hub
	relay RelayPublisher
}

// RelayPublisher is implemented by Redis relay backends that can publish a
// caller-supplied event id for local/Redis loopback deduplication.
type RelayPublisher interface {
	PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error
}

func NewDualWriteBroadcaster(local *Hub, relay RelayPublisher) *DualWriteBroadcaster {
	return newDualWriteBroadcaster(local, relay)
}

func newDualWriteBroadcaster(local *Hub, relay RelayPublisher) *DualWriteBroadcaster {
	return &DualWriteBroadcaster{local: local, relay: relay}
}

func (d *DualWriteBroadcaster) BroadcastToScope(scopeType, scopeID string, message []byte) {
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	// Local fast path: BroadcastToScopeDedup marks each client as having
	// seen `id`, so the Redis loopback for the same id will be ignored.
	d.local.BroadcastToScopeDedup(scopeType, scopeID, frame, id)
	_ = d.relay.PublishWithID(scopeType, scopeID, "", message, id)
}

func (d *DualWriteBroadcaster) BroadcastToWorkspace(workspaceID string, message []byte) {
	d.BroadcastToScope(ScopeWorkspace, workspaceID, message)
}

func (d *DualWriteBroadcaster) SendToUser(userID string, message []byte, excludeWorkspace ...string) {
	exclude := ""
	if len(excludeWorkspace) > 0 {
		exclude = excludeWorkspace[0]
	}
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	d.local.fanoutUser(userID, frame, exclude, id)
	_ = d.relay.PublishWithID(ScopeUser, userID, exclude, message, id)
}

func (d *DualWriteBroadcaster) Broadcast(message []byte) {
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	d.local.fanoutAllDedup(frame, "", id)
	_ = d.relay.PublishWithID("global", "all", "", message, id)
}

var _ Broadcaster = (*DualWriteBroadcaster)(nil)
