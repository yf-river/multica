// plugin.go —— 插件生态的服务层核心：发布内容的安装/升级/配置/启停/卸载。
// 核心心智模型：「安装 = 对某个已发布版本的一次显式同意」。管理员批准的是发布时冻结的 manifest、
// 表面脚本与 skill 文本（版本绑定），新发布只是新增版本、不碰既有安装——升级是第二次显式同意。
// 本文件负责把配置按 schema 拆分（明文落安装行、secret 落加密表）、清理新 manifest 不再声明的孤儿
// 配置/密钥，并保证安装/升级/卸载各自的整组行要么全成要么全不成（库政策禁外键，靠同一事务兜底）。
package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// PluginService 拥有插件发布、安装、配置与插件自有状态的业务逻辑。
type PluginService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	// Secrets 负责加密 `secret` 类配置值。为 nil 时禁止 secret 写入——让配置错误的部署 fail-closed，
	// 而不是存明文。
	Secrets *secretbox.Box
	// LocalDir 是 MULTICA_PLUGIN_DIR：作者无需每次编辑后打包上传、可直接从中发布的插件源码目录。
	// 为空即禁用开发发布通道。
	LocalDir string
	// DevOrigins 是 MULTICA_PLUGIN_DEV_ORIGINS：作者开发插件时可以把 hook 端点指向的地址（通常
	// 是 localhost 服务）。只有显式选择加入的部署才非空——这正是对它们可以跳过公开 HTTPS 校验的
	// 唯一理由。
	DevOrigins []string
	// Host 门控本构建声明的能力里哪些真正能跑。
	Host plugincontract.Capabilities
	// DeploymentKey 是原始 MULTICA_PLUGIN_SECRET_KEY，用来派生每次安装的 hook 签名密钥。它与
	// Secrets 分开持有，因为签名需要一个可复现的密钥，而不是一个封好的盒子。
	DeploymentKey []byte
	// Callbacks 签发 hook handler 回调用的短时令牌。nil 表示 hook 发出时不带回调能力。
	Callbacks *CallbackTokens
	// CallbackBaseURL 是 hook handler 应调用的 Action API 根地址，与回调令牌一起发送——插件因此
	// 不用硬编码我们的主机名。
	CallbackBaseURL string
	// FeatureFlags 门控 hook 引擎。放在这里是因为 EVENT 路径没有可读 flag 的请求：它跑在 worker 上，
	// 所以检查必须随服务而非随 handler。nil 视为关闭——对会触达第三方的那条路径，关闭是安全方向。
	FeatureFlags *featureflag.Service
	// HookClient 覆盖出站客户端，且只对「运维已在 DevOrigins 点名的 origin」生效。其余场合一律为 nil，
	// 让公开端点总是走 SSRF 防护客户端，本字段不会放大部署可达的范围。
	HookClient *http.Client
}

// NewPluginService 从环境变量读取开发发布/开发 origin 配置，组装 PluginService。
func NewPluginService(queries *db.Queries, txStarter TxStarter) *PluginService {
	service := &PluginService{
		Queries:    queries,
		TxStarter:  txStarter,
		LocalDir:   strings.TrimSpace(os.Getenv("MULTICA_PLUGIN_DIR")),
		DevOrigins: parseDevOrigins(os.Getenv("MULTICA_PLUGIN_DEV_ORIGINS")),
		Host:       plugincontract.HostCapabilities(),
	}
	service.HookClient = devHookClient(service.DevOrigins, os.Getenv("MULTICA_PLUGIN_DEV_CA"))
	return service
}

