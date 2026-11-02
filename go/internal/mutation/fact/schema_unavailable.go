package fact

import "fmt"

type SchemaUnavailableError struct {
	reference SchemaReference
}

func SchemaUnavailable(reference SchemaReference) *SchemaUnavailableError {
	return &SchemaUnavailableError{reference: reference}
}

func (failure *SchemaUnavailableError) Reference() SchemaReference { return failure.reference }

func (failure *SchemaUnavailableError) Error() string {
	const remedy = "which no active or historical bundle provides: deliver or drain events written before the schema changed, or supply the bundle that wrote them"
	if failure.reference.FormatVersion == FormatVersionV1 {
		return fmt.Sprintf("GOLEM_EVENT_SCHEMA_UNAVAILABLE: stored event fact (format V1) needs generation digest %s, %s", failure.reference.Generation, remedy)
	}
	return fmt.Sprintf("GOLEM_EVENT_SCHEMA_UNAVAILABLE: stored event fact (format V2) needs event schema %s, %s", failure.reference.EventSchema, remedy)
}
