package realtime

import "context"

// ManagedRelay is a Redis-backed realtime relay with explicit goroutine
// lifecycle management.
type ManagedRelay interface {
	RelayPublisher
	Broadcaster

	NodeID() string
	Start(context.Context)
	Stop()
	Wait()
}

var _ ManagedRelay = (*ShardedStreamRelay)(nil)
