// s3.go 实现基于 AWS S3（及其兼容后端：MinIO、阿里 OSS、腾讯 COS、Cloudflare R2 等）
// 的对象存储：上传（内存字节或流）、下载读取、预签名 URL、删除、以及「给客户端消费的
// 公开 URL」的构造规则（CDN 优先，其次自定义 endpoint，最后 AWS 区域限定主机）。
package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Storage 是对象存储的 S3 实现。
type S3Storage struct {
	client       *s3.Client
	bucket       string
	region       string // 未设 CDN/endpoint 时用它构造 virtual-hosted 风格的公开 URL
	cdnDomain    string // 设置后，返回的 URL 用它替换桶名
	endpointURL  string // 自定义 S3 兼容 endpoint（例如 MinIO）
	usePathStyle bool   // 是否用 path-style 的 S3 寻址
}

// NewS3StorageFromEnv 从环境变量构造 S3Storage。S3_BUCKET 未设置时返回 nil。
//
// 环境变量：
//   - S3_BUCKET（必填）
//   - S3_REGION（默认 us-west-2）
//   - AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY（可选；缺省走默认凭证链）
//   - AWS_ENDPOINT_URL（可选 S3 兼容 endpoint）
//   - S3_USE_PATH_STYLE（可选；设置了 AWS_ENDPOINT_URL 时默认 true）
func NewS3StorageFromEnv() *S3Storage {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		slog.Info("S3_BUCKET not set, cloud upload disabled")
		return nil
	}
	if looksLikeS3Hostname(bucket) {
		slog.Warn(
			"S3_BUCKET looks like a hostname rather than a bucket name — uploads and public URLs will likely both fail. Use only the bucket name (e.g. \"my-bucket\"), not \"<bucket>.s3.<region>.amazonaws.com\".",
			"value", bucket,
		)
	}

	region := os.Getenv("S3_REGION")
	if region == "" {
		region = "us-west-2"
	}

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}

	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if accessKey != "" && secretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}

	cfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		slog.Error("failed to load AWS config", "error", err)
		return nil
	}

	cdnDomain := os.Getenv("CLOUDFRONT_DOMAIN")

	endpointURL := os.Getenv("AWS_ENDPOINT_URL")
	usePathStyle := s3UsePathStyleFromEnv(endpointURL)
	s3Opts := []func(*s3.Options){}
	if endpointURL != "" || usePathStyle {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			if endpointURL != "" {
				o.BaseEndpoint = aws.String(endpointURL)
			}
			o.UsePathStyle = usePathStyle
		})
	}

	slog.Info("S3 storage initialized", "bucket", bucket, "region", region, "cdn_domain", cdnDomain, "endpoint_url", endpointURL, "use_path_style", usePathStyle)
	return &S3Storage{
		client:       s3.NewFromConfig(cfg, s3Opts...),
		bucket:       bucket,
		region:       region,
		cdnDomain:    cdnDomain,
		endpointURL:  endpointURL,
		usePathStyle: usePathStyle,
	}
}

// CdnDomain 返回配置的 CDN 域名（未配置为空串）。
func (s *S3Storage) CdnDomain() string {
	return s.cdnDomain
}

// looksLikeS3Hostname 在配置的 S3_BUCKET 看起来像「S3 endpoint 主机名」而不是桶名时
// 返回 true。真实桶名绝不合法地包含 "amazonaws.com"，所以这是无歧义的配置错误信号——
// 最常见形态是用户把 "<bucket>.s3.<region>.amazonaws.com" 直接粘进 S3_BUCKET。
func looksLikeS3Hostname(bucket string) bool {
	return strings.Contains(bucket, "amazonaws.com")
}

