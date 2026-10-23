// main.go — 数据库迁移（migrate）命令入口。
//
// 职责：执行 `cmd/migrate up|down`，按文件顺序应用/回滚 server/migrations 下
// 的 SQL 迁移，并维护 schema_migrations 台账表。除了"跑 SQL"，它还挂了一套
// 钩子机制：
//   - preMigrationHook：特定迁移执行前要在连接池上跑的幂等准备工作（如并发建
//     索引中断后清理 INVALID 残留、给 198 校验前回填 attribution 等）；
//   - transactionalMigrationHook：与迁移 SQL 和台账更新共享同一个事务的数据
//     迁移（保证"数据变更"和"标记已应用"要么一起成、要么一起不成）；
//   - migrationCondition：只读的"环境门"，判定某段 SQL 是否需要真的执行
//     （不执行也照样推进台账，让一条迁移可表达环境差异而不卡住后续版本）。
//
// 整个流程用 Postgres 会话级咨询锁（migrationAdvisoryLockKey）串行化，多个
// 实例（多副本启动/扩容/手动迁移重叠）不会互相踩踏。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/attributionbackfill"
	"github.com/multica-ai/multica/server/internal/chatoriginbackfill"
	"github.com/multica-ai/multica/server/internal/dbstartup"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/migrations"
	"github.com/multica-ai/multica/server/internal/taskusagebackfill"
)

// preMigrationHook 是在"特定迁移被按某方向应用之前"要执行的工作。钩子必须幂等，
// 且不能依赖迁移循环那条"会话独占的咨询锁"——它们在连接池上跑、而不是在循环
// 固定占用的那条连接上跑，因此可以安全地再去拿其它会话级锁（例如 task_usage
// 小时聚合用的咨询锁 4246）。
//
// 返回错误会中止整轮迁移；对应迁移不会写入（up）/移出（down）schema_migrations，
// 下次运行会重试"钩子 + 迁移"。
type preMigrationHook func(ctx context.Context, pool *pgxpool.Pool) error

// transactionalMigrationHook 跑在迁移连接上，与迁移 SQL、台账更新共享同一个
// 事务。用于"数据迁移"——那种绝不允许"只记了已应用标记却没动数据"的场景。
type transactionalMigrationHook func(ctx context.Context, tx pgx.Tx) error

// migrationCondition 决定一条待执行迁移的 SQL 是否真的该跑。判定为 false 时仍
// 会把它记为已应用（或已回滚）——这样一条迁移可以把"环境相关的 DDL"写进去，
// 而不阻塞后续版本。条件必须只读且幂等；真正的 schema 变更仍留在迁移文件里，
// 便于评审与回滚工具查看。
type migrationCondition func(ctx context.Context, conn *pgxpool.Conn) (apply bool, reason string, err error)

// usableIndexRequirement 描述"可以取代兜底索引"的精确索引形态。只查扩展或关系
// 是否存在并不安全：历史 pg_bigm 迁移曾吞掉 CREATE INDEX 错误，而一次被打断的
// 并发建索引会留下"同名但 INVALID"的关系。
type usableIndexRequirement struct {
	IndexRegclass string
	TableRegclass string
	AccessMethod  string
	OperatorClass string
	Expression    string
	Extension     string
}

// commentContentBigramIndex 描述"首选"的评论内容搜索索引：基于 pg_bigm 扩展的
// lower(content) GIN 表达式索引。是否可用来决定 trigram 兜底索引的去留。
var commentContentBigramIndex = usableIndexRequirement{
	IndexRegclass: "public.idx_comment_content_bigm",
	TableRegclass: "public.comment",
	AccessMethod:  "gin",
	OperatorClass: "gin_bigm_ops",
	Expression:    "lower(content)",
	Extension:     "pg_bigm",
}

