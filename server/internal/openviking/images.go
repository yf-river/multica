package openviking

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// inlineMessageImages 让原生视觉摘要收到模型可读取的图片，同时保留会话原件 URI。
func inlineMessageImages(messages []Message, meta Metadata, dir, rawURI string) error {
	images := map[string]Attachment{}
	for _, a := range meta.Attachments {
		images[rawURI+"/"+a.Path] = a
	}
	for i := range messages {
		references := []map[string]any{}
		for _, part := range messages[i].Parts {
			if part["type"] != "image_url" {
				continue
			}
			image := object(part["image_url"])
			uri := text(image["url"])
			a, ok := images[uri]
			if !ok {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, a.Path))
			if err != nil {
				return err
			}
			if SHA(data) != a.SHA256 {
				return fmt.Errorf("image original checksum mismatch: %s", a.ID)
			}
			image["url"] = "data:" + a.ContentType + ";base64," + base64.StdEncoding.EncodeToString(data)
			references = append(references, map[string]any{"type": "context", "context_type": "resource", "uri": uri, "abstract": a.Filename})
		}
		messages[i].Parts = append(messages[i].Parts, references...)
	}
	return nil
}
