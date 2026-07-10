package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func promptEvaluationAssetToResponse(asset db.PromptEvaluationAsset) PromptEvaluationAssetResponse {
	return PromptEvaluationAssetResponse{
		ID:                       uuidToString(asset.ID),
		WorkspaceID:              uuidToString(asset.WorkspaceID),
		PromptID:                 uuidToPtr(asset.PromptID),
		Name:                     asset.Name,
		Description:              asset.Description,
		AssetType:                asset.AssetType,
		Payload:                  decodeJSONDefault(asset.Payload, map[string]any{}),
		Status:                   asset.Status,
		CreatedBy:                uuidToPtr(asset.CreatedBy),
		CreatedAt:                timestampToString(asset.CreatedAt),
		UpdatedAt:                timestampToString(asset.UpdatedAt),
		StructureSchema:          asset.StructureSchema,
		StructuredCaseCount:      asset.StructuredCaseCount,
		StructuredVariableCount:  asset.StructuredVariableCount,
		StructuredAssertionCount: asset.StructuredAssertionCount,
		LinkedDatasetCount:       asset.LinkedDatasetCount,
		LinkedPromptCount:        asset.LinkedPromptCount,
		EvaluationDimensionCount: asset.EvaluationDimensionCount,
		DatasetRowCount:          asset.DatasetRowCount,
		TestSuiteCaseCount:       asset.TestSuiteCaseCount,
		ExperimentDimensionCount: asset.ExperimentDimensionCount,
	}
}

func promptEvaluationDatasetVersionToResponse(version db.PromptEvaluationDatasetVersion) PromptEvaluationDatasetVersionResponse {
	return PromptEvaluationDatasetVersionResponse{
		ID:             uuidToString(version.ID),
		WorkspaceID:    uuidToString(version.WorkspaceID),
		DatasetAssetID: uuidToString(version.DatasetAssetID),
		Version:        version.Version,
		VersionLabel:   version.VersionLabel,
		RowCount:       version.RowCount,
		RowFingerprint: version.RowFingerprint,
		Metadata:       decodeJSONDefault(version.Metadata, map[string]any{}),
		CreatedBy:      uuidToPtr(version.CreatedBy),
		CreatedAt:      timestampToString(version.CreatedAt),
	}
}

func promptEvaluationDatasetVersionRowToResponse(row db.PromptEvaluationDatasetVersionRow) PromptEvaluationDatasetVersionRowResponse {
	return PromptEvaluationDatasetVersionRowResponse{
		ID:               uuidToString(row.ID),
		WorkspaceID:      uuidToString(row.WorkspaceID),
		DatasetVersionID: uuidToString(row.DatasetVersionID),
		DatasetAssetID:   uuidToString(row.DatasetAssetID),
		SourceRowID:      uuidToPtr(row.SourceRowID),
		CaseID:           uuidToPtr(row.CaseID),
		RowIndex:         row.RowIndex,
		RowName:          row.RowName,
		Variables:        decodeJSONDefault(row.Variables, map[string]any{}),
		ExpectedContains: decodeJSONDefault(row.ExpectedContains, []any{}),
		Expected:         decodeJSONDefault(row.Expected, map[string]any{}),
		Tags:             decodeJSONDefault(row.Tags, []any{}),
		Source:           row.Source,
		CreatedAt:        timestampToString(row.CreatedAt),
	}
}

func promptEvaluationRunToResponse(run db.PromptEvaluationRun) PromptEvaluationRunResponse {
	return PromptEvaluationRunResponse{
		ID:                uuidToString(run.ID),
		WorkspaceID:       uuidToString(run.WorkspaceID),
		AssetID:           uuidToString(run.AssetID),
		PromptID:          uuidToPtr(run.PromptID),
		RunKind:           promptEvaluationRunKindLabel(run.RunKind),
		Status:            run.Status,
		TriggerSource:     run.TriggerSource,
		AgentID:           uuidToPtr(run.AgentID),
		RuntimeID:         uuidToPtr(run.RuntimeID),
		TaskID:            uuidToPtr(run.TaskID),
		ChatSessionID:     uuidToPtr(run.ChatSessionID),
		Model:             run.Model,
		RuntimeProvider:   run.RuntimeProvider,
		TotalCases:        run.TotalCases,
		PassedCases:       run.PassedCases,
		FailedCases:       run.FailedCases,
		PassRate:          run.PassRate,
		TotalDurationMs:   run.TotalDurationMs,
		AverageDurationMs: run.AverageDurationMs,
		InputTokens:       run.InputTokens,
		OutputTokens:      run.OutputTokens,
		EstimatedCost:     run.EstimatedCost,
		FailureReason:     run.FailureReason,
		Conclusion:        run.Conclusion,
		Metrics:           decodeJSONDefault(run.Metrics, map[string]any{}),
		Evidence:          decodeJSONDefault(run.Evidence, map[string]any{}),
		StartedAt:         timestampToString(run.StartedAt),
		CompletedAt:       timestampToString(run.CompletedAt),
		CreatedBy:         uuidToPtr(run.CreatedBy),
		CreatedAt:         timestampToString(run.CreatedAt),
		UpdatedAt:         timestampToString(run.UpdatedAt),
		ReviewDecision:    run.ReviewDecision,
		ReviewNote:        run.ReviewNote,
		ReviewedBy:        uuidToPtr(run.ReviewedBy),
		ReviewedAt:        timestampToString(run.ReviewedAt),
	}
}

