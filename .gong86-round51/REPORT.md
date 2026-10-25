GONG-86 本轮 verify-1 业务验收

结论：四组 verification 业务检查通过，覆盖与限制如下。固定 int 的五项真实环境测试及最终审查仍由下一阶段执行；本轮不计入 integration 通过，不合主分支，不发布生产，不标 done。

交付 01a1723e-ac21-78a7-a120-5842f6732045，源码 51cb16cab2dd68547ecc3d93ef9fc5bd707364b1，候选 51cb16cab2dd68547ecc3d93ef9fc5bd707364b1-1792918336641334079。入口 http://9.134.129.162:33653，verify-1 generation36；种子 seed-20261025-agent-control-v4，SHA256 5fcd7c40b1e78cf96ff8290d25718d6a9e463abbcc15c20a25859ba5371b6090。没有重置或更换环境数据。

1. CodeBuddy真实强关联及逐请求对账

通过正式目标 Agent override 和当前 owner/candidate/seed scope 创建 Flash 样本。首次 run 01a17244-86e3-7cf2-8508-399b39286acc 与续接 run 01a17244-ebe1-747d-92d0-118a5769dc59 均 completed，复用 session 01a17244-8ce4-771b-8d19-304b197b5854。实际 provider=codebuddy、模型 deepseek-v4.1-flash-ioa；首次3请求、续接2请求的输入/输出/cache 均 match。provider_trace_id 与 SDK trace 不同，按 SDK/session/response 关联，没有混用。续接最初暂不可见，后来新版本匹配2请求，旧缺口材料保留。

子调用 run 01a17244-ebf2-70ef-9ec3-998fc02c58ab 实际调用一次内置 Agent；当前外部匹配1条子请求，parent_sdk_matches=true，三项用量对账一致，不能扩大为全部主子请求都已匹配。

取消样本 run 01a17244-ebfa-7c10-91c0-898496e4f161 已进入 CodeBuddy 并执行 sleep30 后，通过正式接口取消；重试 run 01a17245-bf75-78c2-8824-bbc0501be37d completed。取消与重试分别保留材料，外部分别匹配3与1请求。没有用 Codex 取消样本冒充 CodeBuddy。

原始证据：runtime-and-session-proof.json、各 *-runs/messages/report/pages、cancel-running-messages.json、target-apply.json。

2. Codex所属Daemon及混合runtime材料

普通 Codex run 01a17244-86f5-76fc-a903-bd0a41d60ff3 完成回答42，所属 Daemon 的本地持久记录上传并分页读取；外部 HTTP 数0。将同一个隔离 CodeBuddy 任务改派给既有测试 Codex Agent，新 run 01a17246-9ec1-73ef-8698-f1ba2673cb8e 完成。当前同一任务3次执行的材料分别保持 codebuddy/codebuddy/codex 归属；Codex 为 not_applicable，未按当前 Agent 配置改写历史。

Codex材料明确只支持运行级用量，保留请求级不可可靠分配、可见事件截断等gap。累计/增量、权限、预算/限流、映射、生命周期等固定材料及数据库套件在本候选隔离新建数据库运行：34个顶层测试通过，5个固定int专用测试跳过，未将跳过计通过。容器已删除，日志及句柄保留。

3. 材料权限分页与生命周期恢复

本轮所有已准备 trace_usage 正文按 offset/end 连续读取，每次分页内 generation/SHA256 一致；Codex长材料8页。负offset与非法UUID返回400，跨任务batch读取404；无Cookie认证请求401，非成员workspace404。不存在证据返回200及deleted=true墓碑，未误计为正文读取成功。

Codex样本任务 done 后生成open轮次，立即重开后closed/reopened，历史证据保留。全部真实执行和监督批次终态后，仅按已核对的 backend PID 重启当前候选，服务恢复ready；7份历史正文SHA256及长度逐一一致。首次脚本误将“覆盖generation”当成不可变正文版本，断言失败；源码 supervision.go:701 说明手动full检查会递增覆盖generation，复核正文hash没有变化，未隐藏该差异或重新生成正文来过断言。restart.json、immutable-after-first-restart.json、immutable-after-restart.json 保留全部依据。

4. 真实监督模型加载与效率建议判定

本轮6个真实监督批次全部completed，内联技能逐字匹配候选，SHA256 aa7e5011089ff451e065acd4a6b68ab17c2878264c67f052f129f5a1a7b56558。实际使用 supervision_material、supervision_validate 并持久化；各批次实录、工具和实际模型成本见 loaded-skills-and-tools.json、各 supervisor-messages、各 subject.supervisor_usage。

必要工单读取没有误报浪费；CodeBuddy取消/重试的监督结论为效率clear、正确性clear。子调用与混合材料保留外部覆盖缺口，不按高输入量或续接重读直接判浪费。Codex补读后确认已核对的必要读取没有可确认浪费，仍区分截断上下文与完整执行。

重复读取对照 run 01a17246-7736-77cf-bc57-8e00495e1831 的临时测试身份故意强制三次相同GET，三次都返回同revision。真实监督 finding 6138b2a8-2480-4933-8858-c4c8da57806b 指出后两次无收益复读，引用run/调查证据，建议复用首次结果、用相同样本验证一次读取仍回答42，明确不能推算净token/费用或节省比例。另发现样本最终附加过程说明违反“只回答”约束；两者都是受控样本的检测结果，不是本候选新增产品缺陷。监督没有自动修改任务、代码或提示词。

浏览器真实登录监督列表和任务详情，刷新后仍展示历史两个CodeBuddy+一个Codex、关联3/2/0请求、明确缺口和持久化结论；监督自身用量单列。1440px与390px截图为detail.png、detail-mobile.png。详情截图与API数据共同作为证据，不以页面可打开代替业务检查。

限制及下一阶段

- AgentLens分页完整性仍未被供应商数据证明，各材料正确保留partial/discovery_pending；只声称上述已匹配请求。
- Codex请求级分配与截断事件正文仍有gap；真实模型也没有把未读/未知当零成本或全程无问题。监督批次固定各自快照，最新混合任务的全部38份材料并未被同一个批次全量覆盖；API与验收端完整读取覆盖本轮需要的各版本正文。
- 超时、限流、凭据失效等故障通过固定材料/隔离数据库套件覆盖，没有向真实供应商主动注入这些故障。固定int的5项真实测试没有在verify执行，下一阶段必须补测。
- 补充测试最初宽泛正则误包含DB用例，缺user表失败；随后新建独立夹具库运行完整套件通过。原fixed-material-tests.log保留。初始化也曾因本工作区缺pg依赖、候选字段名差异退出，随后复用已安装operations及真实字段接续，没有重复创建Agent/任务。

清理

测试Agent原有效模型策略及指令恢复，其他Agent模型和环境default逐字段核对未变，再归档自建Agent。六批监督及自有样本均终态；监督enabled/investigation恢复false；临时服务配置恢复，0600服务凭据及中转文件删除。未清理样本或历史材料，未修改产品源码，原AGENTS.md改动保留。见policy-restore-proof.json、service-cleanup.json、cleanup-proof.json。

本轮报告、截图、原始JSON/日志与配方保存在 /data/ida/artifacts/quality-token-supervision-20261025/verify51-final/。正式accept回执另存accept-response.json；准确状态以回执为准。
