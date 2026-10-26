// Package dshprofile 协调 daemon 供给与 Agent 启动使用的版本身份和跨进程准入锁。
package dshprofile

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const StateEnv = "MULTICA_DSH_PROFILE_STATE"
const ArtifactEnv = "MULTICA_DSH_ARTIFACT_DIR"

var ErrBusy = errors.New("DSH profile is in use")
var safeProfile = regexp.MustCompile(`^multica-managed-[a-f0-9]{24}$`)
var safeFilename = regexp.MustCompile(`^dsh-runtime-[0-9A-Za-z.+-]+\.tgz$`)

type Manifest struct {
	BridgeVersion   string   `json:"bridgeVersion"`
	ProtocolVersion int      `json:"protocolVersion"`
	DSHRange        string   `json:"dshRange"`
	Capabilities    []string `json:"capabilities"`
	Smoke           []string `json:"smoke"`
	Filename        string   `json:"filename"`
	SHA256          string   `json:"sha256"`
	Bytes           int64    `json:"bytes"`
}

type Generation struct {
	Profile       string    `json:"profile"`
	DSHPath       string    `json:"dsh_path"`
	DSHVersion    string    `json:"dsh_version"`
	BridgeVersion string    `json:"bridge_version"`
	SHA256        string    `json:"sha256"`
	ActivatedAt   time.Time `json:"activated_at"`
}

type State struct {
	Active   Generation  `json:"active"`
	Previous *Generation `json:"previous,omitempty"`
}

// Lock 在执行期间持有共享锁，供给切换使用排他锁；进程退出由内核释放。
func Lock(root string, exclusive bool) (*os.File, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, "admission.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = lock(f, exclusive); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func Read(root string) (*State, error) {
	raw, err := os.ReadFile(filepath.Join(root, "active.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state State
	if err = json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if err = validateGeneration(state.Active); err != nil {
		return nil, err
	}
	if state.Previous != nil {
		if err = validateGeneration(*state.Previous); err != nil {
			return nil, err
		}
	}
	return &state, nil
}

func validateGeneration(g Generation) error {
	if !safeProfile.MatchString(g.Profile) || !filepath.IsAbs(g.DSHPath) || !semver.IsValid("v"+g.DSHVersion) || !semver.IsValid("v"+g.BridgeVersion) || len(g.SHA256) != 64 {
		return errors.New("invalid managed DSH generation")
	}
	_, err := hex.DecodeString(g.SHA256)
	return err
}

func ProfileName(root, identity string) string {
	sum := sha256.Sum256([]byte(root + "\x00" + identity))
	return "multica-managed-" + hex.EncodeToString(sum[:12])
}

// Save 的调用者必须持有排他锁；文件先落盘再原子替换。
func Save(root string, state State) error {
	if err := validateGeneration(state.Active); err != nil {
		return err
	}
	return WriteJSON(root, "active.json", state)
}

func WriteJSON(root, name string, value any) error {
	if name != filepath.Base(name) {
		return errors.New("invalid state filename")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(value); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(root, name))
}

// Supports 接受兼容清单的双边界范围，包含范围内的 DSH 预发布版本。
func Supports(version, constraint string) bool {
	parts := strings.Fields(constraint)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], ">=") || !strings.HasPrefix(parts[1], "<") {
		return false
	}
	v, lo, hi := "v"+strings.TrimPrefix(version, "v"), "v"+strings.TrimPrefix(parts[0], ">="), "v"+strings.TrimPrefix(parts[1], "<")
	return semver.IsValid(v) && semver.IsValid(lo) && semver.IsValid(hi) && semver.Compare(v, lo) >= 0 && semver.Compare(v, hi) < 0
}

func ReadManifest(directory string) (Manifest, error) {
	var m Manifest
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	if !safeFilename.MatchString(m.Filename) || m.ProtocolVersion != 1 || m.Bytes <= 0 || m.Bytes > 64<<20 || !semver.IsValid("v"+m.BridgeVersion) {
		return m, errors.New("invalid DSH artifact manifest")
	}
	return m, nil
}

// VerifyArtifact 在安装前核对受信交付清单及包内兼容元数据，不解包到磁盘。
func VerifyArtifact(directory string, m Manifest) (string, error) {
	if !safeFilename.MatchString(m.Filename) {
		return "", errors.New("invalid artifact filename")
	}
	path := filepath.Join(directory, m.Filename)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() != m.Bytes {
		return "", errors.New("DSH artifact size or type mismatch")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return "", errors.New("DSH artifact checksum mismatch")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return "", err
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer z.Close()
	tr := tar.NewReader(io.LimitReader(z, 128<<20))
	found := false
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		name := strings.TrimSuffix(header.Name, "/")
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return "", errors.New("unsupported DSH artifact entry type")
		}
		if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.Contains("/"+name+"/", "/../") || !strings.HasPrefix(name, "package/") && name != "package" || header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return "", errors.New("unsafe DSH artifact entry")
		}
		if name != "package/compat.json" {
			continue
		}
		if found || header.Size > 64<<10 {
			return "", errors.New("invalid packed compatibility manifest")
		}
		var packed Manifest
		if err = json.NewDecoder(io.LimitReader(tr, 64<<10)).Decode(&packed); err != nil {
			return "", err
		}
		if packed.BridgeVersion != m.BridgeVersion || packed.ProtocolVersion != m.ProtocolVersion || packed.DSHRange != m.DSHRange {
			return "", fmt.Errorf("packed bridge compatibility differs from delivery manifest")
		}
		found = true
	}
	if !found {
		return "", errors.New("packed bridge has no compatibility manifest")
	}
	return path, nil
}
