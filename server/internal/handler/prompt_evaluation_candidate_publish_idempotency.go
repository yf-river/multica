package handler

type promptEvaluationCandidatePublishFingerprint struct {
	CandidateID string `json:"candidate_id"`
}

type promptEvaluationCandidateRejectFingerprint struct {
	CandidateID string `json:"candidate_id"`
	Reason      string `json:"reason"`
}
