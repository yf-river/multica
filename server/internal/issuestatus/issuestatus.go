// Package issuestatus 负责「每个 workspace 的 issue 状态目录」（MUL-6243）。
//
// 模型：有 7 个「内置类别」，与 7 个内置状态一一对应：类别的值本身就是规范状态键。
// 自定义状态声明一个类别，然后完整继承该 canonical 状态在平台侧的一切行为。
//
// 这个「一一对应」正是让本模块保持廉价的关键。类别被定义成「行为等价类」——两个状态
// 只有在平台对它们一视同仁时才共享同一类别——按这一定义，7 个内置状态恰是 7 个不同
// 类别：in_progress、in_review、blocked 在「是否结束一次 autopilot 运行、是否通知委托
// 订阅者、是否清除过期的 task_failed 收件箱行、是否会被卡住 issue 清扫器重置」上都不
// 一样。若把它们折叠成一个 "started" 类别（Linear 的做法），就得再引入每个状态自己的
// "behaves_as" 概念去重新表达差异——也就埋下了「某状态只继承了一半行为」的可能。
//
// 推论：Effective 对内置键就是恒等函数，所以所有既有的 `issue.Status == "todo"` 比较
// 语义不变，`issue.status` 仍是权威的 TEXT 列：不需要 status_id、不需要回填、不需要
// 双写。行为变化只发生在自定义状态上——而该集合在管理员创建它之前是空的。
package issuestatus

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 7 个规范状态键。每个键同时是状态键，也是它定义的类别的名字。
const (
	Backlog    = "backlog"
	Todo       = "todo"
	InProgress = "in_progress"
	InReview   = "in_review"
	Done       = "done"
	Blocked    = "blocked"
	Cancelled  = "cancelled"
)

// canonicalOrder 是前端静态状态配置里的历史 STATUS_ORDER。类别排序原样照搬它，这样
// 没有自定义状态的 workspace 看到的看板与选择器与功能上线前完全一致。
//
// 注意这个顺序并不是按生命周期分组的：in_review 和 done 夹在 in_progress 与 blocked
// 之间。这是线上一直用的顺序——把它重排成「更整齐」会肉眼可见地打乱每个既有用户的
// 看板。
var canonicalOrder = []string{
	Backlog,
	Todo,
	InProgress,
	InReview,
	Done,
	Blocked,
	Cancelled,
}

// canonicalRank 是「键 → 展示序」的查找表。
var canonicalRank = func() map[string]int {
	m := make(map[string]int, len(canonicalOrder))
	for i, key := range canonicalOrder {
		m[key] = i
	}
	return m
}()

// ErrUnknownStatus 在某状态键不在 workspace 目录里（或存在但已归档）时返回。
var ErrUnknownStatus = errors.New("unknown issue status")

// keyPattern 对应 issue_status.key 列的 CHECK 约束。键必须是小写，这样
// `multica issue status <id> human_review` 这类输入才不会产生歧义。
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)

// Querier 是本包需要的生成查询子集。用接口能让解析器在没有真实数据库时也能测。
type Querier interface {
	GetIssueStatusEntryByKey(ctx context.Context, arg db.GetIssueStatusEntryByKeyParams) (db.IssueStatus, error)
	ListIssueStatusEntries(ctx context.Context, arg db.ListIssueStatusEntriesParams) ([]db.IssueStatus, error)
	SeedIssueStatusEntries(ctx context.Context, workspaceID pgtype.UUID) error
	ListIssueStatusKeysByCategories(ctx context.Context, arg db.ListIssueStatusKeysByCategoriesParams) ([]string, error)
}

// Canonical 按展示顺序返回 7 个内置状态键。
func Canonical() []string {
	out := make([]string, len(canonicalOrder))
	copy(out, canonicalOrder)
	return out
}

// IsBuiltIn 报告 key 是否是 7 个规范状态之一。
func IsBuiltIn(key string) bool {
	_, ok := canonicalRank[key]
	return ok
}

// IsCategory 报告 value 是否命名了一个合法类别。按构造它与 IsBuiltIn 相同——类别与
// canonical 键是同一个集合——单独存在是为了让调用方能表明自己想表达哪一个。
func IsCategory(value string) bool { return IsBuiltIn(value) }

// CategoryRank 返回类别的展示序号；不认识的类别返回 len(canonicalOrder)，让它在排序时
// 落到最后而不是和 rank 0 撞车。
func CategoryRank(category string) int {
	if rank, ok := canonicalRank[category]; ok {
		return rank
	}
	return len(canonicalOrder)
}

