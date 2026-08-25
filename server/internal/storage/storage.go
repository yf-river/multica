package storage

import (
	"context"
	"io"
	"log/slog"
	"time"
)

type Storage interface {
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	Delete(ctx context.Context, key string) error
	KeyFromURL(rawURL string) string
	CdnDomain() string
	// GetReader streams an object back to the caller. Used by the attachment
	// preview proxy (GET /api/attachments/{id}/content) to bypass CloudFront
	// CORS and the inline/attachment Content-Disposition decision. Caller
	// must Close the returned reader.
	GetReader(ctx context.Context, key string) (io.ReadCloser, error)
}

func DeleteKeys(ctx context.Context, store Storage, keys []string) {
	for _, key := range keys {
		if err := store.Delete(ctx, key); err != nil {
			slog.Error("storage delete failed", "key", key, "error", err)
		}
	}
}

type DownloadPresigner interface {
	PresignGetWithContentDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error)
}
