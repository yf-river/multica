// shutdown.go — 把进程退出时的清理顺序固化成"带名字的步骤序列"，而不是散落
// 在 main() 里按语句出现的先后顺序隐式排列。
//
// 之所以做成一个类型，是因为其中有一处先后关系是"承重墙"级别的，而且在调用
// 现场完全看不出来：企微（WeCom）跨副本派发器靠 channel supervisor 拥有的
// WebSocket 投递消息，而每一条受监管的连接在退出时会把自己从发送方注册表里
// 清掉。supervisor 只要 context 一被取消就开始退出——而不是等它被 Join 才
// 退——所以"先 Join supervisor 再停派发器"根本不够：到那时注册表早已清空，
// 派发器排空时对任何安装都找不到 socket，会把它存在的意义（要救的每条消息）
// 全部丢弃。
//
// 因此派发器必须先停、再取消 context，而这种顺序用一行注释说明恰恰是那种
// "悄悄就不成立了"的断言。TestShutdownSequence 测试把这个契约钉在了本类型上。
package main

// shutdownSequence 是退出的每一步，按必须执行的顺序排列。nil 的步骤会被跳过
// ——这样没有 metrics server 或没有频道模块的部署也能复用同一套序列。
type shutdownSequence struct {
	// StopAutopilot 最先停：它会调度系统其他部分要执行的工作，先停它
	// 保证不再产生新活。
	StopAutopilot func()

	// DrainHTTP 要在一切为它服务的组件之前停。正在处理中的请求会赶在调度器
	// 停摆前把 Schedule() 调用完，此后再也不会有新的任务完成事件到达——
	// 这正是下面那个"停派发器"能做成排空（drain）而不是竞态的原因。
	DrainHTTP func()

	// StopOutboundRelay 必须在 CancelWorkers 之前。参见本文件头部说明：
	// 派发器的排空只有在受监管 socket 还活着时才能送达，而 CancelWorkers
	// 正是开始拆掉它们的动作。
	StopOutboundRelay func()

	// CancelWorkers 取消清扫器所在的 context——所有后台循环（含 channel
	// supervisor）都绑在它上面。
	CancelWorkers func()

	// StopHeartbeats 把最后一波排队中的心跳 bump 冲刷落库。
	StopHeartbeats func()

	JoinWebhookWorker func()
	JoinTelegram      func()

	// JoinChannelSupervisor 等待各"按安装维度"的 goroutine 收尾，好让租约
	// 续期器（lease renewer）在退出前发出最后一次释放；没有这一步，重新部署
	// 后下一个副本要干等整个 LeaseTTL 才能接手。
	JoinChannelSupervisor func()

	// DrainChannelRouter 冲刷被去抖的 run 触发并等正在处理的出站回复完成——
	// 此时已没有 supervisor 在投递入站事件了。
	DrainChannelRouter func()

	StopMetricsServer func()
	StopProfiling     func()
}

// run 执行整个退出序列。这里的顺序本身就是契约，别随意调整。
func (s shutdownSequence) run() {
	for _, step := range []func(){
		s.StopAutopilot,
		s.DrainHTTP,
		s.StopOutboundRelay,
		s.CancelWorkers,
		s.StopHeartbeats,
		s.JoinWebhookWorker,
		s.JoinTelegram,
		s.JoinChannelSupervisor,
		s.DrainChannelRouter,
		s.StopMetricsServer,
		s.StopProfiling,
	} {
		if step != nil {
			step()
		}
	}
}
