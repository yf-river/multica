// 侧车清单（sidecar manifest）：Prepare 会把在 agent WorkDir（含用户自己
// 的 local_directory 仓库）里创建的每个文件与中间目录记录成磁盘清单；
// Cleanup 依它把这些写入精确回滚。清单一侧"绝不覆盖已存在路径"的约束，
// 让清理永远是"删除我们创建的路径"，而不是"恢复被覆盖的内容"。

package execenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// sidecarManifestFile 是 Prepare 写进 envRoot 的磁盘 JSON 文件名，记录它在
// WorkDir 内创建的每一个文件与中间目录。CleanupSidecars 读回它，把工作目录
// 回滚到 Prepare 之前的状态。该文件住在 envRoot（daemon 暂存区），绝不住在
// WorkDir——否则 local_directory 运行会把"用来撤销垃圾的记账文件"本身留在
// 用户仓库里。
const sidecarManifestFile = ".multica_sidecar_manifest.json"

// errPathPreExists 是 recordWriteFile 在目标路径已存在时返回的哨兵错误。
// 清单契约是：我们绝不改动不归我们所有的路径——一个已存在的文件要么属于
// 用户，要么是某次崩溃运行的陈旧残留（我们无法安全地与有意为之的用户内容
// 区分），因此写入必须被拒绝，使清理能够成为对我们所建路径的纯删除。
//
// 调用方用两种方式处理它：
//
//   - 对按技能建目录的场景，调用方分配一个无冲突的备选 slug
//     （见 allocateCollisionFreeSkillDir）并重试，让 agent 仍能发现 Multica
//     技能，只是目录名不同。
//   - 对 Multica 专属命名空间（.agent_context/issue_context.md、
//     .multica/project/resources.json），调用方吞掉错误继续——agent 的运行时
//     brief 已携带本会出现在这些文件里的全部事实，所以"磁盘上没有"只是降级
//     行为，而非失败。
var errPathPreExists = errors.New("execenv: refuse to overwrite pre-existing path")

// sidecarManifest 记录 writeContextFiles 及其调用方为单次任务在 agent WorkDir
// 内做的文件系统改动。该清单是"让 local_directory 运行可按字节精确撤销"契约
// 的后半段：
//
//   - Files 列出我们创建的常规文件的绝对路径。只有在 recordWriteFile 验证过
//     目标"原本不存在"之后文件才会被记录；recordWriteFile 拒绝覆盖已存在
//     路径，因此"清单的存在性规则"与"写入侧拒绝覆盖规则"是同一个不变量从
//     两个方向看。
//   - Dirs 列出我们创建的目录的绝对路径，按"根在前"的创建顺序。Cleanup 逆序
//     遍历，先尝试最深的目录；对"用户此后又放入了内容"的目录（例如我们的
//     .claude/skills/issue-review 旁边出现了用户的 .claude/skills/my-own-skill）
//     rmdir 会报 ENOTEMPTY，此时静默跳过——用户内容被保留，且无需任何逐目录
//     记账。与文件同理，目录只在其"原本不存在"时才被记录。
//
// 清单刻意保持最小：只携带撤销我们的写入所需的路径，别无其他。它不是每次
// 操作的操作日志，也不能替代运行时配置标记区块——后者在 runtime_config.go
// 里有自己专门的往返机制（brief 是追加到用户已有的内容里，而非写进一个新的
// 侧车目录）。
type sidecarManifest struct {
	Files []string `json:"files,omitempty"`
	Dirs  []string `json:"dirs,omitempty"`
}

