package dshprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Host struct {
	Kind        string `json:"kind"`
	Node        string `json:"node"`
	Entrypoint  string `json:"entrypoint"`
	PackageRoot string `json:"package_root"`
	Version     string `json:"version"`
}

type hostSnapshot struct {
	Identity string            `json:"identity"`
	Version  string            `json:"version"`
	Files    map[string]string `json:"files"`
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// SnapshotHost 保存可独立启动的 Node 与 DSH 包；不复制用户 home、profile 或凭据。
func SnapshotHost(ctx context.Context, destination string, host Host) (string, error) {
	lease, err := Lock(destination, true)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	if host.Kind != "node-package" || !filepath.IsAbs(host.Node) || !filepath.IsAbs(host.PackageRoot) || !filepath.IsAbs(host.Entrypoint) || !within(host.PackageRoot, host.Entrypoint) {
		return "", errors.New("DSH installation has no supported snapshot descriptor")
	}
	meta, err := os.ReadFile(filepath.Join(host.PackageRoot, "package.json"))
	if err != nil {
		return "", err
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err = json.Unmarshal(meta, &pkg); err != nil || pkg.Name != "@deepseek-ai/dsh" || pkg.Version != host.Version {
		return "", errors.New("DSH host package identity mismatch")
	}
	entry, err := os.ReadFile(host.Entrypoint)
	if err != nil {
		return "", err
	}
	node, err := fileDigest(host.Node)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(host.PackageRoot))
	h.Write(meta)
	h.Write(entry)
	h.Write([]byte(node))
	identity := hex.EncodeToString(h.Sum(nil))
	root := filepath.Join(destination, identity)
	launcher := filepath.Join(root, "bin", "dsh")
	if runtime.GOOS == "windows" {
		launcher += ".cmd"
	}
	if data, e := os.ReadFile(filepath.Join(root, "snapshot.json")); e == nil {
		var record hostSnapshot
		if json.Unmarshal(data, &record) == nil && record.Identity == identity && record.Version == host.Version {
			if e = verifyHostSnapshot(ctx, root, record); e != nil {
				return "", e
			}
			if _, e = os.Stat(launcher); e == nil {
				return launcher, nil
			}
		}
		return "", errors.New("DSH snapshot metadata is invalid")
	}
	if err = os.MkdirAll(destination, 0o700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(destination, ".snapshot-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	record := hostSnapshot{Identity: identity, Version: host.Version, Files: map[string]string{}}
	copyFile := func(source, target, key string, mode os.FileMode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
		if err != nil {
			return err
		}
		digest := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, digest), in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		record.Files[key] = hex.EncodeToString(digest.Sum(nil))
		return nil
	}
	err = filepath.WalkDir(host.PackageRoot, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(host.PackageRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(stage, "package", rel)
		info, err := item.Info()
		if err != nil {
			return err
		}
		if item.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			if !within(host.PackageRoot, resolved) {
				return fmt.Errorf("DSH package link leaves installation: %s", rel)
			}
			resolvedRel, err := filepath.Rel(host.PackageRoot, resolved)
			if err != nil {
				return err
			}
			link, err := filepath.Rel(filepath.Dir(target), filepath.Join(stage, "package", resolvedRel))
			if err != nil {
				return err
			}
			if err = os.Symlink(link, target); err != nil {
				return err
			}
			record.Files["package/"+filepath.ToSlash(rel)] = "symlink:" + link
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported DSH package file: %s", rel)
		}
		return copyFile(path, target, "package/"+filepath.ToSlash(rel), info.Mode())
	})
	if err != nil {
		return "", err
	}
	nodeName := "node"
	if runtime.GOOS == "windows" {
		nodeName += ".exe"
	}
	if err = copyFile(host.Node, filepath.Join(stage, "bin", nodeName), "bin/"+nodeName, 0o700); err != nil {
		return "", err
	}
	if record.Files["bin/"+nodeName] != node {
		return "", errors.New("Node changed during DSH snapshot")
	}
	entryRel, err := filepath.Rel(host.PackageRoot, host.Entrypoint)
	if err != nil {
		return "", err
	}
	// 启动器只引用快照内文件，原安装目录被升级或移走后仍可运行。
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nexec " + quote(filepath.Join(root, "bin", nodeName)) + " " + quote(filepath.Join(root, "package", entryRel)) + " \"$@\"\n"
	if runtime.GOOS == "windows" {
		script = "@echo off\r\n\"%~dp0node.exe\" \"%~dp0..\\package\\" + entryRel + "\" %*\r\n"
	}
	if err = os.WriteFile(filepath.Join(stage, "bin", filepath.Base(launcher)), []byte(script), 0o700); err != nil {
		return "", err
	}
	if err = WriteJSON(stage, "snapshot.json", record); err != nil {
		return "", err
	}
	if err = os.Rename(stage, root); err != nil {
		return "", err
	}
	return launcher, nil
}

func verifyHostSnapshot(ctx context.Context, root string, record hostSnapshot) error {
	for name, want := range record.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if !within(root, path) {
			return errors.New("invalid DSH snapshot file path")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(want, "symlink:") {
			link, err := os.Readlink(path)
			if err != nil || link != strings.TrimPrefix(want, "symlink:") {
				return errors.New("DSH snapshot dependency link changed")
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return errors.New("DSH snapshot file type changed")
		}
		got, err := fileDigest(path)
		if err != nil {
			return err
		}
		if got != want {
			return errors.New("DSH snapshot checksum mismatch")
		}
	}
	return nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
