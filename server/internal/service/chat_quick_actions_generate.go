// chat_quick_actions_generate.go —— “追问建议”（quick actions）的生成侧实现：
// 每轮聊天完成后，调用 LLM 生成三个可点击的“建议下一步”按钮（pills），
// 再把结果挂到该轮助手消息上并广播给前端。本文件是生成的主干（同步核心、
// 异步调度、提示词拼装与截断），解析/清洗放在 chat_quick_actions.go。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 单次建议生成的整体预算。这整趟调用只是挂在“用户已经收到的那条回复”上的一项
// 锦上添花，所以预算卡得很紧：一次慢的生成比干脆不生成更糟——客户端在结果出来
// 前一直持有一个骨架占位（前端的 QUICK_ACTIONS_PENDING_TIMEOUT_MS 就是按
// chatQuickActionsTimeout 加上写入和广播的余量来定的）。
const (
	chatQuickActionsTimeout     = 8 * time.Second
	chatQuickActionsTemperature = 0.3
	// 输出长度上限。GenerateJSON 对这种对延迟敏感的调用关掉了 GPT-5.6 的
	// “先推理后回答”，所以整个预算都能花在 JSON 本体上。留出余量是为了避免
	// 过长或多字节的按钮内容在对象中途被截断；之后 sanitizeChatQuickActions
	// 仍会套用产品可见的长度限制。
	chatQuickActionsMaxCompletionTokens = 2048
)

// 送入模型的上下文窗口大小。建议关心的是“对话接下来往哪走”，所以只有靠后的部分
// 有用——更早的轮次只会白花 token 和延迟，却不改变答案。
const (
	chatQuickActionsContextMessages = 6
	// 最新一条助手回复拿最大份额：建议必须以它为锚点。
	chatQuickActionsLatestBudget = 3000
	chatQuickActionsOlderBudget  = 800
	// 对过长的最新回复做“掐头去尾”的拆分。尾部通常带着结论和下一步建议——这正是
	// 生成建议的素材来源——所以如果只留头部，等于把最有用的部分扔掉了。
	chatQuickActionsHeadBudget = 2000
	chatQuickActionsTailBudget = 1000
	// 回放进提示词里的“历史已建议标签”数量上限。
	chatQuickActionsPreviousMax = 6
)

// chatQuickActionsMaxConcurrent 限制全进程同时在跑的生成趟数。生成是从请求处理里
// 随手拉起的、背后没有队列，若不加顶，一波聊天流量（或客户端死循环刷刷新接口）
// 就会让 goroutine、上游连接和模型花费无限增长。超限的请求直接丢弃而非排队：
// 客户端等待窗口过期后再到的建议一文不值，所以“丢负载”优于“攒任务”。
const chatQuickActionsMaxConcurrent = 16

// ChatQuickActionsOrigin 说明是哪个调用方发起了这次生成，它决定失败如何上报：
// 用户主动“刷新”是用户动作，必须把失败亮出来；而自动生成是用户从未要求的后台
// 尽力而为工作，必须静默失败（protocol.ChatQuickActionsPayload 里注明自动那趟的
// Failed 恒为 false，客户端会把每个 failed=true 都弹成“刷新失败”的 toast）。
type ChatQuickActionsOrigin int

const (
	ChatQuickActionsAutomatic ChatQuickActionsOrigin = iota
	ChatQuickActionsRefresh
)

// ChatQuickActionsLLM 是 TaskService 用来生成追问建议的接缝（seam），由
// *llm.Client 实现。之所以用接口而不是具体类型，是为了让测试能不碰 HTTP 上游就
// 打通整条链路——提示词渲染、解析、落库、广播——和 ComposioOverlayBuilder、
// TaskWakeupNotifier 的注入方式一致。
//
// ChatQuickActionsLLM 为 nil、或它的 Enabled() 返回 false 时，整个功能关闭：
// 既不会立 pending 标记，也不会生成任何按钮。自托管且没有配
// MULTICA_LLM_API_KEY / MULTICA_LLM_BASE_URL 的部署正是这种状态，这和聊天自动
// 起标题已有的降级方式保持一致。
type ChatQuickActionsLLM interface {
	Enabled() bool
	GenerateJSON(ctx context.Context, model, systemPrompt, userPrompt string, temperature float64, maxCompletionTokens int64) (string, error)
}

