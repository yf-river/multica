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

- URL 导入、持久状态、幂等请求：`server/internal/handler/knowledge_import.go`
- 公网下载、DNS 固定、重定向与大小/时间边界：`server/internal/handler/knowledge_url_fetch.go`
- `POST /api/workspaces/{id}/knowledge/resources/url`：`url` 与可选 `request_id`；返回导入记录。
- `GET /api/workspaces/{id}/knowledge/imports[/{importId}]`：当前工作区的导入状态。
- `DELETE /api/workspaces/{id}/knowledge/resources?uri=...&recursive=...`：owner/admin 删除。
- `GET .../knowledge/resources?format=json`：结构化目录列表；默认输出供既有 CLI 消费。
- Web 查询与写入缓存：`packages/core/knowledge/queries.ts`；完成索引、删除后刷新目录与搜索。
