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
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

// safePlugin 是随包携带的插件 tarball 白名单。
var safePlugin = regexp.MustCompile(`^dsh-codebuddy-sdk-[0-9A-Za-z.+-]+\.tgz$`)

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
		if _, err = dshprofile.VerifyArtifact(destination, m); err != nil {
			return "", err
		}
		if err = syncPlugins(bundle, destination, manifest.Filename); err != nil {
			return "", err
		}
		return destination, nil
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
	if err = syncPlugins(bundle, destination, manifest.Filename); err != nil {
		return "", err
	}
	return destination, nil
}

// syncPlugins 把随包携带的 DSH 插件 tarball 落到运行时目录，供 daemon 供给时安装。
// 文件名做白名单校验，避免嵌入内容被当成路径。
func syncPlugins(fsys embed.FS, destination, runtimeFile string) error {
	entries, err := fsys.ReadDir("bundle")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".tgz") || name == runtimeFile {
			continue
		}
		if !safePlugin.MatchString(name) {
			// 历史 runtime 版本 tgz 与清单等文件不是插件，跳过即可。
			continue
		}
		data, err := fsys.ReadFile("bundle/" + name)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(destination, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