func promptEvaluationRunKindLabel(runKind string) string {
	if runKind == "本地渲染" {
		return "模板渲染检查"
	}
	return runKind
}

func promptEvaluationTrialToResponse(trial db.PromptEvaluationTrial) PromptEvaluationTrialResponse {
	return PromptEvaluationTrialResponse{
		ID:             uuidToString(trial.ID),
		RunID:          uuidToString(trial.RunID),
		WorkspaceID:    uuidToString(trial.WorkspaceID),
		AssetID:        uuidToString(trial.AssetID),
		CaseIndex:      trial.CaseIndex,
		CaseName:       trial.CaseName,
		Status:         trial.Status,
		Input:          decodeJSONDefault(trial.Input, map[string]any{}),
		Expected:       decodeJSONDefault(trial.Expected, map[string]any{}),
		Output:         decodeJSONDefault(trial.Output, map[string]any{}),
		RenderedPrompt: trial.RenderedPrompt,
		InputTokens:    trial.InputTokens,
		OutputTokens:   trial.OutputTokens,
		DurationMs:     trial.DurationMs,
		FailureReason:  trial.FailureReason,
		Evidence:       decodeJSONDefault(trial.Evidence, map[string]any{}),
		CreatedAt:      timestampToString(trial.CreatedAt),
	}
}

