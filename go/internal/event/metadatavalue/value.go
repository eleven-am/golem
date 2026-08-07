// Package metadatavalue owns the internal capability used to materialize
// golem.EventMetadata. Its import path is protected by Go's internal-package
// rule, so an external application cannot implement a lookalike runtime view
// and call the public package's metadata bridge with arbitrary values.
package metadatavalue

import "time"

type Input struct {
	EventID        [16]byte
	Action         string
	CausationID    [16]byte
	Ordinal        uint32
	RecordedAt     time.Time
	Generation     [32]byte
	EventSchema    [32]byte
	HasEventSchema bool
	ModelID        [16]byte
}

type Value struct{ input Input }

func New(input Input) Value { return Value{input: input} }

func (value Value) EventID() [16]byte     { return value.input.EventID }
func (value Value) Action() string        { return value.input.Action }
func (value Value) CausationID() [16]byte { return value.input.CausationID }
func (value Value) Ordinal() uint32       { return value.input.Ordinal }
func (value Value) RecordedAt() time.Time { return value.input.RecordedAt }
func (value Value) Generation() [32]byte  { return value.input.Generation }
func (value Value) EventSchema() ([32]byte, bool) {
	return value.input.EventSchema, value.input.HasEventSchema
}
func (value Value) ModelID() [16]byte { return value.input.ModelID }