// preMigrationHooks 把"迁移版本 → 钩子"接起来。版本键是去掉 `.up.sql` 后缀的
// 文件名，与 `migrations.ExtractVersion` 返回的格式一致。
//
// MUL-2957：v0.3.4 → 当前的直接升级路径，需要在迁移 103 评估它 fail-closed
// 的滞后闸门之前就把小时聚合的水位播种好——因为执行 `cmd/migrate up` 时服务器
// 还没起来，无论遗留 pg_cron job 还是新应用内调度器都无法推进水位。该钩子跑的
// 正是 `cmd/backfill_task_usage_hourly` 提供给运维的那套幂等"按月切片回填"。
//
// MUL-4897 / GH #5544：迁移 198 要 VALIDATE 由 197 装上的严格 attribution
// 约束（它去掉了迁移 190 里 originator_source IS NULL 的豁免）。自托管数据库没
// 跑过 Multica 云跑过的带外回填，遗留行会让 198 fail-closed、后端拒绝启动。钩子
// 在 VALIDATE 之前幂等地对这些行做对账（accountable_user_id :=
// originator_user_id），于是卡在 197 的实例跑一次 `migrate up` 就自愈，无需手写
// SQL。编号更大的迁移帮不上忙——该实例永远到不了失败的那个 198 之上。
//
// GH #6388：迁移 257 用 CONCURRENTLY 建一条替换用的唯一索引。构建失败会留下
// INVALID 关系，IF NOT EXISTS 会把残留误认成"上次已成功"。钩子只删那条 INVALID
// 残留；随后 257 可以重建它，而有效的 v1 索引一直原地在位。
//
// MUL-5823：迁移 261 用同样方式替换终态任务的部分索引，因此有同样的隐患——把
// INVALID 的 v2 残留记为成功，会让迁移 262 把仍有效的 v1 删掉，导致四张仪表盘
// 汇总表全部退化到全表扫描。
//
// concurrentIndexCleanups 把迁移版本映射到它用 CREATE INDEX CONCURRENTLY 建的
// 索引。每个条目都自动挂一个"清理无效索引"钩子，保证被打断的构建在重试时不会
// 被误判为成功。
//
// 用"表"而不是逐条手写注册，是为了让测试能拿每个条目去对照它迁移文件真正建的
// 索引——这里手滑写错在运行时完全看不见（指向不存在索引的钩子是个无声空操作）。
//
// MUL-5999：迁移 273–277 各自并发建一条索引，其中三条在热表上（agent_task_queue
// 是全库最大的表）。它们与 257/261 有同样的隐患：被打断的构建留下同名 INVALID
// 索引，`IF NOT EXISTS` 跳过重建，runner 把迁移记为已应用，而真正需要索引的查询
// 从此静默全表扫描——这正是这些迁移要修的那个回归。
//
// MUL-6288：注册以前是按批次逐个 opt-in 的，所以 316/317/326/328/330/331 上线
// 时没挂钩子、隐患复发。现在这张表是全量的——所有用并发建索引的 up 迁移都在列
// （down 方向由 concurrentDownIndexCleanups 保持同样不变量）——若新增迁移漏了
// 条目，TestEveryConcurrentUpBuildHasCleanup 会让构建失败。给已应用的历史迁移
// 注册只花一次 to_regclass 查询，且只在它们仍 pending 的数据库上发生：全新自
// 托管安装——这正是被打断的构建最容易留下"永久不可用索引"的地方。
var concurrentIndexCleanups = map[string]string{
	"637_run_text_change_task_idx":                "run_text_change_task_idx",
	"568_upstream_sync_absorption_item":           "upstream_sync_absorption_task_item",
	"566_task_trace_identity_compatibility":       "task_trace_event_pkey",
	"565_user_account_compatibility":              "user_account_key",
	"553_upstream_sync_config_singleton":          "upstream_sync_config_singleton",
	"554_upstream_sync_run_id":                    "upstream_sync_run_id",
	"555_upstream_sync_run_active":                "upstream_sync_run_active",
	"556_upstream_sync_run_task":                  "upstream_sync_run_task",
	"557_upstream_sync_run_list":                  "upstream_sync_run_list",
	"558_upstream_sync_item_id":                   "upstream_sync_item_id",
	"559_upstream_sync_item_cluster":              "upstream_sync_item_cluster",
	"560_upstream_sync_item_pending":              "upstream_sync_item_pending",
	"561_upstream_sync_item_run":                  "upstream_sync_item_run",
	"562_upstream_sync_commit_unique":             "upstream_sync_commit_unique",
	"563_upstream_sync_commit_item":               "upstream_sync_commit_item",
	"574_verification_event_delivery":             "verification_event_delivery",
	"550_supervision_active_issue":                "supervision_active_issue",
	"549_delivery_authorization_source":           "issue_delivery_authorization_source",
	"548_delivery_authorization_key":              "issue_delivery_authorization_key",
	"546_issue_delivery_queue":                    "issue_delivery_queue",
	"545_issue_delivery_operation":                "issue_delivery_operation",
	"544_issue_delivery_key":                      "issue_delivery_key",
	"543_issue_delivery_plan_key":                 "issue_delivery_plan_key",
	"541_supervision_investigation_batch":         "supervision_investigation_batch",
	"540_supervision_investigation_key":           "supervision_investigation_key",
	"537_supervision_action_key":                  "supervision_action_pkey_idx",
	"536_supervision_finding_key":                 "supervision_finding_pkey_idx",
	"535_supervision_batch_key":                   "supervision_batch_pkey_idx",
	"534_supervision_material_key":                "supervision_material_pkey_idx",
	"533_supervision_subject_key":                 "supervision_subject_pkey_idx",
	"532_supervision_config_key":                  "supervision_config_pkey_idx",
	"531_supervision_operation_index":             "supervision_operation",
	"530_supervision_fingerprint_index":           "supervision_fingerprint",
	"529_supervision_active_index":                "supervision_active_workspace",
	"528_supervision_material_index":              "supervision_material_issue",
	"527_supervision_pending_index":               "supervision_pending",
	"516_workspace_mcp_check_unique":              "workspace_mcp_check_server_runtime",
	"514_skill_asset_key":                         "skill_asset_key",
	"512_meal_workspace_unique":                   "meal_workspace_scope_idx",
	"507_health_report_result_unique":             "health_report_result_attempt_idx",
	"506_health_report_digest":                    "health_report_digest_idx",
	"505_health_report_unique":                    "health_report_id_idx",
	"504_health_revision_unique":                  "health_revision_scope_idx",
	"503_health_observation_unique":               "health_observation_id_idx",
	"502_health_state_unique":                     "health_state_scope_idx",
	"419_seat_capacity_share_join_index":          "idx_seat_capacity_outbox_share_join",
	"418_seat_capacity_due_index":                 "idx_seat_capacity_outbox_due",
	"416_seat_capacity_operation_token_index":     "idx_seat_capacity_outbox_operation_token",
	"358_autopilot_quota_reservation_state_index": "idx_autopilot_quota_reservation_state",
	"356_autopilot_run_quota_reservation_index":   "uq_autopilot_run_quota_reservation",
	"355_autopilot_quota_reservation_key_index":   "uq_autopilot_quota_reservation_key",
	"354_autopilot_quota_reservation_id_index":    "autopilot_quota_reservation_pkey_uidx",
	"353_autopilot_quota_period_scope_index":      "uq_autopilot_quota_period_scope",
	"572_verification_slot_name":                  "verification_slot_name",
	"573_verification_slot_delivery":              "verification_slot_delivery",
	// 1. 601–619 这批迁移用并发索引承载唯一性（相关表没有主键），中断后必须能按同名
	// 删除 INVALID 残留；先前漏登记让 TestEveryConcurrentUpBuildHasCleanup 一直失败，
	// 且残留索引会让 ON CONFLICT 推断在运行期失效。
	"602_delivery_owner_key":            "delivery_owner_key",
	"603_delivery_publication_key":      "delivery_publication_key",
	"604_delivery_evidence_key":         "delivery_evidence_key",
	"606_delivery_notice_key":           "delivery_notice_key",
	"608_release_window_id":             "release_window_id",
	"609_release_window_active":         "release_window_active",
	"610_release_paused_task_key":       "release_paused_task_key",
	"612_review_config_workspace":       "review_config_workspace_unique",
	"613_agent_issue_review":            "agent_issue_review_unique",
	"615_issue_review_issue":            "issue_review_issue_unique",
	"616_issue_review_workspace":        "issue_review_workspace_state",
	"619_supervision_managed_agent_key": "agent_quality_supervision_unique",
	"621_supervision_round_key":         "supervision_round_key",
	"622_supervision_round_sequence":    "supervision_round_sequence",
	"623_supervision_round_open":        "supervision_round_open",
	"625_supervision_batch_round_key":   "supervision_batch_round_key",
	"626_supervision_batch_round_round": "supervision_batch_round_round",
	// 2. 种子槽位与发布批次的新索引同样并发建；建表与建索引分开成迁移，中断后按名清理 INVALID 残留再重试。
	"629_verification_slot_maintenance_index":  "verification_slot_maintenance_name_created",
	"630_delivery_batch_active_index":          "delivery_batch_active",
	"631_delivery_batch_member_delivery_index": "delivery_batch_member_delivery",
	"520_repository_id":                        "repository_id_idx",
	"521_repository_identity":                  "repository_identity_idx",
	"522_repository_location_id":               "repository_location_id_idx",
	"523_repository_location_daemon":           "repository_location_daemon_idx",

	"498_cooking_favorite_unique":                                              "cooking_favorite_user_recipe_idx",
	"457_domain_event_outbox_pending_index":                                    "domain_event_outbox_pending_idx",
	"458_domain_event_outbox_stream_index":                                     "domain_event_outbox_stream_idx",
	"461_domain_event_idempotency_index":                                       "domain_event_outbox_idempotency_idx",
	"463_life_idx_companion_profile_agent_concurrent_index":                    "idx_companion_profile_agent",
	"464_life_idx_life_memory_user_status_updated_concurrent_index":            "idx_life_memory_user_status_updated",
	"465_life_idx_life_memory_user_kind_updated_concurrent_index":              "idx_life_memory_user_kind_updated",
	"466_life_idx_life_memory_evidence_source_concurrent_index":                "idx_life_memory_evidence_source",
	"467_life_idx_life_memory_dependency_derived_concurrent_index":             "idx_life_memory_dependency_derived",
	"468_life_idx_life_action_proposal_user_status_concurrent_index":           "idx_life_action_proposal_user_status",
	"469_life_idx_life_experiment_user_updated_concurrent_index":               "idx_life_experiment_user_updated",
	"470_life_idx_life_experiment_round_experiment_created_concurrent_index":   "idx_life_experiment_round_experiment_created",
	"471_life_idx_life_experiment_round_status_ends_concurrent_index":          "idx_life_experiment_round_status_ends",
	"472_life_idx_life_experiment_memory_memory_concurrent_index":              "idx_life_experiment_memory_memory",
	"473_life_idx_life_proactive_check_user_checked_concurrent_index":          "idx_life_proactive_check_user_checked",
	"474_life_idx_life_chronicle_user_period_concurrent_index":                 "idx_life_chronicle_user_period",
	"475_life_idx_life_chronicle_evidence_source_concurrent_index":             "idx_life_chronicle_evidence_source",
	"476_life_life_identity_one_active_concurrent_index":                       "life_identity_one_active",
	"477_life_idx_life_relationship_event_open_concurrent_index":               "idx_life_relationship_event_open",
	"478_life_idx_life_material_user_time_concurrent_index":                    "idx_life_material_user_time",
	"479_life_idx_life_derivation_target_concurrent_index":                     "idx_life_derivation_target",
	"480_life_idx_life_topic_user_status_concurrent_index":                     "idx_life_topic_user_status",
	"481_life_idx_life_commitment_due_concurrent_index":                        "idx_life_commitment_due",
	"482_life_idx_life_cognition_job_due_concurrent_index":                     "idx_life_cognition_job_due",
	"483_life_idx_life_experiment_observation_round_concurrent_index":          "idx_life_experiment_observation_round",
	"484_life_idx_life_observer_judgement_status_concurrent_index":             "idx_life_observer_judgement_status",
	"485_life_life_chronicle_one_published_period_concurrent_index":            "life_chronicle_one_published_period",
	"486_life_idx_life_single_running_material_understanding_concurrent_index": "idx_life_single_running_material_understanding",
	"035_task_queue_issue_id_index":                                            "idx_agent_task_queue_issue_id",
	"067_task_queue_claim_candidate_index":                                     "idx_agent_task_queue_claim_candidates",
	"074_task_usage_updated_at_index":                                          "idx_task_usage_updated_at",
	"075_task_usage_created_at_index":                                          "idx_task_usage_created_at",
	"078_task_usage_created_at_legacy_index":                                   "idx_task_usage_created_at_legacy",
	"080_agent_task_queue_queued_index":                                        "idx_agent_task_queue_queued_created_at",
	"106_member_user_workspace_index":                                          "idx_member_user_workspace",
	"114_agent_task_queue_running_started_at_index":                            "idx_agent_task_queue_running_started_at",
	"115_agent_runtime_last_seen_at_index":                                     "idx_agent_runtime_last_seen_at",
	"119_user_created_at_index":                                                "idx_user_created_at",
	"125_agent_task_queue_dispatched_prepare_index":                            "idx_agent_task_queue_dispatched_prepare",
	"135_comment_workspace_index":                                              "idx_comment_workspace",
	"138_issue_title_trgm_index":                                               "idx_issue_title_trgm",
	"139_issue_description_trgm_index":                                         "idx_issue_description_trgm",
	"140_comment_content_trgm_index":                                           "idx_comment_content_trgm",
	"141_project_title_trgm_index":                                             "idx_project_title_trgm",
	"142_project_description_trgm_index":                                       "idx_project_description_trgm",
	"143_agent_task_queue_chat_pending_v2":                                     "idx_agent_task_queue_chat_pending_v2",
	"153_chat_pinned_agent_user_ws_index":                                      "idx_chat_pinned_agent_user_ws",
	"156_chat_session_pinned_index":                                            "idx_chat_session_pinned",
	"160_chat_message_input_owner_index":                                       "idx_chat_message_input_owner",
	"165_attachment_task_id_index":                                             "idx_attachment_task",
	"167_resource_label_namespace_index":                                       "issue_label_workspace_type_name_lower_idx",
	"168_resource_label_type_index":                                            "issue_label_workspace_type_idx",
	"169_agent_label_lookup_index":                                             "agent_to_label_label_idx",
	"170_skill_label_lookup_index":                                             "skill_to_label_label_idx",
	"172_agent_system_identity_index":                                          "agent_system_identity_unique",
	"177_autopilot_run_webhook_delivery_index":                                 "uq_autopilot_run_webhook_delivery",
	"178_webhook_delivery_queue_index":                                         "idx_webhook_delivery_queue",
	"181_task_chat_finalize_deferred_index":                                    "idx_task_chat_finalize_deferred",
	"183_chat_draft_restore_index":                                             "idx_chat_draft_restore_session",
	"187_autopilot_rule_version_index":                                         "idx_autopilot_rule_version_active",
	"192_issue_properties_gin_index":                                           "idx_issue_properties_gin",
	"194_issue_property_workspace_name_index":                                  "idx_issue_property_ws_name",
	"195_issue_property_workspace_index":                                       "idx_issue_property_workspace",
	"200_inbox_archived_listing_index":                                         "idx_inbox_recipient_archived_created",
	"201_inbox_active_by_issue_index":                                          "idx_inbox_active_by_issue",
	"203_issue_workspace_assignee_index":                                       "idx_issue_workspace_assignee",
	"204_issue_workspace_parent_index":                                         "idx_issue_workspace_parent",
	"205_issue_workspace_position_index":                                       "idx_issue_workspace_position",
	"208_client_usage_daily_unique_index":                                      "client_usage_daily_identity_date_uidx",
	"210_client_usage_daily_query_index":                                       "client_usage_daily_activity_client_user_idx",
	"211_client_usage_daily_workspace_index":                                   "client_usage_daily_workspace_idx",
	"215_chat_session_project_index":                                           "idx_chat_session_project",
	"217_vcs_connection_workspace_index":                                       "idx_vcs_connection_workspace",
	"218_vcs_pull_request_workspace_index":                                     "idx_vcs_pull_request_workspace",
	"219_vcs_pull_request_connection_index":                                    "idx_vcs_pull_request_connection",
	"220_issue_vcs_pull_request_pr_index":                                      "idx_issue_vcs_pull_request_pr",
	"221_vcs_commit_status_lookup_index":                                       "idx_vcs_commit_status_lookup",
	"223_github_pr_check_run_pr_ordinal_index":                                 "github_pull_request_check_run_pr_ordinal_idx",
	"228_channel_media_pending_object_key_index":                               "channel_media_pending_object_storage_key_uidx",
	"230_channel_media_pending_object_claim_index":                             "idx_channel_media_pending_object_claim",
	"231_agent_task_queue_terminal_completed_at_index":                         "idx_agent_task_queue_terminal_completed_at",
	"232_channel_media_pending_object_due_index":                               "idx_channel_media_pending_object_due",
	"233_agent_task_queue_agent_terminal_latest_index":                         "idx_agent_task_queue_agent_terminal_latest",
	"238_quick_action_workspace_index":                                         "idx_quick_action_workspace_status_usage",
	"241_comment_parent_lookup_index":                                          "idx_comment_workspace_issue_parent",
	"244_issue_dependency_issue_index":                                         "idx_issue_dependency_issue_id",
	"245_issue_dependency_depends_on_index":                                    "idx_issue_dependency_depends_on_issue_id",
	"246_inbox_item_issue_index":                                               "idx_inbox_item_issue_id",
	"247_comment_parent_index":                                                 "idx_comment_parent_id",
	"248_agent_task_trigger_comment_index":                                     "idx_agent_task_queue_trigger_comment_id",
	"255_agent_task_queue_chat_pending_deferred_v3":                            "idx_agent_task_queue_chat_pending_v3",
	"257_agent_task_queue_channel_media_pending_unique_v2":                     "idx_one_pending_task_per_issue_agent_v2",
	"261_agent_task_queue_terminal_completed_at_v2":                            "idx_agent_task_queue_terminal_completed_at_v2",
	"266_issue_view_owner_index":                                               "idx_issue_view_owner",
	"267_issue_view_shared_index":                                              "idx_issue_view_shared",
	"273_agent_task_queue_runtime_id_index":                                    "idx_agent_task_queue_runtime_id",
	"274_task_token_workspace_id_index":                                        "idx_task_token_workspace_id",
	"275_task_token_agent_id_index":                                            "idx_task_token_agent_id",
	"276_chat_draft_restore_task_id_index":                                     "idx_chat_draft_restore_task_id",
	"277_autopilot_run_task_id_index":                                          "idx_autopilot_run_task_id",
	"278_agent_task_queue_agent_id_keyset_index":                               "idx_agent_task_queue_agent_id_keyset",
	"279_agent_task_queue_issue_id_keyset_index":                               "idx_agent_task_queue_issue_id_keyset",
	"281_agent_workspace_id_keyset_index":                                      "idx_agent_workspace_id_keyset",
	"282_issue_workspace_id_keyset_index":                                      "idx_issue_workspace_id_keyset",
	"283_agent_runtime_workspace_id_keyset_index":                              "idx_agent_runtime_workspace_id_keyset",
	"286_plugin_identity_key_index":                                            "idx_plugin_identity_key",
	"287_plugin_release_version_index":                                         "idx_plugin_release_version",
	"288_plugin_installation_workspace_plugin_index":                           "idx_plugin_installation_workspace_plugin_active",
	"289_plugin_contribution_key_index":                                        "idx_plugin_contribution_release_key",
	"290_plugin_contribution_ordinal_index":                                    "idx_plugin_contribution_release_ordinal",
	"291_plugin_grant_revision_index":                                          "idx_plugin_grant_revision",
	"292_plugin_binding_revision_index":                                        "idx_plugin_binding_revision",
	"293_plugin_installation_workspace_index":                                  "idx_plugin_installation_workspace",
	"295_plugin_artifact_file_index":                                           "idx_plugin_artifact_file_release_path",
	"296_plugin_snapshot_revision_index":                                       "idx_plugin_snapshot_workspace_revision",
	"297_plugin_execution_task_index":                                          "idx_plugin_execution_manifest_task",
	"298_plugin_health_index":                                                  "idx_plugin_health_installation_observed",
	"299_agent_task_plugin_manifest_index":                                     "idx_agent_task_plugin_execution_manifest",
	"305_dingtalk_group_route_installation_conversation_unique":                "idx_dingtalk_group_route_installation_conversation",
	"306_dingtalk_group_route_workspace_index":                                 "idx_dingtalk_group_route_workspace",
	"307_dingtalk_group_route_id_unique":                                       "idx_dingtalk_group_route_id_unique",
	"384_create_dingtalk_group_presence_identity_index":                        "idx_dingtalk_group_presence_installation_conversation",
	"385_create_dingtalk_group_presence_activity_index":                        "idx_dingtalk_group_presence_workspace_activity",
	"388_create_dingtalk_bot_identity_installation_index":                      "idx_dingtalk_bot_identity_installation",
	"309_agent_runtime_id_index":                                               "idx_agent_runtime_id",
	"311_plugin_identity_scoped_key_index":                                     "idx_plugin_identity_scoped_key",
	"316_workspace_mcp_server_name_unique":                                     "idx_workspace_mcp_server_workspace_name",
	"317_agent_mcp_server_server_index":                                        "idx_agent_mcp_server_server",
	"320_plugin_installation_config_revision_index":                            "idx_plugin_installation_config_contribution_revision",
	"321_plugin_installation_config_workspace_index":                           "idx_plugin_installation_config_workspace",
	"322_plugin_remote_mcp_secret_revision_index":                              "idx_plugin_remote_mcp_secret_revision",
	"323_plugin_remote_mcp_secret_workspace_index":                             "idx_plugin_remote_mcp_secret_workspace",
	"324_plugin_remote_mcp_one_active_secret_index":                            "idx_plugin_remote_mcp_one_active_secret",
	"326_plugin_remote_mcp_oauth_state_expiry_index":                           "plugin_remote_mcp_oauth_state_expiry_idx",
	"328_workspace_share_link_id_uidx":                                         "workspace_share_link_pkey_uidx",
	"330_workspace_share_link_active_ws_uidx":                                  "workspace_share_link_active_ws_uidx",
	"331_workspace_share_link_code_uidx":                                       "workspace_share_link_code_uidx",
	"333_issue_status_pkey_index":                                              "issue_status_pkey_uidx",
	"335_issue_status_workspace_key_index":                                     "idx_issue_status_workspace_key",
	"336_issue_status_workspace_name_index":                                    "idx_issue_status_workspace_name_active",
	"343_comment_delegated_failure_pending_index":                              "idx_comment_delegated_failure_pending",
	"345_plugin_installation_workspace_key_index":                              "idx_plugin_installation_workspace_key",
	"346_plugin_storage_scope_key_index":                                       "idx_plugin_storage_scope_key",
	"347_plugin_secret_installation_key_index":                                 "idx_plugin_secret_installation_key",
	"349_agent_task_queue_chat_terminal_resume_index":                          "idx_agent_task_queue_chat_terminal_resume",
	"350_agent_task_queue_chat_retired_session_index":                          "idx_agent_task_queue_chat_retired_session",
	"357_webhook_delivery_replay_idempotency_index":                            "uq_webhook_delivery_replay_idempotency",
	"361_issue_last_activity_index":                                            "idx_issue_workspace_last_activity",
	"363_plugin_invocation_installation_index":                                 "idx_plugin_invocation_installation_created",
	"364_plugin_invocation_created_at_index":                                   "idx_plugin_invocation_created_at",
	"378_channel_chat_context_generation_key":                                  "channel_chat_context_generation_session_revision_idx",
	"390_agent_task_queue_dispatched_reclaim_v2_index":                         "idx_agent_task_queue_dispatched_reclaim_v2",
	"393_plugin_package_workspace_key_index":                                   "idx_plugin_package_workspace_key",
	"394_plugin_package_version_unique_index":                                  "idx_plugin_package_version_unique",
	"395_plugin_package_version_package_index":                                 "idx_plugin_package_version_package",
	"396_plugin_package_file_path_index":                                       "idx_plugin_package_file_path",
	"397_plugin_installation_package_version_index":                            "idx_plugin_installation_package_version",
	"398_issue_workspace_status_position_index":                                "idx_issue_workspace_status_position",
	"400_plugin_hook_schedule_installation_key_index":                          "idx_plugin_hook_schedule_installation_key",
	"401_plugin_hook_schedule_enabled_index":                                   "idx_plugin_hook_schedule_enabled",
	"408_issue_source_context_id_index":                                        "idx_issue_source_context_id",
	"409_issue_source_context_issue_index":                                     "idx_issue_source_context_issue",
	"410_issue_source_context_origin_task_index":                               "idx_issue_source_context_origin_task",
	"411_attachment_source_context_index":                                      "idx_attachment_source_context",
	"412_issue_source_context_object_intent_key_index":                         "idx_issue_source_context_object_intent_key",
	"413_issue_source_context_object_intent_due_index":                         "idx_issue_source_context_object_intent_due",
	"414_issue_source_context_object_intent_context_index":                     "idx_issue_source_context_object_intent_context",
	"421_channel_chat_active_route_index":                                      "idx_channel_chat_session_binding_active_route",
	"423_channel_task_delivery_pkey_index":                                     "channel_task_delivery_pkey",
	"426_channel_outbound_message_id_index":                                    "idx_channel_outbound_message_id",
	"428_channel_task_delivery_binding_index":                                  "idx_channel_task_delivery_binding",
	"429_channel_task_delivery_installation_index":                             "idx_channel_task_delivery_installation",
	"430_channel_outbound_message_binding_index":                               "idx_channel_outbound_message_binding_route",
	"438_agent_runtime_online_last_seen_index":                                 "idx_agent_runtime_online_last_seen",
	"439_agent_runtime_offline_last_seen_index":                                "idx_agent_runtime_offline_last_seen",
	"440_github_pr_head_sha_index":                                             "idx_github_pull_request_head_sha",
	"443_issue_project_status_index":                                           "idx_issue_project_status",
	"445_comment_delegated_failure_unsettled_index":                            "idx_comment_delegated_failure_unsettled",
	"489_task_trace_event_indexes":                                             "idx_task_trace_event_workspace_created",
	"491_external_credential_profile_indexes":                                  "idx_external_credential_profile_user_idempotency",
	"492_task_trace_event_task_index":                                          "idx_task_trace_event_task_created",
	"493_task_trace_event_issue_index":                                         "idx_task_trace_event_issue_created",
	"494_task_trace_event_agent_index":                                         "idx_task_trace_event_agent_created",
	"495_task_trace_event_squad_index":                                         "idx_task_trace_event_squad_created",
	"496_external_credential_profile_provider_index":                           "idx_external_credential_profile_user_provider",
}

