package realtime

import (
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

type envelope struct {
	EventID     string `json:"event_id"`
	EventType   string `json:"event_type"`
	Scope       string `json:"scope"`
	ScopeID     string `json:"scope_id"`
	WorkspaceID string `json:"workspace_id"`
	ActorID     string `json:"actor_id"`
	CreatedAt   string `json:"created_at"`
	NodeID      string `json:"node_id"`
	PayloadJSON string `json:"payload_json"`
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
	if eventType, actorID := peekTypeActor(frame); eventType != "" {
		ev.EventType = eventType
		ev.ActorID = actorID
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

func redisString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
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

func peekTypeActor(frame []byte) (string, string) {
	var probe struct {
		Type    string `json:"type"`
		ActorID string `json:"actor_id"`
	}
	_ = json.Unmarshal(frame, &probe)
	return probe.Type, probe.ActorID
}

func injectEventID(frame []byte, eventID string) []byte {
	if eventID == "" || len(frame) == 0 || frame[0] != '{' {
		return frame
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(frame, &object); err != nil {
		return frame
	}
	if _, exists := object["event_id"]; exists {
		return frame
	}
	eventIDJSON, _ := json.Marshal(eventID)
	object["event_id"] = eventIDJSON
	out, err := json.Marshal(object)
	if err != nil {
		return frame
	}
	return out
}
