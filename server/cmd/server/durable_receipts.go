package main

import (
	"context"

	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerDurableTerminalReceipts closes durable streams for events whose
// business mutation was already performed by the originating transaction and
// which have no additional database projection. Without a receipt these rows
// remain pending forever and can block later events on the same stream.
func registerDurableTerminalReceipts(dispatcher *eventoutbox.Dispatcher) error {
	return registerDurableEvents(dispatcher, "terminal_receipt", consumeDurableTerminalReceipt,
		// Catalog and resource mutations do not have a database projection in
		// the dispatcher; the originating transaction already changed the
		// canonical row. A receipt still matters: it closes the event and keeps
		// later events on the same stream claimable.
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

func consumeDurableTerminalReceipt(_ context.Context, _ *db.Queries, _ events.Event) ([]events.Event, error) {
	return nil, nil
}