// concurrentDownIndexCleanups 覆盖每条"回滚方向会用 CREATE INDEX CONCURRENTLY
// 重建索引"的迁移。被打断的回滚会留下 INVALID 关系：IF NOT EXISTS 会静默跳过
// 重试，裸 CREATE 又会卡在 "already exists"——两种情况都需要在回滚安全重试前
// 做一次"方向相关"的清理。
var concurrentDownIndexCleanups = map[string]string{
	"551_supervision_parallel":                              "supervision_active_workspace",
	"144_drop_agent_task_queue_chat_pending_v1":             "idx_agent_task_queue_chat_pending",
	"171_drop_legacy_label_namespace_index":                 "issue_label_workspace_name_lower_idx",
	"256_drop_agent_task_queue_chat_pending_v2":             "idx_agent_task_queue_chat_pending_v2",
	"258_drop_pending_issue_agent_v1":                       "idx_one_pending_task_per_issue_agent",
	"262_drop_agent_task_queue_terminal_completed_at_v1":    "idx_agent_task_queue_terminal_completed_at",
	"422_channel_chat_route_history":                        "channel_chat_session_binding_installation_id_channel_chat_i_key",
	"300_drop_redundant_issue_workspace_number_index":       "idx_issue_workspace_number",
	"301_drop_redundant_sys_cron_job_plan_index":            "idx_sys_cron_exec_job_plan",
	"302_drop_redundant_channel_chat_session_binding_index": "idx_channel_chat_session_binding_session",
	"303_drop_redundant_lark_chat_session_binding_index":    "idx_lark_chat_session_binding_session",
	"371_comment_content_search_index_strategy":             "idx_comment_content_trgm",
	"375_drop_issue_last_activity_index":                    "idx_issue_workspace_last_activity",
	"391_drop_agent_task_queue_dispatched_prepare_index":    "idx_agent_task_queue_dispatched_prepare",
	"437_drop_agent_runtime_last_seen_at_index":             "idx_agent_runtime_last_seen_at",
}

