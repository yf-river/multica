// durable_events.go
//
// 职责：给"可靠事件投递"（event outbox）提供注册小帮手。
// 服务器里很多业务是通过事件总线触发的（例如评论创建、Issue 更新），
// 而事件要保证"至少投递一次、谁消费过都记得"，靠的是 eventoutbox.Dispatcher
// 维护的消费回执（receipt）。本文件只做一件事：把"给同一消费者注册多个事件类型"
// 这个重复动作收敛成一个函数，避免每处注册都写一遍循环。
package main

import (
	"github.com/multica-ai/multica/server/internal/eventoutbox"
)

// registerDurableEvents 为 dispatcher 注册一个消费者，让它订阅一批事件类型。
//
// 之所以把"注册"当成可靠投递关系图的唯一事实来源：dispatcher 会按事件
// 类型保存消费回执（已经投递过的不再重投），只要注册时指定了 name 和
// consumer，后续事件入队后就能稳定送到对应消费者手上。这里对每个事件
// 类型逐个调用 Register，任何一个失败都直接返回错误，由调用方决定是重试
// 还是中止装配。
func registerDurableEvents(dispatcher *eventoutbox.Dispatcher, name string, consumer eventoutbox.Consumer, eventTypes ...string) error {
	for _, eventType := range eventTypes {
		if err := dispatcher.Register(eventType, name, consumer); err != nil {
			return err
		}
	}
	return nil
}
