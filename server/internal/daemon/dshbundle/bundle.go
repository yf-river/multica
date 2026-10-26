// Package dshbundle 将 bridge 制品随 daemon 二进制交付，独立安装无需外部发布器。
package dshbundle

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

//go:embed bundle/*
var bundle embed.FS

// Extract 在 daemon 私有目录创建按哈希固定的制品目录，既有目录必须通过校验。
func Extract(base string) (string, error) {
	raw, err := bundle.ReadFile("bundle/manifest.json")
	if err != nil {
		return "", err
	}
	var manifest dshprofile.Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return "", err
	}
	if manifest.Filename != filepath.Base(manifest.Filename) || len(manifest.SHA256) != 64 {
		return "", errors.New("invalid embedded DSH manifest")
	}
	data, err := bundle.ReadFile("bundle/" + manifest.Filename)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != manifest.SHA256 || int64(len(data)) != manifest.Bytes {
		return "", errors.New("embedded DSH artifact checksum mismatch")
	}
	destination := filepath.Join(base, manifest.SHA256)
	lease, err := dshprofile.Lock(base, true)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	if _, err = os.Stat(destination); err == nil {
		m, err := dshprofile.ReadManifest(destination)
		if err != nil {
			return "", err
		}
		if m.SHA256 != manifest.SHA256 {
			return "", errors.New("installed embedded DSH identity mismatch")
		}
		_, err = dshprofile.VerifyArtifact(destination, m)
		return destination, err
	} else if !os.IsNotExist(err) {
		return "", err
	}
	stage, err := os.MkdirTemp(base, ".embedded-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err = os.WriteFile(filepath.Join(stage, manifest.Filename), data, 0o600); err != nil {
		return "", err
	}
	if err = dshprofile.WriteJSON(stage, "manifest.json", manifest); err != nil {
		return "", err
	}
	if _, err = dshprofile.VerifyArtifact(stage, manifest); err != nil {
		return "", err
	}
	if err = os.Rename(stage, destination); err != nil {
		return "", err
	}
	return destination, nil
}