var preMigrationHooks = func() map[string]preMigrationHook {
	hooks := map[string]preMigrationHook{
		"103_drop_legacy_daily_rollups":                         runTaskUsageHourlyHook,
		"198_agent_task_attribution_strict_constraint_validate": runAttributionStrictHook,
		"431_chat_explicit_origin_backfill":                     runChatOriginBackfillHook,
	}
	for version, index := range concurrentIndexCleanups {
		hooks[version] = cleanupInvalidConcurrentIndexHook(index)
	}
	return hooks
}()

var transactionalMigrationHooks = map[string]transactionalMigrationHook{
	"456_remove_retired_builtins": removeRetiredBuiltinsTx,
}

var preRollbackHooks = func() map[string]preMigrationHook {
	hooks := make(map[string]preMigrationHook, len(concurrentDownIndexCleanups)+len(sourceContextMigrationVersions)+1)
	for version, index := range concurrentDownIndexCleanups {
		hooks[version] = cleanupInvalidConcurrentIndexHook(index)
	}
	// 来源上下文（source context）的索引被拆成"每句一条"的并发迁移。给每一步都
	// 注册护栏：无论从哪个"只应用了一部分"的版本回滚，都会在删掉第一条仍在线
	// 的索引之前就失败停下。
	for _, version := range sourceContextMigrationVersions {
		hooks[version] = ensureSourceContextRollbackSafe
	}
	// 迁移 344 有意删掉了 V1 插件表。若回滚跨越那次重置，迁移 312 的历史索引
	// 目标已不存在；这里只在表仍存在时恢复它，并且放在迁移事务之外——保证
	// CREATE INDEX CONCURRENTLY 仍然合法。
	hooks["312_drop_global_plugin_identity_key_index"] = restorePluginIdentityIndexIfPresent
	hooks["430_channel_outbound_message_binding_index"] = refuseChannelChatRouteHistoryRollback
	return hooks
}()