// s3UsePathStyleFromEnv 解析 S3_USE_PATH_STYLE：显式设置才覆盖；默认跟随「是否配置了
// 自定义 endpoint」（配了 endpoint 通常需要 path-style）。
func s3UsePathStyleFromEnv(endpointURL string) bool {
	defaultValue := endpointURL != ""
	raw, ok := os.LookupEnv("S3_USE_PATH_STYLE")
	if !ok || strings.TrimSpace(raw) == "" {
		return defaultValue
	}
	parsed, err := parseBoolEnv(raw)
	if err != nil {
		slog.Warn("invalid S3_USE_PATH_STYLE value, using default", "value", raw, "default", defaultValue)
		return defaultValue
	}
	return parsed
}

// parseBoolEnv 解析常见布尔环境变量写法。
func parseBoolEnv(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool %q", raw)
	}
}

// awsEndpointDomains 是 AWS 提供 S3 服务的父域名。VPC、dualstack、FIPS 主机名都在
// 它们之下，中国分区也包含在内。
var awsEndpointDomains = []string{"amazonaws.com", "amazonaws.com.cn"}

// usesAWSEndpoint 报告上传是否真的发往 AWS S3——默认端点或显式配置的 AWS 端点都算。
// 匹配发生在主机标签边界上，因此 "notamazonaws.com" 与 "s3.amazonaws.com.evil.net"
// 会被正确当作第三方端点。
func (s *S3Storage) usesAWSEndpoint() bool {
	if s.endpointURL == "" {
		return true
	}
	host := endpointHostname(s.endpointURL)
	for _, domain := range awsEndpointDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// endpointHostname 从配置的 endpoint 里取出可比较的主机名：小写、无端口、无结尾根点。
// 没有写 scheme 的值会按 https URL 再试一次，因为否则 url.Parse 会把整串读成路径。
func endpointHostname(endpointURL string) string {
	parsed, err := url.Parse(endpointURL)
	if err != nil {
		return ""
	}
	if parsed.Hostname() == "" {
		parsed, err = url.Parse("https://" + endpointURL)
		if err != nil {
			return ""
		}
	}
	return strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
}

// uploadChecksumOptions 返回「内存缓冲上传路径」的按请求选项。SDK 默认把
// RequestChecksumCalculation 设为 WhenSupported，会把 body 以 aws-chunked + CRC32
// trailer 发送；阿里 OSS 与腾讯 COS 不实现这种编码，会直接拒绝请求。
//
// 降级成 WhenRequired 会去掉 trailer，但也会完全去掉客户端校验和，所以只对非 AWS
// endpoint 应用。真 AWS S3 今天接受 trailer，而且带默认 Object Lock 保留期的桶实际
// 要求有校验和，因此那些请求保持 SDK 原样构造——包括运维配置的
// AWS_REQUEST_CHECKSUM_CALCULATION。
func (s *S3Storage) uploadChecksumOptions() []func(*s3.Options) {
	if s.usesAWSEndpoint() {
		return nil
	}
	return []func(*s3.Options){func(opts *s3.Options) {
		opts.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	}}
}

// storageClass 返回合适的 S3 存储类。自定义端点（例如 MinIO）只支持 STANDARD；
// 真 AWS 默认 INTELLIGENT_TIERING。
func (s *S3Storage) storageClass() types.StorageClass {
	if s.endpointURL != "" {
		return types.StorageClassStandard
	}
	return types.StorageClassIntelligentTiering
}

// KeyFromURL 从 CDN 或桶 URL 里取出 S3 对象键。
// 例如 "https://multica-static.copilothub.ai/abc123.png" → "abc123.png"
//
//	"https://my-bucket.s3.us-east-1.amazonaws.com/uploads/x/y.png" → "uploads/x/y.png"
func (s *S3Storage) KeyFromURL(rawURL string) string {
	if s.endpointURL != "" {
		for _, prefix := range []string{
			customEndpointObjectPrefix(s.endpointURL, s.bucket, true),
			customEndpointObjectPrefix(s.endpointURL, s.bucket, false),
		} {
			if strings.HasPrefix(rawURL, prefix) {
				return strings.TrimPrefix(rawURL, prefix)
			}
		}
	}

	// 剥掉已知的 "https://host/" 前缀。顺序重要：更具体的带区域主机排前面，让它们
	// 赢过「只有桶名」的旧前缀（后者是后缀 bug 修复前写的）。
	prefixes := make([]string, 0, 5)
	if s.cdnDomain != "" {
		prefixes = append(prefixes, "https://"+s.cdnDomain+"/")
	}
	if s.region != "" {
		// virtual-hosted 风格：https://<bucket>.s3.<region>.amazonaws.com/<key>
		prefixes = append(prefixes,
			"https://"+s.bucket+".s3."+s.region+".amazonaws.com/",
			// path 风格：https://s3.<region>.amazonaws.com/<bucket>/<key>
			"https://s3."+s.region+".amazonaws.com/"+s.bucket+"/",
		)
	}
	// 旧版 / 兜底：旧的错误形态 "https://<bucket>/<key>"（老记录可能还留着），以及
	// 一个通用的「桶主机」前缀。
	prefixes = append(prefixes, "https://"+s.bucket+"/")

	for _, prefix := range prefixes {
		if strings.HasPrefix(rawURL, prefix) {
			return strings.TrimPrefix(rawURL, prefix)
		}
	}
	// 兜底：取最后一个 "/" 之后的一切。
	if i := strings.LastIndex(rawURL, "/"); i >= 0 {
		return rawURL[i+1:]
	}
	return rawURL
}

// GetReader 把对象 body 流式返回给调用方。返回的 ReadCloser 必须关闭；关闭它会终止到
// S3 的底层 HTTP 连接。key 缺失会以包在 SDK smithy 包装里的 *types.NoSuchKey 错误形式
// 出现——调用方可用 errors.As 区分「不存在」与传输失败。
func (s *S3Storage) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == "" {
		return nil, fmt.Errorf("s3 GetReader: empty key")
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 GetObject: %w", err)
	}
	return out.Body, nil
}