func promptEvaluationTaskUsageToResponse(usage db.TaskUsage) PromptEvaluationTaskUsageResponse {
	breakdown, priced := metrics.EstimateUsageCostBreakdownUSD(usage.Provider, usage.Model, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
	return PromptEvaluationTaskUsageResponse{
		ID:               uuidToString(usage.ID),
		TaskID:           uuidToString(usage.TaskID),
		Provider:         usage.Provider,
		Model:            usage.Model,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		EstimatedCost:    breakdown.TotalCostUSD,
		Priced:           priced,
		CreatedAt:        timestampToString(usage.CreatedAt),
		UpdatedAt:        timestampToString(usage.UpdatedAt),
	}
}

func promptEvaluationCaseToResponse(item db.PromptEvaluationCase, assertions []db.PromptEvaluationCaseAssertion) PromptEvaluationCaseResponse {
	return PromptEvaluationCaseResponse{
		ID:               uuidToString(item.ID),
		WorkspaceID:      uuidToString(item.WorkspaceID),
		AssetID:          uuidToString(item.AssetID),
		PromptID:         uuidToPtr(item.PromptID),
		CaseIndex:        item.CaseIndex,
		CaseName:         item.CaseName,
		Variables:        decodeJSONDefault(item.Variables, map[string]any{}),
		ExpectedContains: decodeJSONDefault(item.ExpectedContains, []any{}),
		Assertions:       promptEvaluationCaseAssertionsToResponse(assertions),
		Input:            decodeJSONDefault(item.Input, map[string]any{}),
		Expected:         decodeJSONDefault(item.Expected, map[string]any{}),
		Tags:             decodeJSONDefault(item.Tags, []any{}),
		Status:           item.Status,
		Source:           item.Source,
		CreatedBy:        uuidToPtr(item.CreatedBy),
		CreatedAt:        timestampToString(item.CreatedAt),
		UpdatedAt:        timestampToString(item.UpdatedAt),
	}
}

func promptEvaluationCaseOperationToResponse(item db.PromptEvaluationCaseOperation) PromptEvaluationCaseOperationResponse {
	return PromptEvaluationCaseOperationResponse{
		ID:            uuidToString(item.ID),
		WorkspaceID:   uuidToString(item.WorkspaceID),
		AssetID:       uuidToString(item.AssetID),
		OperationType: item.OperationType,
		Filter:        decodeJSONDefault(item.Filter, map[string]any{}),
		Input:         decodeJSONDefault(item.Input, map[string]any{}),
		ChangedCount:  item.ChangedCount,
		SkippedCount:  item.SkippedCount,
		SampleCaseIDs: decodeJSONDefault(item.SampleCaseIds, []any{}),
		CreatedBy:     uuidToPtr(item.CreatedBy),
		CreatedAt:     timestampToString(item.CreatedAt),
		Status:        item.Status,
		ErrorMessage:  item.ErrorMessage,
		StartedAt:     timestampToPtr(item.StartedAt),
		CompletedAt:   timestampToPtr(item.CompletedAt),
		UpdatedAt:     timestampToString(item.UpdatedAt),
	}
}

func promptEvaluationCaseAssertionsToResponse(assertions []db.PromptEvaluationCaseAssertion) []PromptEvaluationCaseAssertionResponse {
	resp := make([]PromptEvaluationCaseAssertionResponse, 0, len(assertions))
	for _, item := range assertions {
		resp = append(resp, PromptEvaluationCaseAssertionResponse{
			ID:             uuidToString(item.ID),
			WorkspaceID:    uuidToString(item.WorkspaceID),
			AssetID:        uuidToString(item.AssetID),
			CaseID:         uuidToString(item.CaseID),
			AssertionIndex: item.AssertionIndex,
			AssertionType:  item.AssertionType,
			ExpectedText:   item.ExpectedText,
			Status:         item.Status,
			Source:         item.Source,
			CreatedAt:      timestampToString(item.CreatedAt),
		})
	}
	return resp
}

func promptEvaluationAssertionsByCase(assertions []db.PromptEvaluationCaseAssertion) map[string][]db.PromptEvaluationCaseAssertion {
	grouped := make(map[string][]db.PromptEvaluationCaseAssertion)
	for _, item := range assertions {
		caseID := uuidToString(item.CaseID)
		grouped[caseID] = append(grouped[caseID], item)
	}
	return grouped
}

func promptEvaluationAssertionTexts(raw []byte) []string {
	values := stringListFromAny(decodeJSONDefault(raw, []any{}))
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func promptEvaluationDimensionScoreToResponse(item db.PromptEvaluationDimensionScore) PromptEvaluationDimensionScoreResponse {
	return PromptEvaluationDimensionScoreResponse{
		ID:             uuidToString(item.ID),
		WorkspaceID:    uuidToString(item.WorkspaceID),
		RunID:          uuidToString(item.RunID),
		AssetID:        uuidToString(item.AssetID),
		PromptID:       uuidToPtr(item.PromptID),
		DimensionIndex: item.DimensionIndex,
		DimensionName:  item.DimensionName,
		Score:          item.Score,
		PassedCases:    item.PassedCases,
		TotalCases:     item.TotalCases,
		Status:         item.Status,
		Rule:           item.Rule,
		Evidence:       item.Evidence,
		Source:         item.Source,
		CreatedAt:      timestampToString(item.CreatedAt),
		UpdatedAt:      timestampToString(item.UpdatedAt),
	}
}

func promptEvaluationDimensionScoreSummaryToResponse(item db.ListPromptEvaluationDimensionScoreSummariesRow) PromptEvaluationDimensionScoreSummaryResponse {
	return PromptEvaluationDimensionScoreSummaryResponse{
		WorkspaceID:    uuidToString(item.WorkspaceID),
		AssetID:        uuidToString(item.AssetID),
		PromptID:       uuidToPtr(item.PromptID),
		DimensionIndex: item.DimensionIndex,
		DimensionName:  item.DimensionName,
		RunCount:       item.RunCount,
		ScoredRunCount: item.ScoredRunCount,
		PassedCases:    item.PassedCases,
		TotalCases:     item.TotalCases,
		Score:          item.Score,
		LatestStatus:   item.LatestStatus,
		LatestRule:     item.LatestRule,
		LatestEvidence: item.LatestEvidence,
		LatestSource:   item.LatestSource,
		LatestScoredAt: timestampToString(item.LatestScoredAt),
	}
}

func promptEvaluationDimensionScoreTrendToResponse(item db.ListPromptEvaluationDimensionScoreTrendsRow) PromptEvaluationDimensionScoreTrendResponse {
	return PromptEvaluationDimensionScoreTrendResponse{
		WorkspaceID:    uuidToString(item.WorkspaceID),
		AssetID:        uuidToString(item.AssetID),
		PromptID:       uuidToPtr(item.PromptID),
		DimensionIndex: item.DimensionIndex,
		DimensionName:  item.DimensionName,
		Period:         item.Period,
		PromptVersion:  item.PromptVersion,
		RunCount:       item.RunCount,
		ScoredRunCount: item.ScoredRunCount,
		PassedCases:    item.PassedCases,
		TotalCases:     item.TotalCases,
		Score:          item.Score,
		LatestStatus:   item.LatestStatus,
		LatestRule:     item.LatestRule,
		LatestEvidence: item.LatestEvidence,
		LatestSource:   item.LatestSource,
		LatestScoredAt: timestampToString(item.LatestScoredAt),
	}
}

func promptEvaluationExpectedContainsFromAssertions(fallback []byte, assertions []db.PromptEvaluationCaseAssertion) []any {
	if len(assertions) == 0 {
		if values, ok := decodeJSONDefault(fallback, []any{}).([]any); ok {
			return values
		}
		return []any{}
	}
	result := make([]any, 0, len(assertions))
	for _, item := range assertions {
		if strings.TrimSpace(item.ExpectedText) != "" {
			result = append(result, item.ExpectedText)
		}
	}
	return result
}

func syncPromptEvaluationCaseAssertions(ctx context.Context, qtx *db.Queries, item db.PromptEvaluationCase, expectedContains []byte) ([]db.PromptEvaluationCaseAssertion, error) {
	if err := qtx.DeletePromptEvaluationCaseAssertionsByCase(ctx, db.DeletePromptEvaluationCaseAssertionsByCaseParams{
		WorkspaceID: item.WorkspaceID,
		CaseID:      item.ID,
	}); err != nil {
		return nil, err
	}
	values := promptEvaluationAssertionTexts(expectedContains)
	assertions := make([]db.PromptEvaluationCaseAssertion, 0, len(values))
	for idx, value := range values {
		assertion, err := qtx.CreatePromptEvaluationCaseAssertion(ctx, db.CreatePromptEvaluationCaseAssertionParams{
			WorkspaceID:    item.WorkspaceID,
			AssetID:        item.AssetID,
			CaseID:         item.ID,
			AssertionIndex: int32(idx),
			ExpectedText:   value,
			AssertionType:  "包含文本",
			Status:         item.Status,
			Source:         "expected_contains",
		})
		if err != nil {
			return nil, err
		}
		assertions = append(assertions, assertion)
	}
	return assertions, nil
}

func syncPromptEvaluationDatasetRow(ctx context.Context, qtx *db.Queries, asset db.PromptEvaluationAsset, item db.PromptEvaluationCase) error {
	deletedAssets, err := qtx.DeletePromptEvaluationDatasetRowsByCase(ctx, db.DeletePromptEvaluationDatasetRowsByCaseParams{
		WorkspaceID: item.WorkspaceID,
		CaseID:      item.ID,
	})
	if err != nil {
		return err
	}
	for _, datasetAssetID := range deletedAssets {
		if err := refreshPromptEvaluationDatasetRowCount(ctx, qtx, item.WorkspaceID, datasetAssetID); err != nil {
			return err
		}
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		return nil
	}
	if _, err := qtx.CreatePromptEvaluationDatasetRow(ctx, db.CreatePromptEvaluationDatasetRowParams{
		WorkspaceID:      item.WorkspaceID,
		DatasetAssetID:   item.AssetID,
		CaseID:           item.ID,
		RowIndex:         item.CaseIndex,
		RowName:          item.CaseName,
		Variables:        item.Variables,
		ExpectedContains: item.ExpectedContains,
		Expected:         item.Expected,
		Tags:             item.Tags,
		Status:           item.Status,
		Source:           item.Source,
		CreatedBy:        item.CreatedBy,
	}); err != nil {
		return err
	}
	return refreshPromptEvaluationDatasetRowCount(ctx, qtx, item.WorkspaceID, item.AssetID)
}

func syncPromptEvaluationTestSuiteCase(ctx context.Context, qtx *db.Queries, asset db.PromptEvaluationAsset, item db.PromptEvaluationCase) error {
	deletedAssets, err := qtx.DeletePromptEvaluationTestSuiteCasesByCase(ctx, db.DeletePromptEvaluationTestSuiteCasesByCaseParams{
		WorkspaceID: item.WorkspaceID,
		CaseID:      item.ID,
	})
	if err != nil {
		return err
	}
	for _, testSuiteAssetID := range deletedAssets {
		if err := refreshPromptEvaluationTestSuiteCaseCount(ctx, qtx, item.WorkspaceID, testSuiteAssetID); err != nil {
			return err
		}
	}
	if asset.AssetType != promptEvaluationAssetTestSuite {
		return nil
	}
	if _, err := qtx.CreatePromptEvaluationTestSuiteCase(ctx, db.CreatePromptEvaluationTestSuiteCaseParams{
		WorkspaceID:      item.WorkspaceID,
		TestSuiteAssetID: item.AssetID,
		CaseID:           item.ID,
		CaseIndex:        item.CaseIndex,
		CaseName:         item.CaseName,
		Variables:        item.Variables,
		ExpectedContains: item.ExpectedContains,
		Expected:         item.Expected,
		Tags:             item.Tags,
		Status:           item.Status,
		Source:           item.Source,
		CreatedBy:        item.CreatedBy,
	}); err != nil {
		return err
	}
	return refreshPromptEvaluationTestSuiteCaseCount(ctx, qtx, item.WorkspaceID, item.AssetID)
}

func syncPromptEvaluationExperimentDimensions(ctx context.Context, qtx *db.Queries, asset db.PromptEvaluationAsset, createdBy pgtype.UUID) error {
	return nil
}

func deletePromptEvaluationDatasetRowsForCase(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, caseID pgtype.UUID) error {
	deletedAssets, err := qtx.DeletePromptEvaluationDatasetRowsByCase(ctx, db.DeletePromptEvaluationDatasetRowsByCaseParams{
		WorkspaceID: workspaceID,
		CaseID:      caseID,
	})
	if err != nil {
		return err
	}
	for _, datasetAssetID := range deletedAssets {
		if err := refreshPromptEvaluationDatasetRowCount(ctx, qtx, workspaceID, datasetAssetID); err != nil {
			return err
		}
	}
	return nil
}

func deletePromptEvaluationTestSuiteCasesForCase(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, caseID pgtype.UUID) error {
	deletedAssets, err := qtx.DeletePromptEvaluationTestSuiteCasesByCase(ctx, db.DeletePromptEvaluationTestSuiteCasesByCaseParams{
		WorkspaceID: workspaceID,
		CaseID:      caseID,
	})
	if err != nil {
		return err
	}
	for _, testSuiteAssetID := range deletedAssets {
		if err := refreshPromptEvaluationTestSuiteCaseCount(ctx, qtx, workspaceID, testSuiteAssetID); err != nil {
			return err
		}
	}
	return nil
}

func refreshPromptEvaluationDatasetRowCount(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, assetID pgtype.UUID) error {
	return qtx.RefreshPromptEvaluationDatasetRowCount(ctx, db.RefreshPromptEvaluationDatasetRowCountParams{
		WorkspaceID:    workspaceID,
		DatasetAssetID: assetID,
	})
}

func refreshPromptEvaluationTestSuiteCaseCount(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, assetID pgtype.UUID) error {
	return qtx.RefreshPromptEvaluationTestSuiteCaseCount(ctx, db.RefreshPromptEvaluationTestSuiteCaseCountParams{
		WorkspaceID:      workspaceID,
		TestSuiteAssetID: assetID,
	})
}

func refreshPromptEvaluationExperimentDimensionCount(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, assetID pgtype.UUID) error {
	return nil
}

func promptEvaluationOptimizationCandidateToResponse(item db.PromptEvaluationOptimizationCandidate) PromptEvaluationOptimizationCandidateResponse {
	return PromptEvaluationOptimizationCandidateResponse{
		ID:                   uuidToString(item.ID),
		WorkspaceID:          uuidToString(item.WorkspaceID),
		AssetID:              uuidToString(item.AssetID),
		RunID:                uuidToString(item.RunID),
		PromptID:             uuidToString(item.PromptID),
		CandidateName:        item.CandidateName,
		CandidateContent:     item.CandidateContent,
		Rationale:            item.Rationale,
		FailedCaseCount:      item.FailedCaseCount,
		SourceFailureSummary: decodeJSONDefault(item.SourceFailureSummary, map[string]any{}),
		SourcePromptSnapshot: decodeJSONDefault(item.SourcePromptSnapshot, map[string]any{}),
		Metrics:              decodeJSONDefault(item.Metrics, map[string]any{}),
		SkillPatch:           skillPatchFromCandidate(item),
		Status:               item.Status,
		PublishedPromptID:    uuidToPtr(item.PublishedPromptID),
		PublishedAt:          timestampToString(item.PublishedAt),
		CreatedBy:            uuidToPtr(item.CreatedBy),
		CreatedAt:            timestampToString(item.CreatedAt),
		UpdatedAt:            timestampToString(item.UpdatedAt),
	}
}

func promptEvaluationEvidenceSnapshotToResponse(item db.PromptEvaluationEvidenceSnapshot, includeEvidence bool) PromptEvaluationEvidenceSnapshotResponse {
	resp := PromptEvaluationEvidenceSnapshotResponse{
		ID:            uuidToString(item.ID),
		WorkspaceID:   uuidToString(item.WorkspaceID),
		RunID:         uuidToString(item.RunID),
		SnapshotType:  item.SnapshotType,
		SchemaVersion: item.SchemaVersion,
		Summary:       decodeJSONDefault(item.Summary, map[string]any{}),
		CreatedBy:     uuidToPtr(item.CreatedBy),
		CreatedAt:     timestampToString(item.CreatedAt),
	}
	if includeEvidence {
		resp.Evidence = decodeJSONDefault(item.Evidence, map[string]any{})
	}
	return resp
}

func promptEvaluationEvidenceSnapshotListRowToResponse(item db.ListPromptEvaluationEvidenceSnapshotsByRunRow) PromptEvaluationEvidenceSnapshotResponse {
	return PromptEvaluationEvidenceSnapshotResponse{
		ID:            uuidToString(item.ID),
		WorkspaceID:   uuidToString(item.WorkspaceID),
		RunID:         uuidToString(item.RunID),
		SnapshotType:  item.SnapshotType,
		SchemaVersion: item.SchemaVersion,
		Summary:       decodeJSONDefault(item.Summary, map[string]any{}),
		CreatedBy:     uuidToPtr(item.CreatedBy),
		CreatedAt:     timestampToString(item.CreatedAt),
	}
}

func promptEvaluationSummaryToResponse(workspaceID pgtype.UUID, row db.GetPromptEvaluationSummaryRow) PromptEvaluationSummaryResponse {
	return PromptEvaluationSummaryResponse{
		WorkspaceID: uuidToString(workspaceID),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		LastRunAt:   timestampToString(row.LastRunAt),
		Metrics: map[string]any{
			"总用例数":    row.TotalCases,
			"启用用例数":   row.ActiveCases,
			"已评估用例数":  row.EvaluatedCases,
			"通过数":     row.PassedCases,
			"失败数":     row.FailedCases,
			"通过率":     row.PassRate,
			"总耗时毫秒":   row.TotalDurationMs,
			"平均耗时毫秒":  row.AverageDurationMs,
			"输入token": row.InputTokens,
			"输出token": row.OutputTokens,
			"预估成本":    row.EstimatedCost,
			"智能体运行数":  row.AgentRuns,
			"模板渲染检查数": row.LocalRuns,
			"需人工复核":   row.ReviewRuns,
			"待确认优化候选": row.PendingCandidates,
			"已发布优化候选": row.PublishedCandidates,
			"服务端证据快照": row.EvidenceSnapshots,
			"验收归档快照":  row.AcceptanceSnapshots,
		},
		Assets: map[string]int64{
			"资产总数":    row.TotalAssets,
			"启用资产数":   row.ActiveAssets,
			"数据集":     row.DatasetAssets,
			"测试套件":    row.TestSuiteAssets,
			"实验":      row.ExperimentAssets,
			"优化运行":    row.OptimizationAssets,
			"结构化用例":   row.TotalCases,
			"启用用例":    row.ActiveCases,
			"画像用例数":   row.AssetProfileCases,
			"画像变量数":   row.AssetProfileVariables,
			"画像断言数":   row.AssetProfileAssertions,
			"关联数据集数":  row.AssetProfileLinkedDatasets,
			"关联提示词数":  row.AssetProfileLinkedPrompts,
			"评估维度数":   row.AssetProfileDimensions,
			"数据集行":    row.DatasetRows,
			"测试套件用例":  row.TestSuiteCases,
			"服务端证据快照": row.EvidenceSnapshots,
			"验收归档快照":  row.AcceptanceSnapshots,
		},
		RunStatus: map[string]int64{
			"运行总数":   row.TotalRuns,
			"模板渲染检查": row.LocalRuns,
			"智能体执行":  row.AgentRuns,
			"已入队":    row.QueuedRuns,
			"运行中":    row.RunningRuns,
			"通过":     row.PassedRuns,
			"未通过":    row.NotPassedRuns,
			"失败":     row.FailedRuns,
			"已取消":    row.CancelledRuns,
			"需人工复核":  row.ReviewRuns,
		},
		Candidates: map[string]int64{
			"候选总数": row.TotalCandidates,
			"待确认":  row.PendingCandidates,
			"已发布":  row.PublishedCandidates,
			"已拒绝":  row.RejectedCandidates,
		},
	}
}

func validPromptEvaluationAssetType(assetType string) bool {
	return assetType == promptEvaluationAssetDataset ||
		assetType == promptEvaluationAssetTestSuite
}

func jsonObjectField(w http.ResponseWriter, raw json.RawMessage, field string) ([]byte, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON object")
		return nil, false
	}
	return raw, true
}

func promptEvaluationPayloadField(w http.ResponseWriter, raw json.RawMessage, field string, preserveEmpty bool) ([]byte, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if preserveEmpty {
			return nil, true
		}
		return mustJSONBytes(normalizePromptEvaluationPayloadObject(map[string]any{})), true
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON object")
		return nil, false
	}
	return mustJSONBytes(normalizePromptEvaluationPayloadObject(obj)), true
}

