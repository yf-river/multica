# Current system summary

This compact, drift-checked summary is the maintained entry point for Multica's
static architecture inventory. Run `pnpm generate:current-system-map` to write
the expanded JSON and Markdown evidence under ignored
`artifacts/code-health/`. CI runs `pnpm check:current-system-map` and
`pnpm test:current-system-map`; expanded generated evidence is not tracked.

| Surface | Count |
| --- | ---: |
| Go Chi routes | 302 |
| Next.js pages | 26 |
| Next.js route handlers | 1 |
| Next.js rewrites | 6 |
| Database tables | 98 |
| Database functions | 15 |
| Database triggers | 9 |
| Database indexes | 169 |
| Migration files | 38 |
| sqlc modules | 45 |
| sqlc queries | 611 |
| Go WebSocket events | 79 |
| TypeScript WebSocket events | 69 |
| Zustand stores | 19 |
| React Query consumer files | 161 |
| Environment variables | 148 |
| Manually identified external systems | 12 |

The maintained domain and transaction narratives live in
[current domain flows](./domain-flows.md). Manual static-analysis overrides live
in `current-system-map-overrides.json` and require concrete source evidence.
