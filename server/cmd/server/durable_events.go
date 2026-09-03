package main

import (
	"github.com/multica-ai/multica/server/internal/eventoutbox"
)

// registerDurableEvents registers one consumer for a set of event types. The
// dispatcher keeps the consumer receipt per event, so registration is the
// single source of truth for the durable delivery graph.
func registerDurableEvents(dispatcher *eventoutbox.Dispatcher, name string, consumer eventoutbox.Consumer, eventTypes ...string) error {
	for _, eventType := range eventTypes {
		if err := dispatcher.Register(eventType, name, consumer); err != nil {
			return err
		}
	}
	return nil
}