// chatQuickActionsSystemPrompt 是这次生成的完整指令集。它是系统提示词（内容稳定，
// 上游可以对它做提示词缓存），每次调用不同的对话内容放在用户消息里。
//
// 规则里刻意规定死“建议是写给谁的”。已被废弃的 daemon 版实现是在 agent 自己的
// 会话里以“续写一轮”的方式跑的，于是继承了 Multica 运行时简报里的身份，逐渐跑偏
// 成“帮 agent 干活的按钮”；这一版没有那种上下文，必须把立场明说。
//
// 输出语言由 chatQuickActionsLanguageRule 决定（它收在用户消息末尾）；这里的
// 提示只声明那条规则说了算。不要在下面这段稳定指令里点名具体语言——模型读到
// “……in Chinese”这种出现在按钮宽度规则里的字句，有时会当成“这轮可以答中文”
// 的许可（MUL-5689）。
//
// 文本里必须保留 “JSON” 这个词：上游对 response_format=json_object 的要求是，
// 没有它请求会被拒绝。
const chatQuickActionsSystemPrompt = `You generate follow-up suggestions for a chat between a user and an AI agent.
Your output is rendered as three clickable buttons under the agent's latest
reply. Clicking one sends that suggestion's "prompt" to the same agent as the
user's next message.

You write FOR THE USER, not for the agent. Every suggestion must be something
the user would plausibly want to ask or do next — never a task the agent should
perform on its own, never a status report, never a question addressed to the user.

Quality bar:
- Return exactly 3 suggestions.
- Every suggestion must be anchored in something concrete the latest agent reply
  actually mentioned — a file, a name, an option it listed, a caveat it raised,
  a next step it proposed. Never invent a topic the conversation did not touch.
- Never suggest something the agent already did in this turn.
- Never repeat or paraphrase anything under ALREADY SUGGESTED.
- Make the three distinct from each other: different intents, not three
  rewordings of the same request.

Field rules:
- "label": the button text. A short verb phrase — at most 6 words, or at most 12
  characters in a script that does not put spaces between words. No trailing
  punctuation, no quotes, no emoji. It is a button, not a sentence.
- "prompt": the full message sent on the user's behalf. First person, the user's
  own voice, and SELF-CONTAINED — the agent never sees the label, so the prompt
  must carry every detail itself. One or two sentences.
- "primary": true on exactly one suggestion, the single most likely next step.
  false on all others.

Language: the user message contains a LANGUAGE RULE line near the end. It is
authoritative; follow it exactly.

Output JSON only, exactly this shape:
{"actions":[{"label":"...","prompt":"...","primary":true}]}
No prose, no markdown, no code fences.`

// chatQuickActionsLanguageRule 收在本次生成的用户消息末尾，整份“语言政策”就
// 这一行。故意放在最后：它上面的全是对话内容，可能用着“按钮绝不能写成的那种
// 语言”，而这条规则必须在任务行之前、作为最后被读到的东西。
//
// 它的锚点是“最近一条用户消息”，而不是整个窗口。因为按钮是要替用户把消息发出去，
// 所以要跟随他当下正在用的语言——这同时意味着切换语言的效果在下一轮立即生效，
// 不用等窗口整体翻篇。
//
// 其余的东西被逐个点名并明确排除，因为每一个都实际发生过、把输出带偏过
// （MUL-5689）：agent 可能用另一种语言回复、系统提示词是英文、ALREADY SUGGESTED
// 会原样回放上一轮的标签——正是这一点曾让一次坏结果“传染”：每个中文标签反过来
// 又种下了下一轮的种子。
//
// 这里刻意不做的事：在服务端根据 `"user".language` 或 Unicode 文字检测去判断语言。
// 存储的偏好是 UI 语言环境，不是这段对话用的语言，让英文界面的用户设就能静默覆盖
// 一个正在用中文聊天的人。脚本检测更糟——它分不清同是拉丁字母的不同语言（模型
// 还是得自己猜），会拿早几轮的韩文盖过最新一轮日文，而一句塞满英文标识符的日文
// 句子会被归类成拉丁文，于是“绝不输出 CJK”的条款反过来禁止了用户自己的文字。
const chatQuickActionsLanguageRule = `LANGUAGE RULE: Write every "label" and "prompt" in the same language as the most recent [user] message above. Ignore the agent's reply, older messages, the system instructions, and ALREADY SUGGESTED when choosing the language. If there is no [user] message, use the latest [agent] message.`