// PresignGet 为 GET 生成带有效期的预签名 URL。
func (s *S3Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return s.PresignGetWithContentDisposition(ctx, key, ttl, "")
}

// PresignGetWithContentDisposition 为 GET 生成预签名 URL，可带 Content-Disposition
// 响应头（例如强制下载名）。ttl 非正时默认 30 分钟。
func (s *S3Storage) PresignGetWithContentDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("s3 PresignGet: empty key")
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if contentDisposition != "" {
		input.ResponseContentDisposition = aws.String(contentDisposition)
	}
	out, err := s3.NewPresignClient(s.client).PresignGetObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("s3 PresignGetObject: %w", err)
	}
	return out.URL, nil
}

// Delete 从 S3 删除一个对象。错误记日志但不致命。
func (s *S3Storage) Delete(ctx context.Context, key string) {
	if err := s.DeleteObject(ctx, key); err != nil {
		slog.Error("s3 DeleteObject failed", "key", key, "error", err)
	}
}

// DeleteObject 是带错误上抛的 Delete——媒体对账器需要它来保留台账行并安排重试，而
// 不是想当然地认为成功。
func (s *S3Storage) DeleteObject(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

// ObjectURL 返回对 key 做一次成功的 Upload/UploadStream 会返回的 URL。它是配置的纯
// 函数，因此媒体意图台账可以在上传前就持久化该 URL，供持久引用检查用。
func (s *S3Storage) ObjectURL(key string) string {
	return s.uploadedURL(key)
}

// DeleteKeys 从 S3 删除多个对象。尽力而为，错误记日志。
func (s *S3Storage) DeleteKeys(ctx context.Context, keys []string) {
	for _, key := range keys {
		s.Delete(ctx, key)
	}
}

// Upload 把内存中的字节上传成对象，返回客户端消费用的 URL。
func (s *S3Storage) Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error) {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:             aws.String(s.bucket),
		Key:                aws.String(key),
		Body:               bytes.NewReader(data),
		ContentType:        aws.String(contentType),
		ContentDisposition: aws.String(ContentDisposition(contentType, filename)),
		CacheControl:       aws.String("max-age=432000,public"),
		StorageClass:       s.storageClass(),
	}, s.uploadChecksumOptions()...)
	if err != nil {
		return "", fmt.Errorf("s3 PutObject: %w", err)
	}
	return s.uploadedURL(key), nil
}

