# 烹饪指南维护

## 内容真源

- `scripts/cooking/sources.json` 锁定原版正文及图解仓库的提交。
- `scripts/cooking/inventory.json` 保存每张图解的对应条目、文字独有项、图解独有项、模板排除原因、失效原图及资源校验值。
- `server/internal/cooking/catalog.json` 随 Go 二进制编译；`apps/web/public/cooking` 随同一 Web 发布包交付。运行时没有外部图片依赖。

## 更新

需要 Python 3 和 Pillow。源仓库先 checkout 到 sources.json 所列提交。图解源可使用 `GIT_LFS_SKIP_SMUDGE=1 git clone`，导入器会下载真实图片并核对 LFS SHA256。

```bash
python3 scripts/cooking/import.py --original /data/ida/HowToCook --illustrated .run/cooking-sources/illustrated --check
python3 scripts/cooking/import.py --original /data/ida/HowToCook --illustrated .run/cooking-sources/illustrated
python3 scripts/cooking/validate.py
```

更新前在 sources.json 修改目标提交，先执行 `--check` 查看新增/删除数量，再正常导入。预览资源与下载缓存只在 `.run`，导入生成的目录和资源一同纳入版本管理。审查正文、图解对应变化和失效源图片后发布。

首次 ID 由原文路径的 SHA256 前 20 位产生；重命名时，在 `stable_ids` 中用新路径指向旧 ID。特殊图文关联写入 `mapping`（图解索引 ID → 原版相对路径），不能靠模糊名称猜测。图解独有条目保留图解阅读，不生成文字步骤。

## 数据与发布

收藏按登录用户保存，跨工作区共享。工作区成员关系仍在 API 入口校验。查询缓存包含用户 ID，登录切换后不能复用他人的结果；重新进入或窗口恢复焦点时重新获取。

497 创建收藏表，498 单独并发创建唯一索引。先核对生产真实台账与兼容性并备份，只执行经审核的缺失迁移，不重放历史迁移。以下入口限定 497、498，由既有 migrate 工具负责锁、并发索引恢复和台账；入口校验生产配置、API 身份及备份文件。

```bash
python3 scripts/cooking/migrate-integration.py --release /path/to/release --config /path/to/prod-services.json --backup /path/to/prod.dump --reviewed
```

执行顺序为构建、审核缺失迁移与备份、按需迁移、请求生产发布、业务验收。每步成功后才能继续。发布包包含迁移目录。新增收藏表对旧版本兼容，回滚程序不删除收藏数据。

当前平台没有用户账号硬删除入口，移除工作区成员不等于删除账号，不清空个人收藏。未来实现账号硬删除时，必须在删除用户的同一事务中执行 `DELETE FROM cooking_favorite WHERE user_id = $1`；不依赖外键或级联。

没有原始配图的条目可经用户授权生成替代配图。`sources.json` 的 `image_replacements` 和 `cover_replacements` 记录原 URL 或正文路径、对应条目、生成提示词、仓库内图片路径及 SHA256；导入时优先读取并校验替代图，再生成正文图片与封面。原始生成图片位于 `scripts/cooking/assets/`，随源码保存。当前图解均为上游生成图片，未声明已完成准确性校对。

## 本次联调验收

2026-10-03 图片补全版本待联调发布，内容版本 `d7572173bc31fba3`。

- 372 篇菜谱正文、18 篇技巧正文、341 项图解。339 项图解对应正文，2 项图解独有，51 篇正文没有图解。1 个示例模板明确排除。
- 页面共 373 道菜谱、19 篇技巧（包括上述图解独有项）；补齐 32 个原本无封面的条目后，共 1,273 个本地图片资源逐项校验通过。
- 实测搜索、分类与组合 URL、分页、全量难度排序、返回位置、完整正文、食材勾选、图解缩放和 Escape、手机侧栏及无横向溢出、深浅主题、文字独有/图解独有/404 页面。
- 实测收藏幂等、跨浏览器与跨工作区同步、跨账号隔离、服务重启持久化、已移除菜谱及失败提示。收藏失败场景使用浏览器故障注入；其他业务调用使用真实联调 API 和数据库。
- 任务页面冒烟通过，浏览器没有向上游请求菜谱图片。验收收藏已恢复，临时账号与临时工作区已删除。
- 2026-10-03 图片补全：完美水煮蛋的失效外链原图已由用户授权生成的配图替代，正文与封面均使用本站资源；内容版本 `d7572173bc31fba3`，当前缺失图片为 0；32 个无封面条目已补充配图。替代关系与生成来源记录在 `sources.json`。
- 生产版本和生产进程未修改。本次源码在主工作区保留，未创建提交或推送。