// GenerateChatQuickActionsForTask 为已完成的一轮聊天执行一次建议生成，并把结果
// 挂到那轮助手消息上，同时广播 chat:quick_actions。它是“完成后的自动生成”和
// “用户显式刷新”两条路径共享的同步核心，也允许测试直接调用。
//
// origin 决定失败怎么上报：刷新要亮出 failed=true，让按下按钮的用户知道没成功；
// 自动那趟则悄悄把占位符化解掉。把自动失败也上报，就会为一个用户从未做过的动作
// 弹“刷新失败”的 toast。
func (s *TaskService) GenerateChatQuickActionsForTask(ctx context.Context, task db.AgentTaskQueue, origin ChatQuickActionsOrigin) error {
	if s.QuickActions == nil || !s.QuickActions.Enabled() {
		return nil
	}
	if !task.ChatSessionID.Valid {
		return nil
	}

	// 先把目标轮定位出来，一切以它为锚。上下文窗口、写入、广播必须指向同一条
	// 助手消息：本函数跑在分离的 goroutine 上，真正执行时会话可能已经又往前走了
	// 整整一轮。
	target, err := s.Queries.GetChatMessageByTaskAssistant(ctx, task.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 没有可挂载的助手行（no_response、渠道空投递等）。也不会有客户端在
			// 等——pending 标记只为“已写入的普通消息”立过。
			return nil
		}
		return fmt.Errorf("load chat quick actions target: %w", err)
	}
	if target.MessageKind != protocol.ChatMessageKindMessage || strings.TrimSpace(target.Content) == "" {
		// 没有可用来生成建议的内容。直接化解占位符，别让客户端骨架等超时。
		return s.SupplementChatQuickActions(ctx, task, "", false)
	}

	prompt, err := s.buildChatQuickActionsPrompt(ctx, target, chatInputOwnerID(task))
	if err != nil {
		return err
	}

	raw, err := s.QuickActions.GenerateJSON(ctx,
		"", // 部署默认模型：MULTICA_LLM_DEFAULT_MODEL，否则 llm.FallbackModel
		chatQuickActionsSystemPrompt,
		prompt,
		chatQuickActionsTemperature,
		chatQuickActionsMaxCompletionTokens,
	)
	if err != nil {
		// 无论成败都先把占位符化解掉；只有显式刷新会把失败上报给用户（见 origin）。
		if suppErr := s.SupplementChatQuickActions(ctx, task, "", origin == ChatQuickActionsRefresh); suppErr != nil {
			slog.Warn("chat quick actions failure broadcast failed",
				"task_id", util.UUIDToString(task.ID),
				"error", suppErr,
			)
		}
		return fmt.Errorf("generate chat quick actions: %w", err)
	}
	return s.SupplementChatQuickActions(ctx, task, raw, false)
}

// GenerateChatQuickActionsAsync 在分离的 goroutine 上跑
// GenerateChatQuickActionsForTask，调用方立即返回。用于完成路径——用户的回复已经
// 送达，绝不能让这个流程拖住它；也用于刷新路径——先回 202，生成在后台跑。
//
// 拉起任何东西之前有两道准入闸门：
//
//   - 每个聊天会话同时只允许一趟。同会话并发生成会抢写同一行、还会为一个可见结果
//     重复烧钱——比如同一轮被刷新两次，或一次刷新撞上自动生成。
//   - 全进程并发上限（chatQuickActionsMaxConcurrent）。
//
// 被拒的请求仍会化解客户端的占位符，所以骨架不会因为等一件从未开工的事而一直挂着。
//
// goroutine 自己持有上下文：调用方的通常是 HTTP 请求上下文，完成回调一返回它就被
// 取消了。
func (s *TaskService) GenerateChatQuickActionsAsync(task db.AgentTaskQueue, origin ChatQuickActionsOrigin) {
	if s.QuickActions == nil || !s.QuickActions.Enabled() {
		return
	}
	sessionKey := util.UUIDToString(task.ChatSessionID)
	if sessionKey == "" {
		return
	}
	if _, inFlight := s.quickActionsInFlight.LoadOrStore(sessionKey, struct{}{}); inFlight {
		slog.Info("chat quick actions pass skipped: one already running for this session",
			"chat_session_id", sessionKey, "task_id", util.UUIDToString(task.ID))
		s.resolveChatQuickActionsPlaceholder(task)
		return
	}
	if s.quickActionsRunning.Add(1) > chatQuickActionsMaxConcurrent {
		s.quickActionsRunning.Add(-1)
		s.quickActionsInFlight.Delete(sessionKey)
		slog.Warn("chat quick actions pass shed: process-wide concurrency ceiling reached",
			"ceiling", chatQuickActionsMaxConcurrent, "task_id", util.UUIDToString(task.ID))
		s.resolveChatQuickActionsPlaceholder(task)
		return
	}

	go func() {
		defer func() {
			s.quickActionsRunning.Add(-1)
			s.quickActionsInFlight.Delete(sessionKey)
		}()
		// 兜住 panic：这个 goroutine 和 HTTP 请求已分离，chi 的 Recoverer 中间件
		// 不在调用栈里，这里 panic 会拖垮整个服务进程。建议只是锦上添花——吞掉、
		// 记日志，让这轮的按钮保持原样即可。
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("chat quick actions generation panicked",
					"task_id", util.UUIDToString(task.ID),
					"panic", rec,
				)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), chatQuickActionsTimeout)
		defer cancel()

		if err := s.GenerateChatQuickActionsForTask(ctx, task, origin); err != nil {
			slog.Warn("chat quick actions generation failed",
				"task_id", util.UUIDToString(task.ID),
				"chat_session_id", sessionKey,
				"error", err,
			)
		}
	}()
}

