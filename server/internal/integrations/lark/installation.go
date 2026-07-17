package lark

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// InstallationService reads and revokes per-agent Lark installations. Its
// secret box is also used by RegistrationService to encrypt credentials before
// atomically inserting the installation and installer binding.
type InstallationService struct {
	queries *db.Queries
	box     *secretbox.Box
}

// NewInstallationService binds the service to a queries handle and a
// secretbox keyed for at-rest encryption. The box MUST be non-nil; we
// refuse to fall back to plaintext storage even in test or dev
// configurations because that is exactly the regression the §4.4
// requirement guards against.
func NewInstallationService(queries *db.Queries, box *secretbox.Box) (*InstallationService, error) {
	if box == nil {
		return nil, errors.New("lark: InstallationService requires a non-nil secretbox.Box")
	}
	return &InstallationService{queries: queries, box: box}, nil
}

// Revoke flips status to 'revoked' so the WS hub tears the connection
// down on its next sweep and the dispatcher drops any in-flight
// events. The row is preserved (no DELETE) so audit history remains
// queryable; the registration transaction can reactivate it atomically.
func (s *InstallationService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.queries.SetLarkInstallationStatus(ctx, db.SetLarkInstallationStatusParams{
		ID:     id,
		Status: installationStatusRevoked,
	})
}

// Credentials resolves the complete current transport identity for an
// installation. Plaintext secrets stay inside the Lark integration and must
// never be logged or returned through an HTTP response.
func (s *InstallationService) Credentials(inst db.LarkInstallation) (InstallationCredentials, error) {
	plain, err := s.box.Open(inst.AppSecretEncrypted)
	if err != nil {
		return InstallationCredentials{}, fmt.Errorf("decrypt app_secret: %w", err)
	}
	region, err := ParseRegion(inst.Region)
	if err != nil {
		return InstallationCredentials{}, err
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: string(plain),
		Region:    region,
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}
	return creds, nil
}

// GetInWorkspace is the workspace-scoped lookup helper. Internal
// callers (Dispatcher) use GetLarkInstallationByAppID directly because
// the event payload only carries app_id; HTTP-side callers always
// know the workspace and should use this so a forged installation_id
// from a different workspace returns NotFound instead of leaking
// existence.
func (s *InstallationService) GetInWorkspace(ctx context.Context, id, workspaceID pgtype.UUID) (db.LarkInstallation, error) {
	row, err := s.queries.GetLarkInstallationInWorkspace(ctx, db.GetLarkInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.LarkInstallation{}, ErrInstallationNotFound
		}
		return db.LarkInstallation{}, err
	}
	return row, nil
}

// ErrInstallationNotFound surfaces "no row matches in this workspace"
// — used by the HTTP layer to return 404. Distinct from a plain
// pgx.ErrNoRows so handlers do not need to import pgx.
var ErrInstallationNotFound = errors.New("lark installation not found")

func textOrNull(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
