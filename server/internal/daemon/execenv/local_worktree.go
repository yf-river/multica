package execenv

// 文件导读：本文件实现 local_directory 资源的 worktree 执行模式——每个任务在
// daemon 自有的 env root 里，给用户的仓库开一个独立的 git worktree。
//
// 背景：local_directory 资源本来让任务直接在用户目录里跑，同一目录的任务只能排队。
// worktree 模式把每个任务放进它自己的 worktree，于是共享同一目录的多个任务可以
// 并发执行；每个任务把成果作为一条分支留在用户自己的仓库里，用户用 `git branch`
// 就能看到，无需新的结果通道。
//
// 本文件要保证的三条性质：
//
//  1. agent 看到的必须与用户看到的一致。光执行 `git worktree add` 只会检出 HEAD，
//     把用户未提交的工作静默藏起来。这里改为把用户目录的「快照」重放进 worktree
//     ——已跟踪的修改与未跟踪的文件合为一棵树（captureUserSnapshot）。
//  2. 绝不写入用户的目录。所有东西——包括 Prepare 写的 sidecar 上下文文件——都
//     落在可丢弃的 worktree 里。在用户仓库里持久存在的只有那条分支，外加每个分支
//     一条隐藏 ref，记录它属于谁、携带了什么。
//  3. 任何东西都不被静默丢弃。agent 未提交的遗留都会在 worktree 消失前提交到分支；
//     用户那些无法合并的改动会在下一轮再次呈现，而不是被登记成「已交付」。
//
// 关键机制：用「提交对象」做用户目录快照、用分支名下隐藏 ref
// （refs/multica/local-state/<branch>）记录状态与归属，靠 owner + checkpoint 双重
// 证明一个分支仍是某个会话的，绝不单凭名字续用分支。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// localWorktreeDirName 是 worktree 相对 env root 的目录名。保持简短：Windows 上
	// worktree 路径加上仓库最深路径必须在不支持长路径的工具面前保持在 MAX_PATH 内。
	localWorktreeDirName = "worktree"

	// gitTimeout 约束本文件发起的每次 git 调用时长。这些都是纯本地操作（无网络），
	// 变慢只意味着索引锁卡住而非远端缓慢；让任务失败比让 daemon 槽位永远挂死强。
	gitTimeout = 2 * time.Minute

	// maxUntrackedFiles / maxUntrackedBytes 限制任务将要重放的未跟踪内容规模。
	// `--exclude-standard` 已先丢掉了所有被 gitignore 的内容（node_modules、构建产物、
	// venvs），所以触到上限的仓库必然带着异常多「未跟踪却未被忽略」的内容，把它们
	// 快照下来等于把每一字节都写进用户自己的对象库。任务宁可拒绝，并点名修复方向。
	maxUntrackedFiles = 2000
	maxUntrackedBytes = 200 << 20 // 200 MiB

	// snapshotIndexFileName 是 captureUserSnapshot 构建用户快照所用的私有索引文件名。
	// 它位于任务的 env root，绝不在用户的仓库里：把 GIT_INDEX_FILE 指向我们自己的
	// 文件，正是让捕获完全不碰用户索引的原因——既不写它，也不等待 .git/index.lock，
	// 后者过去曾能终结任务（#7434）。
	snapshotIndexFileName = ".multica-snapshot-index"

	// localStateRefPrefix 给「记录用户目录每个分支状态」的 ref 起命名空间。放在
	// refs/heads 之外，永远不显示在用户的 `git branch` 里；用 ref 而非松散对象，是
	// 让用户仓库里的 `git gc` 无法在两次轮次之间回收快照。
	localStateRefPrefix = "refs/multica/local-state/"
)

// LocalWorktreeParams 描述 worktree 模式下，Prepare 应为 local_directory 任务搭建
// 的那种 worktree。
type LocalWorktreeParams struct {
	// LocalPath 是用户配置的目录。它可以是仓库根，也可以是仓库里任意子目录；
	// worktree 总是覆盖整个仓库，而 agent 的 cwd 落在 worktree 内与之对应的子目录。
	LocalPath string
	// BaseRef 指定提交基线；配置后不捕获用户目录的未提交修改。
	BaseRef       string
	RepositoryURL string
	// EnvRoot 是 daemon 自有的任务 env root。worktree 建在它里面，普通 env-root
	// GC 就会顺带回收它。
	EnvRoot string
	// AgentName 与 TaskID 共同命名「背后没有会话」的任务的分支：
	// feature/<name>/<short-task-id>。
	AgentName string
	TaskID    string
	// ConversationKey 命名本任务所属的工作线——它的 issue 或聊天会话。共享同一个
	// key 的任务共享同一条分支，因此一次对话的第二轮接着第一轮的成果往下走，而不是
	// 又从 HEAD 分叉（MUL-6881）。没有持久会话的任务为空，改用任务级分支。
	//
	// 它是「展示用」键：`mul-6881` 是用户在 `git branch` 里认得的东西，而两个不同
	// workspace 可能产出相同的键。因此「是否续接某条分支」绝不靠名字决定——见
	// WorkspaceID / AgentID / ConversationID，它们才是与分支记录的归属比对的对象。
	ConversationKey string
	// WorkspaceID、AgentID 与 ConversationID 标识会话本身。它们随分支一起记录，并在
	// 任何后续任务续接该分支前重新核验：同名却属于用户、属于另一个 agent、或属于
	// 另一个 workspace 的分支绝不收养。三者全空 = 没有会话：任务得到一条任务级分支。
	WorkspaceID    string
	AgentID        string
	ConversationID string
}

// owner 返回「本任务创建的分支」应登记在谁名下的身份。
func (p LocalWorktreeParams) owner() branchOwner {
	return branchOwner{WorkspaceID: p.WorkspaceID, AgentID: p.AgentID, ConversationID: p.ConversationID}
}

// localWorktreeConversation 命名一个 worktree 任务所属的工作线，使它的每一轮都交付
// 到同一条分支上（MUL-6881）。
//
// 返回两个值，因为二者回答不同的问题。key 是分支「叫什么」——这里优先用 issue
// 标识符，因为它是用户在 `git branch` 里认得的名字（feature/j/mul-6881 而不是一段
// uuid 尾巴）。id 是分支「属于谁」，只有它能决定后续任务是否可以续接该分支：
// identifier 是 per-workspace、由人选择的，两个 workspace 可能为不同 issue 铸出
// 同一个它。既无 issue 也无聊天会话的任务没有可续接的会话，返回 "", ""。
func localWorktreeConversation(params PrepareParams) (key, id string) {
	if params.Task.IssueID != "" {
		if params.IssueIdentifier != "" {
			return params.IssueIdentifier, params.Task.IssueID
		}
		return taskKey(params.Task.IssueID), params.Task.IssueID
	}
	if params.Task.ChatSessionID != "" {
		return "chat-" + taskKey(params.Task.ChatSessionID), params.Task.ChatSessionID
	}
	return "", ""
}

// LocalWorktree 是一个已就绪的 worktree，外加 daemon 在 agent 退出后做收尾
// （Finalize）所需的全部信息。
type LocalWorktree struct {
	// GitRoot 是用户的仓库根——拥有那条分支的仓库。
	GitRoot string
	// Path 是 env root 内 worktree 的根路径。
	Path string
	// WorkDir 是 agent 的 cwd：即 Path，当用户把资源指向仓库子目录时再叠加上
	// LocalPath 相对仓库根的偏移。
	WorkDir string
	// Branch 是本任务在用户仓库里创建/使用的那条分支。
	Branch string
	// BaseCommit 是 worktree 出发的那个提交——本轮做了 baseline 提交就用它，
	// 否则就是续接到的分支 tip。Finalize 拿「交付后的 tip」与它比两次：一次判断
	// 任务是否产出了东西，一次判断该 tip 是否可以登记。
	//
	// 它是本轮能指向的最靠后的提交，所以第二个问题拿它、而非更早某轮登记的
	// checkpoint 来比。那个 checkpoint 只是它的祖先：用户可能已在交付分支上又提交
	// 过，而本轮自己的 baseline 更在其后。拿更老的提交来度量，会让一次「把本轮
	// 重放的东西全部 reset 掉」的运行仍被登记成已交付——这正是用户的改动在下一轮
	// 凭空消失的成因（MUL-6881 review）。
	BaseCommit string
	// DirtyBaseCaptured 记录用户当时有未提交的已跟踪改动，且这些改动已被重放进
	// worktree。
	DirtyBaseCaptured bool
	// Continued 报告这个 worktree 检出的是「同会话更早一轮留下的分支」，而不是从
	// 用户 HEAD 新分叉一条。
	Continued bool
	// ReplayConflicts 列出「自上一轮以来用户的改动」无法与分支已携带内容合并的
	// 文件。worktree 会「带着这些冲突」交给 agent——解决冲突是普通 git 工作，而且
	// 只有 agent 能判断哪一版才对——所以这也是本轮 prompt 让它去修的东西。
	// Finalize 在它们仍有未合并时会拒绝交付。
	ReplayConflicts []string
	// createdBranch 记录「这条分支是这次 prepare 放上去的」，丢弃它不会丢掉任何更早
	// 轮次交付的东西。续接而来的分支为 false：它承载着此前每一轮的成果，即使本轮
	// 什么都没产出，它也必须存活。
	createdBranch bool
	// userState 是描述「用户目录在本任务眼中的样子」的那个提交：已跟踪改动与未跟踪
	// 文件合为一棵树。一旦分支真的携带了它们就登记，使下一轮只重放之后变化的部分。
	userState string
	// owner 是本分支所属的会话，随快照一起记录，让后续任务能在续接前证明分支是
	// 自己的。
	owner branchOwner
	// tracksState 为 false 表示没有后续轮次会续接这条分支——任务级分支，或被忙碌的
	// 兄弟任务分叉出来的那条。给它们记录快照只会留下一个永远没人读的 ref。
	tracksState bool
	// priorState 是本轮开始时分支携带的快照，也是本轮无法把自己的快照放上分支时
	// 要回退登记的那份。
	priorState string
	// snapshotPending 在 Prepare 把用户的改动以「未合并」状态留在 worktree 时置位：
	// 分支尚未携带 userState，只有等 agent 解决冲突后的那次提交才能放上去。
	snapshotPending bool
	// aborted 置位后会让 Finalize 拒绝提交或删除任何东西。由 daemon 在「某个 commit
	// 前的步骤以一种会让提交出的分支出错的方式失败」时设置（见 AbortWithReason）。
	aborted error
}