var sourceContextMigrationVersions = []string{
	"407_issue_source_context",
	"408_issue_source_context_id_index",
	"409_issue_source_context_issue_index",
	"410_issue_source_context_origin_task_index",
	"411_attachment_source_context_index",
	"412_issue_source_context_object_intent_key_index",
	"413_issue_source_context_object_intent_due_index",
	"414_issue_source_context_object_intent_context_index",
}

// restorePluginIdentityIndexIfPresent 在表 plugin_identity 仍存在时恢复其历史
// 唯一索引（迁移 312 回滚跨过 344 的重置时需要它）；表不存在则空操作。
func restorePluginIdentityIndexIfPresent(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.plugin_identity') IS NOT NULL").Scan(&exists); err != nil {
		return fmt.Errorf("check plugin identity table: %w", err)
	}
	if !exists {
		return nil
	}
	if _, err := pool.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_plugin_identity_key ON public.plugin_identity (plugin_key)"); err != nil {
		return fmt.Errorf("restore plugin identity key index: %w", err)
	}
	return nil
}

// rowQuerier 是"能跑单行查询"的最小接口（便于对护栏函数做测试注入）。
type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// refuseChannelChatRouteHistoryRollback 在连接池版本上执行回滚护栏。
func refuseChannelChatRouteHistoryRollback(ctx context.Context, pool *pgxpool.Pool) error {
	return refuseChannelChatRouteHistoryRollbackWith(ctx, pool)
}

// runChatOriginBackfillHook 跑聊天显式来源（chat explicit origin）回填钩子
// （迁移 431 之前把历史会话的来源补上）。
func runChatOriginBackfillHook(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := chatoriginbackfill.Hook(ctx, pool, chatoriginbackfill.HookOptions{})
	return err
}

