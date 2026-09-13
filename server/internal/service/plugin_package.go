// plugin_package.go —— 插件的「发布」半区：作者上传制品包（或从本地开发目录发布），Multica 存下来并
// 供读取。职责：发布 = 在 (workspace, plugin key) 锁与同一事务里写「package 行 + version 行 + 全部文件」；
// 已发布版本不可变（再发会另建版本，已装工作区不受影响）；本文件还负责版本列表/包列表/包删除、
// surface 脚本读取与安装时确认版本仍存在的锁内复检。hook 端点与 MCP server 仍跑在作者自己的基础设施上，
// 我们不托管第三方后端。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// 发布模型说明：作者上传一个制品包；Multica 存储并提供它。没有来源 URL，也没有第二条进入通道——
// 管理员批准的东西与读者浏览器跑的东西，是这同一数据库里的同一批行。
//
// 已发布的版本不可变。再次发布会生成另一个版本，对已安装旧版的工作区毫无影响——升级是管理员
// 的动作，这正是「发布」与「升级」被拆成两件事的全部原因。
//
// 只有前端制品搬到了这里。hook 端点与 MCP server 仍住在作者自己的基础设施上；我们不托管第三方后端。

// PluginPackageVersionSummary 是设置页列表里呈现的一个已发布版本。
type PluginPackageVersionSummary struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
	SizeBytes   int64  `json:"size_bytes"`
	PublishedAt string `json:"published_at"`
	// Installed 标记当前工作区正在跑的版本——「我在哪个版本上」不用对照两张表就能答出。
	Installed bool `json:"installed"`
}

// PluginPackageSummary 是一个可发布的插件身份及其各版本（最新在前）。
type PluginPackageSummary struct {
	ID        string                        `json:"id"`
	PluginKey string                        `json:"plugin_key"`
	Name      string                        `json:"name"`
	Versions  []PluginPackageVersionSummary `json:"versions"`
	CreatedAt string                        `json:"created_at"`
}

// pluginTimeFormat 是列表里时间字段的展示格式（RFC3339）。
const pluginTimeFormat = "2006-01-02T15:04:05Z07:00"

// PublishBundle 把上传的制品包存成新的不可变版本。
func (s *PluginService) PublishBundle(ctx context.Context, workspaceID, userID pgtype.UUID, archive []byte) (PluginPackageSummary, error) {
	bundle, err := plugincontract.ParseBundle(archive)
	if err != nil {
		// 包裹文本点名出错的文件或字段，且是写给作者的。一次作者无法据此行动的发布失败，会把他们
		// 推回瞎猜——那正是整条路径要消除的失败模式。
		return PluginPackageSummary{}, pluginErrf(PluginErrorInvalid, "plugin package is invalid: %v", err)
	}
	return s.publish(ctx, workspaceID, userID, bundle, false)
}

// PublishLocalBundle 从 MULTICA_PLUGIN_DIR 发布——开发通道，让正在迭代 surface 的作者不用每次编辑
// 都打包上传。
//
// 它产出的是普通不可变版本，而非「面板在渲染时实时读取的活目录」。第二条渲染路径意味着「代码冻结
// 了吗」取决于插件怎么装的——正是这次改动要去掉的歧义。开发循环会不停地重发同一个未变版本字符串，
// 所以这种发布加 `+dev.N` 构建后缀而不是报冲突。
func (s *PluginService) PublishLocalBundle(ctx context.Context, workspaceID, userID pgtype.UUID, name string) (PluginPackageSummary, error) {
	if s.LocalDir == "" {
		return PluginPackageSummary{}, pluginErrf(PluginErrorInvalid, "local plugin sources require MULTICA_PLUGIN_DIR")
	}
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return PluginPackageSummary{}, pluginErrf(PluginErrorInvalid, "local plugin source must be a single directory name under MULTICA_PLUGIN_DIR")
	}
	bundle, err := plugincontract.ParseBundleFromDir(func(entry string) ([]byte, bool, error) {
		content, readErr := s.readLocalFile(name, entry)
		var pluginErr *PluginError
		if errors.As(readErr, &pluginErr) && pluginErr.Kind == PluginErrorNotFound {
			return nil, false, nil
		}
		if readErr != nil {
			return nil, false, readErr
		}
		return content, true, nil
	})
	if err != nil {
		return PluginPackageSummary{}, pluginErrf(PluginErrorInvalid, "local plugin package is invalid: %v", err)
	}
	return s.publish(ctx, workspaceID, userID, bundle, true)
}

