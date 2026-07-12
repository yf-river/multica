# API client boundary

`packages/core/api/client.ts` is the repository's flat, typed endpoint registry.
It intentionally preserves `api.method(...)` for Web and Desktop while the
shared HTTP boundary lives in `packages/core/api/transport.ts`.

## Ownership

`ApiTransport` owns the mutable connection state and cross-cutting behavior:

- base URL and bearer token;
- workspace, CSRF and client-identity headers;
- request IDs and transport logging;
- 401 handling and structured HTTP errors;
- invalid-JSON classification and mutation outcome-unknown semantics;
- the one safe retry helper used by idempotent mutations.

`ApiClient` owns only endpoint declarations: URL/query construction, request
body serialization and the domain response schema chosen for that endpoint.
Domain schemas and fallback values already live in separate
`schemas-*.ts` modules. Business state remains in React Query/Zustand rather
than the client.

## Reviewed large-file exception

At the Wave 222 audit the registry contains 229 async endpoint methods. Its
size is breadth, not multiple mutable responsibilities. The following designs
were rejected:

- flat compatibility methods delegating to domain clients, because every call
  would gain a wrapper while the old method table still exists;
- prototype registration or class mixins, because import side effects and
  hidden method ownership are harder to trace than direct methods;
- an unrelated-domain inheritance chain, because it encodes no valid domain
  relationship;
- changing every caller to `api.domain.method` in one mechanical rewrite,
  because the navigation change alone does not shorten transport or schema
  logic and creates a high-risk repository-wide API migration.

Keeping the direct registry is acceptable only while endpoint methods remain
thin and stateless. New business decisions, cache writes, persistence, retries
or response normalization must not be added here. Response parsing belongs in
domain schema modules; shared HTTP behavior belongs in `ApiTransport`.

Re-open domain decomposition when a domain needs its own transport policy or
when two or more endpoint methods begin sharing non-trivial domain logic that
cannot live in a schema/query helper. At that point callers should migrate to a
single new domain API, and the old flat methods must be deleted in the same
slice rather than retained as aliases.

## Verification

```bash
pnpm --filter @multica/core typecheck
pnpm --filter @multica/core lint
pnpm --filter @multica/core test
pnpm typecheck
pnpm lint
pnpm test
```

The Core client tests cover headers, CSRF, token clearing, error bodies,
transport failures, invalid JSON, mutation outcome uncertainty, idempotent
retry and every live response-schema domain through the inherited boundary.