// recordMkdirAll 行为与 os.MkdirAll(path, perm) 相同，但额外把它不得不创建
// 的每个父目录（跳过任何已存在的）记录进 m，供 CleanupSidecars 日后 rmdir。
// 记录的路径按"根在前"追加；Cleanup 逆序迭代，因此最深的目录先被删除。
//
// 当 m 为 nil 时它与 os.MkdirAll 完全一致——Reuse 路径使用 nil 模式，因为
// Reuse 跑在由 GC 整棵抹掉的云端工作目录上，逐文件清理无关紧要，跟踪这些
// 目录只会留下陈旧的清单字节。
func recordMkdirAll(path string, perm os.FileMode, m *sidecarManifest) error {
	if path == "" {
		return os.MkdirAll(path, perm)
	}
	if m == nil {
		return os.MkdirAll(path, perm)
	}
	// 从叶子向上走，收集当前不存在的各级祖先。在第一个已存在的祖先（或文件
	// 系统根）处停下，于是用户原有的目录永远不会被记录——Cleanup 绝不能
	// rmdir 一条本任务开始前就归用户所有的路径。
	var toCreate []string
	cur := filepath.Clean(path)
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat ancestor %s: %w", cur, err)
		}
		toCreate = append(toCreate, cur)
		parent := filepath.Dir(cur)
		if parent == cur || parent == "." {
			break
		}
		cur = parent
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	// 反转为"根在前"顺序，让 Cleanup 能逆序迭代、从叶子向上逐层剥目录。
	for i, j := 0, len(toCreate)-1; i < j; i, j = i+1, j-1 {
		toCreate[i], toCreate[j] = toCreate[j], toCreate[i]
	}
	m.Dirs = append(m.Dirs, toCreate...)
	return nil
}

// recordWriteFile 以 perm 把 data 写入 path，并把该路径记进 m 供日后清理——
// 但仅当 path 尚不存在时。当 path 已被占用——不管是常规文件、符号链接、
// 目录还是其他任何文件系统条目——函数不改动该路径，直接返回 errPathPreExists。
// 用户的字节（或原有条目类型）被原样保留。
//
// 这是清单设计所依赖的不变量：清理是"对我们创建路径的纯删除"，绝不是
// "恢复"。覆盖一个已存在路径、又在清理时拒绝删除它（修复前的行为）等于两次
// 摧毁用户数据——一次发生在写入时，一次发生在退出时把被污染字节留在原处。
// 拒绝覆盖把该失败模式的两半一并消除。
//
// 当 m 为 nil 时退化为普通 os.WriteFile——Reuse 路径使用 nil 模式，因为 Reuse
// 跑在 GC 整棵抹掉的云端工作目录上，逐文件避免冲突无关紧要。
func recordWriteFile(path string, data []byte, perm os.FileMode, m *sidecarManifest) error {
	if m == nil {
		return os.WriteFile(path, data, perm)
	}
	_, statErr := os.Lstat(path)
	if statErr == nil {
		// 任何已存在的条目——常规文件、符号链接、目录——都是冲突。拒绝碰它。
		return fmt.Errorf("%w: %s", errPathPreExists, path)
	}
	if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("stat target %s: %w", path, statErr)
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	m.Files = append(m.Files, path)
	return nil
}

// allocateCollisionFreeSkillDir 在 skillsParent 下挑一个当前"不存在"的目录，
// 使 writeSkillFiles 能放下一个 Multica 技能而不与"用户已安装的同 slug 技能"
// 撞车。第一次尝试总是自然的 baseSlug——那是 provider 原生发现本来就知道的
// 路径。撞车后依次追加 `-multica`、`-multica-2`、`-multica-3`…直到找到空位。
// 选定的 slug 与绝对路径一起返回，调用方可在 frontmatter 与 brief 清单里用它。
//
// 这个无冲突的备选名仍是同一 skillsParent 下的兄弟目录，所以 provider 原生
// 发现照样能捡到该技能（.claude/skills/ 等之下的每个子目录都是独立扫描的）。
// baseSlug 处的用户目录保持逐位原样。
//
// 探测设了一个很小的上限——用户在同一 slug 上有上千次冲突说明是上游 bug，
// 而非现实状态。此时返回错误能迫使调用方把问题摆上台面，而不是无限循环。
func allocateCollisionFreeSkillDir(skillsParent, baseSlug string) (slug, dir string, err error) {
	const maxAttempts = 64
	for i := 0; i < maxAttempts; i++ {
		candidate := skillSlugCandidate(baseSlug, i)
		path := filepath.Join(skillsParent, candidate)
		if _, statErr := os.Lstat(path); statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				return candidate, path, nil
			}
			return "", "", fmt.Errorf("stat candidate %s: %w", path, statErr)
		}
	}
	return "", "", fmt.Errorf("allocate collision-free skill dir under %s: exhausted %d attempts for base %q", skillsParent, maxAttempts, baseSlug)
}

