// Package dshbundle 将 bridge 制品随 daemon 二进制交付，独立安装无需外部发布器。
package dshbundle

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

// safePlugin 是随包携带的插件 tarball 白名单。
var safePlugin = regexp.MustCompile(`^dsh-codebuddy-sdk-[0-9A-Za-z.+-]+\.tgz$`)

// 1. PluginNameVersion 从插件文件名拆出插件名与语义化版本，供随包同步和供给择优共用；
// 版本段要求 x.y.z 开头，才算真正的版本后缀而不是插件名里的连字符。
var PluginNameVersion = regexp.MustCompile(`^(.+)-([0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*)\.tgz$`)

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
// 2. 同一插件只保留随包那一个版本：目录里由旧发布留下的其它版本要删掉。
// 供给时按 dsh-codebuddy-sdk-*.tgz 通配安装，留着旧版本会让受管 profile 装到哪一个变成
// 文件名排序的偶然结果，升级也就不再可复核。
func syncPlugins(fsys embed.FS, destination, runtimeFile string) error {
	entries, err := fsys.ReadDir("bundle")
	if err != nil {
		return err
	}
	shipped := map[string]string{}
	versions := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".tgz") || name == runtimeFile {
			continue
		}
		if !safePlugin.MatchString(name) {
			// 历史 runtime 版本 tgz 与清单等文件不是插件，跳过即可。
			continue
		}
		parts := PluginNameVersion.FindStringSubmatch(name)
		if parts == nil {
			return fmt.Errorf("bundled plugin %s has no version suffix", name)
		}
		if other, ok := shipped[parts[1]]; ok && other != name {
			return fmt.Errorf("bundled plugin %s ships two versions: %s and %s", parts[1], other, name)
		}
		shipped[parts[1]] = name
		versions[parts[1]] = parts[2]
	}
	for plugin, keep := range shipped {
		stale, err := filepath.Glob(filepath.Join(destination, plugin+"-*.tgz"))
		if err != nil {
			return err
		}
		for _, path := range stale {
			if filepath.Base(path) == keep {
				continue
			}
			if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, name := range shipped {
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
