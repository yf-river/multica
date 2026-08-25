package realtime

// Scope types recognised by the broadcaster. Producers and consumers should
// use these constants rather than raw strings so a typo can never silently
// route an event to a non-existent room.
const (
	ScopeWorkspace = "workspace"
	ScopeUser      = "user"
	ScopeTask      = "task"
	ScopeChat      = "chat"
	// ScopeDaemonRuntime routes daemon wakeup frames through the Redis relay.
	// It is consumed by the daemon WebSocket hub, not by browser clients.
	ScopeDaemonRuntime = "daemon_runtime"
)

// Broadcaster is the abstraction every realtime event producer should depend
// on instead of *Hub directly.
type Broadcaster interface {
	// BroadcastToScope fans a message out to every connection currently
	// subscribed to ({scopeType, scopeID}) on this node.
	BroadcastToScope(scopeType, scopeID string, message []byte)

	// BroadcastToUser fans out to a user's connections. The explicit
	// excludeWorkspaceID supports member:added deduplication: connections in
	// that workspace already receive the workspace-scoped copy.
	BroadcastToUser(userID, excludeWorkspaceID string, message []byte)

	// Broadcast fans a message out to every connection on this node.
	// Used for daemon:* events that have no workspace scope.
	Broadcast(message []byte)
}

// DaemonRuntimeDeliverer consumes daemon-runtime scoped relay frames.
type DaemonRuntimeDeliverer interface {
	DeliverDaemonRuntime(scopeID string, frame []byte, eventID string)
}

// Compile-time assertion that *Hub continues to satisfy Broadcaster.
var _ Broadcaster = (*Hub)(nil)