// MarshalJSON / UnmarshalJSON 让本结构体的非导出状态能穿过「准备辅助进程」的边界。
//
// Prepare 跑在短命的辅助进程里（PrepareIsolated），搭好的 Environment 以 JSON 回到
// daemon，因此 Finalize 需要的每一样东西都得在线。普通结构体序列化会静默丢掉非导出
// 字段：daemon 于是收到一个 owner、snapshot、tracksState 全为零的 worktree，把本文件
// 做的所有证明都变成空操作——没有交付校验、没有交付 tip 的记录、只读轮次保住分支。
// 没有报错；只是这些保证在生产环境缺失，而每个进程内测试却仍然通过。
//
// aborted 刻意不带：它是 Prepare 返回后由 daemon 设置的（AbortWithReason），只属于
// 父进程。
func (w *LocalWorktree) MarshalJSON() ([]byte, error) {
	type wire LocalWorktree
	return json.Marshal(struct {
		*wire
		CreatedBranch   bool        `json:"created_branch"`
		UserState       string      `json:"user_state"`
		PriorState      string      `json:"prior_state"`
		Owner           branchOwner `json:"owner"`
		TracksState     bool        `json:"tracks_state"`
		SnapshotPending bool        `json:"snapshot_pending"`
	}{
		wire:            (*wire)(w),
		CreatedBranch:   w.createdBranch,
		UserState:       w.userState,
		PriorState:      w.priorState,
		Owner:           w.owner,
		TracksState:     w.tracksState,
		SnapshotPending: w.snapshotPending,
	})
}