// skillSlugCandidate 是对自然 slug 为 baseSlug 的技能尝试的第 n 个名字：
// 先是裸 slug，再是 `-multica`，然后带编号的变体。
//
// 两个调用方必须就这一序列达成一致——allocateCollisionFreeSkillDir（探测
// 文件系统）与 resolveSkillSlugs（写入前在内存里给整批去重）。若它们不一致，
// 一个技能就会在清单里叫一个名字、落到磁盘上又叫另一个名字。
func skillSlugCandidate(baseSlug string, attempt int) string {
	switch {
	case attempt <= 0:
		return baseSlug
	case attempt == 1:
		return baseSlug + "-multica"
	default:
		return fmt.Sprintf("%s-multica-%d", baseSlug, attempt)
	}
}

// writeSidecarManifest 把 m 持久化到 {envRoot}/{sidecarManifestFile}。
// 空的清单也会被写，这样后来的 Cleanup 若找到文件就知道"这次确实尝试过
// 记账"（而早于本机制的老构建没有该文件，Cleanup 必须无操作）。
//
// 失败会返回给调用方，其严重程度取决于侧车落在哪里。对云端 envRoot，
// 清单只是 GC 可有可无的便利，因此 Prepare 记日志后继续。对就地 local_directory
// 运行，它是"往用户自己仓库里写了什么"的唯一记录，因此 Prepare 把失败视为
// 致命并在仍持有内存清单时回滚（MUL-6132）。
func writeSidecarManifest(envRoot string, m *sidecarManifest) error {
	if envRoot == "" {
		return nil
	}
	if m == nil {
		m = &sidecarManifest{}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal sidecar manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(envRoot, sidecarManifestFile), data, 0o644)
}

// CleanupSidecars 删除 envRoot 处清单记录的所有文件、再对记录的所有目录做
// rmdir（最深者优先），把用户的工作目录回滚到 Prepare 之前的状态。
//
// 两种失败模式是本函数刻意吞掉的：
//
//   - 记录的路径返回 ENOENT。该文件或目录已经不在了——要么用户在任务期间
//     删了它，要么同一 envRoot 上先前某次 Cleanup 已把它清掉。无论哪种，
//     本次调用都无事可做。
//   - rmdir 遇上非空目录。用户在我们创建的目录里放了内容（例如在
//     .claude/skills/ 下加了个兄弟文件），rmdir 会毁掉那些内容。我们通过
//     rmdir 失败后重读目录来识别它：非空清单意味着"这里归用户——到此为止"。
//     这是 PR #3444 review 里必须修的问题——此前版本把"任何非 ENOENT 的
//     rmdir 错误"都当作"非空"吞掉，会静默丢弃真实的 I/O 失败（EACCES、
//     EPERM、EBUSY），让本未成功的清理看起来成功了。
//
// 其余所有错误——ReadFile 失败、JSON 解析失败、删除文件时的真实
// EACCES/EPERM/EIO、删目录时的真实 EACCES/EPERM/EIO——都被收进 firstErr
// 并抛给调用方。Cleanup 仍会继续处理清单里剩下的条目，避免一个坏路径拖住
// 其余的回滚。
//
// 以下情况本函数为无操作：
//   - envRoot 为空（本任务没有 daemon 暂存区），
//   - 清单文件缺失（更老构建，或 Prepare 根本没跑）。
//
// 在 local_directory 清理路径上把它与 CleanupRuntimeConfig 配对：那个函数
// 处理 CLAUDE.md / AGENTS.md 里的运行时 brief，这个处理侧车目录树
// （.agent_context/、.multica/、.claude/skills/、.github/skills/、
// .opencode/skills/、.codeartsdoer/skills/、skills/、.pi/skills/、
// .omp/mcp.json、.omp/skills/、.cursor/skills/、
// .kimi/skills/、.reasonix/skills/、.kiro/skills/、.agents/skills/、兜底
// .agent_context/skills/）。两者合起来把工作目录恢复到任务前的逐字节状态。
func CleanupSidecars(envRoot string) error {
	if envRoot == "" {
		return nil
	}
	manifestPath := filepath.Join(envRoot, sidecarManifestFile)
	data, err := os.ReadFile(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sidecar manifest %s: %w", manifestPath, err)
	}
	var m sidecarManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse sidecar manifest %s: %w", manifestPath, err)
	}

	return rollBackManifest(m, manifestPath)
}

