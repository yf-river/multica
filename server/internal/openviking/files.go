package openviking

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

type hashWriter struct {
	io.Writer
	hash hash.Hash
}

func newHashWriter(w io.Writer) *hashWriter { return &hashWriter{w, sha256.New()} }
func (h *hashWriter) Write(p []byte) (int, error) {
	n, err := h.Writer.Write(p)
	_, _ = h.hash.Write(p[:n])
	return n, err
}
func (h *hashWriter) sum() string { return hex.EncodeToString(h.hash.Sum(nil)) }

func ObjectPath(root, id string) string { return filepath.Join(root, "objects", SHA([]byte(id))) }

func SaveObject(root, id string, data []byte) error {
	if root == "" {
		return fmt.Errorf("MULTICA_OPENVIKING_ARCHIVE_ROOT is required")
	}
	dir := filepath.Join(root, "objects")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".object-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, ObjectPath(root, id)); err != nil {
		return err
	}
	return syncDir(dir)
}

func SHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

const TempArchiveMarker = ".multica-openviking-pending.json"

func (j *Journal) ProtectTempRoot(root string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Meta.NativeTempRoot = root
	if err := writeJSONFile(root, TempArchiveMarker, map[string]string{"journal": j.Dir}); err != nil {
		return err
	}
	return writeJSONFile(j.Dir, "metadata.json", j.Meta)
}
func (j *Journal) ClearTempProtection() error {
	if j.Meta.NativeTempRoot == "" {
		return nil
	}
	file := filepath.Join(j.Meta.NativeTempRoot, TempArchiveMarker)
	raw, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var owner struct {
		Journal string `json:"journal"`
	}
	if err = json.Unmarshal(raw, &owner); err != nil {
		return err
	}
	if owner.Journal != j.Dir {
		return nil
	}
	if err = os.Remove(file); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(j.Meta.NativeTempRoot)
}
func (j *Journal) PendingNativeOutputs() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err != nil {
		return true
	}
	for _, a := range j.Meta.Attachments {
		if a.SourcePath != "" && a.Path == "" {
			return true
		}
	}
	return false
}
func (j *Journal) WantNativeOutput(id, source string) error {
	if err := j.WantAttachment(id, filepath.Base(source), "text/plain"); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.Meta.Attachments {
		if j.Meta.Attachments[i].ID == id {
			j.Meta.Attachments[i].SourcePath = source
			break
		}
	}
	return writeJSONFile(j.Dir, "metadata.json", j.Meta)
}