func (w *LocalWorktree) UnmarshalJSON(data []byte) error {
	type wire LocalWorktree
	aux := struct {
		*wire
		CreatedBranch   bool        `json:"created_branch"`
		UserState       string      `json:"user_state"`
		PriorState      string      `json:"prior_state"`
		Owner           branchOwner `json:"owner"`
		TracksState     bool        `json:"tracks_state"`
		SnapshotPending bool        `json:"snapshot_pending"`
	}{wire: (*wire)(w)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	w.createdBranch = aux.CreatedBranch
	w.userState = aux.UserState
	w.priorState = aux.PriorState
	w.owner = aux.Owner
	w.tracksState = aux.TracksState
	w.snapshotPending = aux.SnapshotPending
	return nil
}

// LocalWorktreeOutcome 是一个 worktree 任务结束时实际交付的东西。
type LocalWorktreeOutcome struct {
	// Branch 是承载任务工作的分支；任务完全没做改动（只读运行）时为 ""——此时
	// 分支会被删除，免得它作为一个空工件出现在用户的 `git branch` 里。
	Branch string
	// AutoCommitted 为 true 表示 agent 留下了未提交的改动，由 Finalize 提交，使其
	// 能在 worktree 被移除后存活。
	AutoCommitted bool
	// PreservedPath 仅在 Finalize 无法提交 agent 改动时设置。位于此路径的 worktree
	// 被有意留在磁盘上，因为它是那份工作仅存的副本。
	PreservedPath string
}

// PrepareLocalWorktree 创建任务的 worktree，并把用户未提交的状态重放进去。它绝不
// 写入用户的工作树：快照构建在任务 env root 内的私有索引里，用户的索引、文件与
// refs 都保持原样。
func PrepareLocalWorktree(params LocalWorktreeParams, logger *slog.Logger) (*LocalWorktree, error) {
	if params.LocalPath == "" {
		return nil, errors.New("execenv: local worktree requires a local path")
	}
	if params.EnvRoot == "" {
		return nil, errors.New("execenv: local worktree requires an env root")
	}
	if params.TaskID == "" {
		return nil, errors.New("execenv: local worktree requires a task id")
	}

	gitRoot, err := resolveGitRoot(params.LocalPath)
	if err != nil {
		return nil, err
	}

	// agent 的 cwd 保持用户选定的深度：指向 <repo>/services/api 的资源必须把 agent
	// 放进 <worktree>/services/api，而不是仓库根，否则任务对「项目」的整个认知就
	// 漂移了。
	//
	// 比较前先做规范化：gitRoot 返回时已是规范路径，而配置路径经常不是（macOS 上
	// 每个 /tmp、/var 路径都是指向 /private 的 symlink）。直接比较两种形式会把仓库
	// 根误读成「在它自己之外」。
	localPath := params.LocalPath
	if resolved, evalErr := filepath.EvalSymlinks(localPath); evalErr == nil {
		localPath = resolved
	}
	rel, err := filepath.Rel(gitRoot, localPath)
	if err != nil {
		return nil, fmt.Errorf("execenv: locate %q inside repo %q: %w", localPath, gitRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("execenv: %q is not inside its repository root %q", localPath, gitRoot)
	}

	worktreePath := filepath.Join(params.EnvRoot, localWorktreeDirName)

	// 下面所有操作都会改动仓库的 worktree 管理状态或 refs，所以先取 per-repo 锁。
	// 锁覆盖「陈旧路径清理」（它要跑 `git worktree remove`，否则会和兄弟任务的
	// `worktree add` 竞态）以及分支决策（它必须看到别的任务在同一时刻正在创建的分
	// 支）。锁跨进程生效，因为每次 prepare 都跑在各自的辅助进程里（#7434）。
	unlock, err := lockGitRoot(gitRoot, logger)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if _, statErr := os.Stat(worktreePath); statErr == nil {
		// Prepare 会清空并重建 envRoot，所以一个已存在的 worktree 路径意味着用户
		// 仓库里有一条指向这里的陈旧登记。把两者都清掉，而不是让任务失败。
		removeLocalWorktreeDir(gitRoot, worktreePath, logger)
	}

	// 自愈被崩溃的 daemon 遗留的孤儿登记：它们的 env root 早已不存在，但用户仓库
	// 仍列着它们。prune 只删「目录已不存在」的条目，因此绝不会打扰一个活动任务。
	if out, pruneErr := runGit(gitRoot, "worktree", "prune"); pruneErr != nil && logger != nil {
		logger.Warn("execenv: git worktree prune failed (non-fatal)",
			"git_root", gitRoot, "output", out, "error", pruneErr)
	}
	pruneOrphanedStateRefs(gitRoot, logger)

	headSHA, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("execenv: repository %q has no commit to branch from "+
			"(worktree mode needs at least one commit; make an initial commit or switch the resource back to in_place): %w", gitRoot, err)
	}

	var userState string
	var plan taskBranchPlan
	if params.BaseRef != "" || params.RepositoryURL != "" {
		if _, err = runGit(gitRoot, "check-ref-format", "--branch", params.BaseRef); params.BaseRef != "" && err != nil {
			return nil, fmt.Errorf("invalid development branch: %w", err)
		}
		local, localErr := runGitTrimmed(gitRoot, "rev-parse", "--verify", "refs/heads/"+params.BaseRef)
		if localErr == nil {
			headSHA = local
		}
		plan = resolveTaskBranch(gitRoot, params, headSHA, logger)
		if !plan.continues {
			if params.BaseRef == "" {
				params.BaseRef, err = remoteDefaultBranch(gitRoot, params.RepositoryURL)
				if err != nil {
					return nil, err
				}
			}
			headSHA, err = resolveCommittedBase(gitRoot, params.BaseRef, params.RepositoryURL)
			if err != nil {
				return nil, err
			}
			plan = resolveTaskBranch(gitRoot, params, headSHA, logger)
		}
		userState = headSHA
		if plan.continues && plan.priorState != "" {
			userState = plan.priorState
		}
	} else {
		if err := checkUntrackedReplayable(gitRoot, logger); err != nil {
			return nil, err
		}
		userState, err = captureUserSnapshot(gitRoot, params.EnvRoot, headSHA, logger)
		if err != nil {
			return nil, fmt.Errorf("execenv: could not capture user directory: %w", err)
		}
		plan = resolveTaskBranch(gitRoot, params, headSHA, logger)
	}

	actualBranch, createdBranch, err := addLocalWorktree(gitRoot, worktreePath, plan, params.TaskID)
	if err != nil {
		return nil, err
	}

	wt := &LocalWorktree{
		GitRoot:       gitRoot,
		Path:          worktreePath,
		WorkDir:       filepath.Join(worktreePath, rel),
		Branch:        actualBranch,
		BaseCommit:    plan.base,
		Continued:     plan.continues,
		createdBranch: createdBranch,
		userState:     userState,
		priorState:    plan.priorState,
		owner:         plan.owner,
		// 兄弟任务因「会话自己的分支正忙」而分叉出来的分支只交付一次、不再被续接，
		// 所以它什么也不记录。
		tracksState: plan.tracksState && actualBranch == plan.name,
	}

	// 下面每个失败路径都要把 worktree 拆回去。半重放的树是最糟的结果：它看起来像
	// 一个可用的检出，下游不会质疑，而 agent 静默地读着与用户不同的代码。分支只在
	// 「本次 prepare 创建了它」时才一起删——续接而来的分支承载着更早轮次的成果，
	// 只因本轮无法启动就丢弃它，等于毁掉任务本该立足其上的东西。
	rollback := func() {
		removeLocalWorktreeDir(gitRoot, worktreePath, logger)
		if createdBranch {
			dropBranch(gitRoot, actualBranch, logger)
		}
	}

	// 把用户目录重放进 worktree。
	//
	// 从 HEAD 分叉的分支不携带其中任何内容，所以整个快照都要放进去。续接的分支
	// 已携带上一轮记录的快照、上面还叠着 agent 的提交，因此那里只有「用户在其后改
	// 了什么」才是新信息。
	replay, replayErr := replayUserState(worktreePath, plan, userState, logger)
	if replayErr != nil {
		rollback()
		return nil, replayErr
	}
	wt.ReplayConflicts = replay.conflicts
	// 未解决的合并意味着分支还没携带本轮快照；只有 agent 解决后的那次提交才能把
	// 它放上去。
	wt.snapshotPending = len(replay.conflicts) > 0
	// 用户到底有没有未提交的工作——被本轮重放，或已被续接的分支携带。
	_, diffErr := runGit(gitRoot, "diff", "--quiet", headSHA, userState)
	wt.DirtyBaseCaptured = diffErr != nil

	// 把重放的状态提交成一个 baseline，让「这个任务改过东西没有」之后有精确答案。
	// 没有它，用户自己的未提交工作会被算作改动：一个只读任务若仓库里有个未跟踪的
	// 临时文件，结束时就会自动提交那个文件并留下一支 agent 从没碰过的分支。baseline
	// 也让交付的分支可读——`git diff <baseline>..<branch>` 恰好就是 agent 的成果，
	// 用户 WIP 则作为带标签的独立提交。
	//
	// 重放未解决时整个跳过：带着未合并条目的索引提交会把冲突标记一并提交进去，
	// 而 agent 还没轮到处理它们。
	if len(replay.conflicts) == 0 {
		dirty, dirtyErr := worktreeIsDirty(worktreePath)
		if dirtyErr != nil {
			rollback()
			return nil, fmt.Errorf("execenv: could not inspect the prepared worktree for %q: %w", gitRoot, dirtyErr)
		}
		// 本次 prepare 创建的分支即便没什么可重放的也会拿到 baseline。两样东西需要
		// 它：空的情形是过去被跳过的那个，而它恰是「分支正好停在用户自己的 HEAD」
		// 的情形——分支没有属于自己的提交，与用户在那里自己创建的分支毫无区别。
		// 并且有了它，`git diff <baseline>..<branch>` 就适用于每条分支，而不仅是从
		// 脏状态起步的那些。
		if dirty || !plan.continues {
			baseline, baseErr := commitBaseline(worktreePath, plan.continues, dirty)
			if baseErr != nil {
				// 没有 baseline，任务就分不清用户的成果与 agent 的，稍后会把用户的
				// 文件当作 agent 产出提交。宁可拒绝，也不交付误导性的分支。
				rollback()
				return nil, fmt.Errorf("execenv: could not record a baseline commit for the replayed state of %q: %w", gitRoot, baseErr)
			}
			// 分支现在站在一个携带本轮快照的提交上，其后所有交付度量都针对它。
			wt.BaseCommit = baseline
		}
		// 分支现在携带这份快照了，把它连同 owner 一起记录——owner 正是让下一个
		// 任务能证明分支是自己的、进而续接它的依据。在 Finalize 之外这里也记一次，
		// 使一个永远到不了 Finalize 的轮次仍让分支可被识别。
		if err := wt.recordState(wt.BaseCommit, logger); err != nil && logger != nil {
			logger.Warn("execenv: could not record the task branch before the run (non-fatal; Finalize records the delivered tip)",
				"branch", wt.Branch, "error", err)
		}
	}

	// 关于「不让 sidecar 进交付分支」的说明：这里刻意不写 .git/info/exclude。
	// 链接式 worktree 从仓库的 COMMON git dir 读 info/exclude，所以唯一会生效的
	// 文件是用户自己的 .git/info/exclude——改它等于改变用户检出里 `git status` 的
	// 显示，那是用户的而非我们的。替代做法是：daemon 在 Finalize 前对 worktree 跑
	// 一遍既有的 CleanupRuntimeConfig + CleanupSidecars，sidecar 在提交前就已消失。
	// 这同时保留了对已跟踪 CLAUDE.md 的真实 agent 编辑——那种一刀切的 exclude 会
	// 把它一起吞掉。

	if logger != nil {
		logger.Info("execenv: local worktree ready",
			"git_root", gitRoot,
			"path", worktreePath,
			"branch", actualBranch,
			"base", wt.BaseCommit,
			"continued", wt.Continued,
			"dirty_base_captured", wt.DirtyBaseCaptured,
			"replay_conflicts", len(wt.ReplayConflicts),
		)
	}
	return wt, nil
}

// Finalize 提交 agent 留下的所有改动、移除 worktree，并汇报分支。在 agent 退出后、
// env root 交给 GC 之前调用。
//
// 自动提交正是 worktree 任务不丢工作的原因：`git worktree remove --force` 会爽快地
// 删掉未提交的改动，用户将无法找回。先提交把「agent 改过文件」变成「分支上多了一
// 个提交」，这就是本模式的交付契约。
//
// 若那次提交无法完成——仓库开了 commit.gpgSign 而 daemon 没有签名密钥、磁盘写满、
// 或丢失了 ref 锁——Finalize 返回错误并「刻意把 worktree 留在原地」。移除它会是本
// 文件中唯一「销毁工作且无法回头」的操作，而 daemon 日志里一条警告不能替代用户的
// 改动。存活的 worktree 仍登记在用户仓库中，`git worktree list` 能直接指到它。
func (w *LocalWorktree) Finalize(logger *slog.Logger) (LocalWorktreeOutcome, error) {
	if w == nil {
		return LocalWorktreeOutcome{}, nil
	}
	outcome := LocalWorktreeOutcome{Branch: w.Branch}

	unlock, err := lockGitRoot(w.GitRoot, logger)
	if err != nil {
		// 尚未提交或删除任何东西，agent 的工作仍躺在 worktree 里。汇报成「已保留」
		// 而不是报一条并不携带它的分支名。
		outcome.Branch = ""
		outcome.PreservedPath = w.Path
		return outcome, fmt.Errorf("could not lock %q to finalize branch %s: %w; "+
			"the work is preserved in the worktree at %s", w.GitRoot, w.Branch, err, w.Path)
	}
	defer unlock()

	// 提交之前的某个环节出了「会让交付分支误导人」的问题。什么都不提交、保留
	// worktree：agent 的工作还在里面，调用方没能清掉的东西也还在，人类现在可以直接
	// 查看。
	if w.aborted != nil {
		// 不汇报分支。用户仓库里确实存在一条，但什么都没提交上去，把它当作本任务
		// 的结果会指向一条缺了用户正找的那份工作的分支。下面保留的 worktree 路径
		// 才是诚实的指针。
		outcome.Branch = ""
		outcome.PreservedPath = w.Path
		if logger != nil {
			logger.Error("execenv: worktree finalize aborted; nothing committed, worktree kept for inspection",
				"path", w.Path, "branch", w.Branch, "git_root", w.GitRoot, "error", w.aborted)
		}
		return outcome, fmt.Errorf(
			"refusing to deliver branch %s: %w; the task worktree is preserved at %s (listed by `git worktree list` in %s)",
			w.Branch, w.aborted, w.Path, w.GitRoot)
	}

	// 未解决的合并绝不提交。worktree 可能还带着本轮之前用户的改动（replayUserState
	// 把冲突交给 agent 而不是丢弃它），而 `git add -A` 会把冲突标记变成一次已交付
	// 的提交——这是本模式唯一能产出「在哪都编不过、看起来却像有意为之」分支的途径。
	// 保留 worktree、说明哪些文件仍待处理、并让快照保持未登记，使下一轮重新重放
	// 同样的改动而不是假定它们已落地。
	if unmerged, unmergedErr := unmergedPaths(w.Path); unmergedErr != nil || len(unmerged) > 0 {
		outcome.Branch = ""
		outcome.PreservedPath = w.Path
		if unmergedErr != nil {
			return outcome, fmt.Errorf("could not check %s for an unresolved merge: %w; "+
				"the work is preserved in the worktree at %s", w.Branch, unmergedErr, w.Path)
		}
		if logger != nil {
			logger.Error("execenv: worktree left with an unresolved merge; nothing committed, worktree kept",
				"path", w.Path, "branch", w.Branch, "files", unmerged)
		}
		return outcome, fmt.Errorf(
			"refusing to deliver branch %s: your local edits to %s are still unmerged in the task worktree; "+
				"the worktree is preserved at %s (listed by `git worktree list` in %s) — resolve the conflict there, "+
				"or re-run the task and let the agent finish the merge",
			w.Branch, quotedPaths(unmerged), w.Path, w.GitRoot)
	}

	// 把「说不准」当「脏」处理：提交最多浪费一次空提交，而假设干净则可能删掉
	// agent 的改动。
	dirty, statusErr := worktreeIsDirty(w.Path)
	if statusErr != nil {
		if logger != nil {
			logger.Warn("execenv: inspect worktree status failed; committing defensively",
				"path", w.Path, "error", statusErr)
		}
		dirty = true
	}
	if dirty {
		committed, err := w.commitAll(logger)
		if err != nil {
			outcome.PreservedPath = w.Path
			if logger != nil {
				logger.Error("execenv: could not commit the agent's changes; keeping the worktree so the work is recoverable",
					"path", w.Path, "branch", w.Branch, "git_root", w.GitRoot, "error", err)
			}
			return outcome, fmt.Errorf(
				"could not commit the agent's changes to branch %s: %w; the work is preserved in the worktree at %s (listed by `git worktree list` in %s) — recover it before that directory is reclaimed",
				w.Branch, err, w.Path, w.GitRoot)
		}
		outcome.AutoCommitted = committed
	}

	// 分支仍原样停在 baseline 提交上，说明任务什么都没改——只读的情形。删掉它，
	// 让用户的分支列表只为真正产出过工作的任务增长。永远只删「本任务创建」的分支：
	// 续接来的分支停在 baseline 上，恰恰因为本轮没给前几轮交付的成果加任何东西，
	// 删它等于连同删掉它们。
	tip, err := runGitTrimmed(w.Path, "rev-parse", "--verify", "HEAD")
	producedWork := err != nil || tip != w.BaseCommit
	dropped := !producedWork && w.createdBranch

	// 从「合并进行到一半」开始的一轮，只有解决冲突并提交过什么，才配推进分支记录
	// 的状态。当分支仍停在「本轮找到它的地方」时，无法区分「agent 选择保留分支上
	// 已有的版本」和「agent 把合并丢掉了」——两棵树完全相同——所以记录保持分支已知
	// 携带的状态，下一轮把用户的改动再呈一次。
	//
	// 代价是：在「agent 的结论没留下任何痕迹」的那一种情况下，同一个合并可能要
	// 重复收敛多次。另一选择是把「可能已被丢弃的改动」登记成已交付——那正是它们
	// 会在无人察觉时消失的方式。
	if w.snapshotPending && tip == w.BaseCommit {
		if w.priorState == "" {
			outcome.Branch = ""
			outcome.PreservedPath = w.Path
			return outcome, fmt.Errorf(
				"refusing to record branch %s: the merge with your local edits was never concluded and this branch has no earlier "+
					"state to fall back on; the task worktree is preserved at %s (listed by `git worktree list` in %s)",
				w.Branch, w.Path, w.GitRoot)
		}
		if logger != nil {
			logger.Info("execenv: the run concluded its merge without committing; keeping the branch's previous recorded state so the edits are offered again",
				"path", w.Path, "branch", w.Branch)
		}
		w.userState = w.priorState
	}

	// 在 worktree 消失「之前」登记，并把失败当作「交付失败」。这份记录正是分支
	// 可被续接的原因：没有它，下一轮无法证明分支是这段会话的，只能另起新工作线，
	// 把本轮成果丢在那里。先做登记也使它可恢复——worktree 还在、可保留，与一次
	// 无法完成的提交完全相同。
	if !dropped {
		if verifyErr := w.verifyDeliveryPoint(tip); verifyErr != nil {
			outcome.Branch = ""
			outcome.PreservedPath = w.Path
			if logger != nil {
				logger.Error("execenv: the run's delivery point cannot be recorded as this conversation's; nothing recorded, worktree kept",
					"path", w.Path, "branch", w.Branch, "git_root", w.GitRoot, "tip", tip, "base", w.BaseCommit, "error", verifyErr)
			}
			return outcome, fmt.Errorf(
				"refusing to record branch %s: %w; the task worktree is preserved at %s (listed by `git worktree list` in %s) — "+
					"recover the work from there, and let the run keep the commit the worktree started from instead of resetting past it",
				w.Branch, verifyErr, w.Path, w.GitRoot)
		}
		if recErr := w.recordState(tip, logger); recErr != nil {
			outcome.PreservedPath = w.Path
			if logger != nil {
				logger.Error("execenv: could not record the delivered task branch; keeping the worktree",
					"path", w.Path, "branch", w.Branch, "git_root", w.GitRoot, "error", recErr)
			}
			return outcome, fmt.Errorf(
				"could not record branch %s as this conversation's: %w; the work is committed to that branch and the "+
					"task worktree is preserved at %s (listed by `git worktree list` in %s) — a follow-up run will start "+
					"a new branch instead of continuing this one",
				w.Branch, recErr, w.Path, w.GitRoot)
		}
	}

	if removeErr := removeLocalWorktreeDir(w.GitRoot, w.Path, logger); removeErr != nil {
		outcome.PreservedPath = w.Path
		return outcome, fmt.Errorf(
			"could not remove finalized worktree for branch %s: %w; the task worktree remains at %s",
			w.Branch, removeErr, w.Path)
	}

	if dropped {
		dropBranch(w.GitRoot, w.Branch, logger)
		outcome.Branch = ""
	}

	if logger != nil {
		logger.Info("execenv: local worktree finalized",
			"git_root", w.GitRoot,
			"branch", outcome.Branch,
			"auto_committed", outcome.AutoCommitted,
			"produced_work", producedWork,
			"continued", w.Continued,
		)
	}
	return outcome, nil
}

// Discard 不交付任何东西地拆掉一个 worktree：注销它、删除目录、丢弃其分支。
//
// 只用于「agent 运行之前就放弃」的情形。Finalize 是保留工作的路径；本方法假定
// 没有工作可保留，调用方必须确定 worktree 里还没有任何东西运行过。
func (w *LocalWorktree) Discard(logger *slog.Logger) {
	if w == nil {
		return
	}
	unlock, err := lockGitRoot(w.GitRoot, logger)
	if err != nil {
		// 按契约尽力而为：下面每一步失败都只记日志。留下的登记会被本仓库的下一次
		// prepare 清理——与崩溃 daemon 走同一条自愈路径。
		if logger != nil {
			logger.Warn("execenv: could not lock the repository to discard the task worktree",
				"git_root", w.GitRoot, "path", w.Path, "branch", w.Branch, "error", err)
		}
		return
	}
	defer unlock()
	removeLocalWorktreeDir(w.GitRoot, w.Path, logger)
	// 与其它所有拆除相同的规则：本次 prepare 没创建的分支属于它之前的轮次，
	// 活得比这个任务久。
	if w.createdBranch {
		dropBranch(w.GitRoot, w.Branch, logger)
	}
	if logger != nil {
		logger.Info("execenv: local worktree discarded before the agent ran",
			"git_root", w.GitRoot, "path", w.Path, "branch", w.Branch, "branch_dropped", w.createdBranch)
	}
}

// AbortWithReason 把 worktree 标记为不可交付。此后 Finalize 不提交、不删除任何东西，
// 并返回一条点出保留路径的错误。
//
// 它存在的原因是「这条分支交付是否安全」的决定发生在包外——daemon 知道自己那趟
// sidecar 清理是否成功——而能对决定采取行动的只有 Finalize。最先写入的原因胜出：
// 它最接近根因。
func (w *LocalWorktree) AbortWithReason(err error) {
	if w == nil || err == nil || w.aborted != nil {
		return
	}
	w.aborted = err
}

// commitBaseline 把用户重放后的未提交状态记为任务分支上的一个独立提交，返回新的
// tip。续接的分支上，该状态是「自上一轮以来的增量」，所以提交信息如实说明，而不是
// 自称是分支的 baseline。
//
// 新分支即使没什么可记录的也会得到一个（空）提交，而这正是关键：那是分支第一个
// 属于自己的提交，是日后能把它与用户在同一位置创建的分支区分开的东西——见
// LocalWorktree.BaseCommit。
func commitBaseline(worktreePath string, continued, dirty bool) (string, error) {
	message := "chore(agent): baseline — uncommitted work from the local directory"
	switch {
	case continued:
		message = "chore(agent): uncommitted work from the local directory since the previous turn"
	case !dirty:
		message = "chore(agent): baseline — the task worktree started here"
	}
	if _, err := commitEverything(worktreePath, message, !dirty); err != nil {
		return "", err
	}
	tip, err := runGitTrimmed(worktreePath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve baseline commit: %w", err)
	}
	return tip, nil
}

// commitAll 暂存并提交 agent 留下的所有内容。返回是否真的创建了一次提交；返回错误
// 意味着改动仍只在磁盘上，调用方绝不能删除 worktree。
func (w *LocalWorktree) commitAll(logger *slog.Logger) (bool, error) {
	// 这里绝不 --allow-empty：一次空提交会让只读轮次看起来产出了工作，并把它的
	// 分支留下来。
	return commitEverything(w.Path, "chore(agent): uncommitted changes from task", false)
}

// commitEverything 对良性「没有东西可提交」返回 (false, nil)，对真正的失败返回
// (false, err)——调用方需要这个区分来决定树是否可以安全丢弃。
func commitEverything(worktreePath, message string, allowEmpty bool) (bool, error) {
	if out, err := runGit(worktreePath, "add", "-A"); err != nil {
		return false, fmt.Errorf("git add: %s: %w", strings.TrimSpace(out), err)
	}
	// --no-verify：用户的 commit hooks 是为用户自己的工作流写的（交互式 lint、测试
	// 套件、签名提示），一次 hook 失败在这里意味着为省一次 lint 而丢 agent 的工作。
	// 注意它并不会禁用 commit.gpgSign——这正是调用方必须把「提交失败」当作「保留
	// worktree」而不是一声警告的原因。
	args := append(commitIdentityArgs(worktreePath), "commit", "--no-verify")
	if allowEmpty {
		args = append(args, "--allow-empty")
	}
	args = append(args, "-m", message)
	if out, err := runGit(worktreePath, args...); err != nil {
		if strings.Contains(out, "nothing to commit") {
			return false, nil
		}
		return false, fmt.Errorf("git commit: %s: %w", strings.TrimSpace(out), err)
	}
	return true, nil
}

// commitIdentityArgs 只在仓库本身没有提交者身份时提供一份。配了 user.email 的仓库
// 保留它，使提交看起来仍出自用户自己的配置。
func commitIdentityArgs(dir string) []string {
	if email, err := runGitTrimmed(dir, "config", "user.email"); err == nil && email != "" {
		return nil
	}
	return []string{
		"-c", "user.name=Multica Agent",
		"-c", "user.email=agent@multica.local",
	}
}

func worktreeIsDirty(worktreePath string) (bool, error) {
	out, err := runGit(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %s: %w", strings.TrimSpace(out), err)
	}
	return strings.TrimSpace(out) != "", nil
}

// removeLocalWorktreeDir 从用户仓库注销 worktree 并删除其目录。分支被刻意留在
// 原地——它是任务的交付物。
func removeLocalWorktreeDir(gitRoot, worktreePath string, logger *slog.Logger) error {
	var removeErr error
	if out, err := runGit(gitRoot, "worktree", "remove", "--force", worktreePath); err != nil {
		removeErr = err
		if logger != nil {
			logger.Warn("execenv: git worktree remove failed; pruning registration",
				"path", worktreePath, "output", out, "error", err)
		}
		// 回退：自己删除目录并清掉现在悬空的登记，别让用户仓库列着一个已不存在的
		// worktree。
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			removeErr = errors.Join(removeErr, rmErr)
			if logger != nil {
				logger.Warn("execenv: remove worktree directory failed", "path", worktreePath, "error", rmErr)
			}
		}
		if out, pruneErr := runGit(gitRoot, "worktree", "prune"); pruneErr != nil && logger != nil {
			logger.Warn("execenv: git worktree prune failed", "output", out, "error", pruneErr)
		}
	}
	// 用 Lstat 而不是 Stat 确认路径条目本身消失：Stat 会把损坏的 symlink 当成
	// 不存在，即便一个陈旧条目仍占着移交路径。
	if _, statErr := os.Lstat(worktreePath); errors.Is(statErr, os.ErrNotExist) {
		return nil
	} else if statErr != nil {
		return fmt.Errorf("confirm worktree removal: %w", statErr)
	}
	if removeErr != nil {
		return fmt.Errorf("worktree directory still exists after removal fallback: %w", removeErr)
	}
	return errors.New("worktree directory still exists after git removal reported success")
}

