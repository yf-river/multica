# Knowledge base command source map

- CLI commands: `server/cmd/multica/cmd_knowledge.go`
- Server proxy and Agent scope: `server/internal/handler/knowledge.go`
- Routes: `server/cmd/server/router.go` (`/api/workspaces/{id}/knowledge*`)
- OpenViking endpoints used:
  - `GET /health`
  - `GET /api/v1/fs/ls`
  - `POST /api/v1/search/find`
  - `GET /api/v1/content/read`
- Browser surface: `packages/views/knowledge/components/knowledge-page.tsx`

- `server/internal/openviking/environment.go`: startup isolation checks for explicitly enabled non-production acceptance.
