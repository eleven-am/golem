// Package bind converts schema-agnostic frozen public policy values into the
// closed, schema-validated policy IR.
package bind

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// ErrorCode is a stable bind failure category. Detail is diagnostic context;
// callers that need to branch on a failure use Code.
type ErrorCode string

const (
	CodeVersion   ErrorCode = "P2_BIND_VERSION"
	CodeProvider  ErrorCode = "P2_BIND_PROVIDER"
	CodeModel     ErrorCode = "P2_BIND_MODEL"
	CodeField     ErrorCode = "P2_BIND_FIELD"
	CodeRelation  ErrorCode = "P2_BIND_RELATION"
	CodeOperator  ErrorCode = "P2_BIND_OPERATOR"
	CodeValue     ErrorCode = "P2_BIND_VALUE"
	CodeCondition ErrorCode = "P2_BIND_CONDITION"
	CodeRule      ErrorCode = "P2_BIND_RULE"
	CodeInternal  ErrorCode = "P2_BIND_INTERNAL"
)

// Error is a typed, deterministic authorization-policy diagnostic. IDs and
// operator numbers are carried separately so callers never need to parse the
// human-readable Detail string.
type Error struct {
	Code         ErrorCode
	Path         string
	Detail       string
	RulePosition uint32
	HasRule      bool
	ModelID      ir.ModelID
	FieldID      ir.FieldID
	RelationID   ir.RelationID
	OperatorID   ir.OperatorID
	Cause        error
}

func (failure *Error) Error() string {
	if failure == nil {
		return ""
	}
	result := string(failure.Code)
	if failure.HasRule {
		result += fmt.Sprintf(" rule=%d", failure.RulePosition)
	}
	if failure.ModelID != (ir.ModelID{}) {
		result += " model=" + hex.EncodeToString(failure.ModelID[:])
	}
	if failure.FieldID != (ir.FieldID{}) {
		result += " field=" + hex.EncodeToString(failure.FieldID[:])
	}
	if failure.RelationID != (ir.RelationID{}) {
		result += " relation=" + hex.EncodeToString(failure.RelationID[:])
	}
	if failure.OperatorID != 0 {
		result += fmt.Sprintf(" operator=%d", failure.OperatorID)
	}
	if failure.Path != "" {
		result += " path=" + failure.Path
	}
	if failure.Detail != "" {
		result += ": " + failure.Detail
	}
	return result
}

func (failure *Error) Unwrap() error { return failure.Cause }
