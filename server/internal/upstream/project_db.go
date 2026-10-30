package upstream

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const DefaultProject = "multica"

func (s *Service) Project() string {
	if s.ProjectID == "" {
		return DefaultProject
	}
	return s.ProjectID
}

func (s *Service) ForProject(id string) *Service {
	copy := *s
	copy.ProjectID = id
	return &copy
}

// 项目标记与普通位置参数一起绑定，批次内的事务使用相同项目身份。
func projectQuery(id, sql string, args []any) (string, []any) {
	if !strings.Contains(sql, "@project") {
		return sql, args
	}
	return strings.ReplaceAll(sql, "@project", fmt.Sprintf("$%d", len(args)+1)), append(args, id)
}
func (s *Service) exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	sql, args = projectQuery(s.Project(), sql, args)
	return s.Pool.Exec(ctx, sql, args...)
}
func (s *Service) query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	sql, args = projectQuery(s.Project(), sql, args)
	return s.Pool.Query(ctx, sql, args...)
}
func (s *Service) queryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	sql, args = projectQuery(s.Project(), sql, args)
	return s.Pool.QueryRow(ctx, sql, args...)
}

type projectTx struct {
	pgx.Tx
	project string
}

func (s *Service) begin(ctx context.Context) (*projectTx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &projectTx{Tx: tx, project: s.Project()}, nil
}
func (t *projectTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	sql, args = projectQuery(t.project, sql, args)
	return t.Tx.Exec(ctx, sql, args...)
}
func (t *projectTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	sql, args = projectQuery(t.project, sql, args)
	return t.Tx.Query(ctx, sql, args...)
}
func (t *projectTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	sql, args = projectQuery(t.project, sql, args)
	return t.Tx.QueryRow(ctx, sql, args...)
}