// ValidateKey 用存储约束和保留的内置名校验一个候选的自定义状态键。
func ValidateKey(key string) (string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return "", errors.New("status key is required")
	}
	if !keyPattern.MatchString(key) {
		return "", errors.New("status key must be 1-32 characters of lowercase letters, digits or underscore, starting with a letter or digit")
	}
	if IsBuiltIn(key) {
		return "", fmt.Errorf("%q is a built-in status key and cannot be reused", key)
	}
	return key, nil
}

// maxKeyLen 对应 keyPattern 与 issue_status CHECK 里的 32 字符上限。给键加后缀必须
// 仍小于它，所以要截断「基础名」而不是丢掉后缀——截断后的基础名仍能靠后缀保持唯一，
// 丢掉后缀反而不能。
const maxKeyLen = 32

// slugify 把展示名降成 ASCII 键字母表，啥都不剩时返回 ""。一律小写（`multica issue
// status <id> human_review` 必须无歧义地可打）；其它字符的连续段折叠成单个下划线，
// 这样 "Gate — Approved!" 不会变成 "gate___approved"。
func slugify(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore && b.Len() > 0:
			b.WriteRune('_')
			lastUnderscore = true
		}
	}
	slug := strings.Trim(b.String(), "_")
	if len(slug) > maxKeyLen {
		slug = strings.Trim(slug[:maxKeyLen], "_")
	}
	return slug
}

// DeriveKey 为一个「创建者只给了展示名」的状态挑选存储键（MUL-6749 / GitHub #7627）。
//
// 键是 ASCII 机器句柄——issue.status 存的是它、CLI 收的是它、列 CHECK 约束也只允许
// 它——而展示名是任意 Unicode。因此由展示名派生键，必须扛得住「名字与键字母表没有
// 任何交集」的情况：
//
//   - 名字能 slug 成可用串就保留该 slug：英文命名的状态照旧得到可读的
//     `human_review`；
//   - 完全用非拉丁文字书写的名字 slug 为空，就退回「类别 + 序号」。用类别而不是随机
//     后缀：它本来就是 agent 推理所依托的锚点，所以 `in_review_2` 仍能看出该状态继承
//     哪个平台行为；即使模型把它跟类别搞混，也会退化到正确的邻域。状态的含义跟着它
//     的名字和描述走，agent 简报会把它们印在键旁边。
//
// 无论哪条路，结果都要与 workspace 已拥有的键消歧，因此两个坍缩到同一 slug 的名字
// （"待客户 Review" 与 "待供应商 Review" 都 slug 成 "review"）不会再让第二个创建
// 报一个把锅甩给展示名的冲突错。
//
// taken 报告某个键是否已存在于该 workspace，而且必须把 ARCHIVED（已归档）状态也
// 算上：idx_issue_status_workspace_key 不是部分索引，归档行仍占着它的键。内置键无论
// taken 说什么都算被占——既阻止自定义状态遮蔽内置状态，也让回退从 `<category>_2`
// 起跳。
//
// 任何能走到这里的名字都应派生成功。剩下唯一的拒绝是「名字恰好 slug 到内置键上」：
// 这是该让管理员看见的命名冲突，而不是被悄悄改名的——与改动前行为一致。
func DeriveKey(name, category string, taken map[string]bool) (string, error) {
	if slug := slugify(name); slug != "" {
		if IsBuiltIn(slug) {
			return "", fmt.Errorf("%q is a built-in status key and cannot be reused; rename the status or pass an explicit key", slug)
		}
		return firstFreeKey(slug, taken)
	}
	// 名字里没有任何键字母表里的字符。类别按构造是合法键，而它的内置状态永远占着它，
	// 所以第一个这种状态会落到 <category>_2。
	if !IsCategory(category) {
		return "", fmt.Errorf("category must be one of: %s", strings.Join(canonicalOrder, ", "))
	}
	return firstFreeKey(category, taken)
}