// devHookClient 只对开发 origin 的 hook 端点额外信任一个 CA。
//
// hook URL 必须是 HTTPS（manifest 要求如此），所以作者要对本地服务做测试，就得让证书被某个东西
// 信任。这里选择「信任一个点名 CA 文件」而不是关掉校验：InsecureSkipVerify 的失败模式是它会跟着
// 一个被人忘掉的配置旗标活进生产环境，而且从代码上根本看不出它是否生效。按路径提供的 CA 不会
// 静默作用于任何别的东西。
//
// 只有 origin 白名单与 CA 路径都设置了才非 nil，且只会被白名单内的端点用到。
func devHookClient(devOrigins []string, caPath string) *http.Client {
	if len(devOrigins) == 0 || strings.TrimSpace(caPath) == "" {
		return nil
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		slog.Warn("plugins: MULTICA_PLUGIN_DEV_CA could not be read; dev hook endpoints will not be trusted", "path", caPath, "error", err)
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		slog.Warn("plugins: MULTICA_PLUGIN_DEV_CA contained no certificates", "path", caPath)
		return nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	slog.Info("plugins: dev hook endpoints will trust an additional CA", "path", caPath, "origins", devOrigins)
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		// 与安全客户端一致地拒绝重定向。开发端点 302 会把「已签名的 body」和回调令牌重放到它指向的
		// 任意地址——而重定向需要的正是这条路径唯一跳过的目的地检查。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("hook endpoints must not redirect")
		},
	}
}

// PluginErrorKind 是插件 API 错误的稳定分类，handler 据此映射 HTTP 状态。
type PluginErrorKind string

const (
	PluginErrorInvalid      PluginErrorKind = "invalid"
	PluginErrorNotFound     PluginErrorKind = "not_found"
	PluginErrorConflict     PluginErrorKind = "conflict"
	PluginErrorForbidden    PluginErrorKind = "forbidden"
	PluginErrorIncompatible PluginErrorKind = "incompatible"
	PluginErrorQuota        PluginErrorKind = "quota"
	PluginErrorUnavailable  PluginErrorKind = "unavailable"
)

// PluginError 是带分类的插件错误：Kind 供上层分流，Message 给人看，Err 保留底层原因（可 Unwrap）。
type PluginError struct {
	Kind    PluginErrorKind
	Message string
	Err     error
}

// Error 拼出给人看的错误文案：带底层原因则追加之。
func (e *PluginError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

// Unwrap 暴露底层错误，供 errors.Is/As 穿透。
func (e *PluginError) Unwrap() error { return e.Err }

// pluginErrf 按 kind + 格式化文案构造 *PluginError。
func pluginErrf(kind PluginErrorKind, format string, args ...any) *PluginError {
	return &PluginError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// PluginPreview 是授权（consent）界面要渲染的东西。它刻意就是「原始 manifest 文本 + 权限清单」：
// 本模型里没有签名、没有信任层级、没有发布者核验——读这份权限清单的管理员本身就是信任决策。
//
// 它描述的是某个已发布版本，而安装也绑定到同一版本——所以与被它取代的 URL 模型不同，在这里被批准
// 的代码，就是会运行的代码。
type PluginPreview struct {
	Manifest     plugincontract.Manifest `json:"manifest"`
	Scopes       []string                `json:"scopes"`
	ConfigSchema []PluginConfigField     `json:"config_schema"`
	VersionID    string                  `json:"version_id"`
	Version      string                  `json:"version"`
	Digest       string                  `json:"digest"`
	// Upgrade 描述若该安装替换既有安装会有什么变化。
	Installed        bool     `json:"installed"`
	InstalledVersion string   `json:"installed_version,omitempty"`
	AddedScopes      []string `json:"added_scopes,omitempty"`
}

// PluginConfigField 是单个由宿主渲染的配置输入项。
type PluginConfigField struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Multiline   bool     `json:"multiline,omitempty"`
}

// ConfigFieldsForManifest 把 manifest 配置 schema 压平成声明顺序的输入项，让客户端渲染的表单与服务端
// 校验用的是同一张表。
func ConfigFieldsForManifest(manifest plugincontract.Manifest) []PluginConfigField {
	fields := make([]PluginConfigField, 0, manifest.Config.Len())
	for _, field := range manifest.Config.Fields {
		fields = append(fields, PluginConfigField{
			Key:         field.Key,
			Type:        field.Type,
			Label:       field.Label,
			Description: field.Description,
			Required:    field.Required,
			Options:     field.Options,
			Placeholder: field.Placeholder,
			Multiline:   field.Multiline,
		})
	}
	return fields
}

// ManifestForVersion 解码某个已发布版本在发布时冻结的 manifest。
//
// 不做任何网络获取。manifest、表面脚本与 skill 文本在作者发布时一起校验过，读回时也是一起读——
// 安装不会出现「同意的是某版本的 manifest，跑的是另一版本代码」的情形。
func ManifestForVersion(version db.PluginPackageVersion) (plugincontract.Manifest, []byte, error) {
	var manifest plugincontract.Manifest
	if err := json.Unmarshal(version.Manifest, &manifest); err != nil {
		return plugincontract.Manifest{}, nil, &PluginError{Kind: PluginErrorInvalid, Message: "published plugin manifest is unreadable", Err: err}
	}
	return manifest, version.Manifest, nil
}

// parseDevOrigins 解析选择加入的 origin 列表。凡不是裸 origin 的一律丢弃而不是半承认：这里的 path 或
// query 会被读成比实际更宽的授权。
func parseDevOrigins(raw string) []string {
	origins := make([]string, 0)
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimRight(strings.TrimSpace(candidate), "/")
		if candidate == "" {
			continue
		}
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" {
			continue
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			continue
		}
		origins = append(origins, parsed.Scheme+"://"+parsed.Host)
	}
	return origins
}

