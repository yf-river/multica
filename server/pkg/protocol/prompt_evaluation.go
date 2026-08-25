package protocol

// PromptEvaluationDimensionScore is the persisted and API-visible score for
// one current evaluation dimension.
type PromptEvaluationDimensionScore struct {
	DimensionIndex int32   `json:"维度序号"`
	DimensionName  string  `json:"维度名称"`
	Score          float64 `json:"得分"`
	PassedCases    int     `json:"通过用例数"`
	TotalCases     int     `json:"总用例数"`
	Status         string  `json:"状态"`
	Rule           string  `json:"评分规则"`
	Evidence       string  `json:"证据"`
}