// firstFreeKey 在 base 未被占用时直接返回 base，否则返回第一个空闲的 base_<n>。
// n 从 2 开始，让序列读起来像「第二个」。
//
// 扫描的上界来自「目录规模」而不是某个拍脑袋的数字。每个候选互不相同；最多只有
// len(taken)+7 个键可能被占（workspace 自己的加上内置的），按鸽笼原理，空闲键一定
// 会在这么多次尝试加一次之内出现。若改用取整的常量，就会凭空给自定义状态套一个产品
// 里别处不存在的上限，而且会以「请显式提供一个」失败——正是本包当初为了避免向「没有
// key 输入框的 UI」展示而改掉的报错。
func firstFreeKey(base string, taken map[string]bool) (string, error) {
	if !keyOccupied(base, taken) {
		return ValidateKey(base)
	}
	limit := len(taken) + len(canonicalOrder) + 2
	for n := 2; n <= limit; n++ {
		suffix := "_" + strconv.Itoa(n)
		candidate := truncateForSuffix(base, len(suffix)) + suffix
		if !keyOccupied(candidate, taken) {
			return ValidateKey(candidate)
		}
	}
	// 按上面的论证到不了这里；保留它是为了让将来改动候选生成时以报错而不是死循环收场。
	return "", errors.New("could not derive an unused status key from that name; provide one explicitly")
}

// keyOccupied 把内置集合也并进 workspace 自己的键里。即使某 workspace 的目录行还没
// 播种，内置键也算被占——未播种的 workspace 不能造出遮蔽内置状态的自定义状态。
func keyOccupied(key string, taken map[string]bool) bool {
	if IsBuiltIn(key) {
		return true
	}
	return taken[key]
}

// truncateForSuffix 缩短 base，让 base+suffix 装得进 maxKeyLen。尾部下划线去掉是
// 防止 "human_review" 变成 "human_" + "_2"；base 总以字母数字开头，所以裁剪永不把它
// 掏空。
func truncateForSuffix(base string, suffixLen int) string {
	limit := maxKeyLen - suffixLen
	if len(base) <= limit {
		return base
	}
	return strings.TrimRight(base[:limit], "_")
}

// Ensure 幂等地为一个 workspace 播种 7 个内置状态。可以并发调用——(workspace_id, key)
// 唯一索引会把抢输的并发调用变成无操作。滚动部署期间这点很重要：旧 pod 可能在新建
// workspace，新 pod 同时在播种它。
func Ensure(ctx context.Context, q Querier, workspaceID pgtype.UUID) error {
	return q.SeedIssueStatusEntries(ctx, workspaceID)
}

// Effective 把状态键映射到「承载其平台行为的那个 canonical 键」。这是让既有逻辑继续
// 正确的那把钥匙：
//
//   - 内置键原样返回自身、不碰数据库，所以没有任何既有代码路径会多一次查询或改变行为；
//   - 自定义键返回它所属的类别，即它继承的 canonical 状态。
//
// 键无法解析时原样返回。这是「失败即安全」的方向：一个不认识的状态匹配不上任何
// canonical 比较，issue 就保持原样——不会被清扫、不会被自动触发、也不会在猜测中被
// 结束 autopilot 运行。
func Effective(ctx context.Context, q Querier, workspaceID pgtype.UUID, status string) string {
	if IsBuiltIn(status) {
		return status
	}
	entry, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{
		WorkspaceID: workspaceID,
		Key:         status,
	})
	if err != nil {
		return status
	}
	if !IsCategory(entry.Category) {
		return status
	}
	return entry.Category
}

// EffectiveAndName 用一次目录读取同时解析出状态的「类别 + 展示名」，给需要这一对的
// payload 构建者用。
//
// 分开调两次会让每个携带自定义状态的后台事件都多一倍查询。与 Effective 相同的失败
// 方向：无法解析的键原样返回、名字为空。
//
// 内置状态的名字刻意留空——客户端都从键本地化它们，回显英文的播种名反而会成为非英文
// workspace 需要无视的那条字符串。（MUL-6749）
func EffectiveAndName(ctx context.Context, q Querier, workspaceID pgtype.UUID, status string) (string, string) {
	if IsBuiltIn(status) {
		return status, ""
	}
	entry, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{
		WorkspaceID: workspaceID,
		Key:         status,
	})
	if err != nil {
		return status, ""
	}
	if !IsCategory(entry.Category) {
		return status, entry.Name
	}
	return entry.Category, entry.Name
}

