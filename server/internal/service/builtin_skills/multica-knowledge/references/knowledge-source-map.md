# Knowledge base command source map

- CLI commands: `server/cmd/multica/cmd_knowledge.go`
- Server proxy and workspace scoping: `server/internal/handler/knowledge.go`
- Routes: `server/cmd/server/router.go` (`/api/workspaces/{id}/knowledge*`)
- OpenViking endpoints used:
  - `GET /health`
  - `GET /api/v1/fs/ls`
  - `POST /api/v1/fs/mkdir`
  - `POST /api/v1/search/find`
  - `GET /api/v1/content/read`
- Browser surface: `packages/views/knowledge/components/knowledge-page.tsx`