// rollBackManifest 删除 m 记录的一切，然后当 manifestPath 非空时删除
// manifestPath 本身。它是 CleanupSidecars（任务跑完后从磁盘读回 m）与
// rollBackPreparedSidecars（传入一个从未完成的 Prepare 的内存清单，且没有
// 清单文件可删——因为 Prepare 在写它之前就失败了）的共享主体。
//
// 这样拆分让"失败 Prepare 路径"能复用文档记载在 CleanupSidecars 上的同一套
// 删除语义——容忍 ENOENT 与非空目录、上报真实 I/O 错误——而不必再长出一套
// 与它细微不同、迟早漂移的回滚。
func rollBackManifest(m sidecarManifest, manifestPath string) error {
	var firstErr error
	captureErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for _, f := range m.Files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			captureErr(fmt.Errorf("remove %s: %w", f, err))
		}
	}

	// 逆序迭代，先尝试最深的目录。rmdir 失败时重读目录，把 ENOTEMPTY
	// （有用户内容——静默跳过）与真实 I/O 错误（权限拒绝、忙等——捕获上报）
	// 区分开。
	for i := len(m.Dirs) - 1; i >= 0; i-- {
		d := m.Dirs[i]
		err := os.Remove(d)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			continue
		}
		hasEntries, ok := dirHasEntries(d)
		switch {
		case !ok:
			// ReadDir 也失败了——无法区分 ENOTEMPTY 与真实 I/O 错误。上报
			// "原始 rmdir 错误"（而非 ReadDir 失败），让运维看到真正的清理
			// 阻碍；ReadDir 分支只是诊断管道，会分散对根因的注意。v1 在这里
			// 静默跳过，是 bug：它用一个幻影般的"目录非空"假设掩盖了加锁
			// 目录上的 EACCES。
			captureErr(fmt.Errorf("rmdir %s: %w", d, err))
		case hasEntries:
			// 用户在此目录于 Prepare 运行后放入了内容。保留它且不上报 rmdir
			// 错误——清单设计的全部意义就在于保留"我们创建的目录之下"的
			// 用户内容。
		default:
			// 目录为空但 rmdir 仍失败 → 真实 I/O 错误（EACCES、EPERM、EBUSY、
			// EIO，或一个被误记录、其实不归我们的目录）。上报，让调用方记
			// 警告、让运维去查。
			captureErr(fmt.Errorf("rmdir %s: %w", d, err))
		}
	}

	if manifestPath != "" {
		if err := os.Remove(manifestPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			captureErr(fmt.Errorf("remove manifest %s: %w", manifestPath, err))
		}
	}

	return firstErr
}

// rollBackPreparedSidecars 用一个尚未落盘清单的失败 Prepare 仍在填充的内存
// 清单，撤销该 Prepare 已做的侧车写入。
//
// 这是那条路径上唯一的回滚手段。Prepare 很早就会把 daemon 任务标记与其余
// 侧车树写进工作目录，但只在最后才持久化清单；其间的每个错误返回都会把那
// 棵树留在磁盘上、却没有任何磁盘记录，而调用方也收不到 Environment，于是
// 下游没有任何 teardown defer 知道有东西要清理（MUL-6132）。对 local_directory
// 任务，工作目录就是用户自己的仓库，所以"留在磁盘上"意味着一个会禁用它所在
// 目录树内每条 multica 命令的标记，直到有人手动删除为止。
//
// 没有清单文件要删，因此 manifestPath 为空。
func rollBackPreparedSidecars(m sidecarManifest) error {
	return rollBackManifest(m, "")
}

