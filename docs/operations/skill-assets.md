# Skill 原生附件与 NAS 迁移

技能正文和文本文件保存在 PostgreSQL；图片、Office 文件及其他二进制资源通过现有 Storage 接口保存。当前使用环境独立的 `LOCAL_UPLOAD_DIR`。原始相对路径、类型、字节数、SHA-256 和普通/可执行权限保存在 `skill_file`。对象键由服务端生成，置于 `private/skills/<workspace-id>/<uuid>`，替换创建新对象。

## 用户行为

- 文件夹、ZIP、URL 和运行时本地技能导入保留二进制文件及嵌套的 `SKILL.md`，不会把图片转换为文本。
- PNG、JPEG、GIF、WebP 在文件页按需预览；SVG 作为文本查看，其他文件显示类型、大小并可下载。
- 文件页提供上传、替换和删除；有未保存的文本草稿时先保存或放弃，再上传。
- 上传单文件最大 16 MiB，支持文件最多 512 个、合计 64 MiB，ZIP 上传最大 80 MiB。重复、越界路径及超限会拒绝整包导入。
- 图片不会自动加入模型上下文。已绑定技能在任务准备阶段还原完整资源，模型按技能说明读取图片。

## 数据与接口

迁移 `513_skill_assets` 增加文件元数据和对象意图台账，`514_skill_asset_key` 单独并发创建索引。无新增外键。原有文本行默认 `kind=text`。

| 接口 | 行为 |
| --- | --- |
| `POST /api/skills/{id}/files/upload` | multipart 的 `file`、`path` 和可选 `mode`，保存单个文件 |
| `GET /api/skills/{id}/files/{fileId}/content` | 工作区鉴权后的文件内容，安全图片允许 inline |
| `GET /api/skills/{id}/files/{fileId}/download` | 强制下载 |
| `PUT /api/skills/{id}/bundle` | multipart 的 ZIP，原子替换正文和资源，保留名称、描述、配置、身份和绑定 |
| 原有 JSON CRUD | 文本可继续编辑；二进制通过 `id`、`kind=binary` 保留，空字符串不能覆盖图片 |

对象先登记上传意图，再写入存储，最后在数据库事务中提交引用。上传或事务失败不会改变原引用。清理器每小时扫描超过 24 小时的未引用对象；有准备中任务的工作区暂缓清理，删除失败保留台账重试。公开 `/uploads/` 禁止读取 `private/`。

Daemon 宣告 `skill-assets-v1`。服务端按任务、运行时、工作区及已启用技能检查附件访问权限。Daemon 校验大小及 SHA-256 后缓存，原子落盘；下载失败阻止启动。准备期间文件更新产生版本冲突时重新解析整包，最多三次。运行中的任务使用已经落盘的副本。旧 Daemon 的文本技能照常工作，二进制技能要求升级。

## 当前 20 个技能迁移

工具读取 Multica 中现有的 `.multica-install/bundle.json`，按清单验证和还原原始字节。不会从本地源重新导入音频，也不会改变原始源目录、凭据或智能体绑定。

```bash
python3 scripts/migrate-skill-assets.py dry-run \
  --profile <目标环境-profile.json> --workspace <工作区-id> \
  --inventory <安装结果-id列表.json> --backup <新的备份目录>
python3 scripts/migrate-skill-assets.py execute \
  --profile <同一-profile.json> --workspace <同一工作区-id> --backup <备份目录>
python3 scripts/migrate-skill-assets.py verify \
  --profile <同一-profile.json> --workspace <同一工作区-id> --backup <备份目录>
```

`dry-run` 保存技能完整快照和绑定清单；`execute` 校验备份后是否有更新，并逐个原子替换；重复执行会验证已迁移技能。完成时逐文件比较路径、内容和权限，再检查技能属性、配置及绑定。花叔设计及 product-ui-design 内嵌副本应各保留 24 张截图，已删背景音乐和短音效应缺席。专利包的嵌套技能、Word/PPT 示例必须完整。

恢复旧资源包使用同一备份：

```bash
python3 scripts/migrate-skill-assets.py restore \
  --profile <目标环境-profile.json> --workspace <工作区-id> --backup <备份目录>
```

## 备份、恢复与 NAS

`pnpm release backup` 对本地存储调用配对备份，包含数据库 dump、技能对象及校验清单。备份使用数据库咨询锁与对象回收互斥。联调可单独执行：

```bash
node scripts/backup-skill-assets.mjs int
node scripts/restore-skill-assets.mjs <备份目录> <恢复目录> verify
node scripts/restore-skill-assets.mjs <备份目录> <恢复目录> restore
```

文件恢复先逐项校验后写入原键；数据库使用既有 PostgreSQL 恢复流程恢复同一批次的 dump。生产数据库恢复另行授权。附件备份目录包含业务数据，权限为 0700，应纳入现有异地备份和保留策略。

未来接 NAS 时停止写入和清理任务，把完整上传目录复制到已挂载目录，逐文件核对 SHA-256，配置新的 `LOCAL_UPLOAD_DIR` 后重启并验证。Linux 服务可设置 `LOCAL_UPLOAD_REQUIRED_MOUNT=<挂载点>`：挂载不可用时拒绝启用文件存储，避免把文件写入空的本地挂载目录。对象键和数据库记录无需改变。回滚时切回原目录和原配置，切换窗口内禁止双边写入。S3 接口沿用既有实现；若改用 S3，备份应使用桶版本化/快照，此处文件系统备份命令会明确拒绝 S3 环境。

## 验收与发布

先在现有 int 使用测试账号和合成资源验证；不复制生产凭据或原件。覆盖上传、预览、替换、下载、删除、刷新，桌面和 390px 页面，以及真实 Agent 读取图片、执行脚本和产生可核验产物。检查越权读取、哈希损坏、上传失败、旧客户端保护、文件权限、缓存和备份恢复。

功能实现、定向检查和 int 验收完成后，生产部署和生产技能迁移按授权边界执行，使用同一个已验收发布包。commit、push 和生产数据库恢复单独授权。