// chatQuickActionsInFlight 报告本会话是否已有一趟生成在跑。刷新接口先查它，重复
// 请求就能直接回 409 让客户端处理，而不是被收下后悄悄撞上
// GenerateChatQuickActionsAsync 里的准入闸门被丢弃。
func (s *TaskService) chatQuickActionsInFlight(sessionID pgtype.UUID) bool {
	_, running := s.quickActionsInFlight.Load(util.UUIDToString(sessionID))
	return running
}

// resolveChatQuickActionsPlaceholder 在不生成任何内容的情况下结束客户端的 pending
// 骨架——把当前这轮已有的按钮重新广播一遍。当某趟请求没被准入时使用：占位符已经
// 由 chat:done 立起来了，任它超时，等于给一件从未开工的活一直转圈。
func (s *TaskService) resolveChatQuickActionsPlaceholder(task db.AgentTaskQueue) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.SupplementChatQuickActions(ctx, task, "", false); err != nil {
		slog.Warn("chat quick actions placeholder resolve failed",
			"task_id", util.UUIDToString(task.ID), "error", err)
	}
}

// buildChatQuickActionsPrompt 用“截至 target 为止”的对话窗口拼出本次生成的用户
// 消息，target 就是建议要挂靠的那条助手回复。
//
// 以 target 为锚（而不是重读会话里最新几条消息），正是异步生成不出错的保证：如果
// 在完成回调和这次读取之间又落了一轮，拿那轮新消息当上下文、结果却写回更早那轮，
// 就会错位；而若是用户刚发的新消息，窗口会停在一条没人回复的用户消息上。
func (s *TaskService) buildChatQuickActionsPrompt(ctx context.Context, target db.ChatMessage, inputOwnerID pgtype.UUID) (string, error) {
	// 只要严格早于 target 的消息，新的在前；下面再反转。故意多取一些，这样被丢弃
	// 的行（no_response、失败等）不会把窗口压得低于预期的轮数。
	rows, err := s.Queries.ListChatMessagesPage(ctx, db.ListChatMessagesPageParams{
		ChatSessionID:   target.ChatSessionID,
		Limit:           chatQuickActionsContextMessages * 2,
		BeforeCreatedAt: target.CreatedAt,
		BeforeID:        target.ID,
	})
	if err != nil {
		return "", fmt.Errorf("load chat messages for quick actions: %w", err)
	}
	msgs := selectChatQuickActionsContext(rows, target, inputOwnerID)
	return renderChatQuickActionsContext(msgs, collectPreviousChatQuickActions(msgs)), nil
}

// selectChatQuickActionsContext 从预取的消息里挑出能进上下文窗口的行，并保证窗口
// 以 target 收尾。排队的后继任务虽然逻辑上是下一轮，其输入却可能比 target 更早
// 落库——所以用户消息只在“它所属的任务已在本窗口内有回答”时才保留（自动重试的
// 回复用的是子任务 ID，而输入挂在根任务上，因此 inputOwnerID 也要算作已回答）。
func selectChatQuickActionsContext(rows []db.ChatMessage, target db.ChatMessage, inputOwnerID pgtype.UUID) []db.ChatMessage {
	// 已在该锚定窗口内被回答过的任务集合（含 target 自身所属任务）。
	answeredTaskIDs := map[pgtype.UUID]struct{}{target.TaskID: {}}
	answeredTaskIDs[inputOwnerID] = struct{}{}
	for _, msg := range rows {
		if msg.Role == "assistant" && msg.TaskID.Valid {
			answeredTaskIDs[msg.TaskID] = struct{}{}
		}
	}
	msgs := make([]db.ChatMessage, 0, len(rows)+1)
	for i := len(rows) - 1; i >= 0; i-- {
		msg := rows[i]
		if msg.Role == "user" && msg.TaskID.Valid {
			if _, answered := answeredTaskIDs[msg.TaskID]; !answered {
				continue
			}
		}
		// 只有普通轮次才携带可用文本：no_response 行里是英文占位文案、失败行里是
		// 错误信息，两者都说明不了这段对话在聊什么。
		if msg.MessageKind != protocol.ChatMessageKindMessage {
			continue
		}
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		msgs = append(msgs, msg)
	}
	if len(msgs) > chatQuickActionsContextMessages-1 {
		msgs = msgs[len(msgs)-(chatQuickActionsContextMessages-1):]
	}
	msgs = append(msgs, target)
	return msgs
}

