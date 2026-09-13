// Package storage 定义对象存储（S3 或本地目录）的统一接口，并提供本地与 S3 两种
// 实现。上层代码只依赖本接口，方便测试注入与实现切换。
package storage

import (
	"context"
	"io"
	"time"
)

// Storage 是对象存储能力的抽象：上传、删除、解析 URL 以及流式读回对象。
type Storage interface {
	// Upload 上传对象并返回其可访问 URL。
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	// Delete 删除对象；失败被吞掉（面向尽力而为的清理场景）。
	Delete(ctx context.Context, key string)
	// DeleteObject 是能把失败暴露出来的 Delete——channel-media 协调器会在失败时
	// 安排重试，而不是假定成功。
	DeleteObject(ctx context.Context, key string) error
	// DeleteKeys 批量删除多个对象。
	DeleteKeys(ctx context.Context, keys []string)
	// KeyFromURL 从对象 URL 反解出存储键。
	KeyFromURL(rawURL string) string
	// ObjectURL 是"对 key 成功 Upload 会返回的 URL"——纯配置函数，因此媒体意图
	// 台账可以在上传前就持久化它。
	ObjectURL(key string) string
	// CdnDomain 返回当前 CDN 域名。
	CdnDomain() string
	// GetReader 把对象以流形式读回给调用方。附件预览代理
	//（GET /api/attachments/{id}/content）用它绕过 CloudFront CORS 及
	// inline/attachment Content-Disposition 决策。调用方必须 Close 返回的 reader。
	GetReader(ctx context.Context, key string) (io.ReadCloser, error)
}

// Presigner 提供获取预签名读 URL 的能力。
type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// DownloadPresigner 在 Presigner 基础上允许额外指定 Content-Disposition。
type DownloadPresigner interface {
	PresignGetWithContentDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error)
}
