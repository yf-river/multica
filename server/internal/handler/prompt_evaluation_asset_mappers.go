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
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func promptEvaluationAssetToResponse(asset db.PromptEvaluationAsset) PromptEvaluationAssetResponse {
	payload, err := decodeJSONObject(asset.Payload, "prompt evaluation asset payload")
	if err != nil {
		// The current schema and migration enforce this database invariant. A
		// violation is persistence corruption, not a value that the API may
		// safely replace with an empty object.
		panic("handler: " + err.Error())
	}
	return PromptEvaluationAssetResponse{
		ID:                       uuidToString(asset.ID),
		WorkspaceID:              uuidToString(asset.WorkspaceID),
		PromptID:                 uuidToPtr(asset.PromptID),
		Name:                     asset.Name,
		Description:              asset.Description,
		AssetType:                asset.AssetType,
		Payload:                  payload,
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
		Metadata:       mustDecodePersistedJSONObject(version.Metadata, "prompt evaluation dataset version metadata"),
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
		Variables:        mustDecodePersistedJSONObject(row.Variables, "prompt evaluation dataset version row variables"),
		ExpectedContains: mustDecodePersistedJSONArray(row.ExpectedContains, "prompt evaluation dataset version row expected_contains"),
		Expected:         mustDecodePersistedJSONObject(row.Expected, "prompt evaluation dataset version row expected"),
		Tags:             mustDecodePersistedJSONArray(row.Tags, "prompt evaluation dataset version row tags"),
		Source:           row.Source,
		CreatedAt:        timestampToString(row.CreatedAt),
	}
}

func promptEvaluationRunToResponse(run db.PromptEvaluationRun) PromptEvaluationRunResponse {
	metrics := mustDecodePersistedJSONObject(run.Metrics, "prompt evaluation run metrics")
	evidence := mustDecodePersistedJSONObject(run.Evidence, "prompt evaluation run evidence")
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
		Metrics:           metrics,
		Evidence:          evidence,
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
		Input:          mustDecodePersistedJSONObject(trial.Input, "prompt evaluation trial input"),
		Expected:       mustDecodePersistedJSONObject(trial.Expected, "prompt evaluation trial expected"),
		Output:         mustDecodePersistedJSONValue(trial.Output, "prompt evaluation trial output"),
		RenderedPrompt: trial.RenderedPrompt,
		InputTokens:    trial.InputTokens,
		OutputTokens:   trial.OutputTokens,
		DurationMs:     trial.DurationMs,
		FailureReason:  trial.FailureReason,
		Evidence:       mustDecodePersistedJSONObject(trial.Evidence, "prompt evaluation trial evidence"),
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
		Variables:        mustDecodePersistedJSONObject(item.Variables, "prompt evaluation case variables"),
		ExpectedContains: mustDecodePersistedJSONArray(item.ExpectedContains, "prompt evaluation case expected_contains"),
		Assertions:       promptEvaluationCaseAssertionsToResponse(item, assertions),
		Input:            mustDecodePersistedJSONObject(item.Input, "prompt evaluation case input"),
		Expected:         mustDecodePersistedJSONObject(item.Expected, "prompt evaluation case expected"),
		Tags:             mustDecodePersistedJSONArray(item.Tags, "prompt evaluation case tags"),
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
		Filter:        mustDecodePersistedJSONObject(item.Filter, "prompt evaluation case operation filter"),
		Input:         mustDecodePersistedJSONObject(item.Input, "prompt evaluation case operation input"),
		ChangedCount:  item.ChangedCount,
		SkippedCount:  item.SkippedCount,
		SampleCaseIDs: mustDecodePersistedJSONArray(item.SampleCaseIds, "prompt evaluation case operation sample_case_ids"),
		CreatedBy:     uuidToPtr(item.CreatedBy),
		CreatedAt:     timestampToString(item.CreatedAt),
		Status:        item.Status,
		ErrorMessage:  item.ErrorMessage,
		StartedAt:     timestampToPtr(item.StartedAt),
		CompletedAt:   timestampToPtr(item.CompletedAt),
		UpdatedAt:     timestampToString(item.UpdatedAt),
	}
}

