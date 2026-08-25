package lark

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestInstallationServiceCredentials(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	sealed, err := box.Seal([]byte("secret-current"))
	if err != nil {
		t.Fatalf("seal secret: %v", err)
	}
	service := &InstallationService{box: box}
	inst := db.LarkInstallation{
		AppID:              "cli_current",
		AppSecretEncrypted: sealed,
		Region:             "lark",
		TenantKey:          pgtype.Text{String: "tenant-current", Valid: true},
	}
	got, err := service.Credentials(inst)
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

func TestInstallationServiceCredentialsRejectsInvalidCiphertextAndRegion(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("new secretbox: %v", err)
	}
	service := &InstallationService{box: box}
	if _, err := service.Credentials(db.LarkInstallation{AppSecretEncrypted: []byte("invalid")}); err == nil {
		t.Fatal("expected ciphertext error")
	}
	sealed, err := box.Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("seal secret: %v", err)
	}
	if _, err := service.Credentials(db.LarkInstallation{AppSecretEncrypted: sealed, Region: "unsupported"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("region error = %v, want invalid stored region", err)
	}
}