// deleteBranch 丢弃一条「没带任何值得保留内容」的任务分支——只读空跑，或中途失败
// 的 prepare。尽力而为：留下分支只是不整洁，从不造成危害。
func deleteBranch(gitRoot, branch string, logger *slog.Logger) {
	if branch == "" {
		return
	}
	if out, err := runGit(gitRoot, "branch", "-D", branch); err != nil && logger != nil {
		logger.Warn("execenv: delete task branch failed (non-fatal)",
			"branch", branch, "output", out, "error", err)
	}
}

// resolveGitRoot 返回包含 dir 的仓库根。worktree 模式是按资源逐个选择的，所以这里
// 出现非 git 目录是需要用户看到并修复的配置错误——关闭即失败并给出可操作的提示，
// 而不是静默降级到 in-place 锁（那会让用户困惑：为什么他们的任务还在排队）。
func resolveGitRoot(dir string) (string, error) {
	root, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return "", fmt.Errorf("execenv: local_directory %q is not a git repository, "+
			"but its project resource is set to execution_mode=worktree; "+
			"initialise a repository there or switch the resource back to in_place", dir)
	}
	// EvalSymlinks 使根路径与 git 之后在 worktree 内报告的一致——macOS 上
	// /tmp 与 /private/tmp 否则会让同一个仓库产生两把不同的锁。
	if resolved, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
		root = resolved
	}
	return filepath.Clean(root), nil
}