func promptEvaluationAssetProfileFromPayload(raw []byte, promptID pgtype.UUID, assetType string) promptEvaluationAssetProfile {
	payload := decodePayloadObject(raw)
	cases := promptEvaluationCases(payload)
	variableCount := 0
	assertionCount := 0
	for index, item := range cases {
		normalized := normalizePromptEvaluationCase(index, item)
		variableCount += len(normalized.Variables)
		assertionCount += len(normalized.ExpectedContains)
	}
	linkedPromptCount := countPromptEvaluationProfileValues(payload, "prompt_ids", "提示词版本", "关联提示词", "候选提示词", "对比提示词", "baseline_prompt_id", "基线提示词")
	if promptID.Valid {
		linkedPromptCount++
	}
	experimentDimensions := promptEvaluationExperimentDimensions(payload)
	if assetType == promptEvaluationAssetExperiment && len(experimentDimensions) == 0 {
		experimentDimensions = promptEvaluationDefaultExperimentDimensions()
	}
	return promptEvaluationAssetProfile{
		StructureSchema:          promptEvaluationAssetProfileV1,
		StructuredCaseCount:      int32(len(cases)),
		StructuredVariableCount:  int32(variableCount),
		StructuredAssertionCount: int32(assertionCount),
		LinkedDatasetCount:       int32(countPromptEvaluationProfileValues(payload, "dataset_ids", "数据集ID", "关联数据集", "包含数据集", "linked_dataset_ids")),
		LinkedPromptCount:        int32(linkedPromptCount),
		EvaluationDimensionCount: int32(countPromptEvaluationProfileValues(payload, "evaluation_dimensions", "评估维度", "指标", "指标口径", "metric_contract")),
		ExperimentDimensionCount: int32(len(experimentDimensions)),
	}
}