// publish 在 (workspace, plugin key) 锁下、以「一个事务」写入 package 行、version 与它的文件。
//
// 三部分同事务是同一个理由：任何部分结果都是版本列表无法圆回来的谎。文件只落一半的版本是加载
// 不出东西的面板；发布冲突时把显示名改掉了的 package 行，声称描述一个从未落库的版本；首次发布在
// 建出 package 之后失败，则留下一个列表里零版本的插件。
func (s *PluginService) publish(ctx context.Context, workspaceID, userID pgtype.UUID, bundle plugincontract.Bundle, devLoop bool) (PluginPackageSummary, error) {
	if err := bundle.Manifest.CheckCapabilities(s.Host); err != nil {
		return PluginPackageSummary{}, &PluginError{Kind: PluginErrorIncompatible, Message: capabilityMessage(err), Err: err}
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return PluginPackageSummary{}, &PluginError{Kind: PluginErrorUnavailable, Message: "begin publish", Err: err}
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)

	if err := lockPluginPackageKey(ctx, queries, workspaceID, bundle.Manifest.Key); err != nil {
		return PluginPackageSummary{}, err
	}
	pkg, err := upsertPackage(ctx, queries, workspaceID, userID, bundle.Manifest)
	if err != nil {
		return PluginPackageSummary{}, err
	}
	existing, err := queries.ListPluginPackageVersions(ctx, pkg.ID)
	if err != nil {
		return PluginPackageSummary{}, &PluginError{Kind: PluginErrorUnavailable, Message: "list published versions", Err: err}
	}
	version, err := resolvePublishVersion(bundle.Manifest.Version, existing, devLoop)
	if err != nil {
		return PluginPackageSummary{}, err
	}

	row, err := queries.CreatePluginPackageVersion(ctx, db.CreatePluginPackageVersionParams{
		PackageID:   pkg.ID,
		WorkspaceID: workspaceID,
		Version:     version,
		Manifest:    bundle.Canonical,
		Digest:      BundleDigest(bundle),
		SizeBytes:   int64(bundle.TotalSize()),
		PublishedBy: userID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return PluginPackageSummary{}, pluginErrf(PluginErrorConflict, "version %s of this plugin is already published; published versions are immutable, so publish a new version instead", version)
		}
		return PluginPackageSummary{}, &PluginError{Kind: PluginErrorUnavailable, Message: "publish plugin version", Err: err}
	}
	for _, file := range bundle.Files {
		sum := sha256.Sum256(file.Content)
		if err := queries.CreatePluginPackageFile(ctx, db.CreatePluginPackageFileParams{
			VersionID: row.ID,
			Path:      file.Path,
			Content:   file.Content,
			SizeBytes: int64(len(file.Content)),
			Sha256:    hex.EncodeToString(sum[:]),
		}); err != nil {
			return PluginPackageSummary{}, &PluginError{Kind: PluginErrorUnavailable, Message: "store plugin package file", Err: err}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PluginPackageSummary{}, &PluginError{Kind: PluginErrorUnavailable, Message: "commit publish", Err: err}
	}
	return s.packageSummary(ctx, workspaceID, pkg)
}

// BundleDigest 对包内容做哈希：规范 manifest，然后按排序顺序哈希每个文件的路径与字节。
//
// 刻意不是对上传归档本身哈希。两份文件完全相同的 zip 会在时间戳与压缩方式上不同；摘要要回答的是
// 「我们看的是不是同一个插件」，而非「这是不是同一次上传」。
func BundleDigest(bundle plugincontract.Bundle) string {
	hash := sha256.New()
	hash.Write(bundle.Canonical)
	for _, file := range bundle.Files {
		fmt.Fprintf(hash, "\n%s\n%d\n", file.Path, len(file.Content))
		hash.Write(file.Content)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// resolvePublishVersion 决定这次发布落在哪个版本字符串下。
//
// 上传发布保留 manifest 的版本，若被占用就响亮地冲突——那个冲突本身就是不可变保证：想改点什么的
// 作者需要在 manifest 里说出来。本地开发发布改取 `+dev.N`——编辑文件然后刷新，正是「重发同一个未变
// 版本」的样子。
func resolvePublishVersion(version string, existing []db.PluginPackageVersion, devLoop bool) (string, error) {
	if !devLoop {
		return version, nil
	}
	taken := map[string]bool{}
	highest := 0
	for _, row := range existing {
		taken[row.Version] = true
		if suffix, ok := strings.CutPrefix(row.Version, version+"+dev."); ok {
			if number, err := strconv.Atoi(suffix); err == nil && number > highest {
				highest = number
			}
		}
	}
	if !taken[version] {
		return version, nil
	}
	candidate := version + "+dev." + strconv.Itoa(highest+1)
	if len(candidate) > plugincontract.MaxVersionLength {
		return "", pluginErrf(PluginErrorInvalid, "version %q leaves no room for a development suffix; shorten it below %d bytes", version, plugincontract.MaxVersionLength)
	}
	return candidate, nil
}

// lockPluginPackageKey 拿「事务作用域」的锁，串行化同一插件上的发布、安装与删除。调用方必须在事务里
// ——锁随其提交或回滚释放。
func lockPluginPackageKey(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID, pluginKey string) error {
	if err := queries.LockPluginPackageKey(ctx, uuidString(workspaceID)+":"+pluginKey); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "lock plugin package", Err: err}
	}
	return nil
}

// upsertPackage 接收 `queries` 而非读 s.Queries，从而跑在调用方事务内：显示名必须只在「承载它的那个
// 版本真的被存下」时才跟着动。
func upsertPackage(ctx context.Context, queries *db.Queries, workspaceID, userID pgtype.UUID, manifest plugincontract.Manifest) (db.PluginPackage, error) {
	existing, err := queries.GetWorkspacePluginPackageByKey(ctx, db.GetWorkspacePluginPackageByKeyParams{
		WorkspaceID: workspaceID,
		PluginKey:   manifest.Key,
	})
	if err == nil {
		if existing.Name == manifest.Name {
			return existing, nil
		}
		// 显示名跟随最新发布；key 不移动。
		updated, updateErr := queries.UpdatePluginPackageName(ctx, db.UpdatePluginPackageNameParams{
			ID:   existing.ID,
			Name: manifest.Name,
		})
		if updateErr != nil {
			return db.PluginPackage{}, &PluginError{Kind: PluginErrorUnavailable, Message: "update plugin package", Err: updateErr}
		}
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.PluginPackage{}, &PluginError{Kind: PluginErrorUnavailable, Message: "load plugin package", Err: err}
	}
	created, err := queries.CreatePluginPackage(ctx, db.CreatePluginPackageParams{
		WorkspaceID: workspaceID,
		PluginKey:   manifest.Key,
		Name:        manifest.Name,
		CreatedBy:   userID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			// 锁背后的双保险：与另一个尚未拿锁、跨进程发布的发布赛跑时，仍会在索引上干净地输掉。
			return db.PluginPackage{}, pluginErrf(PluginErrorConflict, "this plugin was just published by someone else; try again")
		}
		return db.PluginPackage{}, &PluginError{Kind: PluginErrorUnavailable, Message: "create plugin package", Err: err}
	}
	return created, nil
}

// ListPackages 返回本工作区发布的一切及其各版本。
func (s *PluginService) ListPackages(ctx context.Context, workspaceID pgtype.UUID) ([]PluginPackageSummary, error) {
	packages, err := s.Queries.ListWorkspacePluginPackages(ctx, workspaceID)
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "list plugin packages", Err: err}
	}
	summaries := make([]PluginPackageSummary, 0, len(packages))
	for _, pkg := range packages {
		summary, err := s.packageSummary(ctx, workspaceID, pkg)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (s *PluginService) packageSummary(ctx context.Context, workspaceID pgtype.UUID, pkg db.PluginPackage) (PluginPackageSummary, error) {
	versions, err := s.Queries.ListPluginPackageVersions(ctx, pkg.ID)
	if err != nil {
		return PluginPackageSummary{}, &PluginError{Kind: PluginErrorUnavailable, Message: "list published versions", Err: err}
	}
	// 该工作区正在跑的版本（若有）。从安装行读、而非「最新那个」：两者不一致正是发布后的常态，
	// 藏起这一点就违背了初衷。
	installedVersionID := ""
	installation, err := s.Queries.GetWorkspacePluginInstallationByKey(ctx, db.GetWorkspacePluginInstallationByKeyParams{
		WorkspaceID: workspaceID,
		PluginKey:   pkg.PluginKey,
	})
	if err == nil {
		installedVersionID = uuidString(installation.PackageVersionID)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return PluginPackageSummary{}, &PluginError{Kind: PluginErrorUnavailable, Message: "load plugin installation", Err: err}
	}

	rendered := make([]PluginPackageVersionSummary, 0, len(versions))
	for _, version := range versions {
		id := uuidString(version.ID)
		rendered = append(rendered, PluginPackageVersionSummary{
			ID:          id,
			Version:     version.Version,
			Digest:      version.Digest,
			SizeBytes:   version.SizeBytes,
			PublishedAt: version.CreatedAt.Time.UTC().Format(pluginTimeFormat),
			Installed:   id != "" && id == installedVersionID,
		})
	}
	return PluginPackageSummary{
		ID:        uuidString(pkg.ID),
		PluginKey: pkg.PluginKey,
		Name:      pkg.Name,
		Versions:  rendered,
		CreatedAt: pkg.CreatedAt.Time.UTC().Format(pluginTimeFormat),
	}, nil
}

// DeletePackage 删除一个包及其发布过的每个版本和文件。
//
// 只要有安装还在跑其中某个版本就拒绝：删了会让已安装的面板无码可载，而且卸载一条安装是管理员的
// 决定，不是发布者的。
func (s *PluginService) DeletePackage(ctx context.Context, workspaceID pgtype.UUID, packageID string) error {
	parsed, err := util.ParseUUID(packageID)
	if err != nil {
		return pluginErrf(PluginErrorNotFound, "plugin package not found")
	}
	// 只在事务外读一次，是为了知道锁要打在哪把 plugin key 上。决策赖以成立的一切都将在事务内重读。
	pkg, err := s.Queries.GetWorkspacePluginPackage(ctx, db.GetWorkspacePluginPackageParams{
		WorkspaceID: workspaceID,
		ID:          parsed,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginErrf(PluginErrorNotFound, "plugin package not found")
	}
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "load plugin package", Err: err}
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "begin delete", Err: err}
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := s.Queries.WithTx(tx)

	if err := lockPluginPackageKey(ctx, queries, workspaceID, pkg.PluginKey); err != nil {
		return err
	}
	// 在锁内而非锁外计数。锁外计数会让「计数后才开始的安装」在删除之后提交，留下一份其版本已消失
	// 的安装。
	installed, err := queries.CountInstallationsOfPackageVersions(ctx, pkg.ID)
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "count installations", Err: err}
	}
	if installed > 0 {
		return pluginErrf(PluginErrorConflict, "this plugin is still installed; uninstall it before deleting the published package")
	}

	// 先删文件：它们点名一个即将消失的版本；按仓库政策没有级联。
	if err := queries.DeletePluginPackageFilesByPackage(ctx, pkg.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin package files", Err: err}
	}
	if err := queries.DeletePluginPackageVersionsByPackage(ctx, pkg.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin package versions", Err: err}
	}
	if err := queries.DeletePluginPackage(ctx, pkg.ID); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin package", Err: err}
	}
	if err := tx.Commit(ctx); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "commit delete", Err: err}
	}
	return nil
}