// collectPreviousChatQuickActions 收集本窗口内已经给过的标签，让提示词禁止重复。
// 最新的助手行也在收集范围内：显式刷新时它装着的正是用户想换掉的那批按钮，而
// “把同样的三个再给回去”恰恰是刷新永远不该产出的结果。
//
// 这些标签仅仅是去重列表。它们被原样回放，所以一旦某次会话跑偏过，它们就是错的语言
// ——这正是当年一次坏结果传给下一轮的机制。chatQuickActionsLanguageRule 把它们
// 点名列为“选输出语言时要忽略的东西”。
func collectPreviousChatQuickActions(msgs []db.ChatMessage) []string {
	labels := make([]string, 0, chatQuickActionsPreviousMax)
	seen := make(map[string]struct{}, chatQuickActionsPreviousMax)
	// 新的在前，这样最近的建议能活过上限截断。
	for i := len(msgs) - 1; i >= 0; i-- {
		if len(msgs[i].QuickActions) == 0 {
			continue
		}
		var actions []protocol.ChatQuickAction
		if err := json.Unmarshal(msgs[i].QuickActions, &actions); err != nil {
			continue
		}
		for _, action := range actions {
			label := strings.TrimSpace(action.Label)
			if label == "" {
				continue
			}
			key := strings.ToLower(label)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			labels = append(labels, label)
			if len(labels) == chatQuickActionsPreviousMax {
				return labels
			}
		}
	}
	return labels
}

// renderChatQuickActionsContext 把对话窗口和历史已建议标签格式化成本次生成的用户
// 消息。纯函数，因此截断规则可以不依赖数据库做单元测试。
//
// msgs 按时间从旧到新，且最后一个元素必须是“要为其生成建议”的那条助手回复。
func renderChatQuickActionsContext(msgs []db.ChatMessage, previous []string) string {
	var b strings.Builder
	b.WriteString("CONVERSATION (oldest first):\n")
	for i, msg := range msgs {
		speaker := "agent"
		if msg.Role == "user" {
			speaker = "user"
		}
		content := strings.TrimSpace(msg.Content)
		if i == len(msgs)-1 {
			content = truncateChatQuickActionsLatest(content)
		} else {
			content = truncateChatQuickActionsRunes(content, chatQuickActionsOlderBudget)
		}
		fmt.Fprintf(&b, "[%s]: %s\n", speaker, content)
	}

	b.WriteString("\nALREADY SUGGESTED (do not repeat or paraphrase):\n")
	if len(previous) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, label := range previous {
			fmt.Fprintf(&b, "- %s\n", label)
		}
	}

	b.WriteString("\n")
	b.WriteString(chatQuickActionsLanguageRule)

	b.WriteString("\n\nProduce the follow-up suggestions for the latest agent reply.")
	return b.String()
}

// truncateChatQuickActionsLatest 缩短作为锚点的那条回复，但两头都保留。开头交代
// 这条回复在讲什么，结尾放着结论和它提出的下一步——只留头会把生成建议最该取材的
// 部分切掉。
func truncateChatQuickActionsLatest(content string) string {
	runes := []rune(content)
	if len(runes) <= chatQuickActionsLatestBudget {
		return content
	}
	head := string(runes[:chatQuickActionsHeadBudget])
	tail := string(runes[len(runes)-chatQuickActionsTailBudget:])
	return head + "\n…[truncated]…\n" + tail
}

// truncateChatQuickActionsRunes 把一条较旧的消息截到指定 rune 数。只留头即可：
// 对上下文轮次来说，消息开头的部分就足以跟上话题了。
func truncateChatQuickActionsRunes(content string, maxRunes int) string {
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	return string(runes[:maxRunes]) + "…"
}