// removeReusedManagedSkillDirs 强制删除上一次分发记录在 envRoot 侧车清单里、
// 位于 skillsParent 之下的技能目录，即使它们现在非空。它是 reuse 路径上
// CleanupSidecars 的搭档，紧挨着后者运行。
//
// CleanupSidecars 刻意保留一个一旦非空的已记录目录——agent 可能在我们创建
// 的目录里放了文件，而在 local_directory 拆除路径上那些内容必须存活。但同一
// 份保留逻辑在 reuse 路径上会重开 #3684：若某次先前的 agent 往
// .claude/skills/issue-review/ 里写过东西，CleanupSidecars 会删掉已记录的
// SKILL.md 却保留目录，于是规范的 slug 仍然被占，刷新后的技能只能躲到
// issue-review-multica。受管技能目录归平台所有——清单就是"我们创建了它"的
// 证明——所以 reuse 时我们回收整个目录（丢弃 agent 留在里面的临时文件，正如
// Codex 路径的 os.RemoveAll(skillsDir) 已经在做的），让刷新后的技能以自然
// slug 重建。
//
// 只有"直接父目录就是 skillsParent"的目录会被删除，因此爆炸半径恰好是平台
// 自己的技能根：agent 在同一父目录下装的兄弟技能、检出的仓库与工作目录其余
// 部分都不受影响。reuse 路径只跑在云端工作目录（daemon 对 local_directory
// 任务跳过 Reuse），所以这里本来就没有"用户自有的技能树"要保护。
//
// envRoot 或 skillsParent 为空、清单缺失或解析失败都是无操作——刷新照常
// 进行。清单文件留在原地；紧接着运行的 CleanupSidecars 负责删除它。
func removeReusedManagedSkillDirs(envRoot, skillsParent string) error {
	if envRoot == "" || skillsParent == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(envRoot, sidecarManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sidecar manifest for reuse skill rollback: %w", err)
	}
	var m sidecarManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse sidecar manifest for reuse skill rollback: %w", err)
	}

	cleanParent := filepath.Clean(skillsParent)
	var firstErr error
	for _, d := range m.Dirs {
		if filepath.Dir(filepath.Clean(d)) != cleanParent {
			continue
		}
		if err := os.RemoveAll(d); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove managed skill dir %s: %w", d, err)
		}
	}
	return firstErr
}

// dirHasEntries 检查 dir 当前是否含任何条目。第二个返回值区分 CleanupSidecars
// 必须分别处理的三种状态：
//
//   - (false, true) —— dir 存在且为空，或 dir 在"rmdir 失败"到"我们 readdir"
//     之间消失了（该竞争塌缩成"空"，清理得以继续）。与 CleanupSidecars 里
//     一次非 ENOENT 的 rmdir 失败配对时，这对应"空目录却拒绝 rmdir"分支——
//     一个会被上报的真实 I/O 错误。
//   - (true, true) —— dir 有用户内容。与 rmdir 失败配对时正是预期的
//     ENOTEMPTY 分支——静默跳过，保住用户内容。
//   - (_, false) —— readdir 以真实 I/O 错误失败（被 chmod 过的目录上的
//     EACCES、对"其实不是目录"的已记录路径的 ENOTDIR、硬件故障的 EIO 等）。
//     调用方无法区分 ENOTEMPTY 与真实失败，必须上报"原始 rmdir 错误"而非
//     静默跳过。本 helper 的 v1 在这里返回 `true`，使 CleanupSidecars 把每次
//     readdir 失败都当成"有用户内容"，掩盖了底层的 rmdir 错误。
func dirHasEntries(dir string) (hasEntries bool, ok bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, true
		}
		return false, false
	}
	return len(entries) > 0, true
}