// refuseChannelChatRouteHistoryRollbackWith 检查是否已存在"被退休的频道聊天绑定"
// 或"显式创建的会话"；一旦 /new 已建出 channel Chat，就拒绝回滚频道聊天路由
// （数据已不可逆）。
func refuseChannelChatRouteHistoryRollbackWith(ctx context.Context, query rowQuerier) error {
	var channelChatRouteStateExists bool
	if err := query.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM channel_chat_session_binding AS binding
			LEFT JOIN chat_session AS session ON session.id = binding.chat_session_id
			WHERE binding.retired_at IS NOT NULL
			   OR session.explicitly_created_at IS NOT NULL
		)
	`).Scan(&channelChatRouteStateExists); err != nil {
		return fmt.Errorf("check channel chat route state: %w", err)
	}
	if channelChatRouteStateExists {
		return errors.New("cannot roll back channel chat routes after /new has created a channel Chat")
	}
	return nil
}

var upMigrationConditions = map[string]migrationCondition{
	// 全新数据库若已成功建出 CJK 友好的 bigram 索引，就不必再建 trigram 兜底、
	// 回头又要在 371 把它删掉。
	"140_comment_content_trgm_index": whenIndexNotUsable(commentContentBigramIndex),
	// 已在用 pg_bigm 的部署两套索引都有。只有先证明首选索引具备精确可用形态才
	// 删兜底；没有 pg_bigm 的自托管库保留 trigram，并把 371 记成空操作。
	"371_comment_content_search_index_strategy": whenIndexUsable(commentContentBigramIndex),
}

// hooksForDirection 按方向挑选 pre-migration 钩子表：up 用 preMigrationHooks，
// down 用 preRollbackHooks。
func hooksForDirection(direction string) map[string]preMigrationHook {
	switch direction {
	case "up":
		return preMigrationHooks
	case "down":
		return preRollbackHooks
	default:
		return nil
	}
}

// transactionalHooksForDirection 按方向挑选事务型钩子表（目前只有 up 有）。
func transactionalHooksForDirection(direction string) map[string]transactionalMigrationHook {
	if direction == "up" {
		return transactionalMigrationHooks
	}
	return nil
}

// ensureSourceContextRollbackSafe 拒绝在来源上下文仍有数据（捕获记录或对象）时
// 回滚相关迁移：必须先通过应用层清理把捕获内容与其存储对象删干净再重试。
func ensureSourceContextRollbackSafe(ctx context.Context, pool *pgxpool.Pool) error {
	var dataExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM issue_source_context)
		    OR EXISTS (SELECT 1 FROM attachment WHERE source_context_id IS NOT NULL)
		    OR EXISTS (SELECT 1 FROM issue_source_context_object_intent)
	`).Scan(&dataExists); err != nil {
		return fmt.Errorf("inspect source-context rollback ownership: %w", err)
	}
	if dataExists {
		return errors.New("cannot roll back issue source context while captured data or stored objects still exist; remove source-context captures and their stored objects through application cleanup, then retry")
	}
	return nil
}

func conditionsForDirection(direction string) map[string]migrationCondition {
	if direction == "up" {
		return upMigrationConditions
	}
	// 回滚方向刻意忽略"环境门"：无论当初哪段 up SQL 真的跑过，回滚都是把 schema
	// 恢复成可移植的迁移前形态。
	return nil
}

// whenIndexUsable 构造"首选索引可用才执行 SQL"的条件（false 时会给出原因）。
func whenIndexUsable(requirement usableIndexRequirement) migrationCondition {
	return func(ctx context.Context, conn *pgxpool.Conn) (bool, string, error) {
		usable, err := indexIsUsable(ctx, conn, requirement)
		if err != nil {
			return false, "", err
		}
		if !usable {
			return false, fmt.Sprintf("preferred index %s is unavailable or unusable", requirement.IndexRegclass), nil
		}
		return true, "", nil
	}
}

// whenIndexNotUsable 构造"首选索引不可用才执行 SQL"的条件（可用时跳过并给原因）。
func whenIndexNotUsable(requirement usableIndexRequirement) migrationCondition {
	return func(ctx context.Context, conn *pgxpool.Conn) (bool, string, error) {
		usable, err := indexIsUsable(ctx, conn, requirement)
		if err != nil {
			return false, "", err
		}
		if usable {
			return false, fmt.Sprintf("preferred index %s is ready", requirement.IndexRegclass), nil
		}
		return true, "", nil
	}
}

// indexIsUsable fail-closed（不满足就当不可用）：搜索查询需要的每个属性都必须
// 匹配，才允许跳过或删掉兜底索引。特别是，首选关系必须是"活的、ready 的、
// 有效的、非部分的 GIN 表达式索引"，且归期望的扩展所有。同名但陈旧或漂移的
// 索引会被当成不可用，从而保住兜底。
func indexIsUsable(ctx context.Context, conn *pgxpool.Conn, requirement usableIndexRequirement) (bool, error) {
	var usable bool
	err := conn.QueryRow(ctx, `
		SELECT COALESCE(
			idx.relkind = 'i'
			AND i.indisvalid
			AND i.indisready
			AND i.indislive
			AND NOT i.indisunique
			AND i.indpred IS NULL
			AND i.indexprs IS NOT NULL
			AND i.indrelid = to_regclass($2)
			AND i.indnkeyatts = 1
			AND i.indnatts = 1
			AND am.amname = $3
			AND opc.opcname = $4
			AND pg_get_indexdef(i.indexrelid, 1, FALSE) = $5
			AND EXISTS (
				SELECT 1
				FROM pg_depend dep
				JOIN pg_extension ext ON ext.oid = dep.refobjid
				WHERE dep.classid = 'pg_opclass'::regclass
				  AND dep.objid = opc.oid
				  AND dep.refclassid = 'pg_extension'::regclass
				  AND dep.deptype = 'e'
				  AND ext.extname = $6
			),
			FALSE
		)
		FROM pg_class idx
		LEFT JOIN pg_index i ON i.indexrelid = idx.oid
		LEFT JOIN pg_am am ON am.oid = idx.relam
		-- pg_index.indclass is an int2vector, whose subscripts start at zero.
		LEFT JOIN pg_opclass opc ON opc.oid = i.indclass[0]
		WHERE idx.oid = to_regclass($1)
	`,
		requirement.IndexRegclass,
		requirement.TableRegclass,
		requirement.AccessMethod,
		requirement.OperatorClass,
		requirement.Expression,
		requirement.Extension,
	).Scan(&usable)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect preferred index %q: %w", requirement.IndexRegclass, err)
	}
	return usable, nil
}

// cleanupInvalidConcurrentIndexHook 在迁移重试前删掉"被中断或失败的 CREATE INDEX
// CONCURRENTLY"留下的 INVALID 索引。没有这道护栏，CREATE INDEX ... IF NOT EXISTS
// 会把残留关系当成成功，并允许更晚的迁移删掉那条仍有效的旧索引。若同名关系不是
// 索引则 fail-closed 报错，绝不隐式删表。
func cleanupInvalidConcurrentIndexHook(indexRegclass string) preMigrationHook {
	return func(ctx context.Context, pool *pgxpool.Pool) error {
		var schemaName, relationName string
		var isIndex, isValid bool
		err := pool.QueryRow(ctx, `
			SELECT n.nspname, c.relname, c.relkind = 'i', COALESCE(i.indisvalid, FALSE)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_index i ON i.indexrelid = c.oid
			WHERE c.oid = to_regclass($1)
		`, indexRegclass).Scan(&schemaName, &relationName, &isIndex, &isValid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect concurrent index %q: %w", indexRegclass, err)
		}
		if !isIndex {
			return fmt.Errorf("relation %q exists but is not an index", indexRegclass)
		}
		if isValid {
			return nil
		}

		qualifiedName := pgx.Identifier{schemaName, relationName}.Sanitize()
		if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+qualifiedName); err != nil {
			return fmt.Errorf("drop invalid concurrent index %s: %w", qualifiedName, err)
		}
		slog.Warn("removed invalid index before migration retry", "index", qualifiedName)
		return nil
	}
}

// runTaskUsageHourlyHook 在迁移 103 评估 fail-closed 滞后闸门之前，用与
// `cmd/backfill_task_usage_hourly` 相同的幂等按月切片回填把小时聚合水位推上去
// （MUL-2957）。
func runTaskUsageHourlyHook(ctx context.Context, pool *pgxpool.Pool) error {
	res, err := taskusagebackfill.Hook(ctx, pool, taskusagebackfill.HookOptions{})
	if err != nil {
		return fmt.Errorf("task_usage_hourly pre-103 hook: %w", err)
	}
	if res.Skipped != "" {
		slog.Info("task_usage hourly rollup hook: skipped",
			"reason", res.Skipped,
			"watermark_stamped", res.WatermarkStamped)
		return nil
	}
	slog.Info("task_usage hourly rollup hook: backfill complete",
		"slices", res.SlicesProcessed,
		"rows_touched", res.RowsTouched,
		"from", res.From.Format("2006-01-02T15:04:05Z07:00"),
		"to", res.To.Format("2006-01-02T15:04:05Z07:00"))
	return nil
}

