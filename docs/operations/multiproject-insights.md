# 多项目管理与上下文洞察

## 固定来源与交付

上下文洞察固定移植 dsh-context 0.78.2 的 `402742a885a7aaf030e17b280627871cd5238de6`。`vendor/dsh-context/SOURCE.json`、源码和 LICENSE 保存完整来源；计算包和原生页面分别位于 `packages/context-insights` 与 `packages/views/insights`。DSH 会话速度与步骤使用 0.2.1-rc.3 官方 session-stats 的实现及许可证。

原生页面独立使用上游的 strict TypeScript 编译选项生成声明与 JavaScript，再由 Multica views 的检查消费；Multica 其他源码继续使用项目自己的更严格索引检查。`pnpm --filter @multica/views build:insights` 生成页面模块，views build/typecheck 包含该步骤。

本次直接修改当前分支，直接构建固定 int，业务验收后 commit/push。不使用 Multica 任务、发布 Agent 或交付队列。Multica 生产不在本次部署范围；OpenViking 正式更新单独验收。

## 项目与部署

上游清单是实例级，各项目分别保存范围、分析进度、建议和运行。旧记录归属 multica 并保留 ID。项目的创建与修改使用现有管理员权限，查看沿用现有成员规则。手动同步通过既有观察员派发执行；保存配置不创建自动调度，迁移会禁用已有调度。首期全局只允许一个分析批次。

OpenViking 使用本地部署配置 `MULTICA_UPSTREAM_DEPLOYMENT_CONFIG`，仅运维可配置服务器路径，浏览器只提交准备或部署动作及固定版本。候选使用独立环境和存储，正式切换前需通过完整验收及旧版恢复验证。部署路径为 `/data/ida/openviking`；旧软链接仍然有效。

只替换程序且已验证旧版可读、没有数据迁移时，无需重复全量备份；有迁移或兼容性未知时，先取得并验证可恢复备份。准备成功、候选业务验证通过、部署完成、正式业务验收通过分别记录。

## 采集和数据契约

统计启用时间由各环境 `context_insights_config.enabled_since` 固定。新环境在没有采集记录时默认未启用；int 已采集范围沿用原时间。正式发布先确认 Server 接收协议以及各 Daemon 已激活目标 runtime，再以明确 UTC 时间执行 `UPDATE context_insights_config SET enabled_since=TIMESTAMPTZ '正式启用时间' WHERE singleton AND enabled_since IS NULL`，随后开放新任务。生产启用操作属于正式发布；本次不执行，也不把本地或 int 验收数据带入生产。DSH 从原始事件生成投影和可靠上报，不依赖 OpenViking。历史事件只用于重建当前上下文，启用前消耗和子会话继承的父事件不重复计费。

Daemon 在其工作目录下 `.insights` 持久化 JSONL 队列。Server 事务落库后返回确认，Daemon 才原子更新确认位置；网络中断和重复补报不会增加统计。原始事件与投影使用 PostgreSQL bytea，完整保留 JSON 中的 NUL；总览单独保存头部字节，正文按实际会话读取。Server 保存会话投影、事件、运行关联、偏好和统一价格目录，离线仍可查看。正式删除所属聊天或任务时同事务清理洞察；Daemon 在空闲的清理轮次通过现有任务状态接口确认删除，再移除本地队列。归档只影响筛选。

平台 `/insights` 查询所有有权访问的工作区；工作区 `/{slug}/insights` 固定范围。聊天及任务以 `view=insights` 定位原页面，`insight` 定位实际 DSH 会话，`run` 定位单次 Multica 运行。旧 usage 路由保留可映射参数重定向。混合运行不伪造 DSH 数据。

## 完整验收清单

- 多项目：历史迁移、手动同步、无自动调度、去重、隔离、权限、失败与历史改写。
- 部署：固定候选、独立存储、兼容验证、忙碌等待、重启、重复操作、正式切换与恢复。
- 原版计算：投影、Token、缓存、费用、耗时、估算、官方速度、启用边界及父子会话。
- Context：三类 Stats、Current Context、全部 Trend 模式与联动、Browser Live/保留请求/分类/正文/差异、Events、File Activity、Command Execution、页内子 Agent。
- Overview：日期、自定义范围、全部指标与图表、热力图、技能、搜索分组排序归档分页、卡片跳转和两级对账。
- 内容与恢复：长上下文、图片附件、长工具输出、分块、保留窗口、断网、取消失败、重启、离线、刷新和直接链接。
- 价格：统一目录、原版匹配与套餐、未知及歧义、获取失败、峰谷、币种、其他摘要一致。
- 权限：平台、工作区、对象、Agent 和原文均遵循已有访问限制。
- 浏览器：桌面/390px、中英文、深色、键盘、长标题、密集/空/错数据，无控制台错误及意外横向溢出。
- 收敛：运行时统计全部移除、工作区统计替换、旧用量/上下文弹窗入口改向新洞察、无消费者代码翻译测试清理。
- 排除项核对：无独立 Fleet、插件市场、推广升级、插槽放置、余额、提示词/运行配置改造及 profile 改造。

实际执行状态与证据保存于 `.run/multiproject-insights`。代码存在、编译或探针成功不代表完整业务验收完成。

冻结的 Desktop 仍引用旧 Dashboard 及其图表和翻译，因此这些仍有消费者的模块保留；Web 的 usage 路由已经重定向。固定上游的原生字体与 CSS 按原版视觉对照验收，Multica 外层适配继续受设计规则检查。运行包在 dist/third-party 保存原版与官方统计、Zod、Schemastery 的许可证与来源。

洞察偏好提供跟随 Multica、中文和英文显示；费用沿用原版按语言选择人民币或美元的规则。Multica 外层导航使用平台现有语言。图片验证使用原生 DSH 与明确标识的本地模型夹具，覆盖原生图片引用、原件校验、可靠上报和有权限的下载；CodeBuddy DSH 适配器目前将模型声明为纯文本，不把 PNG 解析或文字描述当成视觉模型验收。

## 原生界面适配边界

聊天与任务复用完整 Context 页面；任务在右侧属性栏切换，聊天通过现有视图入口切换。实际 DSH 会话由当前关联及 `run` / `insight` 链接定位，子 Agent 跳转同步链接。总览中的会话卡片负责完整累计口径，Context 展示一个实际会话。

统计偏好由原版 Overview 的设置入口打开，复用固定基线的 SettingsCard 展示字段、选项和默认值，仅保留统计视图分组与 Multica 语言适配。原版页面样式只对真实 body 选择器隔离作用域，其他选择器及规则与基线一致。

底栏复用官方 chat 的 ActivityPill、UsagePill，以及 conversation 的 ContextMeter。三项分别打开原版明细；无统计或容量时遵循官方隐藏规则。源码片段、原版 CSS、字典、来源散列及许可证位于 `packages/views/insights/official`，运行不读取本机 DSH 安装目录。适配层将持久化会话统计与分离计费桶提供给官方组件。

原版视觉验收在独立文档中加载 vendor 的原始 CSS；共享的仅为宿主字体、颜色及通用 utility 样式，不使用移植后的洞察 CSS 作为参照。原版排除的推广、插件更新及插槽配置仍遵循固定范围。
