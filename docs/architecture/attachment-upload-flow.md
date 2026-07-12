# Attachment upload flow

All current Web and Desktop upload surfaces use `ApiClient.uploadFile`. Each
user action gets one UUIDv4 `Idempotency-Key`; an unknown transport or response
outcome is retried once with the same key and a freshly constructed multipart
body.

`POST /api/upload-file` authenticates the actor, resolves workspace membership
and validates optional Issue, Comment or Chat ownership before touching object
storage. It fingerprints the file SHA-256, filename, detected content type,
size and validated association targets. One database transaction reserves
`resource_create_request[type=attachment]` before the external upload. The
unique request row serializes concurrent calls, so a different file using the
same key is rejected before it can overwrite the deterministic object key.

After object storage succeeds, the same transaction creates the Attachment and
stores the exact 200 response. Any pre-commit database failure rolls back the
row and compensates the object. A commit with an unknown result is reconciled
against the durable request: a committed response is returned; a proven
rollback removes the object; an unavailable reconciliation keeps the
deterministic object so the same-key retry can recover without corrupting a
possibly committed Attachment.

Verification anchors:

- HTTP, fingerprint, transaction and compensation:
  `server/internal/handler/file.go`.
- Shared replay state machine and resource namespace:
  `server/internal/handler/resource_create_idempotency.go`.
- Replay, different-content conflict and eight-way convergence:
  `server/internal/handler/file_test.go`.
- Durable request schema:
  `server/migrations/050_add_attachment_create_requests.up.sql`.
- Client same-key retry:
  `packages/core/api/client.ts` and `packages/core/api/client.test.ts`.
