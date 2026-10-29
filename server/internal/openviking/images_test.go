package openviking

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessageImagesPreserveBytesAndOriginalReference(t *testing.T) {
	dir := t.TempDir()
	data := []byte{137, 80, 78, 71, 0, 255, 128}
	file := "image.png"
	if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
		t.Fatal(err)
	}
	root := "viking://user/ida/sessions/test/multica/raw"
	url := root + "/" + file
	meta := Metadata{Attachments: []Attachment{{ID: "image", Path: file, ContentType: "image/png", Filename: file, SHA256: SHA(data)}}}
	messages := []Message{{Role: "user", Parts: []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": url}}}}}
	if err := inlineMessageImages(messages, meta, dir, root); err != nil {
		t.Fatal(err)
	}
	actual := text(object(messages[0].Parts[0]["image_url"])["url"])
	encoded, ok := strings.CutPrefix(actual, "data:image/png;base64,")
	if !ok {
		t.Fatal("model received an unreadable private Viking URI")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatal("image model input bytes changed")
	}
	if len(messages[0].Parts) != 2 || messages[0].Parts[1]["uri"] != url {
		t.Fatal("original attachment provenance lost")
	}
	if err = os.WriteFile(filepath.Join(dir, file), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	messages[0].Parts = []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": url}}}
	if err = inlineMessageImages(messages, meta, dir, root); err == nil {
		t.Fatal("changed attachment accepted without matching original hash")
	}
}