func countPromptEvaluationProfileValues(payload map[string]any, keys ...string) int {
	seen := map[string]bool{}
	for _, key := range keys {
		collectPromptEvaluationProfileValues(seen, firstValue(payload, key))
	}
	return len(seen)
}

func collectPromptEvaluationProfileValues(seen map[string]bool, value any) {
	switch v := value.(type) {
	case nil:
		return
	case string:
		if item := strings.TrimSpace(v); item != "" {
			seen[item] = true
		}
	case []any:
		for _, item := range v {
			collectPromptEvaluationProfileValues(seen, item)
		}
	case map[string]any:
		for key, item := range v {
			if strings.TrimSpace(key) != "" {
				seen[key] = true
			}
			collectPromptEvaluationProfileValues(seen, item)
		}
	default:
		if item := strings.TrimSpace(stringFromAny(v)); item != "" {
			seen[item] = true
		}
	}
}

func promptEvaluationExperimentDimensions(payload map[string]any) []normalizedPromptEvaluationExperimentDimension {
	target := stringFromAny(firstValue(payload, "实验对象", "experiment_target", "target", "对象"))
	baseline := stringFromAny(firstValue(payload, "基线输出", "baseline_output", "baseline", "baseline_result"))
	raw := firstValue(payload, "对比维度", "实验维度", "evaluation_dimensions", "评估维度", "指标", "metric_contract")
	values := promptEvaluationDimensionValues(raw)
	result := make([]normalizedPromptEvaluationExperimentDimension, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value.Name)
		if name == "" {
			continue
		}
		result = append(result, normalizedPromptEvaluationExperimentDimension{
			Name:              name,
			ExperimentTarget:  target,
			BaselineOutput:    baseline,
			ComparisonPayload: value.Payload,
		})
	}
	return result
}

