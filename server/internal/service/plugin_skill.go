// plugin_skill.go —— 插件的 `skill` 资源安装：写入普通 skill 表并在安装事务内修剪不再声明的项。
package service

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/skill"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// 关于 skill 资源。
//
// 插件的 `skill` 资源变成既有 skill 表里一行普通记录——不是插件自有副本、不是 bundle、不是带摘要的产物：旧插件
// 系统跨十四张表造了所有这些，最终交付的不过是一份 SKILL.md，本表早就能装下。
//
// 平台只需记得“哪个安装贡献了哪个技能”，使卸载恰好移除那些、不碰任何人写的东西。那只是一个可空列。
//
// 资源不是 hook——没有任何东西会被调用。文件在作者发布版本时已校验并存好，因此安装是从我们自己的数据库
// 读取，而不是抓取作者今天正在提供的内容。

// InstallSkillResources 写入 manifest 声明的 skill 资源，并修剪该安装不再声明的旧项。
//
// 在安装事务内调用：一个只装了一半技能的插件比失败的更糟——缺失的半边不可见。
func (s *PluginService) InstallSkillResources(ctx context.Context, queries *db.Queries, installation db.PluginInstallation, manifest plugincontract.Manifest, userID pgtype.UUID) error {
	resources := skillResources(manifest)

	// 先修剪：改名先释放旧名、再写新名。反序会在任何“只改大小写或空格”的改名上与表的 (workspace_id, name)
	// 唯一约束冲突。
	keep := make([]string, 0, len(resources))
	for _, resource := range resources {
		keep = append(keep, resource.Key)
	}
	if err := queries.DeletePluginSkillsNotIn(ctx, db.DeletePluginSkillsNotInParams{
		PluginInstallationID: installation.ID,
		KeepNames:            keep,
	}); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "prune plugin skills", Err: err}
	}
	if len(resources) == 0 {
		return nil
	}

	for _, resource := range resources {
		raw, err := s.packageFile(ctx, queries, installation.PackageVersionID, resource.Entry)
		if err != nil {
			return err
		}
		content := string(raw)
		_, description := skill.ParseSkillFrontmatter(content)
		// manifest key 才是名字的权威，不是 frontmatter。授权屏列出 key、工具命名空间用它，
		// 内容与其不符的文件绝不能悄悄以别的名字安装。
		name := resource.Key
		if strings.TrimSpace(description) == "" {
			description = "Provided by the " + manifest.Name + " Plugin."
		}

		if _, err := queries.UpsertPluginSkill(ctx, db.UpsertPluginSkillParams{
			WorkspaceID:          installation.WorkspaceID,
			Name:                 name,
			Description:          description,
			Content:              content,
			CreatedBy:            userID,
			PluginInstallationID: installation.ID,
		}); err != nil {
			if isUniqueViolation(err) {
				return pluginErrf(PluginErrorConflict,
					"a skill named %q already exists in this workspace", name)
			}
			return &PluginError{Kind: PluginErrorUnavailable, Message: "install plugin skill", Err: err}
		}
	}
	return nil
}

// skillResources 从 manifest 中筛出全部类型为 skill 的资源。
func skillResources(manifest plugincontract.Manifest) []plugincontract.Resource {
	resources := make([]plugincontract.Resource, 0, len(manifest.Contributes.Resources))
	for _, resource := range manifest.Contributes.Resources {
		if resource.Type == plugincontract.ResourceSkill {
			resources = append(resources, resource)
		}
	}
	return resources
}