// requireVersionStillPublished 在调用方已加锁的事务里重读一次版本。
//
// 安装在校验版本、其 manifest 与其权限时还没开事务——这些都不能在锁下做，否则就得把用不到锁的活
// 也锁着。所以版本要在锁到手后再确认一次，此时并发删除再也插不进来了。
func requireVersionStillPublished(ctx context.Context, queries *db.Queries, workspaceID, versionID pgtype.UUID) error {
	_, err := queries.GetWorkspacePluginPackageVersion(ctx, db.GetWorkspacePluginPackageVersionParams{
		WorkspaceID: workspaceID,
		ID:          versionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginErrf(PluginErrorConflict, "this version was deleted while the install was being confirmed; publish or pick another version")
	}
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "re-read published plugin version", Err: err}
	}
	return nil
}

// VersionForWorkspace 加载一个已发布版本并确认它属于 URL 里的工作区。发布是按工作区私有的，来自
// 其他工作区的版本 id 绝不能在此被安装。
func (s *PluginService) VersionForWorkspace(ctx context.Context, workspaceID pgtype.UUID, versionID string) (db.PluginPackageVersion, error) {
	parsed, err := util.ParseUUID(strings.TrimSpace(versionID))
	if err != nil {
		return db.PluginPackageVersion{}, pluginErrf(PluginErrorNotFound, "published plugin version not found")
	}
	version, err := s.Queries.GetWorkspacePluginPackageVersion(ctx, db.GetWorkspacePluginPackageVersionParams{
		WorkspaceID: workspaceID,
		ID:          parsed,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PluginPackageVersion{}, pluginErrf(PluginErrorNotFound, "published plugin version not found")
	}
	if err != nil {
		return db.PluginPackageVersion{}, &PluginError{Kind: PluginErrorUnavailable, Message: "load published plugin version", Err: err}
	}
	return version, nil
}

// PluginSurfaceScript 是某个 surface 要跑的代码，外加其来源版本的摘要。
type PluginSurfaceScript struct {
	Code    string `json:"code"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// SurfaceScript 返回一个安装的某个 surface 的脚本。
//
// 从已装版本存储的文件读取、绝不联网取——正是这个方法让「管理员同意过这段代码」成真而非愿望。
// 所查的 manifest 是安装自己的快照：即便后来某版本声明了新 surface，管理员没批准过的 surface 在这里
// 也点不出名。
func (s *PluginService) SurfaceScript(ctx context.Context, installation db.PluginInstallation, surfaceKey string) (PluginSurfaceScript, error) {
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		return PluginSurfaceScript{}, &PluginError{Kind: PluginErrorInvalid, Message: "stored plugin manifest is unreadable", Err: err}
	}
	entry := ""
	for _, surface := range manifest.Contributes.Surfaces {
		if surface.Key == surfaceKey {
			entry = surface.Entry
			break
		}
	}
	if entry == "" {
		return PluginSurfaceScript{}, pluginErrf(PluginErrorNotFound, "this Plugin does not contribute a surface named %q", surfaceKey)
	}
	file, err := s.Queries.GetPluginPackageFile(ctx, db.GetPluginPackageFileParams{
		VersionID: installation.PackageVersionID,
		Path:      entry,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PluginSurfaceScript{}, pluginErrf(PluginErrorNotFound, "the installed version does not contain %q", entry)
	}
	if err != nil {
		return PluginSurfaceScript{}, &PluginError{Kind: PluginErrorUnavailable, Message: "read plugin surface", Err: err}
	}
	return PluginSurfaceScript{
		Code:    string(file.Content),
		Version: installation.Version,
		Digest:  file.Sha256,
	}, nil
}

// packageFile 读取已发布版本的单个文件，供安装期工作（如 skill 资源）使用。
func (s *PluginService) packageFile(ctx context.Context, queries *db.Queries, versionID pgtype.UUID, entry string) ([]byte, error) {
	file, err := queries.GetPluginPackageFile(ctx, db.GetPluginPackageFileParams{
		VersionID: versionID,
		Path:      entry,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pluginErrf(PluginErrorInvalid, "the published version does not contain %q", entry)
	}
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "read plugin package file", Err: err}
	}
	return file.Content, nil
}

// readLocalFile 读取本地插件目录里的一个文件。
//
// `entry` 已由 contract 包校验为相对且无穿越（traversal-free），但本层是碰文件系统的层、不能想当然：
// 拼接后的路径会在 clean 之后再次对照插件自己的目录复核。
func (s *PluginService) readLocalFile(name, entry string) ([]byte, error) {
	if s.LocalDir == "" {
		return nil, pluginErrf(PluginErrorInvalid, "local plugin sources require MULTICA_PLUGIN_DIR")
	}
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return nil, pluginErrf(PluginErrorInvalid, "local plugin source must be a single directory name under MULTICA_PLUGIN_DIR")
	}
	root := filepath.Join(s.LocalDir, name)
	path := filepath.Clean(filepath.Join(root, entry))
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return nil, pluginErrf(PluginErrorInvalid, "plugin resource %q escapes its directory", entry)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, pluginErrf(PluginErrorNotFound, "plugin resource %q was not found", entry)
	}
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorInvalid, Message: "read plugin resource", Err: err}
	}
	return raw, nil
}