// runAttributionStrictHook 在迁移 198 校验严格 attribution 约束之前，先把
// accountable_user_id 从 originator_user_id 回填过来——没跑过带外回填的自托管
// 升级由此自动恢复（GH #5544 / MUL-4897）。
func runAttributionStrictHook(ctx context.Context, pool *pgxpool.Pool) error {
	res, err := attributionbackfill.Hook(ctx, pool, attributionbackfill.HookOptions{})
	if err != nil {
		return fmt.Errorf("attribution strict-constraint pre-198 hook: %w", err)
	}
	slog.Info("attribution backfill hook: complete",
		"rows_backfilled", res.RowsBackfilled,
		"batches", res.Batches,
		"mismatch_normalized", res.MismatchNormalized)
	return nil
}

// migrationAdvisoryLockKey 是配合 Postgres pg_advisory_lock 使用的 int64 标识，
// 用于在并发 runner（多副本后端 Deployment、扩容、手动 `migrate up` 与 pod 启动
// 重叠）之间串行化迁移循环。具体数值任意——只需在"所有对同一数据库跑迁移的进
// 程"之间保持稳定。见 GitHub multica-ai/multica#3647。
const migrationAdvisoryLockKey int64 = 7244554146635925501

// defaultSchemaMigrationsTable 是台账表的非限定名，记录哪些迁移已被应用。测试会
// 覆盖这个值，让并发竞态测试可以在共享 Postgres 上各跑各的表、不撞生产表。
const defaultSchemaMigrationsTable = "schema_migrations"

// runOptions 携带 runMigrations 需要、但不是连接池本身的东西。测试用它注入封闭
// 的迁移目录、每测试唯一的台账表、以及不会与共享同一 Postgres 实例的其他迁移
// runner 相撞的唯一咨询锁键。
type runOptions struct {
	// Direction 取值 "up" 或 "down"。
	Direction string
	// Files 是要应用的 .sql 文件的有序列表。生产调用方传 migrations.Files(direction)；
	// 测试传一组写到 t.TempDir() 的精选文件。
	Files []string
	// SchemaMigrationsTable 是要读写的台账表，可带 schema 前缀（例如
	// "migrate_test_xyz.schema_migrations"）。空值表示 defaultSchemaMigrationsTable。
	SchemaMigrationsTable string
	// AdvisoryLockKey 是给 pg_advisory_lock 的 int64。0 表示
	// migrationAdvisoryLockKey。测试每轮传唯一键，让并发的测试 worker 即使与
	// 生产迁移 runner 共享数据库也不会互相阻塞。
	AdvisoryLockKey int64
	// Hooks 映射"迁移版本 → 迁移前钩子"。钩子拿到的是连接池（不是循环固定占用
	// 的 conn），因此可以自己去拿会话级锁。nil 或缺省条目表示"没有钩子"、迁移直
	// 接跑。生产 main() 传入按方向挑选的钩子表；测试不测钩子就留 nil。
	Hooks map[string]preMigrationHook
	// TransactionalHooks 与迁移 SQL 及台账行同事务执行。它们刻意独立于 Hooks，
	// 因为并发建索引的迁移不能在事务块里跑。
	TransactionalHooks map[string]transactionalMigrationHook
	// Conditions 映射"迁移版本 → 决定该版本 SQL 是否执行的只读门"。判定为 false
	// 仍然推进台账，让那些在特定环境里"刻意不需要该 DDL"的版本不挡后面的迁移。
	Conditions map[string]migrationCondition
}

