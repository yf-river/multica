package realtime

import "github.com/oklog/ulid/v2"

// RelayBroadcaster delivers each message to the local hub immediately and
// to the Redis relay for cross-node fanout. Event IDs deduplicate the relay
// loopback on the originating node.
type RelayBroadcaster struct {
	local *Hub
	relay RelayPublisher
}

type RelayPublisher interface {
	PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error
}

func NewRelayBroadcaster(local *Hub, relay RelayPublisher) *RelayBroadcaster {
	return &RelayBroadcaster{local: local, relay: relay}
}

func (d *RelayBroadcaster) BroadcastToScope(scopeType, scopeID string, message []byte) {
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	d.local.BroadcastToScopeDedup(scopeType, scopeID, frame, id)
	_ = d.relay.PublishWithID(scopeType, scopeID, "", message, id)
}

func (d *RelayBroadcaster) BroadcastToWorkspace(workspaceID string, message []byte) {
	d.BroadcastToScope(ScopeWorkspace, workspaceID, message)
}

func (d *RelayBroadcaster) SendToUser(userID string, message []byte, excludeWorkspace ...string) {
	exclude := ""
	if len(excludeWorkspace) > 0 {
		exclude = excludeWorkspace[0]
	}
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	d.local.fanoutUser(userID, frame, exclude, id)
	_ = d.relay.PublishWithID(ScopeUser, userID, exclude, message, id)
}

func (d *RelayBroadcaster) Broadcast(message []byte) {
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	d.local.fanoutAllDedup(frame, "", id)
	_ = d.relay.PublishWithID("global", "all", "", message, id)
}

var _ Broadcaster = (*RelayBroadcaster)(nil)
