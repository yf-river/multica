package lark

import (
	"errors"
	"fmt"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func resolveInstallationCredentials(resolver CredentialsResolver, inst db.LarkInstallation) (InstallationCredentials, error) {
	if resolver == nil {
		return InstallationCredentials{}, errors.New("lark credentials resolver missing")
	}
	secret, err := resolver.DecryptAppSecret(inst)
	if err != nil {
		return InstallationCredentials{}, fmt.Errorf("decrypt app_secret: %w", err)
	}
	region, err := ParseRegion(inst.Region)
	if err != nil {
		return InstallationCredentials{}, err
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: secret,
		Region:    region,
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}
	return creds, nil
}