// captureUserSnapshot 把用户的工作目录记成一个提交：已跟踪的修改「和」未跟踪但未
// 被忽略的文件，合并为以用户 HEAD 为父的一棵树。
//
// 用单一快照而非旧式拆分——`git stash create` 管已跟踪改动、文件复制管未跟踪文件
// ——是因为续接的分支必须回答「用户在我已携带的那一轮之后改了些什么」，而这个问题
// 对「从未被任何快照记录过的文件」无法回答：agent 随后编辑过的一个未跟踪文件，会
// 每一轮都被用用户更早的版本重新复制，或者它的后来删除永远不会被携带。树还能表达
// 删除与模式变更，文件复制做不到。
//
// 这里不碰用户的索引、工作树或 refs。GIT_INDEX_FILE 指向任务 env root 里的私有
// 索引，所以 `git add` 只写 blob 对象和那个文件——这也让捕获免疫于过去能终结任务的
// .git/index.lock 竞态（#7434）：唯一会被取用的锁是我们自己临时文件上的。
func captureUserSnapshot(gitRoot, envRoot, headSHA string, logger *slog.Logger) (string, error) {
	if envRoot == "" {
		return "", errors.New("execenv: user snapshot requires an env root to build its index in")
	}
	indexPath := filepath.Join(envRoot, snapshotIndexFileName)
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("clear snapshot index: %w", err)
	}
	// 它是临时空间，不是任务状态：env root 本来就会交给 agent。
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}

	// 从用户自己的索引播种，使 git 能信它的 stat 缓存，而不必每一轮重哈希整个仓库。
	// 一份缺失或损坏的副本不是失败——read-tree 会重建一份正确但更「冷」的索引——
	// 所以回退在 add 的任何错误上都会跑，而不只在复制失败时。
	seeded := seedSnapshotIndex(gitRoot, indexPath)
	addArgs := append([]string{"add", "-A", "--"}, snapshotExcludes()...)
	if out, err := runGitEnv(gitRoot, env, addArgs...); err != nil {
		if !seeded {
			return "", fmt.Errorf("git add: %s: %w", strings.TrimSpace(out), err)
		}
		if logger != nil {
			logger.Debug("execenv: snapshot index seeded from the repository index was unusable; rebuilding it",
				"git_root", gitRoot, "output", strings.TrimSpace(out), "error", err)
		}
		if out, resetErr := runGitEnv(gitRoot, env, "read-tree", headSHA); resetErr != nil {
			return "", fmt.Errorf("git read-tree: %s: %w", strings.TrimSpace(out), resetErr)
		}
		if out, retryErr := runGitEnv(gitRoot, env, addArgs...); retryErr != nil {
			return "", fmt.Errorf("git add: %s: %w", strings.TrimSpace(out), retryErr)
		}
	}
	tree, err := runGitTrimmedEnv(gitRoot, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("git write-tree: %w", err)
	}
	// identity args 覆盖没有配置 user.email 的仓库：写提交对象必须有 committer，
	// 没有它们，用户的未提交工作会因这种技术性原因被丢弃。
	args := append(commitIdentityArgs(gitRoot), "commit-tree", tree, "-p", headSHA, "-m",
		"multica: local directory snapshot\n\nThe tree of this commit is the user's working directory as a task saw it.")
	snapshot, err := runGitTrimmed(gitRoot, args...)
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %w", err)
	}
	return snapshot, nil
}

// seedSnapshotIndex 把仓库的索引复制到 path，返回是否成功拿到一份。用「读普通文件」
// 的方式而非经由 git：git 会想拿索引锁，而这份复制存在的目的恰恰是不等那把锁。
func seedSnapshotIndex(gitRoot, path string) bool {
	src, err := runGitTrimmed(gitRoot, "rev-parse", "--git-path", "index")
	if err != nil || src == "" {
		return false
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(gitRoot, src)
	}
	return copyFile(src, path) == nil
}

// snapshotExcludes 把 daemon 自己的 sidecar 挡在用户快照之外。同一路径上有 in_place
// 任务在跑、或被杀死而没跑完清理时，它们是用户目录里的未跟踪文件；把它们带进来会
// 把另一个 issue 的 brief 放进本任务的 worktree——agent 会把它读成自己的上下文——
// 还会提交到分支。任意深度都匹配，因为 in_place 资源可能指向本仓库的子目录。
func snapshotExcludes() []string {
	specs := make([]string, 0, len(multicaSidecarDirNames))
	for _, name := range multicaSidecarDirNames {
		specs = append(specs, ":(exclude,glob)**/"+name+"/**")
	}
	return specs
}

// branchOwner 是任务分支所属的会话。随分支快照一起记录，并在任何后续任务续接前
// 比对：分支「名字」携带人类可读的 issue 键——用户能照打、两个 workspace 也可能铸出
// 完全相同的它——因此光凭名字永远无法确立「这条分支属于我们、可追加」（MUL-6881
// review）。
type branchOwner struct {
	WorkspaceID    string
	AgentID        string
	ConversationID string
}

func (o branchOwner) valid() bool {
	return o.WorkspaceID != "" && o.AgentID != "" && o.ConversationID != ""
}

// fingerprint 是同一身份的稳定、抗碰撞形式；当可读名已被别人的分支占用时，用它给
// 分支命名。跨轮次稳定，所以回退分支同样能被续接。
func (o branchOwner) fingerprint() string {
	sum := sha256.Sum256([]byte(o.WorkspaceID + "\x00" + o.AgentID + "\x00" + o.ConversationID))
	return hex.EncodeToString(sum[:])[:12]
}

const (
	ownerTrailerWorkspace    = "Multica-Workspace"
	ownerTrailerAgent        = "Multica-Agent"
	ownerTrailerConversation = "Multica-Conversation"
)

// branchRecord 是 refs/multica/local-state/<branch> 持有的内容：一个提交，其 TREE 是
// 分支最后携带的用户目录、其「第二个父」是那一刻的分支 tip、其提交信息点名 owner。
//
// checkpoint 正是让记录是关于「这条分支」而非仅关于「这个名字」的东西。单有 owner
// 只能证明 Multica 曾写过一条叫这个名字的分支，这在用户删了它、又用同名建了自己
// 的分支之后依然成立——下一个任务于是续接到了用户的工作里（MUL-6881 review）。
// 要求 checkpoint 仍是 tip 的祖先，就是连续性证明：一条被删又重建、被 force 移上
// 无关历史、或被 rebase 过的分支，不再包含这里记录的提交。
type branchRecord struct {
	// state 是其树为「分支携带的用户快照」的提交。
	state string
	// checkpoint 是这份记录对之写入时的分支 tip。
	checkpoint string
	owner      branchOwner
}

// writeBranchRecord 把分支记录成「在 checkpoint 处携带 userState」，并把分支的 ref
// 指向这份记录。
//
// checkpoint 由调用方传入，绝不在这里从分支 ref 重读：调用方知道它实际交付了哪个
// 提交，而 ref 是用户的、可能在这交付与本写入之间移动。记录「我们交付了什么」意味着
// 在窗口内移动过的分支下次只是祖先测试不过——这是安全方向。
func writeBranchRecord(gitRoot, branch, userState, checkpoint string, owner branchOwner) (string, error) {
	if checkpoint == "" {
		return "", fmt.Errorf("no checkpoint to record for branch %s", branch)
	}
	args := append(commitIdentityArgs(gitRoot), "commit-tree", userState+"^{tree}",
		"-p", userState, "-p", checkpoint, "-m", branchRecordMessage(owner))
	record, err := runGitTrimmed(gitRoot, args...)
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %w", err)
	}
	if out, err := runGit(gitRoot, "update-ref", userStateRef(branch), record); err != nil {
		return "", fmt.Errorf("git update-ref: %s: %w", strings.TrimSpace(out), err)
	}
	return record, nil
}

