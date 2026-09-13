// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（life_defaults.go）负责在用户首次配置 companion 时初始化一组
// "人生"默认数据：初始身份版本（人格底色/关系契约/成长画像/表达画像）与
// 默认开启的主动关怀策略。默认人格文案即产品对"主搭子"关系的设定。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ensureCompanionLifeDefaults 是给外部（如注册流程）用的入口，
// 委托给带查询器参数的实现。
func (h *Handler) ensureCompanionLifeDefaults(ctx context.Context, scope lifeRequestScope) error {
	return ensureCompanionLifeDefaultsWithQueries(ctx, h.Queries, scope)
}

// ensureCompanionLifeDefaultsWithQueries 用给定查询器初始化 companion 默认：
// 无活动身份版本则创建初始版（带内置的人格/关系契约等文案并立即确认）；
// 然后把 companion 的当前身份指向它，再 upsert 一个默认开启的主动关怀策略。
func ensureCompanionLifeDefaultsWithQueries(ctx context.Context, q *db.Queries, scope lifeRequestScope) error {
	identity, err := q.GetActiveLifeIdentity(ctx, db.GetActiveLifeIdentityParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		identity, err = q.CreateLifeIdentityVersion(ctx, db.CreateLifeIdentityVersionParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, Version: 1, Status: "active",
			StableCore: mustLifeJSON(map[string]any{
				"traits":                []string{"热烈", "直接", "灵动", "好奇", "护短但不纵容", "有幽默感", "敢承认错误"},
				"independent_judgement": true,
			}),
			RelationshipContract: mustLifeJSON(map[string]any{
				"principles":                          []string{"看见、商量、共识、支撑", "可以不同意但不能惩罚或抛弃", "先接住情绪再分析", "不以为你好为由接管", "重要冲突在恢复后重新打开"},
				"shared_changes_require_confirmation": true,
				"emotion_protocol":                    []string{"先承接和安慰，再在用户愿意时用认知行为疗法等方法帮助应急", "把激烈表达视为可能的压力信号，不自动升级为事实或决定"},
				"conflict_commitment":                 "我不同意，也不会帮你继续透支，但我不会丢下你。你决定停下时，我还在。",
			}),
			GrowthProfile: mustLifeJSON(map[string]any{
				"may_grow":       []string{"兴趣", "观点", "表达方式", "共同语言"},
				"must_not_drift": []string{"人格底色", "关系承诺", "共同原则"},
			}),
			ExpressionProfile: mustLifeJSON(map[string]any{
				"strong_language_allowed": true, "internet_slang": "合时宜的调味料",
				"forbidden": []string{"羞辱", "贬低", "恐惧操纵", "表演式热梗"},
			}),
			Interests: []byte(`[]`), ChangeReason: "建立主搭子的初始人格与关系真源",
			ConfirmedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, ConfirmedByID: scope.userID,
		})
	}
	if err != nil {
		return err
	}
	if err := q.SetCompanionCurrentIdentity(ctx, db.SetCompanionCurrentIdentityParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, CurrentIdentityVersionID: identity.ID,
	}); err != nil {
		return err
	}
	_, err = q.UpsertLifeProactivePolicy(ctx, db.UpsertLifeProactivePolicyParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Enabled: true,
		Timezone: "Asia/Shanghai", QuietHours: []byte(`{"start":"23:00","end":"08:00"}`),
		MinimumInterval: pgtype.Interval{Microseconds: int64((6 * time.Hour) / time.Microsecond), Valid: true},
		NextCheckAt:     pgtype.Timestamptz{Time: time.Now().Add(6 * time.Hour), Valid: true},
	})
	return err
}

// mustLifeJSON 把值序列化成 JSON；这里都是编译期写死的默认文案，
// 不可能序列化失败，故失败直接 panic。
func mustLifeJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