// Resolve 校验 status 在该 workspace 可用，并返回目录条目。写路径用它；它是迁移 337
// 删掉的 enum CHECK 的应用层替代。
//
// 即使 workspace 没有对应目录行，7 个内置键也能解析成功。目录是「扩展」内置状态，而
// 不是「定义」它们——workspace 一直能用全部 7 个。若要求必须有行，那么「播种还没落
// 地」的 workspace（由早于该功能的 pod 创建，或在迁移 339 之前的滚动部署中段）将完全
// 无法创建或更新 issue。这里的失败开放被精确限定在「该功能出现前本来就有效」的集合上；
// 其余任何键都仍需要有行。
func Resolve(ctx context.Context, q Querier, workspaceID pgtype.UUID, status string) (db.IssueStatus, error) {
	key := strings.ToLower(strings.TrimSpace(status))
	if key == "" {
		return db.IssueStatus{}, ErrUnknownStatus
	}
	entry, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{
		WorkspaceID: workspaceID,
		Key:         key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if IsBuiltIn(key) {
				return builtInEntry(workspaceID, key), nil
			}
			return db.IssueStatus{}, ErrUnknownStatus
		}
		return db.IssueStatus{}, err
	}
	if entry.ArchivedAt.Valid {
		// 内置状态不可能被归档（表约束保证），所以能走到这里说明是自定义状态被收编了。
		return db.IssueStatus{}, ErrUnknownStatus
	}
	return entry, nil
}

// builtInEntry 为「播种还没落地」的 workspace 合成内置状态的目录行。它携带定义行为
// 的字段——key 与 category，二者按构造相等——并且刻意不持久化：播种由 Ensure 负责。
func builtInEntry(workspaceID pgtype.UUID, key string) db.IssueStatus {
	return db.IssueStatus{
		WorkspaceID: workspaceID,
		Key:         key,
		Category:    key,
		IsSystem:    true,
	}
}

// ActiveKeys 按展示顺序返回 workspace 可用的状态键，用于错误消息与 CLI 校验。未播种
// 的 workspace 仍会报告 7 个内置键，保证错误消息永远不遗漏 Resolve 会接受的键。
func ActiveKeys(ctx context.Context, q Querier, workspaceID pgtype.UUID) ([]string, error) {
	entries, err := q.ListIssueStatusEntries(ctx, db.ListIssueStatusEntriesParams{
		WorkspaceID:     workspaceID,
		IncludeArchived: false,
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(entries)+len(canonicalOrder))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Key)
		seen[e.Key] = true
	}
	for _, key := range canonicalOrder {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// ActiveKeyLabels 是 ActiveKeys 的「每个自定义状态都带上展示名」版本，形如
// `key (Name)`。它支撑「写完一个未知状态后用户看到的错误」：只回一列裸键是最没用的
// ——由非拉丁名字派生的键（`in_review_2`）本身毫无含义，读者（人或 agent）拿不到任何
// 能跟「别人叫它用的名字」对上的信息。
//
// 内置状态保持裸键。它们的键就是类别、播种名是英文、所有客户端本来就从键本地化它们，
// 所以打印 `todo (Todo)` 只会增加噪音而不是信息。
func ActiveKeyLabels(ctx context.Context, q Querier, workspaceID pgtype.UUID) ([]string, error) {
	entries, err := q.ListIssueStatusEntries(ctx, db.ListIssueStatusEntriesParams{
		WorkspaceID:     workspaceID,
		IncludeArchived: false,
	})
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(entries)+len(canonicalOrder))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		seen[e.Key] = true
		if IsBuiltIn(e.Key) || strings.TrimSpace(e.Name) == "" {
			labels = append(labels, e.Key)
			continue
		}
		labels = append(labels, e.Key+" ("+e.Name+")")
	}
	for _, key := range canonicalOrder {
		if !seen[key] {
			labels = append(labels, key)
		}
	}
	return labels, nil
}

// Resolver 针对「一个 workspace 的目录」批量解析多个状态，全程至多一次查询。列表
// 端点没有它就得逐行查。
//
// 内置快路径不变：内置键不碰目录就原样返回，所以没有自定义状态的 workspace，无论列表
// 多长仍是零查询。目录在遇到第一个自定义键时才惰性加载，之后每行复用。
//
// 不支持并发使用，且只作用于单次请求：它会把目录缓存到自己的生命周期结束——长命
// Resolver 在管理员编辑目录后会提供过期的类别。
type Resolver struct {
	workspaceID pgtype.UUID
	categories  map[string]string
	names       map[string]string
	loaded      bool
}

// NewResolver 返回针对一个 workspace 的 Resolver。它不做任何 I/O。
func NewResolver(workspaceID pgtype.UUID) *Resolver {
	return &Resolver{workspaceID: workspaceID}
}