func promptEvaluationExperimentDimensionsForAsset(assetType string, payload map[string]any) []normalizedPromptEvaluationExperimentDimension {
	dimensions := promptEvaluationExperimentDimensions(payload)
	if len(dimensions) == 0 && assetType == promptEvaluationAssetExperiment {
		return promptEvaluationDefaultExperimentDimensions()
	}
	return dimensions
}

func promptEvaluationDefaultExperimentDimensions() []normalizedPromptEvaluationExperimentDimension {
	names := []string{"命中率", "缺失变量", "中文一致性"}
	result := make([]normalizedPromptEvaluationExperimentDimension, 0, len(names))
	for _, name := range names {
		result = append(result, normalizedPromptEvaluationExperimentDimension{
			Name:              name,
			ComparisonPayload: map[string]any{"来源": "默认实验维度"},
		})
	}
	return result
}

type promptEvaluationDimensionValue struct {
	Name    string
	Payload map[string]any
}

func promptEvaluationDimensionValues(value any) []promptEvaluationDimensionValue {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if item := strings.TrimSpace(v); item != "" {
			return []promptEvaluationDimensionValue{{Name: item, Payload: map[string]any{}}}
		}
	case []any:
		result := make([]promptEvaluationDimensionValue, 0, len(v))
		for _, item := range v {
			result = append(result, promptEvaluationDimensionValues(item)...)
		}
		return result
	case map[string]any:
		if name := strings.TrimSpace(stringFromAny(firstValue(v, "name", "名称", "dimension", "维度"))); name != "" {
			payload := make(map[string]any, len(v))
			for key, item := range v {
				payload[key] = item
			}
			return []promptEvaluationDimensionValue{{Name: name, Payload: payload}}
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			if strings.TrimSpace(key) != "" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		result := make([]promptEvaluationDimensionValue, 0, len(keys))
		for _, key := range keys {
			payload := map[string]any{}
			if nested, ok := v[key].(map[string]any); ok {
				payload = nested
			} else if v[key] != nil {
				payload = map[string]any{"值": v[key]}
			}
			result = append(result, promptEvaluationDimensionValue{Name: key, Payload: payload})
		}
		return result
	default:
		if item := strings.TrimSpace(stringFromAny(v)); item != "" {
			return []promptEvaluationDimensionValue{{Name: item, Payload: map[string]any{}}}
		}
	}
	return nil
}

