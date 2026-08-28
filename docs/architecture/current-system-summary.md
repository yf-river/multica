# Current system summary

This compact, drift-checked summary is the maintained entry point for Multica's
static architecture inventory. Run `pnpm generate:current-system-map` to write
the expanded JSON and Markdown evidence under ignored
`artifacts/code-health/`. CI runs `pnpm check:current-system-map` and
`pnpm test:current-system-map`; expanded generated evidence is not tracked.

| Surface | Count |
| --- | ---: |
| Go Chi routes | 248 |
| Next.js pages | 24 |
| Next.js route handlers | 1 |
| Next.js rewrites | 6 |
| Database tables | 64 |
| Database functions | 9 |
| Database triggers | 4 |
| Database indexes | 145 |
| Migration files | 18 |
| sqlc modules | 40 |
| sqlc queries | 437 |
| Go WebSocket events | 79 |
| TypeScript WebSocket events | 69 |
| Zustand stores | 19 |
| React Query consumer files | 157 |
| Environment variables | 140 |
| Manually identified external systems | 12 |

The maintained domain and transaction narratives live in
[current domain flows](./domain-flows.md). Manual static-analysis overrides live
in `current-system-map-overrides.json` and require concrete source evidence.