// isDevOrigin 判断给定 URL 的 origin 是否在开发白名单里。
func (s *PluginService) isDevOrigin(sourceURL string) bool {
	if len(s.DevOrigins) == 0 {
		return false
	}
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	origin := parsed.Scheme + "://" + parsed.Host
	for _, allowed := range s.DevOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

// PreviewPlugin 只读某个已发布版本的 manifest，不写任何东西。它是两步安装的前一半：在安装行
// 存在之前，管理员必须看到权限清单。
func (s *PluginService) PreviewPlugin(ctx context.Context, workspaceID pgtype.UUID, versionID string) (*PluginPreview, error) {
	version, err := s.VersionForWorkspace(ctx, workspaceID, versionID)
	if err != nil {
		return nil, err
	}
	manifest, _, err := ManifestForVersion(version)
	if err != nil {
		return nil, err
	}
	if err := manifest.CheckCapabilities(s.Host); err != nil {
		return nil, &PluginError{Kind: PluginErrorIncompatible, Message: capabilityMessage(err), Err: err}
	}

	preview := &PluginPreview{
		Manifest:     manifest,
		Scopes:       manifest.Scopes,
		ConfigSchema: ConfigFieldsForManifest(manifest),
		VersionID:    uuidString(version.ID),
		Version:      version.Version,
		Digest:       version.Digest,
	}
	existing, err := s.Queries.GetWorkspacePluginInstallationByKey(ctx, db.GetWorkspacePluginInstallationByKeyParams{
		WorkspaceID: workspaceID,
		PluginKey:   manifest.Key,
	})
	if err == nil {
		preview.Installed = true
		preview.InstalledVersion = existing.Version
		preview.AddedScopes = addedScopes(decodeScopes(existing.GrantedScopes), manifest.Scopes)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "load existing installation", Err: err}
	}
	return preview, nil
}

// capabilityMessage 把能力校验错误整理成给人看的文案。
func capabilityMessage(err error) string {
	var unavailable *plugincontract.ErrCapabilityUnavailable
	if errors.As(err, &unavailable) {
		return "This plugin declares capabilities that are not enabled yet: " + strings.Join(unavailable.Missing, ", ")
	}
	return "This plugin declares capabilities that are not enabled yet"
}

// addedScopes 求 wanted 里尚未 granted 的权限清单，供预览提示「升级后会新增这些权限」。
func addedScopes(granted []string, wanted []string) []string {
	have := make(map[string]bool, len(granted))
	for _, scope := range granted {
		have[scope] = true
	}
	added := make([]string, 0)
	for _, scope := range wanted {
		if !have[scope] {
			added = append(added, scope)
		}
	}
	return added
}

// decodeScopes 解析安装行里持久化的权限 JSON；空/坏数据按 nil 处理。
func decodeScopes(raw []byte) []string {
	var scopes []string
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &scopes); err != nil {
		return nil
	}
	return scopes
}

