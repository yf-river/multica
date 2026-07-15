package handler

type promptEvaluationReEvalAssetFingerprint struct {
	CandidateID string                                    `json:"candidate_id"`
	Request     PreparePromptEvaluationSkillReEvalRequest `json:"request"`
}