func branchRecordMessage(owner branchOwner) string {
	var b strings.Builder
	b.WriteString("multica: task branch record\n\n")
	b.WriteString("Written by Multica for a local_directory task running in worktree mode. Its\n")
	b.WriteString("tree is the user's working directory as this branch last carried it, and its\n")
	b.WriteString("second parent is the branch tip at that moment — together they let the next\n")
	b.WriteString("turn replay only what changed since, and prove the branch is still the one\n")
	b.WriteString("recorded here. Safe to delete along with the branch.\n\n")
	fmt.Fprintf(&b, "%s: %s\n", ownerTrailerWorkspace, owner.WorkspaceID)
	fmt.Fprintf(&b, "%s: %s\n", ownerTrailerAgent, owner.AgentID)
	fmt.Fprintf(&b, "%s: %s\n", ownerTrailerConversation, owner.ConversationID)
	return b.String()
}

// readBranchRecord 读回「分支被记录成携带什么」。缺少全部三个 trailer 或没有第二
// 个父的提交——即不是 writeBranchRecord 写出的——会得到一份永远匹配不上任何有效
// owner 的记录。
func readBranchRecord(gitRoot, commit string) (branchRecord, error) {
	body, err := runGitTrimmed(gitRoot, "log", "-1", "--format=%B", commit)
	if err != nil {
		return branchRecord{}, err
	}
	record := branchRecord{state: commit}
	for _, line := range strings.Split(body, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case ownerTrailerWorkspace:
			record.owner.WorkspaceID = value
		case ownerTrailerAgent:
			record.owner.AgentID = value
		case ownerTrailerConversation:
			record.owner.ConversationID = value
		}
	}
	if checkpoint, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", commit+"^2"); err == nil {
		record.checkpoint = checkpoint
	}
	return record, nil
}

// addLocalWorktree 创建 worktree；当分支已存在时（重新派发的任务保留同一 id，因此
// 它的分支可能从上一次运行活下来），会带着带后缀的分支名重试一次。
// taskBranchPlan 是「任务 worktree 检出哪条分支、从哪个提交出发」的决策。
type taskBranchPlan struct {
	// name 是要检出或创建的分支。
	name string
	// base 是 worktree 出发的提交。
	base string
	// continues 为 true 表示 base 是更早一轮的分支 tip 而非用户 HEAD，检出已经携带
	// 那一轮的成果。
	continues bool
	// priorState 是那条分支「已记录为携带」的用户快照。仅 continues 为 true 时设置；
	// 它是本轮重放的合并基准。
	priorState string
	// priorCheckpoint 是那条分支被记录时所在、且至今仍包含的提交——本轮证明分支是
	// 该会话所有的依据。本轮出发的分支 tip 由它衍生，所以 prepare 之后的一切度量都
	// 对准那个 tip 而非它。
	priorCheckpoint string
	// reset 为 true 表示会话分支存在但已完全并入 HEAD，本任务在 HEAD 处重启它。
	reset bool
	// conversational 为 true 表示 name 挂在会话而非本任务上，兄弟任务按理也可以
	// 想要它。
	conversational bool
	// tracksState 为 true 表示后续轮次可能续接这条分支，因此必须记录用户目录当时
	// 的样子。
	tracksState bool
	// owner 是分支登记在其下的身份。
	owner branchOwner
}

// altName 为「活动兄弟任务已占着」的分支给出消歧名。
func (p taskBranchPlan) altName(taskID string) string {
	if p.conversational {
		// 任务级命名，且要与它分叉来源的会话分支并列可读：feature/j/mul-6881-<task>。
		return p.name + "-" + taskKey(taskID)
	}
	// 已经是任务级，因此只有同一任务的重跑才会在这里撞名。
	return fmt.Sprintf("%s-%d", p.name, time.Now().Unix())
}

// resolveTaskBranch 为任务挑选分支。
//
// 归属同一会话的任务——同一 issue、同一聊天会话的轮次——共享一条分支，因为它们是
// 同一条工作线：用户说「顺带把调用方也修了」，期望 agent 站在它一分钟前写的东西上。
// 若把分支挂在任务上，则每一轮都从 HEAD 分叉出自己的分支，第二轮于是从一个不含
// 第一轮成果的树开始，而且没有任何信号提示这一点（MUL-6881）。per-task 的 env root
// 保持原样——兄弟任务仍然并发跑，只是都交付到它们会话拥有的那条分支上。
//
// 是否续接一条分支，由它「记录的 OWNER」决定，绝不看名字。`feature/j/mul-6881` 是
// 用户能照打的名字、另一个同名展示 agent 也能造出来、第三个 workspace 更可以为另一
// 个 issue 铸出它；往其中任何一个追加都会静默地混两条工作线。所以一条同名却无法
// 证明属于我们的分支，会把本任务推到 `feature/j/mul-6881-<fingerprint>`——对这个确切
// 会话稳定，所以它自己的后续轮次继续它——万一连它也被人占着，就落到一条不续接
// 任何东西的任务级分支上。
func resolveTaskBranch(gitRoot string, params LocalWorktreeParams, headSHA string, logger *slog.Logger) taskBranchPlan {
	agentSegment := sanitizeName(params.AgentName)
	taskScoped := taskBranchPlan{name: fmt.Sprintf("feature/%s/%s", agentSegment, taskKey(params.TaskID)), base: headSHA}

	owner := params.owner()
	if params.ConversationKey == "" || !owner.valid() {
		return taskScoped
	}
	preferred := fmt.Sprintf("feature/%s/%s", agentSegment, sanitizeName(params.ConversationKey))
	for _, name := range []string{preferred, preferred + "-" + owner.fingerprint()} {
		plan, ok := planForConversationBranch(gitRoot, name, headSHA, owner, logger)
		if ok {
			return plan
		}
		if logger != nil {
			logger.Info("execenv: branch exists but is not this conversation's; not continuing it",
				"git_root", gitRoot, "branch", name)
		}
	}
	return taskScoped
}

// planForConversationBranch 报告本任务将如何使用一个候选分支名，以及它是否可用它。
func planForConversationBranch(gitRoot, name, headSHA string, owner branchOwner, logger *slog.Logger) (taskBranchPlan, bool) {
	plan := taskBranchPlan{
		name:           name,
		base:           headSHA,
		conversational: true,
		tracksState:    true,
		owner:          owner,
	}
	tip, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	if err != nil || tip == "" {
		// 可以自由创建。
		return plan, true
	}
	record, owned := branchOwnedBy(gitRoot, name, owner, logger)
	if !owned {
		return taskBranchPlan{}, false
	}
	// 用户已合并它：分支 tip 不携带任何 HEAD 没有的东西，从它续接会让任务滞留于
	// 用户自己提交的身后。
	if _, mergedErr := runGit(gitRoot, "merge-base", "--is-ancestor", name, headSHA); mergedErr == nil {
		plan.reset = true
		return plan, true
	}
	plan.base = tip
	plan.continues = true
	plan.priorState = record.state
	plan.priorCheckpoint = record.checkpoint
	if logger != nil {
		logger.Info("execenv: continuing the conversation's existing branch",
			"git_root", gitRoot, "branch", name, "tip", tip)
	}
	return plan, true
}

// branchOwnedBy 报告一条分支是否仍是本会话记录的那条，并返回它所携带的用户快照。
//
// 两个问题必须同时成立。其一，记录是否属于我们——workspace、agent、会话三个 id。
// 其二，分支是否仍是当初对之写入的那条——记录的 checkpoint 必须是当前 tip 的祖先。
// 第二问不是吹毛求疵：一条被用户删除后以同名重建的分支依然满足第一问，续接它等于
// 把这段会话追加到用户无关的工作上。
//
// 没有记录的分支按定义不属于我们：本代码创建的每条分支都会在允许任务运行前写入
// 一份记录。
func branchOwnedBy(gitRoot, branch string, owner branchOwner, logger *slog.Logger) (branchRecord, bool) {
	ref, err := readUserStateRef(gitRoot, branch)
	if err != nil || ref == "" {
		return branchRecord{}, false
	}
	record, err := readBranchRecord(gitRoot, ref)
	if err != nil || record.owner != owner || record.checkpoint == "" {
		return branchRecord{}, false
	}
	if _, err := runGit(gitRoot, "merge-base", "--is-ancestor", record.checkpoint, "refs/heads/"+branch); err != nil {
		if logger != nil {
			logger.Info("execenv: branch no longer contains the commit it was recorded at; not continuing it",
				"git_root", gitRoot, "branch", branch, "checkpoint", record.checkpoint)
		}
		return branchRecord{}, false
	}
	return record, true
}

// addLocalWorktree 把计划好的分支物化成 worktree，并报告实际用到的分支、以及这次
// 调用是否就是把它放到那里的人——调用方只能删除自己创建的分支。
//
// 回退覆盖「兄弟任务已占着会话分支」的情形：git 每条分支只允许一个 worktree，而拒
// 绝运行比交付到一条任务级分支更糟。它从同一个 base 分叉，因此兄弟仍站在会话最新
// 的成果上。
func addLocalWorktree(gitRoot, worktreePath string, plan taskBranchPlan, taskID string) (string, bool, error) {
	var args []string
	switch {
	case plan.continues:
		args = []string{"worktree", "add", worktreePath, plan.name}
	case plan.reset:
		// -B 把分支移动到 base。没有丢失任何东西：此路径只在分支被证明是 HEAD 的
		// 祖先之后运行。
		args = []string{"worktree", "add", "-B", plan.name, worktreePath, plan.base}
	default:
		args = []string{"worktree", "add", "-b", plan.name, worktreePath, plan.base}
	}
	out, err := runGit(gitRoot, args...)
	if err == nil {
		return plan.name, !plan.continues, nil
	}
	if !branchUnavailable(out) {
		return "", false, fmt.Errorf("execenv: git worktree add: %s: %w", strings.TrimSpace(out), err)
	}
	alt := plan.altName(taskID)
	if out, err := runGit(gitRoot, "worktree", "add", "-b", alt, worktreePath, plan.base); err != nil {
		return "", false, fmt.Errorf("execenv: git worktree add: %s: %w", strings.TrimSpace(out), err)
	}
	return alt, true, nil
}

