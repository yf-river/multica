package lark

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type failingCredentialsResolver struct{ err error }

func (r failingCredentialsResolver) DecryptAppSecret(db.LarkInstallation) (string, error) {
	return "", r.err
}

type staticCredentialsResolver struct{ secret string }

func (r staticCredentialsResolver) DecryptAppSecret(db.LarkInstallation) (string, error) {
	return r.secret, nil
}

func TestResolveInstallationCredentials(t *testing.T) {
	inst := db.LarkInstallation{
		AppID:     "cli_current",
		Region:    "lark",
		TenantKey: pgtype.Text{String: "tenant-current", Valid: true},
	}
	got, err := resolveInstallationCredentials(staticCredentialsResolver{secret: "secret-current"}, inst)
	if err != nil {
		t.Fatalf("resolve credentials: %v", err)
	}
	want := InstallationCredentials{
		AppID: "cli_current", AppSecret: "secret-current", Region: RegionLark, TenantKey: "tenant-current",
	}
	if got != want {
		t.Fatalf("credentials = %#v, want %#v", got, want)
	}
}

func TestResolveInstallationCredentialsRejectsInvalidDependenciesAndData(t *testing.T) {
	if _, err := resolveInstallationCredentials(nil, db.LarkInstallation{}); err == nil {
		t.Fatal("expected missing resolver error")
	}
	decryptErr := errors.New("vault unavailable")
	if _, err := resolveInstallationCredentials(failingCredentialsResolver{err: decryptErr}, db.LarkInstallation{}); !errors.Is(err, decryptErr) {
		t.Fatalf("decrypt error = %v, want wrapped resolver error", err)
	}
	if _, err := resolveInstallationCredentials(staticCredentialsResolver{secret: "secret"}, db.LarkInstallation{Region: "unsupported"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("region error = %v, want invalid stored region", err)
	}
}
