本轮 verify-1 功能验收

交付：01a171e2-116d-79e6-bf67-5cb4d087625b；revision 3。
源码：87e11863efd663bb220a0b073a9f914c697267a0。
候选：87e11863efd663bb220a0b073a9f914c697267a0-1792912266420670525。
入口：http://9.134.129.162:33653；环境 verify-1，generation 34。
种子：seed-20261025-agent-control-v4；SHA256 5fcd7c40b1e78cf96ff8290d25718d6a9e463abbcc15c20a25859ba5371b6090。

本轮结论：下列四项 verification 业务检查通过，证据覆盖范围及限制如下。最终固定 int 集成与交付审查不属于本轮已通过范围，不合主分支，不发布生产。

1. CodeBuddy真实强关联及逐请求对账

经正式生产测试控制入口只保存本任务新建测试 Agent 的 override，再经已安装的 target 维护路径应用。实际 Agent 变为 CodeBuddy Flash；task.started.provider=codebuddy，实际请求模型为deepseek-v4.1-flash-ioa。首次普通样本run 01a171e8-bddf-7f08-a2e5-fe8b445ede4e回答42，两次请求均与外部span的输入、输出、缓存一致。provider请求标识01a171e8c3de7addb36ffa5f7b508154与SDK trace c763e886dabe3e125594a9a484622963不同，SDK、session、response共同匹配，未混用ID。

原生子调用样本run 01a171f0-e08f-70a2-9095-7ed55a106bb3实际调用一次内置Agent工具，主子会话合计本地6个请求、两个强关联reference。当前外部材料验证到1个子请求，parent_sdk_matches=true，输入对账match；没有把该1条扩大成6条全部匹配。重复读取样本另有4条请求匹配。详见对应report/pages/messages、started-runtime-proof.json。

2. Codex所属Daemon及混合runtime材料

Codex普通样本run 01a171e8-bdf2-72cd-8a22-38d6e8446fba完成回答42并上传所属Daemon的本地持久会话证据；外部HTTP请求0。将同一个CodeBuddy样本任务改由原Codex Agent执行后，新run 01a171ea-5ccc-7dff-bc6d-ea3d68cd3456完成。材料分别保持历史codebuddy与codex归属，不按当前Agent配置改写过去runtime。Codex只报告运行级用量及可见事件，明确请求级不可可靠分配、事件前缀截断等gap。

另实际启动Codex运行01a171f0-4d3c-769b-b07d-90d3c285515c，通过正式接口取消，再正式重试生成01a171f1-b8ba-766f-8e57-5bb611037050。重试执行sleep15后完成回答42；取消与重试分别保留独立run材料。监督没有将用户要求的等待或取消本身认作浪费。解析固定材料覆盖续接基线扣除、缓存增量、关联会话和当前thread等13项，均通过。

3. 材料权限分页与生命周期恢复

验收端逐段读取当前全部trace_usage材料，offset/end连续、每份同generation与SHA256；Codex长材料8页，真实监督模型还对混合任务25份材料全部读完，covered=25、pending=0。负offset与非法UUID返回400，其他任务batch读取本任务材料返回404；无认证与非成员工作区也拒绝。

同候选、同种子，在无活动执行时通过受控入口重启测试后端，待处理准备恢复并生成混合runtime材料；原材料正文和SHA256在重启、复审及重开后保持不变。样本任务done创建open完成快照，立即重新打开后轮次closed/reopened，保留2份历史runtime材料。定向维护操作原operation重放返回replayed=true；过期owner首次应用被拒绝，正式续期后应用成功。

独立新建PostgreSQL夹具库执行准确候选迁移至649，再运行完整internal/supervision套件：34个顶层测试通过，5个要求固定int的测试跳过（完整名称见test-counts.json），没有把跳过算通过。包括预算、HTTP鉴权/权限/参数、限流退避、取消、解析脱敏、分页不一致、映射、并发/轮次准备及证据保留等固定材料/数据库回归；不把模拟HTTP故障当作真实AgentLens故障注入。独立容器已删除，句柄与清理回执保留。

4. 真实监督模型加载与效率建议判定

本轮8个真实监督批次全部completed，实际内联技能逐字匹配候选，SHA256 aa7e5011089ff451e065acd4a6b68ab17c2878264c67f052f129f5a1a7b56558；实际使用supervision_material及supervision_validate并持久化结果，详见loaded-skills-and-tools.json与各批次messages。

必要读取样本：首次工单读取用于平台恢复上下文，监督不报浪费、不按高token或重复取数快照报重复执行。
受控浪费对照：临时测试Agent身份固定重复读取三次（专为本次样本设置，执行后恢复），任务说明首次完整读取后的额外读取没有解题收益。真实模型实际执行三次同一GET且返回相同revision。监督finding c69653ee-ad29-4e76-a21c-7b9193899f41准确识别后两次调用，引用run/request/tool证据，建议在任务流程或身份提示词层取消固定重复，保留首次读取，用相同题目对比调用与缓存口径；明确观测请求token不能全部当作可节省成本，且未取得强制三读的身份规则全文，不据此断言执行者违规。该finding是人为测试样本的预期结果，不是本交付新缺陷。

浏览器实际登录监督页，刷新及390px视口成功。任务详情显示CodeBuddy部分关联2请求、Codex不适用、差异0、明确缺口和监督自身用量单列，刷新后结论持久化。detail.png是实际详情截图，未把列表加载当成详情行为验证。

限制与失败记录

- AgentLens当前过滤分页不能证明完整发现，材料正确保留partial及discovery_pending；普通样本已匹配2请求，子调用只确认上述1个外部请求。不得宣称所有外部trace/span已完整采集。
- Codex请求级用量、被截断事件完整正文仍有明确gap，未推算系统提示token或节省比例。
- 限流/超时/权限/迟到恢复等异常矩阵部分通过固定材料与隔离数据库回归验证；真实供应商没有主动注入这些故障。最终固定int与其5项真实环境测试待平台下一阶段安排。
- 首次临时凭据错误地保留shell变量表达式，实际返回provider_error_220002。按脚本变量规则修正后新材料真实关联成功，失败版本及监督gap保留，没有覆盖失败历史。
- 初次补充测试误包含需数据库的rollback夹具，目标无user表导致setup失败，原日志保留；随后纯测试11项通过，并在独立候选夹具库重跑完整监督套件通过，未把前次失败写成通过。

清理与证据

所有自有样本执行及监督批次均已终态。通过正式desired新revision+target路径将测试Agent恢复原有效Codex策略，逐字段核对其他Agent模型设置及环境default未变，再归档该自建Agent。监督enabled/investigation恢复false，短期服务配置恢复、服务凭据和来源凭据文件删除。保留禁用的托管监督Agent、导入技能及全部样本材料；新建Agent的等效Codex覆盖键留作审计，不删除业务证据。独立fixture数据库容器已删除。密钥扫描回执见redaction-and-cleanup.json。

本轮没有修改产品源码；工作区原有AGENTS.md改动保留。报告与完整证据包保存于/data/ida/artifacts/quality-token-supervision-20261025/verify87-final。正式accept已成功，回执交付revision=4、四项业务证据已保存；当前phase=testing，由平台下一阶段接续。回执accept-response.json和final-delivery.json已保存，不自行推动环境阶段。