// main 是 `cmd/migrate` 的命令入口：解析 up|down 方向、建连接池（含启动重试）、
// 枚举迁移文件，调用 runMigrations；若迁移被"瞬时数据库故障"打断且配了启动超时，
// 会按重试策略整个再来一遍。任何一步失败都以非零码退出。
func main() {
	logger.Init()

	if len(os.Args) < 2 {
		fmt.Println("Usage: go run ./cmd/migrate <up|down>")
		os.Exit(1)
	}

	direction := os.Args[1]
	if direction != "up" && direction != "down" && direction != "accept-repositories" && direction != "restore-repositories" {
		fmt.Println("Usage: go run ./cmd/migrate <up|down>")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	startupSettings := dbstartup.SettingsFromEnv()
	pool, err := dbstartup.NewPool(context.Background(), dbURL, startupSettings.ConnectTimeout)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if direction == "accept-repositories" || direction == "restore-repositories" {
		if err := repositoryTransition(context.Background(), pool, direction); err != nil {
			slog.Error("repository migration transition failed", "error", err)
			os.Exit(1)
		}
		return
	}

	files, err := migrations.Files(direction)
	if err != nil {
		slog.Error("failed to find migration files", "error", err)
		os.Exit(1)
	}

	if len(os.Args) > 2 {
		if len(os.Args) != 4 || os.Args[2] != "--versions" {
			slog.Error("expected --versions name,name")
			os.Exit(1)
		}
		selected := map[string]bool{}
		for _, name := range strings.Split(os.Args[3], ",") {
			selected[name] = true
		}
		filtered := files[:0]
		for _, file := range files {
			if selected[migrations.ExtractVersion(file)] {
				filtered = append(filtered, file)
				delete(selected, migrations.ExtractVersion(file))
			}
		}
		if len(selected) > 0 {
			slog.Error("unknown migration version")
			os.Exit(1)
		}
		files = filtered
	}
	options := runOptions{
		Direction:          direction,
		Files:              files,
		Hooks:              hooksForDirection(direction),
		TransactionalHooks: transactionalHooksForDirection(direction),
		Conditions:         conditionsForDirection(direction),
	}
	startupCtx, stopStartup := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopStartup()
	retryOptions := startupSettings.RetryOptions()
	retryOptions.ShouldRetry = dbstartup.IsTransientDatabaseError
	retryOptions.OnRetry = func(event dbstartup.RetryEvent) {
		slog.Warn("database unavailable before migrations; retrying",
			"attempt", event.Attempt,
			"retry_in", event.Delay,
			"error", event.Err,
		)
	}
	if err := dbstartup.Retry(startupCtx, retryOptions, pool.Ping); err != nil {
		slog.Error("unable to ping database", "error", err)
		os.Exit(1)
	}

	migrationErr := runMigrations(startupCtx, pool, options)
	if migrationErr != nil && startupSettings.StartupTimeout > 0 && dbstartup.IsTransientDatabaseError(migrationErr) {
		slog.Warn("migration interrupted by database unavailability; retrying", "error", migrationErr)
		migrationRetryOptions := startupSettings.RetryOptions()
		migrationRetryOptions.ShouldRetry = dbstartup.IsTransientDatabaseError
		migrationRetryOptions.AllowOperationPastTimeout = true
		migrationRetryOptions.OnRetry = func(event dbstartup.RetryEvent) {
			slog.Warn("database unavailable during migration retry",
				"attempt", event.Attempt,
				"retry_in", event.Delay,
				"error", event.Err,
			)
		}
		migrationErr = dbstartup.Retry(startupCtx, migrationRetryOptions, func(attemptCtx context.Context) error {
			if err := pool.Ping(attemptCtx); err != nil {
				return fmt.Errorf("ping database: %w", err)
			}
			return runMigrations(attemptCtx, pool, options)
		})
	}
	if migrationErr != nil {
		slog.Error("migration run failed", "error", migrationErr)
		os.Exit(1)
	}

	fmt.Println("Done.")
}

// runMigrations 在给定连接池上按方向 up/down 应用/回滚文件列表，并用 Postgres
// 会话级咨询锁串行化，让多个并发 runner（多副本启动、扩容、手动迁移重叠）轮流
// 执行而不是互相竞态。
//
// 多个 goroutine/进程用相同 options 对同一数据库并发调用是安全的：每个调用方都
// 阻塞在 pg_advisory_lock 上；轮到它时，"已应用 EXISTS 检查"会把每条跑完的迁移
// 变成空操作跳过。见 GitHub multica-ai/multica#3647 / MUL-2923。
func runMigrations(ctx context.Context, pool *pgxpool.Pool, opts runOptions) error {
	switch opts.Direction {
	case "up", "down":
		// ok
	default:
		return fmt.Errorf("invalid direction %q (want \"up\" or \"down\")", opts.Direction)
	}

	table := opts.SchemaMigrationsTable
	if table == "" {
		table = defaultSchemaMigrationsTable
	}
	tableIdent, err := quoteQualifiedIdentifier(table)
	if err != nil {
		return fmt.Errorf("invalid schema migrations table %q: %w", table, err)
	}
	lockKey := opts.AdvisoryLockKey
	if lockKey == 0 {
		lockKey = migrationAdvisoryLockKey
	}

	// pg_advisory_lock 的作用域是一个会话，所以整轮必须固定占用一条 *pgxpool.Conn
	// ——用 pool.Exec 会把锁挂到一条随机的连接上，而 pgxpool 可能在循环结束前把
	// 这条连接发回去，等于锁形同虚设。用阻塞版 pg_advisory_lock（而非 pg_try_*）
	// 让晚到的 runner 在现任后面排队而不是崩溃循环；一旦它拿到锁，下面的 EXISTS
	// 检查会把跑完的迁移变成空操作跳过。
	//
	// 刻意不用单个大事务包住整个循环：仓库里已有用 CREATE INDEX CONCURRENTLY 的
	// 迁移，而 Postgres 不允许它在事务块里执行。
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	// 成功路径上尽力显式解锁。出错返回时 defer 仍会执行；main() 里 os.Exit 的
	// 出错路径不会执行 defer，但会话级咨询锁会随进程退出时连接关闭而自动释放，
	// 因此下一个 runner 绝不会被永久阻塞。
	defer func() {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			slog.Warn("failed to release migration advisory lock", "error", err)
		}
	}()

	// 创建迁移台账表。
	if _, err := conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`, tableIdent)); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	existsSQL := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE version = $1)", tableIdent)
	insertSQL := fmt.Sprintf("INSERT INTO %s (version) VALUES ($1)", tableIdent)
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE version = $1", tableIdent)

	var hasBaseline bool
	if err := conn.QueryRow(ctx, existsSQL, "000_initial_schema").Scan(&hasBaseline); err != nil {
		return err
	}
	for _, file := range opts.Files {
		version := migrations.ExtractVersion(file)
		if hasBaseline && migrations.CoveredByBaseline(version) {
			continue
		}

		var exists bool
		if err := conn.QueryRow(ctx, existsSQL, version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %q: %w", version, err)
		}

		if opts.Direction == "up" {
			if exists {
				fmt.Printf("  skip  %s (already applied)\n", version)
				continue
			}
		} else {
			if !exists {
				fmt.Printf("  skip  %s (not applied)\n", version)
				continue
			}
		}

		sql, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", file, err)
		}

		// 事务型数据钩子按方向区分。清理已退休内置项是不可逆的升级动作；回滚
		// （down）时再跑一遍会静默删掉刚创建的新数据。
		if opts.Direction == "up" {
			if hook, ok := opts.TransactionalHooks[version]; ok && hook != nil {
				if _, hasCondition := opts.Conditions[version]; hasCondition {
					return fmt.Errorf("transactional migration %q cannot use a conditional SQL gate", version)
				}
				if err := runTransactionalMigration(ctx, conn, hook, string(sql), insertSQL, version); err != nil {
					return err
				}
				fmt.Printf("  %s  %s\n", opts.Direction, version)
				continue
			}
		}

		// 先跑 SQL 文件之前的任何 pre-migration 钩子。钩子收到 *pgxpool.Pool
		//（而非循环固定占用的 conn），所以它们可以去拿其它会话级锁、不会和
		// migrationAdvisoryLockKey 撞车。钩子失败会在更新 schema_migrations 之前
		// 中止整轮，于是同一版本下次调用能干净地重试。
		if hook, ok := opts.Hooks[version]; ok && hook != nil {
			slog.Info("running pre-migration hook", "version", version, "direction", opts.Direction)
			if err := hook(ctx, pool); err != nil {
				return fmt.Errorf("pre-migration hook for %q (%s): %w", version, opts.Direction, err)
			}
		}

		applySQL := true
		conditionReason := ""
		if condition, ok := opts.Conditions[version]; ok && condition != nil {
			applySQL, conditionReason, err = condition(ctx, conn)
			if err != nil {
				return fmt.Errorf("evaluate migration condition for %q (%s): %w", version, opts.Direction, err)
			}
		}

		if applySQL {
			if _, err := conn.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("apply migration %q: %w", file, err)
			}
		}

		if opts.Direction == "up" {
			_, err = conn.Exec(ctx, insertSQL, version)
		} else {
			_, err = conn.Exec(ctx, deleteSQL, version)
		}
		if err != nil {
			return fmt.Errorf("record migration %q: %w", version, err)
		}

		if applySQL {
			fmt.Printf("  %s  %s\n", opts.Direction, version)
		} else {
			fmt.Printf("  %s  %s (SQL skipped: %s)\n", opts.Direction, version, conditionReason)
		}
	}

	return nil
}

// runTransactionalMigration 在一个事务里依次执行：事务钩子 → 迁移 SQL → 插入
// 台账记录，然后提交。任一步失败都会回滚，保证"数据变更 + 已应用标记"原子一致。
func runTransactionalMigration(ctx context.Context, conn *pgxpool.Conn, hook transactionalMigrationHook, sql, insertSQL, version string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transactional migration %q: %w", version, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	slog.Info("running transactional migration hook", "version", version, "direction", "up")
	if err := hook(ctx, tx); err != nil {
		return fmt.Errorf("transactional migration hook for %q: %w", version, err)
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("apply migration %q: %w", version, err)
	}
	if _, err := tx.Exec(ctx, insertSQL, version); err != nil {
		return fmt.Errorf("record migration %q: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %q: %w", version, err)
	}
	committed = true
	return nil
}

// quoteQualifiedIdentifier 安全地给表名加引号：支持裸表名（"foo"）或带 schema
// 的名字（"schema.foo"），用于拼进 SQL 语句。Postgres 不允许用参数化查询提供
// 标识符，所以只能拼接，但 pgx.Identifier 会做正确的转义（双引号、内嵌引号处理）。
//
// 接受形态恰好是一或两段、点分隔。含多于一个点的名字直接拒绝——与其被悄悄净化
// 成 "schema"."b.c" 这种合法 SQL 但几乎肯定不是调用方意图的引用，不如报错。
func quoteQualifiedIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty identifier")
	}
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("identifier %q has more than one dot; only schema.table is supported", name)
	}
	for _, p := range parts {
		if p == "" {
			return "", fmt.Errorf("empty component in %q", name)
		}
	}
	return pgx.Identifier(parts).Sanitize(), nil
}
