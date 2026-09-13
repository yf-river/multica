package channel

import "context"

// 文件角色：入站消息的统一入口 InboundHandler——Channel 适配器与引擎之间唯一的数据
// 通路。类型是"函数 + 消息"（无返回值），把两端解耦到只靠一个签名沟通。

// InboundHandler 是共享的、渠道无关的入站入口：Channel 每收到一条入站消息就调用它一次。
// 引擎 supervisor 给每条它构建的 Channel 注入**同一个** handler（经 Config.Handler），
// 与参考设计的"单一 set_message_handler"接线一致——引擎的入站处理只写一次，所有平台
// 适配器都汇进这一个入口。适配器自己持有收消息循环并负责调用 handler；核心从不轮询
// Channel。
//
// 契约：
//
//   - 返回非 nil 错误 = 基础设施故障（核心压根没处理成这条消息——数据库挂了、分发器
//     配错等）。适配器应当把它当"一次失败的投递"处理：上报运维，交给 supervisor 的
//     重连/退避机制。**绝不**用于表达产品层面的结果。
//   - 返回 nil = 消息已被接收并分类。消息仍可能因正当的产品原因被丢弃（去重命中、
//     未绑定发送者、群聊过滤）——那**不是**错误。判定结果暗示的任何出站回复
//     （绑定卡片、离线提示、输入指示器）都是 handler 自己的责任，与适配器的 ACK 通路
//     解耦。
//
// handler 刻意做成"点火即分类"（除 error 外不返回任何值）：适配器不去分支判断处理
// 结果，若把 handler 绑死到平台专属的结果类型上，抽象就失去意义了。
type InboundHandler func(ctx context.Context, msg InboundMessage) error
