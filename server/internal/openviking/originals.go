package openviking

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"
)

// StoreOriginals 保存可核对的原件；原生 defer_processing 避免把同步元数据当作知识索引。
func (c *Client) StoreOriginals(ctx context.Context, user, root string, files map[string]string) error {
	uri := "viking://resources"
	var parts []string
	if strings.HasPrefix(root, uri+"/") {
		parts = strings.Split(strings.TrimPrefix(root, uri+"/"), "/")
	} else {
		prefix := "viking://user/" + user + "/sessions/"
		if !strings.HasPrefix(root, prefix) {
			return fmt.Errorf("shared resource or owned session original root required")
		}
		tail := strings.Split(strings.TrimPrefix(root, prefix), "/")
		if len(tail) < 2 || !userPattern.MatchString(tail[0]) {
			return fmt.Errorf("originals must be inside a session subdirectory")
		}
		uri = prefix + tail[0]
		parts = tail[1:]
	}

	for _, part := range parts {
		if !userPattern.MatchString(part) {
			return fmt.Errorf("invalid original resource path")
		}
		uri += "/" + part
		if err := c.JSON(ctx, user, "POST", "/api/v1/fs/mkdir", map[string]any{"uri": uri}, nil); err != nil {
			if h, ok := err.(*HTTPError); !ok || h.Code != 409 {
				return err
			}
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	pending := []map[string]any{}
	size := 0
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		err := c.JSON(ctx, user, "POST", "/api/v1/content/batch-write", map[string]any{"root_uri": root, "operations": pending, "defer_processing": true, "skip_conflicts": true, "wait": true}, nil)
		pending = nil
		size = 0
		return err
	}
	for _, rel := range names {
		if strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
			return fmt.Errorf("unsafe original file path")
		}
		for _, part := range strings.Split(rel, "/") {
			if part == ".." || part == "." || part == "" {
				return fmt.Errorf("unsafe original file path")
			}
		}
		data, err := os.ReadFile(files[rel])
		if err != nil {
			return err
		}
		if size > 0 && size+len(data) > 4*1024*1024 {
			if err = flush(); err != nil {
				return err
			}
		}
		pending = append(pending, map[string]any{"uri": root + "/" + rel, "content_base64": base64.StdEncoding.EncodeToString(data), "mode": "create"})
		size += len(data)
		if len(pending) >= 100 {
			if err = flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	for _, rel := range names {
		want, err := os.ReadFile(files[rel])
		if err != nil {
			return err
		}
		got, err := c.Download(ctx, user, root+"/"+rel)
		if err != nil {
			return err
		}
		if SHA(want) != SHA(got) {
			return fmt.Errorf("original checksum mismatch: %s", rel)
		}
	}
	return nil
}
