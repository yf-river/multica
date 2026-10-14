package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSkillAssetDownloadCacheAndCorruption(t *testing.T) {
	data := []byte("\x89PNG\r\n\x1a\n\x00\xffpicture")
	sum := sha256.Sum256(data)
	bundle := SkillData{ID: "skill", Source: "workspace", Name: "pictures", Content: "main", Files: []SkillFileData{{Path: "images/design.png", Kind: "binary", FileID: "file", SHA256: "sha256:" + hex.EncodeToString(sum[:]), SizeBytes: int64(len(data)), Mode: 0644}}}
	ref := skillRefFromBundle(bundle)
	bundle.Hash = ref.Hash
	bundle.SizeBytes = ref.SizeBytes
	corrupt := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Error("attachment missing auth")
		}
		if r.Method == "POST" {
			_ = json.NewEncoder(w).Encode(map[string]any{"bundles": []SkillData{bundle}})
			return
		}
		if corrupt {
			_, _ = w.Write([]byte("broken"))
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.SetToken("scoped-token")
	downloaded, _, err := client.ResolveSkillBundle(context.Background(), "rt", "task", ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloaded.Files[0].Data, data) || !validateSkillBundle(ref, downloaded) {
		t.Fatal("invalid downloaded bytes")
	}
	cache := NewSkillBundleCache(t.TempDir())
	if err = cache.Store("workspace", downloaded); err != nil {
		t.Fatal(err)
	}
	stored, ok := cache.Load("workspace", ref)
	if !ok || !bytes.Equal(stored.Files[0].Data, data) {
		t.Fatal("cache lost binary")
	}
	stored.Files[0].Data[0] ^= 0xff
	if validateSkillBundle(ref, stored) {
		t.Fatal("corrupt cache accepted")
	}
	corrupt = true
	if _, _, err = client.ResolveSkillBundle(context.Background(), "rt", "task", ref); err == nil {
		t.Fatal("corrupt transfer accepted")
	}
}