// InstallPlugin 是安装的第二步。grantedScopes 必须与 manifest 完全一致：部分同意会让插件静默残缺，
// 而同意 manifest 未请求的权限等于授予管理员从未见过理由的访问。
//
// 安装绑定到某个已发布版本。之后的发布只会产生新版本、不碰本行——升级是第二次显式同意，这正让
// 「批准了 manifest、跑的就是这份代码」成为一句真话而不是愿望。
func (s *PluginService) InstallPlugin(ctx context.Context, workspaceID, userID pgtype.UUID, versionID string, grantedScopes []string) (db.PluginInstallation, error) {
	version, err := s.VersionForWorkspace(ctx, workspaceID, versionID)
	if err != nil {
		return db.PluginInstallation{}, err
	}
	manifest, canonical, err := ManifestForVersion(version)
	if err != nil {
		return db.PluginInstallation{}, err
	}
	if err := manifest.CheckCapabilities(s.Host); err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorIncompatible, Message: capabilityMessage(err), Err: err}
	}
	if err := requireExactScopes(manifest.Scopes, grantedScopes); err != nil {
		return db.PluginInstallation{}, err
	}
	scopesJSON, err := json.Marshal(manifest.Scopes)
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorInvalid, Message: "encode granted scopes", Err: err}
	}

	existing, err := s.Queries.GetWorkspacePluginInstallationByKey(ctx, db.GetWorkspacePluginInstallationByKeyParams{
		WorkspaceID: workspaceID,
		PluginKey:   manifest.Key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 虽然只插一行，仍走事务：skill 资源要在同一提交里落库。skill 只到一半的安装比失败还糟——
		// 缺失的那一半是看不见的。
		tx, txErr := s.TxStarter.Begin(ctx)
		if txErr != nil {
			return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "begin install", Err: txErr}
		}
		defer func() { _ = tx.Rollback(ctx) }()
		queries := s.Queries.WithTx(tx)

		if lockErr := lockPluginPackageKey(ctx, queries, workspaceID, manifest.Key); lockErr != nil {
			return db.PluginInstallation{}, lockErr
		}
		if recheckErr := requireVersionStillPublished(ctx, queries, workspaceID, version.ID); recheckErr != nil {
			return db.PluginInstallation{}, recheckErr
		}

		installation, createErr := queries.CreatePluginInstallation(ctx, db.CreatePluginInstallationParams{
			WorkspaceID:      workspaceID,
			PluginKey:        manifest.Key,
			PackageVersionID: version.ID,
			// 用已发布的版本字符串，而非 manifest 里的：开发发布会带上 `+dev.N` 后缀，设置页必须点名
			// 真正存在的那个版本。
			Version:       version.Version,
			Manifest:      canonical,
			GrantedScopes: scopesJSON,
			InstalledBy:   userID,
		})
		if createErr != nil {
			// 两个管理员同时装同一个插件：必有一个输给唯一索引。这是可重试的冲突，不是后端坏了。
			if isUniqueViolation(createErr) {
				return db.PluginInstallation{}, pluginErrf(PluginErrorConflict, "this plugin is already installed in this workspace")
			}
			return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "install plugin", Err: createErr}
		}
		if skillErr := s.InstallSkillResources(ctx, queries, installation, manifest, userID); skillErr != nil {
			return db.PluginInstallation{}, skillErr
		}
		if scheduleErr := s.reconcilePluginHookSchedules(ctx, queries, installation, manifest); scheduleErr != nil {
			return db.PluginInstallation{}, scheduleErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "commit install", Err: commitErr}
		}
		return installation, nil
	}
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "load existing installation", Err: err}
	}

	// 原地升级。新 manifest 丢弃的字段，其值会被裁剪而非留成任何东西都够不到的状态——secret 也
	// 一样，而那种情况下不可达的残留正是密文。整个升级是一个事务，快照永远不会带着上一版本的
	// secret 落地。
	config := pruneConfig(existing.Config, manifest)
	orphanedSecrets, err := s.orphanedSecretKeys(ctx, existing.ID, manifest)
	if err != nil {
		return db.PluginInstallation{}, err
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "begin upgrade", Err: err}
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)

	if err := lockPluginPackageKey(ctx, queries, workspaceID, manifest.Key); err != nil {
		return db.PluginInstallation{}, err
	}
	if err := requireVersionStillPublished(ctx, queries, workspaceID, version.ID); err != nil {
		return db.PluginInstallation{}, err
	}

	for _, key := range orphanedSecrets {
		if _, err := queries.DeletePluginSecret(ctx, db.DeletePluginSecretParams{InstallationID: existing.ID, Key: key}); err != nil {
			return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "prune plugin secret", Err: err}
		}
	}
	updated, err := queries.UpdatePluginInstallationManifest(ctx, db.UpdatePluginInstallationManifestParams{
		ID:               existing.ID,
		PackageVersionID: version.ID,
		Version:          version.Version,
		Manifest:         canonical,
		GrantedScopes:    scopesJSON,
		Config:           config,
	})
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "upgrade plugin", Err: err}
	}
	// 升级时重跑：改过的 SKILL.md 要生效、被删掉的要剪掉。与 manifest 快照同一事务——两者绝不能
	// 在这个版本贡献了什么上互相矛盾。
	if err := s.InstallSkillResources(ctx, queries, updated, manifest, userID); err != nil {
		return db.PluginInstallation{}, err
	}
	if err := s.reconcilePluginHookSchedules(ctx, queries, updated, manifest); err != nil {
		return db.PluginInstallation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "commit upgrade", Err: err}
	}
	return updated, nil
}