func jsonObjectBytesOrDefault(w http.ResponseWriter, raw json.RawMessage, field string, fallback []byte) ([]byte, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fallback, true
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON object")
		return nil, false
	}
	return raw, true
}

func jsonArrayBytesOrDefault(w http.ResponseWriter, raw json.RawMessage, field string, fallback []byte) ([]byte, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fallback, true
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON array")
		return nil, false
	}
	return raw, true
}

func jsonObjectBytesForUpdate(w http.ResponseWriter, raw json.RawMessage, field string, existing []byte) ([]byte, bool) {
	return jsonObjectBytesOrDefault(w, raw, field, existing)
}

func jsonArrayBytesForUpdate(w http.ResponseWriter, raw json.RawMessage, field string, existing []byte) ([]byte, bool) {
	return jsonArrayBytesOrDefault(w, raw, field, existing)
}

func (h *Handler) promptEvaluationPromptID(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, raw json.RawMessage, fallback pgtype.UUID) (pgtype.UUID, bool) {
	if len(raw) == 0 {
		return fallback, true
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return pgtype.UUID{}, true
	}
	var promptID string
	if err := json.Unmarshal(raw, &promptID); err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id must be a string or null")
		return pgtype.UUID{}, false
	}
	if promptID == "" {
		return pgtype.UUID{}, true
	}
	promptUUID, ok := parseUUIDOrBadRequest(w, promptID, "prompt_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	if _, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{ID: promptUUID, WorkspaceID: workspaceID}); err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
		return pgtype.UUID{}, false
	}
	return promptUUID, true
}

