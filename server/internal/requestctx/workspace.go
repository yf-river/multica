package requestctx

import (
	"context"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type workspaceContextKey struct{}

type workspaceContext struct {
	id     string
	member db.Member
}

func WithWorkspace(ctx context.Context, id string, member db.Member) context.Context {
	return context.WithValue(ctx, workspaceContextKey{}, workspaceContext{id: id, member: member})
}

func WorkspaceID(ctx context.Context) string {
	workspace, _ := ctx.Value(workspaceContextKey{}).(workspaceContext)
	return workspace.id
}

func WorkspaceMember(ctx context.Context) (db.Member, bool) {
	workspace, ok := ctx.Value(workspaceContextKey{}).(workspaceContext)
	return workspace.member, ok
}