// orphanedSecretKeys 返回「库里有、但新 manifest 不再声明为 secret 字段」的 secret——含从 secret
// 改型为其他类型的字段。
func (s *PluginService) orphanedSecretKeys(ctx context.Context, installationID pgtype.UUID, manifest plugincontract.Manifest) ([]string, error) {
	stored, err := s.Queries.ListPluginSecretKeys(ctx, installationID)
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "list plugin secrets", Err: err}
	}
	orphaned := make([]string, 0)
	for _, row := range stored {
		field, ok := manifest.Config.Field(row.Key)
		if !ok || field.Type != plugincontract.ConfigSecret {
			orphaned = append(orphaned, row.Key)
		}
	}
	return orphaned, nil
}

// requireExactScopes 校验授予的权限与 manifest 声明逐项相等——多给少给都是拒绝。
func requireExactScopes(manifestScopes, grantedScopes []string) error {
	if len(manifestScopes) != len(grantedScopes) {
		return pluginErrf(PluginErrorConflict, "granted_scopes must match the manifest scopes exactly")
	}
	wanted := make(map[string]bool, len(manifestScopes))
	for _, scope := range manifestScopes {
		wanted[scope] = true
	}
	for _, scope := range grantedScopes {
		if !wanted[scope] {
			return pluginErrf(PluginErrorConflict, "granted_scopes contains %q, which the manifest does not request", scope)
		}
	}
	return nil
}

