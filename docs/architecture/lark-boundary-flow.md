# Lark integration boundary

## Boundary rule

Multica database state never waits for Lark HTTP. Inbound identity, dedup,
chat-message append and task enqueue are durable database responsibilities.
Outbound typing indicators are transient UI hints and remain best-effort.
Final chat replies and failure notices are user-visible delivery and therefore
use the durable domain-event dispatcher.

## Inbound path

The Lark long-connection Hub verifies the installation, resolves a bound user,
claims `lark_inbound_message_dedup`, appends the user message and enqueues work.
Provider reconnects may replay an event; the claimed Lark message identity is
the current deduplication boundary. Invalid/unbound input is recorded in the
inbound audit surface rather than converted into an authenticated Multica
write.

## Outbound path

Task terminal state first commits a durable `task:completed` or `task:failed`
event. The chat projection persists the assistant message and unread state. If
the session has a `lark_chat_session_binding`, it also enqueues `chat:done` in
the same projection transaction.

`Patcher.RegisterDurable` consumes `chat:done` and `task:failed` through the
domain-event dispatcher. A provider or credential failure returns an error,
retains the event, and retries with bounded backoff. Successful delivery gets
a per-consumer receipt; exhausted delivery becomes an observable dead letter.
The old synchronous Bus subscriber that only logged and swallowed provider
errors is not a production path.

This is at-least-once delivery. A process crash after Lark accepts a message
but before the local receipt commits can produce a duplicate because the Lark
send API has no caller-owned idempotency key in the current protocol. That
tradeoff is explicit: durable retry is preferred to silently losing the only
user-visible answer. Rendering and destination lookup are deterministic so a
retry targets the same bound chat.

## Verification anchors

- Inbound ordering and dedup: `server/internal/integrations/lark/dispatcher.go`
  and `server/internal/integrations/lark/chat.go`
- Durable terminal events: `server/internal/service/task.go`
- Assistant-message and outbound-event projection:
  `server/cmd/server/chat_projection.go`
- Retrying outbound consumer: `server/internal/integrations/lark/outbound.go`
- Dispatcher receipts/retry/dead letter: `server/internal/eventoutbox/outbox.go`
- Bound-session and provider-failure tests:
  `server/cmd/server/chat_projection_test.go` and
  `server/internal/integrations/lark/outbound_test.go`
