// durable_receipts.go
//
// 职责：给"业务变更已在源事务里完成、且没有额外数据库投影"的事件登记可靠
// 消费回执（terminal receipt）。可靠投递里，事件只有被消费者显式回执后才算
// "关闭"；像 Issue/评论/成员这类目录变更，数据早已随源事务写进主表，没有
// 投影要做，但若不消费回执，这些事件会永远停留在 pending，堵住同一事件流上
// 后面的事件。本文件给整批这类事件挂一个"空消费者"，只负责回执不干活。
package main

import (
	"context"

	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerDurableTerminalReceipts 为一批"业务写入已由源事务完成、再无额外
// 数据库投影"的事件注册可靠消费回执。没有回执的话，这些行会永远停留在
// pending 状态，进而阻塞同一条事件流上更晚的事件。
func registerDurableTerminalReceipts(dispatcher *eventoutbox.Dispatcher) error {
	return registerDurableEvents(dispatcher, "terminal_receipt", consumeDurableTerminalReceipt,
		// Catalog and resource mutations do not have a database projection in
		// the dispatcher; the originating transaction already changed the
		// canonical row. A receipt still matters: it closes the event and keeps
		// later events on the same stream claimable.
		//
		// 中文说明：目录/资源类变更在 dispatcher 里没有对应的数据库投影——
		// 源事务早已改好了权威行。但回执仍然重要：它负责"关闭"这条事件，
		// 让同一事件流上更晚的事件可以被认领投递。
		protocol.EventIssueCreated,
		protocol.EventIssueUpdated,
		protocol.EventIssueDeleted,
		protocol.EventIssueMetadataChanged,
		protocol.EventIssueAttachmentsChanged,
		protocol.EventCommentCreated,
		protocol.EventCommentUpdated,
		protocol.EventCommentDeleted,
		protocol.EventCommentResolved,
		protocol.EventCommentUnresolved,
		protocol.EventReactionAdded,
		protocol.EventReactionRemoved,
		protocol.EventIssueReactionAdded,
		protocol.EventIssueReactionRemoved,
		protocol.EventAgentCreated,
		protocol.EventAgentStatus,
		protocol.EventAgentArchived,
		protocol.EventAgentRestored,
		protocol.EventTaskQueued,
		protocol.EventTaskDeferred,
		protocol.EventTaskDispatch,
		protocol.EventTaskRunning,
		protocol.EventTaskWaitingLocalDirectory,
		protocol.EventSubscriberAdded,
		protocol.EventSubscriberRemoved,
		protocol.EventWorkspaceUpdated,
		protocol.EventWorkspaceDeleted,
		protocol.EventMemberAdded,
		protocol.EventMemberUpdated,
		protocol.EventMemberRemoved,
		protocol.EventSkillCreated,
		protocol.EventSkillUpdated,
		protocol.EventSkillDeleted,
		protocol.EventInboxNew,
		protocol.EventInboxRead,
		protocol.EventInboxUnread,
		protocol.EventInboxArchived,
		protocol.EventInboxUnarchived,
		protocol.EventInboxBatchRead,
		protocol.EventInboxBatchArchived,
		protocol.EventChatMessage,
		protocol.EventChatDone,
		protocol.EventChatQuickActions,
		protocol.EventChatCancelFinalized,
		protocol.EventChatSessionCreated,
		protocol.EventChatSessionRead,
		protocol.EventChatSessionDeleted,
		protocol.EventChatSessionUpdated,
		protocol.EventProjectCreated,
		protocol.EventProjectUpdated,
		protocol.EventProjectDeleted,
		protocol.EventProjectResourceCreated,
		protocol.EventProjectResourceUpdated,
		protocol.EventProjectResourceDeleted,
		protocol.EventLabelCreated,
		protocol.EventLabelUpdated,
		protocol.EventLabelDeleted,
		protocol.EventIssueLabelsChanged,
		protocol.EventPropertyCreated,
		protocol.EventPropertyUpdated,
		protocol.EventIssuePropertiesChanged,
		protocol.EventIssueStatusChanged,
		protocol.EventPinCreated,
		protocol.EventPinDeleted,
		protocol.EventPinReordered,
		protocol.EventInvitationCreated,
		protocol.EventInvitationAccepted,
		protocol.EventInvitationDeclined,
		protocol.EventInvitationRevoked,
		protocol.EventAutopilotCreated,
		protocol.EventAutopilotUpdated,
		protocol.EventAutopilotDeleted,
		protocol.EventAutopilotRunStart,
		protocol.EventAutopilotRunDone,
		protocol.EventSquadCreated,
		protocol.EventSquadUpdated,
		protocol.EventSquadDeleted,
		protocol.EventGitHubInstallationCreated,
		protocol.EventGitHubInstallationDeleted,
		protocol.EventPullRequestLinked,
		protocol.EventPullRequestUpdated,
		protocol.EventPullRequestUnlinked,
		protocol.EventVCSConnectionCreated,
		protocol.EventVCSConnectionDeleted,
		protocol.EventLarkInstallationCreated,
		protocol.EventLarkInstallationRevoked,
		protocol.EventSlackInstallationCreated,
		protocol.EventSlackInstallationRevoked,
		protocol.EventDingTalkInstallationCreated,
		protocol.EventDingTalkInstallationRevoked,
		protocol.EventDingTalkAccountBindingUpdated,
		protocol.EventWecomInstallationCreated,
		protocol.EventWecomInstallationRevoked,
		protocol.EventTelegramInstallationCreated,
		protocol.EventTelegramInstallationRevoked,
		protocol.EventLifeChanged,
	)
}

// consumeDurableTerminalReceipt 是"空消费者"：这些事件没有任何投影或派生要
// 做，回执即完成。返回 nil 表示不派生出新事件。
func consumeDurableTerminalReceipt(_ context.Context, _ *db.Queries, _ events.Event) ([]events.Event, error) {
	return nil, nil
}