// pruneConfig 从既有配置里删掉新 manifest 已不声明的键；空/坏数据按空配置处理。
func pruneConfig(raw []byte, manifest plugincontract.Manifest) []byte {
	var values map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil {
		return []byte("{}")
	}
	for key := range values {
		if _, ok := manifest.Config.Field(key); !ok {
			delete(values, key)
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// parseUUIDValue 解析字符串为 UUID（转发到 util.ParseUUID）。
func parseUUIDValue(value string) (pgtype.UUID, error) { return util.ParseUUID(value) }

// uuidString 把 UUID 转字符串。
func uuidString(value pgtype.UUID) string { return util.UUIDToString(value) }

// ParseInstallationManifest 读回被同意的快照。调用方必须用它、绝不用新取回的 manifest：来源 URL 今天
// 提供的内容，不是管理员批准过的内容。
func ParseInstallationManifest(installation db.PluginInstallation) (plugincontract.Manifest, error) {
	var manifest plugincontract.Manifest
	if err := json.Unmarshal(installation.Manifest, &manifest); err != nil {
		return plugincontract.Manifest{}, fmt.Errorf("decode stored plugin manifest: %w", err)
	}
	return manifest, nil
}

// SetConfig 对照存储的 manifest schema 校验值，再按去向拆分：明文值落到安装行，secret 值进加密表。
// secret 值永远不会进入 `config`。
func (s *PluginService) SetConfig(ctx context.Context, installation db.PluginInstallation, values map[string]any) (db.PluginInstallation, error) {
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorInvalid, Message: "stored plugin manifest is unreadable", Err: err}
	}

	plain := map[string]any{}
	secrets := map[string]string{}
	for key, value := range values {
		field, ok := manifest.Config.Field(key)
		if !ok {
			return db.PluginInstallation{}, pluginErrf(PluginErrorInvalid, "unknown config field %q", key)
		}
		if field.Type == plugincontract.ConfigSecret {
			text, ok := value.(string)
			if !ok {
				return db.PluginInstallation{}, pluginErrf(PluginErrorInvalid, "config field %q must be a string", key)
			}
			if len(text) > MaxPluginSecretBytes {
				return db.PluginInstallation{}, pluginErrf(PluginErrorInvalid, "config field %q exceeds %d bytes", key, MaxPluginSecretBytes)
			}
			secrets[key] = text
			continue
		}
		normalized, err := normalizeConfigValue(field, value)
		if err != nil {
			return db.PluginInstallation{}, err
		}
		plain[key] = normalized
	}

	// 在存量值之上合并，让部分更新不会静默清掉表单没提交的字段。
	merged := map[string]any{}
	if len(installation.Config) > 0 {
		_ = json.Unmarshal(installation.Config, &merged)
	}
	for key, value := range plain {
		merged[key] = value
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorInvalid, Message: "encode plugin config", Err: err}
	}

	if len(secrets) > 0 && s.Secrets == nil {
		return db.PluginInstallation{}, pluginErrf(PluginErrorUnavailable, "plugin secrets are disabled: MULTICA_PLUGIN_SECRET_KEY is not configured")
	}

	// secret 与明文值是两张表、彼此没有外键，所以一个事务才能保证保存的表单不会只落地一半。
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "begin configure", Err: err}
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)

	for key, text := range secrets {
		// 空提交 = 清除该 secret，而不是存空串。
		if text == "" {
			if _, err := queries.DeletePluginSecret(ctx, db.DeletePluginSecretParams{InstallationID: installation.ID, Key: key}); err != nil {
				return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "clear plugin secret", Err: err}
			}
			continue
		}
		ciphertext, sealErr := s.Secrets.Seal([]byte(text))
		if sealErr != nil {
			return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "encrypt plugin secret", Err: sealErr}
		}
		if err := queries.UpsertPluginSecret(ctx, db.UpsertPluginSecretParams{
			InstallationID: installation.ID,
			Key:            key,
			Ciphertext:     ciphertext,
		}); err != nil {
			return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "store plugin secret", Err: err}
		}
	}

	updated, err := queries.UpdatePluginInstallationConfig(ctx, db.UpdatePluginInstallationConfigParams{
		ID:     installation.ID,
		Config: encoded,
	})
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "store plugin config", Err: err}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "commit configure", Err: err}
	}
	return updated, nil
}

// isUniqueViolation 识别 Postgres 23505——插件面唯一可能产生的写冲突类别。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// MaxPluginSecretBytes 是加密前单个 secret 值的字节上限。
const MaxPluginSecretBytes = 8192

// normalizeConfigValue 按字段类型归一化并校验单个配置值（string 上限 4096 字节、number/bool 验类型、
// enum 必须在声明选项中）。类型不符即返回带字段名的错误。
func normalizeConfigValue(field plugincontract.ConfigField, value any) (any, error) {
	label := fmt.Sprintf("config field %q", field.Key)
	switch field.Type {
	case plugincontract.ConfigString:
		text, ok := value.(string)
		if !ok {
			return nil, pluginErrf(PluginErrorInvalid, "%s must be a string", label)
		}
		if len(text) > 4096 {
			return nil, pluginErrf(PluginErrorInvalid, "%s exceeds 4096 bytes", label)
		}
		return text, nil
	case plugincontract.ConfigNumber:
		number, ok := value.(float64)
		if !ok {
			return nil, pluginErrf(PluginErrorInvalid, "%s must be a number", label)
		}
		return number, nil
	case plugincontract.ConfigBool:
		flag, ok := value.(bool)
		if !ok {
			return nil, pluginErrf(PluginErrorInvalid, "%s must be a boolean", label)
		}
		return flag, nil
	case plugincontract.ConfigEnum:
		text, ok := value.(string)
		if !ok {
			return nil, pluginErrf(PluginErrorInvalid, "%s must be a string", label)
		}
		for _, option := range field.Options {
			if option == text {
				return text, nil
			}
		}
		return nil, pluginErrf(PluginErrorInvalid, "%s must be one of the declared options", label)
	default:
		return nil, pluginErrf(PluginErrorInvalid, "%s has an unsupported type", label)
	}
}

