CREATE INDEX CONCURRENTLY IF NOT EXISTS knowledge_import_workspace_idx ON knowledge_import (workspace_id, created_at DESC, id DESC);
