package ir

// ScopeTemplateID identifies one named, model-owned positive authority template.
// Actor arguments are not part of this static identity.
type ScopeTemplateID string

type FactTemplateID string

// FactTemplateIR owns the actual physical data predicate and associates it
// with one authority template of the same model. Its projection is derived.
type FactTemplateIR struct {
	ID              FactTemplateID     `json:"id"`
	Name            string             `json:"name"`
	ScopeTemplateID ScopeTemplateID    `json:"scopeTemplateId"`
	Arguments       GoNamedTypeIR      `json:"arguments"`
	Parameters      []ScopeParameterIR `json:"parameters"`
	Expression      ScopeExpressionIR  `json:"expression"`
	Fingerprint     Fingerprint        `json:"fingerprint"`
}

type ScopeTemplateIR struct {
	ID          ScopeTemplateID    `json:"id"`
	Name        string             `json:"name"`
	Arguments   GoNamedTypeIR      `json:"arguments"`
	Parameters  []ScopeParameterIR `json:"parameters"`
	Expression  ScopeExpressionIR  `json:"expression"`
	Fingerprint Fingerprint        `json:"fingerprint"`
}

type ScopeParameterIR struct {
	Name string        `json:"name"`
	Type LogicalTypeIR `json:"type"`
	Many bool          `json:"many"`
}

// ScopeExpressionIR reuses the scalar, Boolean and positive relation predicate
// operators. A scalar operand is either a typed parameter or typed literals;
// it never contains a stand-in actor value.
type ScopeExpressionIR struct {
	ModelID       ModelID             `json:"modelId"`
	Operator      string              `json:"operator"`
	FieldID       FieldID             `json:"fieldId,omitempty"`
	RelationID    RelationID          `json:"relationId,omitempty"`
	TargetModelID ModelID             `json:"targetModelId,omitempty"`
	Parameter     string              `json:"parameter,omitempty"`
	Literals      []TypedLiteralIR    `json:"literals,omitempty"`
	Children      []ScopeExpressionIR `json:"children,omitempty"`
}
