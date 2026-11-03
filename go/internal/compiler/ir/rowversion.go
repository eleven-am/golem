package ir

const (
	RowVersionColumn      SQLIdentifier = "golem_version"
	RowVersionObject                    = "golem_row_version"
	RowVersionGoName                    = "GolemVersion"
	RowVersionGraphQLName               = "_version"
)

func IsRowVersionField(field FieldIR) bool {
	return field.Scalar != nil && field.Scalar.Column == RowVersionColumn
}