// branchUnavailable 识别 git 因「分支被别的 worktree 持有」或「分支已在我们打算
// 创建的名字下存在」而拒绝的情况。
func branchUnavailable(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "already checked out") ||
		strings.Contains(lower, "already used by worktree")
}

// replayResult 是重放留在 worktree 里的结果。
type replayResult struct {
	// conflicts 列出 git 无法合并的文件。非空表示 worktree 有意持有一个未解决的
	// 合并。
	conflicts []string
}

// replayUserState 把用户的目录带进 worktree。
//
// 两类分支对不同的出发点执行同一操作：cherry-pick「检出已携带状态」与「用户当前
// 状态」之间的差异。从 HEAD 分叉的分支，前者就是 HEAD 本身，所以整个快照原样适用、
// 不可能冲突。续接的分支，前者是它记录的快照——这使它成为对用户「上一轮→现在」
// 改动的重放，而不是对整棵用户树的重放。
//
// 把整棵树重放到续接分支上是诱人而错误的版本：那次合并以用户 HEAD 为基准，会把
// 分支已有的工作重新提议一遍，并与 agent 对同样几行的改动冲突——也就是说，它恰好
// 在 agent 做了被要求的事时冲突。已被证实：在轮次之间用户目录保持不动的前提下，
// 一条朴素的 `stash apply` 到分支 tip 就已经失败。
//
// 这里的冲突是真实的意见分歧——用户改写了几行 agent 也改写过的行——它留在 worktree
// 里由 agent 用普通 git 命令解决，这既是 agent 的本职，也是用户较新的改动得以存活的
// 唯一方式。丢弃它会让那次改动双重丢失：一次丢出本轮树，再一次丢出之后每一轮——
// 因为快照会越过分支从未采纳的改动前进。
func replayUserState(worktreePath string, plan taskBranchPlan, snapshot string, logger *slog.Logger) (replayResult, error) {
	carried := plan.base
	if plan.continues {
		carried = plan.priorState
	}
	if carried == "" {
		return replayResult{}, fmt.Errorf("execenv: no baseline to replay the local directory against for branch %s", plan.name)
	}
	// 自该检出已携带的状态以来没有新东西。后续轮次里这是常事：用户留了言，没改
	// 代码。
	if _, err := runGit(worktreePath, "diff", "--quiet", carried, snapshot); err == nil {
		return replayResult{}, nil
	}

	// 一个以「已携带状态」为父、以用户当前状态为树的提交。它的父正是 git 用作合并
	// 基准的东西，而这正是全部要点：没有别的办法可达它。
	args := append(commitIdentityArgs(worktreePath), "commit-tree", snapshot+"^{tree}", "-p", carried,
		"-m", "multica: local directory edits to replay")
	increment, err := runGitTrimmed(worktreePath, args...)
	if err != nil || increment == "" {
		return replayResult{}, fmt.Errorf("execenv: could not describe your local edits for replay into the task worktree: %w", err)
	}

	out, pickErr := runGit(worktreePath, "cherry-pick", "--no-commit", increment)
	if pickErr == nil {
		return replayResult{}, nil
	}
	conflicts, listErr := unmergedPaths(worktreePath)
	if listErr != nil || len(conflicts) == 0 {
		// 不是冲突，说明重放因 agent 无法解决的原因失败。关闭即失败，而不是从一棵
		// 半应用状态的树开始。
		abortCherryPick(worktreePath, logger)
		return replayResult{}, fmt.Errorf("execenv: could not replay your local edits into the task worktree "+
			"(the agent would have seen a different tree than you have): %s: %w", strings.TrimSpace(out), pickErr)
	}
	if !plan.continues {
		// 按构造不可达：全新分支检出在 increment 自己的父上，git 没有可争执的东西。
		// 若真发生，那棵树不是用户认得的样子，旧有的关闭即失败规则是对的。
		abortCherryPick(worktreePath, logger)
		return replayResult{}, fmt.Errorf("execenv: could not replay your local edits onto a fresh task worktree: %s: %w",
			strings.TrimSpace(out), pickErr)
	}

	// 保留冲突、只清掉 sequencer 状态：agent 应看到一个可以用 `git status` /
	// `git add` 解决的普通冲突 worktree，而不是一个需要用一个它从未发起的命令来
	// 收尾的 cherry-pick。
	if out, quitErr := runGit(worktreePath, "cherry-pick", "--quit"); quitErr != nil && logger != nil {
		logger.Warn("execenv: could not clear the cherry-pick state after a conflicting replay (non-fatal)",
			"path", worktreePath, "output", strings.TrimSpace(out), "error", quitErr)
	}
	if logger != nil {
		logger.Warn("execenv: your local edits since the previous turn conflict with the work on this branch; handing the conflict to the agent",
			"path", worktreePath, "branch", plan.name, "files", conflicts)
	}
	return replayResult{conflicts: conflicts}, nil
}

// quotedPaths 把人读信息中的仓库路径渲染成带引号的形式。要加引号是因为 git 路径
// 可能含换行、引号或控制字符，而这串文本会被读进日志和任务错误——裸路径在那会像
// 好几条记录。
func quotedPaths(paths []string) string {
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, strconv.Quote(path))
	}
	return strings.Join(quoted, ", ")
}

// verifyDeliveryPoint 检查 tip 是否是「这段会话可以把自己的名字盖上去」的提交，然后
// 它才会成为分支记录的 checkpoint。
//
// 断言两件事，而它们正是交付「不是本任务构建的东西」的两种途径。tip 必须「就是」
// 任务的分支——一次检出别的东西、或有人在下面移动了分支的运行，交付了一个本记录
// 不该描述的提交。且 tip 必须仍包含本轮出发的那个提交——本轮自己做了 baseline 就用
// baseline，否则用续接的分支 tip。一次把 worktree reset 回用户自己 HEAD 的运行两条
// 都过不了，但第二条才是关键，而且双倍关键：把一个普通用户提交记录成 checkpoint，
// 正是让「他们之后在那里重建的分支」看起来像我们的东西；而没有本轮出发点的 tip，
// 不再携带即将被记录成「已交付」的快照（MUL-6881 review）。
func (w *LocalWorktree) verifyDeliveryPoint(tip string) error {
	if !w.tracksState {
		// 这条分支不会记录任何东西，所以没什么可证明。
		return nil
	}
	if tip == "" {
		return errors.New("the task worktree has no resolvable HEAD")
	}
	branchTip, err := runGitTrimmed(w.GitRoot, "rev-parse", "--verify", "refs/heads/"+w.Branch)
	if err != nil {
		return fmt.Errorf("resolve branch %s: %w", w.Branch, err)
	}
	if branchTip != tip {
		return fmt.Errorf("the worktree delivered %s while branch %s points at %s, so the run did not deliver onto its own branch",
			shortID(tip), w.Branch, shortID(branchTip))
	}
	if w.BaseCommit == "" {
		return fmt.Errorf("branch %s has no commit of this task's own to prove it by", w.Branch)
	}
	if _, err := runGit(w.GitRoot, "merge-base", "--is-ancestor", w.BaseCommit, tip); err != nil {
		return fmt.Errorf("the delivered commit %s no longer contains %s, the commit this turn started from",
			shortID(tip), shortID(w.BaseCommit))
	}
	return nil
}

