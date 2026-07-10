package handler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsAgentNameUniqueViolation(t *testing.T) {
	t.Parallel()

	for _, constraint := range []string{
		"agent_workspace_name_active_unique",
		"agent_personal_owner_name_active_unique",
		"agent_personal_no_owner_name_active_unique",
	} {
		constraint := constraint
		t.Run(constraint, func(t *testing.T) {
			err := fmt.Errorf("insert agent: %w", &pgconn.PgError{
				Code:           "23505",
				ConstraintName: constraint,
			})
			if !isAgentNameUniqueViolation(err) {
				t.Fatalf("expected current agent name constraint %q to match", constraint)
			}
		})
	}

	for name, err := range map[string]error{
		"legacy workspace constraint": &pgconn.PgError{Code: "23505", ConstraintName: "agent_workspace_name_unique"},
		"legacy private constraint":   &pgconn.PgError{Code: "23505", ConstraintName: "agent_private_owner_name_active_unique"},
		"unrelated unique constraint": &pgconn.PgError{Code: "23505", ConstraintName: "member_workspace_user_unique"},
		"different postgres code":     &pgconn.PgError{Code: "23514", ConstraintName: "agent_workspace_name_active_unique"},
		"non postgres error":          errors.New("insert failed"),
	} {
		name, err := name, err
		t.Run(name, func(t *testing.T) {
			if isAgentNameUniqueViolation(err) {
				t.Fatalf("unexpected match for %T: %v", err, err)
			}
		})
	}
}
