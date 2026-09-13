// Agent 相关的前端/后端对称执行的用户可见限制（前端做计数 + 禁用保存，后端做
// 校验 + 数据库 CHECK 约束）。放在 core 里，让各个应用与测试套件都从同一处读取。
/** Agent 简介的最大字符数。 */
export const AGENT_DESCRIPTION_MAX_LENGTH = 255;

/** 一个 Agent 最多可配的对话开场白（conversation starter）条数。 */
export const AGENT_CONVERSATION_STARTERS_MAX = 3;
/** 单条开场白标题的最大长度。 */
export const AGENT_CONVERSATION_STARTER_LABEL_MAX_LENGTH = 80;
/** 单条开场白正文的最大长度。 */
export const AGENT_CONVERSATION_STARTER_MAX_LENGTH = 4000;

// 每个 Agent 的调度并发上限的合法区间。放这里避免创建、复制与设置编辑各自漂移。
/** 每个 Agent 最大并发任务数下限。 */
export const AGENT_MAX_CONCURRENT_TASKS_MIN = 1;
/** 每个 Agent 最大并发任务数上限。 */
export const AGENT_MAX_CONCURRENT_TASKS_MAX = 50;