// unmergedPaths 列出 worktree 中 git 认为尚未解决的文件。
func unmergedPaths(worktreePath string) ([]string, error) {
	out, err := runGitStdout(worktreePath, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range strings.Split(out, "\x00") {
		if name != "" {
			paths = append(paths, name)
		}
	}
	return paths, nil
}

// abortCherryPick 把 worktree 恢复到分支 tip。只在「冲突不是 agent 能处理的东西」时
// 使用；普通冲突路径刻意保留 git 留下的 worktree。
func abortCherryPick(worktreePath string, logger *slog.Logger) {
	for _, args := range [][]string{{"cherry-pick", "--quit"}, {"reset", "--hard", "HEAD"}, {"clean", "-fdq"}} {
		if out, err := runGit(worktreePath, args...); err != nil && logger != nil {
			logger.Warn("execenv: could not restore the task worktree after a failed replay",
				"path", worktreePath, "command", args[0], "output", strings.TrimSpace(out), "error", err)
		}
	}
}

// userStateRef 是分支记录存放的位置：它已携带的用户目录快照、所属会话、以及它被
// 记录时的 tip。
func userStateRef(branch string) string {
	return localStateRefPrefix + branch
}

// readUserStateRef 返回记录的快照；分支没有记录时返回 ""。
func readUserStateRef(gitRoot, branch string) (string, error) {
	return runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", userStateRef(branch))
}

// recordState 把「用户目录在本任务眼中的样子」与分支所在的提交钉在一起。两样东西
// 依赖这份记录：下一轮从它的树重放，之后每个任务凭它的 owner 与 checkpoint 证明
// 分支仍是自己的。
//
// checkpoint 必须是「分支不可能在缺了这段会话工作时恰好停着」的提交——这就是全部
// 证明。仍是用户自己 HEAD 的 tip 不合格：那正是用户删除并重建分支后落下的位置，把
// 它记录进去等于授权后续轮次追加到用户无关的工作上。因此 Prepare 只在分支带上我们
// 的提交后记录，Finalize 记录它实际交付的 tip。
func (w *LocalWorktree) recordState(checkpoint string, logger *slog.Logger) error {
	if w == nil || !w.tracksState || w.Branch == "" || w.userState == "" {
		return nil
	}
	if _, err := writeBranchRecord(w.GitRoot, w.Branch, w.userState, checkpoint, w.owner); err != nil {
		return err
	}
	if logger != nil {
		logger.Debug("execenv: recorded the local-directory snapshot for the task branch",
			"branch", w.Branch, "checkpoint", checkpoint)
	}
	return nil
}

// dropBranch 删除一条「没带任何值得保留内容」的任务分支，连同它记录的快照——两者
// 分开都毫无意义。
func dropBranch(gitRoot, branch string, logger *slog.Logger) {
	if branch == "" {
		return
	}
	deleteBranch(gitRoot, branch, logger)
	if out, err := runGit(gitRoot, "update-ref", "-d", userStateRef(branch)); err != nil && logger != nil {
		logger.Debug("execenv: no local-directory snapshot to drop for task branch",
			"branch", branch, "output", strings.TrimSpace(out))
	}
}

// pruneOrphanedStateRefs 清掉「分支已不存在」的快照。Multica 会两者一起删，但分支
// 是用户的，随时可被删除、重命名或合并走；留下的 ref 会把用户的整棵工作树按某个
// 过去的轮次钉住，永远对抗 `git gc`。
//
// 尽力而为、非致命：这是在用户仓库里的保洁，不是任务的前提。
func pruneOrphanedStateRefs(gitRoot string, logger *slog.Logger) {
	out, err := runGitTrimmed(gitRoot, "for-each-ref", "--format=%(refname)", localStateRefPrefix)
	if err != nil {
		if logger != nil {
			logger.Debug("execenv: could not list local-directory snapshots", "git_root", gitRoot, "error", err)
		}
		return
	}
	for _, ref := range strings.Split(out, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		branch := strings.TrimPrefix(ref, localStateRefPrefix)
		if branch == ref {
			continue
		}
		if _, headErr := runGit(gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); headErr == nil {
			continue
		}
		if out, delErr := runGit(gitRoot, "update-ref", "-d", ref); delErr != nil {
			if logger != nil {
				logger.Warn("execenv: could not drop the snapshot of a deleted task branch (non-fatal)",
					"ref", ref, "output", strings.TrimSpace(out), "error", delErr)
			}
			continue
		}
		if logger != nil {
			logger.Info("execenv: dropped the local-directory snapshot of a branch that no longer exists",
				"git_root", gitRoot, "branch", branch)
		}
	}
}

// checkUntrackedReplayable 在任何内容被写入任何地方之前，先拒绝「未跟踪内容大到
// 无法忠实重现」的目录。
//
// 上限与旧式文件复制重放强制的一致，存在理由也相同：`--exclude-standard` 已经丢弃
// 所有被 gitignore 的内容，越过上限的仓库意味着构建产物从未被忽略。把那些字节快照
// 下来等于写进用户自己的对象库。未跟踪 symlink 的情形被拒绝则出于更窄的理由——它
// 是用户看得见的内容，而本次重放不负责决定复制链接还是其目标（包括仓库外的目标）。
func checkUntrackedReplayable(gitRoot string, logger *slog.Logger) error {
	out, err := runGitStdout(gitRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("execenv: could not list the untracked files in %q: %w", gitRoot, err)
	}
	var (
		files   int
		budget  int64 = maxUntrackedBytes
		skipped int
	)
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" || isMulticaSidecarPath(rel) {
			continue
		}
		info, statErr := os.Lstat(filepath.Join(gitRoot, rel))
		if statErr != nil {
			// 刚才还在清单里，现在不可读：树在我们脚下变了。git 同样找不到它，
			// 所以这不算拒绝。
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			skipped++
			if logger != nil {
				logger.Warn("execenv: untracked symlink cannot be replayed into a worktree", "file", rel)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			// socket、FIFO、设备：不是内容，git 也不会添加。
			continue
		}
		files++
		budget -= info.Size()
		if files > maxUntrackedFiles || budget < 0 {
			skipped++
		}
	}
	if skipped == 0 {
		return nil
	}
	return fmt.Errorf("execenv: cannot replay every untracked file from %q into a task worktree "+
		"(%d left over; the replay covers regular files up to %d files / %d MiB and does not follow symlinks) "+
		"— gitignore or clean up the untracked files, or switch the resource back to in_place",
		gitRoot, skipped, maxUntrackedFiles, maxUntrackedBytes>>20)
}

// multicaSidecarDirNames 是 Prepare 会写进 workdir 的那些目录。同一目录上若有 in_place
// 任务正在跑，这些目录会以未跟踪文件的形式存在整个运行期，因此并发的 worktree
// 快照会看到它们。CLAUDE.md / AGENTS.md 刻意缺席：它们通常是用户自己的已跟踪文件，
// 运行时只会往其中注入一块标记，而 CleanupRuntimeConfig 会移除它。
var multicaSidecarDirNames = []string{
	".agent_context",
	".multica",
}

// isMulticaSidecarPath 报告一个仓库相对路径是 daemon 自己的 sidecar 还是用户内容。
// 按「完整路径段」在任意深度匹配，而非只在仓库根：in_place 资源可能指向本仓库的
// 子目录，此时它的 sidecar 位于 <subdir>/.agent_context——重放它们会把另一个 issue
// 的 brief 放进本任务的 worktree，并提交到交付分支。
func isMulticaSidecarPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		for _, name := range multicaSidecarDirNames {
			if seg == name {
				return true
			}
		}
	}
	return false
}

// 只读取提交并更新远端跟踪引用，不切换或重置用户 checkout。
// 仓库的本机路径必须指向登记的远端；SSH 与 HTTPS 地址按仓库身份比较。
func matchingRemote(root, repositoryURL string) (string, error) {
	remotes, err := runGitTrimmed(root, "remote")
	if err != nil {
		return "", err
	}
	identity := func(raw string) string {
		raw = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(raw), "/"), ".git")
		if strings.Contains(raw, "://") {
			if u, e := url.Parse(raw); e == nil {
				raw = strings.ToLower(u.Hostname()) + "/" + strings.Trim(u.Path, "/")
			}
		} else if at := strings.Index(raw, "@"); at >= 0 {
			raw = strings.Replace(raw[at+1:], ":", "/", 1)
		}
		if strings.HasPrefix(strings.ToLower(raw), "github.com/") {
			raw = strings.ToLower(raw)
		}
		return raw
	}
	for _, name := range strings.Fields(remotes) {
		value, e := runGitTrimmed(root, "remote", "get-url", name)
		if e == nil && identity(value) == identity(repositoryURL) {
			return name, nil
		}
	}
	return "", fmt.Errorf("configured repository has no matching Git remote: %s", repositoryURL)
}

func remoteDefaultBranch(root, repositoryURL string) (string, error) {
	remote, err := matchingRemote(root, repositoryURL)
	if err != nil {
		return "", err
	}
	out, err := runGitEnv(root, []string{"GIT_TERMINAL_PROMPT=0"}, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", fmt.Errorf("read remote default branch: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" && strings.HasPrefix(fields[1], "refs/heads/") {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}
	return "", errors.New("remote has no default branch; configure a development branch")
}

func resolveCommittedBase(root, branch, repositoryURL string) (string, error) {
	remote, err := matchingRemote(root, repositoryURL)
	if err != nil {
		return "", err
	}
	ref := "refs/remotes/" + remote + "/" + branch
	if _, err = runGitEnv(root, []string{"GIT_TERMINAL_PROMPT=0"}, "fetch", "--no-tags", remote, "+refs/heads/"+branch+":"+ref); err != nil {
		return "", fmt.Errorf("fetch development branch %s: %w", branch, err)
	}
	remoteSHA, err := runGitTrimmed(root, "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	localSHA, err := runGitTrimmed(root, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return remoteSHA, nil
	}
	if _, err = runGit(root, "merge-base", "--is-ancestor", localSHA, remoteSHA); err == nil {
		return remoteSHA, nil
	}
	if _, err = runGit(root, "merge-base", "--is-ancestor", remoteSHA, localSHA); err == nil {
		return localSHA, nil
	}
	return "", fmt.Errorf("local and remote development branch %s diverged; integrate commits before starting a new task", branch)
}

func runGit(dir string, args ...string) (string, error) {
	return runGitEnv(dir, nil, args...)
}

// runGitEnv 是 runGit 加额外环境条目，供唯一需要重定向 GIT_INDEX_FILE 的调用方使用。
func runGitEnv(dir string, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runGitTrimmed 运行 git 取它的 stdout 值，丢弃 stderr，以免一行诊断文本被误当成
// 值（`rev-parse` 输出、config 值、stash sha）。
func runGitTrimmed(dir string, args ...string) (string, error) {
	out, err := runGitStdout(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runGitTrimmedEnv 是 runGitTrimmed 加额外环境条目。
func runGitTrimmedEnv(dir string, extraEnv []string, args ...string) (string, error) {
	out, err := runGitEnv(dir, extraEnv, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	return strings.TrimSpace(out), nil
}

// runGitStdout 是不做裁剪的 runGitTrimmed，用于空白有意义时的输出——NUL 分隔的文件
// 清单里，首尾空格本是文件名的一部分。
func runGitStdout(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", withGitStderr(err)
	}
	return string(out), nil
}

// withGitStderr 把 git 的 stderr 渲染进错误。
//
// cmd.Output() 早已把 stderr 收进 ExitError.Stderr，但从没人把它读出来，于是这条
// 路径的每次失败都以光秃秃的 "exit status 1" 抵达用户——git 自己的解释被收集起来、
// 又在最需要它的地方被扔掉了。从「结果」里丢弃 stderr 是有意的（见 runGitTrimmed：
// 诊断行绝不能与值混淆）；从「错误」里丢弃它则不是。
func withGitStderr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if msg := strings.TrimSpace(string(exitErr.Stderr)); msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
	}
	return err
}