func promptEvaluationCaseAssertionsToResponse(item db.PromptEvaluationCase, assertions []db.PromptEvaluationCaseAssertion) []PromptEvaluationCaseAssertionResponse {
	expectedTexts := promptEvaluationAssertionTexts(item.ExpectedContains)
	resp := make([]PromptEvaluationCaseAssertionResponse, 0, len(assertions))
	for _, assertion := range assertions {
		index := int(assertion.AssertionIndex)
		if index < 0 || index >= len(expectedTexts) {
			panic("prompt evaluation case assertion index is outside expected_contains")
		}
		resp = append(resp, PromptEvaluationCaseAssertionResponse{
			ID:             uuidToString(assertion.ID),
			WorkspaceID:    uuidToString(item.WorkspaceID),
			AssetID:        uuidToString(item.AssetID),
			CaseID:         uuidToString(item.ID),
			AssertionIndex: assertion.AssertionIndex,
			AssertionType:  "包含文本",
			ExpectedText:   expectedTexts[index],
			Status:         item.Status,
			Source:         "expected_contains",
			CreatedAt:      timestampToString(assertion.CreatedAt),
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
	values := stringListFromAny(mustDecodePersistedJSONArray(raw, "prompt evaluation case expected_contains"))
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

func promptEvaluationExecutableExpectedContains(raw []byte) []any {
	values := promptEvaluationAssertionTexts(raw)
	if len(values) == 0 {
		return mustDecodePersistedJSONArray(raw, "prompt evaluation case expected_contains")
	}
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func syncPromptEvaluationCaseAssertions(ctx context.Context, qtx *db.Queries, item db.PromptEvaluationCase) ([]db.PromptEvaluationCaseAssertion, error) {
	if err := qtx.DeletePromptEvaluationCaseAssertionsByCase(ctx, item.ID); err != nil {
		return nil, err
	}
	values := promptEvaluationAssertionTexts(item.ExpectedContains)
	assertions := make([]db.PromptEvaluationCaseAssertion, 0, len(values))
	for idx := range values {
		assertion, err := qtx.CreatePromptEvaluationCaseAssertion(ctx, db.CreatePromptEvaluationCaseAssertionParams{
			CaseID:         item.ID,
			AssertionIndex: int32(idx),
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

func promptEvaluationOptimizationCandidateToResponse(item db.PromptEvaluationOptimizationCandidate) PromptEvaluationOptimizationCandidateResponse {
	sourceFailureSummary := mustDecodePersistedJSONObject(item.SourceFailureSummary, "prompt evaluation optimization candidate source failure summary")
	sourcePromptSnapshot := mustDecodePersistedJSONObject(item.SourcePromptSnapshot, "prompt evaluation optimization candidate source prompt snapshot")
	metrics := mustDecodePersistedJSONObject(item.Metrics, "prompt evaluation optimization candidate metrics")
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
		SourceFailureSummary: sourceFailureSummary,
		SourcePromptSnapshot: sourcePromptSnapshot,
		Metrics:              metrics,
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
		Summary:       mustDecodePersistedJSONObject(item.Summary, "prompt evaluation evidence snapshot summary"),
		CreatedBy:     uuidToPtr(item.CreatedBy),
		CreatedAt:     timestampToString(item.CreatedAt),
	}
	if includeEvidence {
		resp.Evidence = mustDecodePersistedJSONObject(item.Evidence, "prompt evaluation evidence snapshot evidence")
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
		Summary:       mustDecodePersistedJSONObject(item.Summary, "prompt evaluation evidence snapshot summary"),
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
	if agentID, exists := obj["agent_id"]; exists {
		value, ok := agentID.(string)
		if !ok || strings.TrimSpace(value) == "" {
			writeError(w, http.StatusBadRequest, field+".agent_id must be a non-empty string")
			return nil, false
		}
	}
	return mustJSONBytes(normalizePromptEvaluationPayloadObject(obj)), true
}

func promptEvaluationAssetProfileFromPayload(raw []byte, promptID pgtype.UUID) promptEvaluationAssetProfile {
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
	return promptEvaluationAssetProfile{
		StructureSchema:          promptEvaluationAssetProfileV1,
		StructuredCaseCount:      int32(len(cases)),
		StructuredVariableCount:  int32(variableCount),
		StructuredAssertionCount: int32(assertionCount),
		LinkedDatasetCount:       int32(countPromptEvaluationProfileValues(payload, "dataset_ids", "数据集ID", "关联数据集", "包含数据集", "linked_dataset_ids")),
		LinkedPromptCount:        int32(linkedPromptCount),
		EvaluationDimensionCount: int32(countPromptEvaluationProfileValues(payload, "metric_contract")),
		ExperimentDimensionCount: int32(len(experimentDimensions)),
	}
}

func promptEvaluationAssetPayloadUpdateParams(
	asset db.PromptEvaluationAsset,
	payload []byte,
) db.UpdatePromptEvaluationAssetParams {
	params := db.UpdatePromptEvaluationAssetParams{
		ID: asset.ID, WorkspaceID: asset.WorkspaceID, PromptID: asset.PromptID, Payload: payload,
	}
	return withPromptEvaluationAssetProfile(
		params,
		promptEvaluationAssetProfileFromPayload(payload, asset.PromptID),
	)
}

func withPromptEvaluationAssetProfile(
	params db.UpdatePromptEvaluationAssetParams,
	profile promptEvaluationAssetProfile,
) db.UpdatePromptEvaluationAssetParams {
	params.StructureSchema = pgtype.Text{String: profile.StructureSchema, Valid: true}
	params.StructuredCaseCount = pgtype.Int4{Int32: profile.StructuredCaseCount, Valid: true}
	params.StructuredVariableCount = pgtype.Int4{Int32: profile.StructuredVariableCount, Valid: true}
	params.StructuredAssertionCount = pgtype.Int4{Int32: profile.StructuredAssertionCount, Valid: true}
	params.LinkedDatasetCount = pgtype.Int4{Int32: profile.LinkedDatasetCount, Valid: true}
	params.LinkedPromptCount = pgtype.Int4{Int32: profile.LinkedPromptCount, Valid: true}
	params.EvaluationDimensionCount = pgtype.Int4{Int32: profile.EvaluationDimensionCount, Valid: true}
	params.ExperimentDimensionCount = pgtype.Int4{Int32: profile.ExperimentDimensionCount, Valid: true}
	return params
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
		if item := strings.TrimSpace(util.StringFromAny(v)); item != "" {
			seen[item] = true
		}
	}
}

func promptEvaluationExperimentDimensions(payload map[string]any) []normalizedPromptEvaluationExperimentDimension {
	target := util.StringFromAny(payload["experiment_target"])
	baseline := util.StringFromAny(payload["baseline_output"])
	raw := payload["experiment_dimensions"]
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
		if name := strings.TrimSpace(util.StringFromAny(v["name"])); name != "" {
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
		if item := strings.TrimSpace(util.StringFromAny(v)); item != "" {
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
		writeValidationLookupError(w, err, "prompt_id does not belong to this workspace", "prompt", "prompt_id", promptID)
		return pgtype.UUID{}, false
	}
	return promptUUID, true
}