// ConfiguredSecretKeys 报告哪些 secret 字段已持有值。只回名字——任何端点都不会返回 secret 值，连
// 掩码都不返回。
func (s *PluginService) ConfiguredSecretKeys(ctx context.Context, installationID pgtype.UUID) ([]string, error) {
	rows, err := s.Queries.ListPluginSecretKeys(ctx, installationID)
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "list plugin secrets", Err: err}
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.Key)
	}
	return keys, nil
}

// SetEnabled 切换安装的启用状态。禁用会立刻隐藏所有贡献，但保留存储与 secret——重新启用不是重装。
func (s *PluginService) SetEnabled(ctx context.Context, installation db.PluginInstallation, enabled bool) (db.PluginInstallation, error) {
	if installation.Enabled == enabled {
		return installation, nil
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "begin plugin state update", Err: err}
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)

	updated, err := queries.SetPluginInstallationEnabled(ctx, db.SetPluginInstallationEnabledParams{
		ID:      installation.ID,
		Enabled: enabled,
	})
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "update plugin state", Err: err}
	}
	if err := s.setPluginHookSchedulesEnabled(ctx, queries, installation.ID, enabled); err != nil {
		return db.PluginInstallation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "commit plugin state update", Err: err}
	}
	return updated, nil
}

// Uninstall 删除安装及其全部应用自有行。按仓库政策没有外键与级联，所以这些删除共享一个事务：
// 半途卸载会留下任何东西都够不到、将来也清不掉的行。
func (s *PluginService) Uninstall(ctx context.Context, installation db.PluginInstallation) error {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "begin uninstall", Err: err}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := s.Queries.WithTx(tx)
	if err := queries.DeletePluginHookSchedulesByInstallation(ctx, installation.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin hook schedules", Err: err}
	}
	if err := queries.DeletePluginStorageByInstallation(ctx, installation.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin storage", Err: err}
	}
	if err := queries.DeletePluginSecretsByInstallation(ctx, installation.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin secrets", Err: err}
	}
	// hook 记录一并删。它们点名一个即将消失的安装，留着只会留下无法归属的行——与这里就清掉 storage
	// 和 secret、而不是等以后清扫是同一个理由。
	if err := queries.DeletePluginInvocationsByInstallation(ctx, installation.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin invocations", Err: err}
	}
	// 本安装贡献的 skill 也随它走。按 plugin_installation_id 限定——人写的 skill 即使恰好与插件曾经
	// 提供的重名也不受影响。
	if err := queries.DeletePluginSkillsByInstallation(ctx, installation.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin skills", Err: err}
	}
	if err := queries.DeletePluginInstallation(ctx, installation.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin installation", Err: err}
	}
	if err := tx.Commit(ctx); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "commit uninstall", Err: err}
	}
	return nil
}

// InstallationForWorkspace 加载安装并确认它属于 URL 里的工作区——来自其他工作区的 id 因此无法被操作。
func (s *PluginService) InstallationForWorkspace(ctx context.Context, workspaceID pgtype.UUID, installationID string) (db.PluginInstallation, error) {
	parsed, err := util.ParseUUID(installationID)
	if err != nil {
		return db.PluginInstallation{}, pluginErrf(PluginErrorNotFound, "plugin installation not found")
	}
	installation, err := s.Queries.GetWorkspacePluginInstallation(ctx, db.GetWorkspacePluginInstallationParams{
		WorkspaceID: workspaceID,
		ID:          parsed,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PluginInstallation{}, pluginErrf(PluginErrorNotFound, "plugin installation not found")
	}
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "load plugin installation", Err: err}
	}
	return installation, nil
}