// load 在第一次需要时把目录取一次。读取失败会让两个 map 保持 nil，使后续每个查找都
// 落回「键原样返回」分支——与单发解析器相同的失败即安全策略。
func (r *Resolver) load(ctx context.Context, q Querier) {
	if r.loaded {
		return
	}
	r.loaded = true
	entries, err := q.ListIssueStatusEntries(ctx, db.ListIssueStatusEntriesParams{
		WorkspaceID:     r.workspaceID,
		IncludeArchived: true,
	})
	if err != nil {
		return
	}
	r.categories = make(map[string]string, len(entries))
	r.names = make(map[string]string, len(entries))
	for _, e := range entries {
		r.categories[e.Key] = e.Category
		r.names[e.Key] = e.Name
	}
}

// Effective 镜像包级 Effective，但把目录读取摊到每次调用里。同样的失败方向：无法解析
// 的键原样返回，不做猜测。
func (r *Resolver) Effective(ctx context.Context, q Querier, status string) string {
	if IsBuiltIn(status) {
		return status
	}
	r.load(ctx, q)
	category, ok := r.categories[status]
	if !ok || !IsCategory(category) {
		return status
	}
	return category
}

// Name 返回自定义状态的展示名；内置状态或目录里没有的键返回 ""。
//
// 内置状态刻意返回 "" 而不是它们播种的英文名：所有客户端都经 i18n 从键渲染它们，
// 回传 "In Progress" 反而会成为中文 workspace 需要无视的那条字符串。自定义名由用户
// 撰写、无法翻译，所以处处都是标签——这也是「携带自定义键的 payload 应在旁边带它」的
// 原因。像 `in_review_2` 这样的生成键本身什么都说明不了。（MUL-6749）
//
// 与 Effective 共用 Resolver 的那一次目录读取，所以给响应补一个名字不增加查询。
func (r *Resolver) Name(ctx context.Context, q Querier, status string) string {
	if IsBuiltIn(status) {
		return ""
	}
	r.load(ctx, q)
	return r.names[status]
}

// ExpandCategories 把一组类别展开成属于它们的状态键，供「走了索引的
// `status = ANY(...)`」谓词使用。
//
// 之所以有它，而不是在查询里过滤 issue_effective_status(workspace, status)：把列包进
// 函数会让 (workspace_id, status) 索引失效，把两页的索引读取变成整表扫描。先展开则
// 保持原访问路径——而且对没有自定义状态的 workspace，每个类别恰好展开成自己的键，
// 查询与该功能出现前逐字节一致。
//
// 归档状态也包含进来：归档停止的是「未来赋值」，既有 issue 仍留在原处，而它们必须
// 继续出现在所属类别的列里。
//
// 未播种的 workspace 查不到行；此时直接返回类别本身——这是对的，因为内置键就是它
// 自己的类别。
func ExpandCategories(ctx context.Context, q Querier, workspaceID pgtype.UUID, categories []string) ([]string, error) {
	valid := make([]string, 0, len(categories))
	for _, c := range categories {
		if IsCategory(c) {
			valid = append(valid, c)
		}
	}
	if len(valid) == 0 {
		return nil, nil
	}
	keys, err := q.ListIssueStatusKeysByCategories(ctx, db.ListIssueStatusKeysByCategoriesParams{
		WorkspaceID: workspaceID,
		Categories:  valid,
	})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(keys)+len(valid))
	out := make([]string, 0, len(keys)+len(valid))
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	// 类别里至少总有自己的 canonical 键，即使目录行缺失（未播种的 workspace、
	// 滚动部署中段）。
	for _, c := range valid {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// CustomKeyCategories 返回 workspace 的「自定义状态键 → 所属类别」映射。内置状态刻意
// 缺席：内置键就是自己的类别，所以「键 → 类别」的调用方只需要例外项。
//
// 调用方用它构造一条静态的 `CASE i.status WHEN ... ELSE i.status END` 标量表达式供
// GROUP BY。这让类别分组变成纯列改写，而不是逐行函数调用或 join；没有自定义状态的
// workspace 映射为空、CASE 坍缩成 `i.status`——与该功能出现前的表达式逐字节一致。
//
// 归档状态同样包含，理由与 ExpandCategories 相同：留在归档状态上的 issue 仍须归入
// 它们的类别。
func CustomKeyCategories(ctx context.Context, q Querier, workspaceID pgtype.UUID) (map[string]string, error) {
	entries, err := q.ListIssueStatusEntries(ctx, db.ListIssueStatusEntriesParams{
		WorkspaceID:     workspaceID,
		IncludeArchived: true,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if IsBuiltIn(e.Key) || !IsCategory(e.Category) {
			continue
		}
		out[e.Key] = e.Category
	}
	return out, nil
}