// UploadStream 把流上传成对象。流不可重绕，所以必须显式给 Content-Length。
func (s *S3Storage) UploadStream(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string, filename string) (string, error) {
	if sizeBytes < 0 {
		return "", fmt.Errorf("s3 PutObject: content length is required for streaming upload")
	}
	input := &s3.PutObjectInput{
		Bucket:             aws.String(s.bucket),
		Key:                aws.String(key),
		Body:               data,
		ContentLength:      aws.Int64(sizeBytes),
		ContentType:        aws.String(contentType),
		ContentDisposition: aws.String(ContentDisposition(contentType, filename)),
		CacheControl:       aws.String("max-age=432000,public"),
		StorageClass:       s.storageClass(),
	}
	_, err := s.client.PutObject(ctx, input, func(opts *s3.Options) {
		// 不可 seek 的流无法为 SigV4 负载哈希回绕。S3 对已认证请求支持
		// UNSIGNED-PAYLOAD。可选的尾校验和路径同样要避开：它需要再一次回绕或
		// aws-chunked 框架，而 S3 兼容后端对其支持参差不齐。
		opts.APIOptions = append(opts.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
		opts.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	if err != nil {
		return "", fmt.Errorf("s3 PutObject: %w", err)
	}
	return s.uploadedURL(key), nil
}

// uploadedURL 返回上传后给客户端消费存的 URL。优先级：CDN 域名 > 自定义 endpoint >
// AWS S3 区域限定主机。即使配了自定义 endpoint，CDN 域名也胜出——这样 S3 兼容后端
// （MinIO、R2、B2、Wasabi 等）可以配一个独立的公共读域名：写入仍经 SDK 走自定义
// endpoint，只有面向读者的 URL 变了。
//
// 默认 AWS S3 场景优先 virtual-hosted 风格：https://<bucket>.s3.<region>.amazonaws.com/<key>。
// 当桶名含点号时，AWS 签发的通配 TLS 证书（`*.s3.amazonaws.com`）无法对那个主机完成
// 校验，于是回退到 path 风格：https://s3.<region>.amazonaws.com/<bucket>/<key>。
func (s *S3Storage) uploadedURL(key string) string {
	if s.cdnDomain != "" {
		return fmt.Sprintf("https://%s/%s", s.cdnDomain, key)
	}
	if s.endpointURL != "" {
		return customEndpointObjectURL(s.endpointURL, s.bucket, key, s.usePathStyle)
	}
	if s.usePathStyle || strings.Contains(s.bucket, ".") {
		return fmt.Sprintf("https://s3.%s.amazonaws.com/%s/%s", s.region, s.bucket, key)
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", s.bucket, s.region, key)
}

// customEndpointObjectURL 在自定义 endpoint 下拼出对象的访问 URL。
func customEndpointObjectURL(endpointURL, bucket, key string, usePathStyle bool) string {
	return customEndpointObjectPrefix(endpointURL, bucket, usePathStyle) + key
}

// customEndpointObjectPrefix 计算自定义 endpoint 下指向某桶的对象前缀：path-style 为
// "<endpoint>/<bucket>/"，否则把桶名作为虚拟主机子域："https://<bucket>.<host>/"。
func customEndpointObjectPrefix(endpointURL, bucket string, usePathStyle bool) string {
	trimmed := strings.TrimRight(endpointURL, "/")
	if usePathStyle {
		return trimmed + "/" + bucket + "/"
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return trimmed + "/"
	}
	u.Host = bucket + "." + u.Host
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
