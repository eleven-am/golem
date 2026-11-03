package compileerr

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/golem"
)

type Kind uint8

const (
	KindInvariant Kind = iota + 1
	KindNullNotAllowed
	KindEmpty
	KindExactlyOne
	KindRequired
	KindIntegerRange
	KindNegativeTakeRequiresOrderBy
	KindInvalidValue
	KindOperatorUnsupported
	KindDuplicate
	KindNotAllowedHere
	KindFieldUnavailable
	KindSelectionShape
	KindDirectiveInvalid
	KindFragmentInvalid
	KindInputLimit
	KindSelectionLimit
)

type Detail uint8

const (
	DetailNone Detail = iota

	IntegerSkip
	IntegerTake
	IntegerTakeNonZero
	IntegerPlatform

	ValueMalformed
	ValueNotCanonical
	ValueOutOfRange
	ValueWrongKind
	ValueWrongShape
	ValueNotFinite
	ValueTooLong
	ValueNotOneOf
	ValueUnknownEnum
	ValueContainsNUL

	OperatorNotSupported

	DuplicateField
	DuplicateArgument

	NotAllowedAllAlone

	UnavailableArgument
	UnavailableInput
	UnavailableField

	ShapeNeedsSelection
	ShapeForbidsSelection
	ShapeSingleRoot
	ShapeEventField

	DirectiveNeedsBoolean

	FragmentIncompatible
	FragmentMissing
	FragmentCycle

	LimitJSONDepth
	LimitJSONNodes
	LimitJSONBytes

	EmptySelection

	IntegerPositive
	IntegerVersion

	NotAllowedCreateWhere
	NotAllowedBatchRelation
	NotAllowedVersionedRelation
	NotAllowedNestedInCreate
	NotAllowedNestedToOne
	NotAllowedNestedNeedsExpectation
	NotAllowedMustBeTrue
	NotAllowedNumericOnly
	NotAllowedVersionedBatch
	NotAllowedAggregateNeedsMeasure
	NotAllowedKeyNotInBy
	NotAllowedRelationGroupNeedsDimension
)

type Error struct {
	kind     Kind
	detail   Detail
	path     string
	model    string
	typeName string
	response string
	min      *int
	max      *int
	limit    string
	cause    error
}

type Option func(*Error)

func Path(path string) Option    { return func(e *Error) { e.path = path } }
func Model(name string) Option   { return func(e *Error) { e.model = name } }
func Type(name string) Option    { return func(e *Error) { e.typeName = name } }
func Bounds(min, max int) Option { return func(e *Error) { e.min, e.max = &min, &max } }
func Max(max int) Option         { return func(e *Error) { e.max = &max } }
func Limit(name string, max int) Option {
	return func(e *Error) { e.limit, e.max = name, &max }
}

func New(kind Kind, detail Detail, options ...Option) *Error {
	result := &Error{kind: kind, detail: detail}
	for _, option := range options {
		option(result)
	}
	return result
}

func Invariant(cause error) *Error {
	if cause == nil {
		cause = errors.New("unclassified compile failure")
	}
	return &Error{kind: KindInvariant, cause: cause}
}

func Invariantf(format string, arguments ...any) *Error {
	return &Error{kind: KindInvariant, cause: fmt.Errorf(format, arguments...)}
}

func (e *Error) Kind() Kind     { return e.kind }
func (e *Error) Detail() Detail { return e.detail }
func (e *Error) Cause() error   { return e.cause }

func (e *Error) Internal() bool { return e.kind == KindInvariant }

func (e *Error) Argument() string {
	switch {
	case e.kind == KindSelectionShape, e.kind == KindDirectiveInvalid, e.detail == UnavailableField:
		return ""
	default:
		return e.path
	}
}
func (e *Error) Limit() string { return e.limit }

func (e *Error) Max() (int, bool) {
	if e.max == nil {
		return 0, false
	}
	return *e.max, true
}

func (e *Error) Min() (int, bool) {
	if e.min == nil {
		return 0, false
	}
	return *e.min, true
}

func (e *Error) Reason() golem.Reason {
	switch e.kind {
	case KindInvalidValue, KindIntegerRange:
		return golem.ReasonInvalidValue
	case KindInputLimit, KindSelectionLimit:
		return golem.ReasonLimitExceeded
	default:
		return ""
	}
}

func (e *Error) Error() string {
	if e.kind == KindInvariant {
		return "internal compile failure: " + e.cause.Error()
	}
	return e.Message()
}

func (e *Error) Message() string {
	switch e.kind {
	case KindInvariant:
		return "internal server error"
	}
	template, ok := templates[templateKey{e.kind, e.detail}]
	if !ok {
		return "GraphQL operation is invalid"
	}
	if e.model == "" {
		template = strings.ReplaceAll(template, " on {model}", "")
	}
	replacements := []string{
		"{Subject}", e.subject(), "{model}", e.model, "{path}", e.path, "{scalar}", e.typeName, "{limit}", e.limit,
	}
	if e.min != nil {
		replacements = append(replacements, "{min}", fmt.Sprint(*e.min))
	}
	if e.max != nil {
		replacements = append(replacements, "{max}", fmt.Sprint(*e.max))
	}
	return strings.NewReplacer(replacements...).Replace(template)
}

func (e *Error) subject() string {
	switch {
	case e.path == "":
		return "Value"
	case strings.ContainsAny(e.path, ".["):
		return "Input " + e.path
	default:
		return "Argument " + e.path
	}
}

func Join(parent, child string) string {
	switch {
	case parent == "":
		return child
	case child == "":
		return parent
	case strings.HasPrefix(child, "["):
		return parent + child
	default:
		return parent + "." + child
	}
}

func Index(parent string, index int) string {
	return fmt.Sprintf("%s[%d]", parent, index)
}

func classify(err error) (*Error, bool) {
	var typed *Error
	if errors.As(err, &typed) {
		return typed, true
	}
	var public *golem.Error
	if errors.As(err, &public) {
		return nil, true
	}
	return nil, false
}

func Ensure(err error) error {
	if err == nil {
		return nil
	}
	if _, known := classify(err); !known {
		return Invariant(err)
	}
	return err
}

func At(err error, parent string) error {
	if err == nil {
		return nil
	}
	typed, known := classify(err)
	if !known {
		return Invariant(err)
	}
	if typed == nil || typed.kind == KindInvariant || typed.limit != "" {
		return err
	}
	copied := *typed
	copied.path = Join(parent, typed.path)
	return &copied
}

func On(err error, model string) error {
	if err == nil {
		return nil
	}
	typed, known := classify(err)
	if !known {
		return Invariant(err)
	}
	if typed == nil || typed.kind == KindInvariant || typed.model != "" {
		return err
	}
	copied := *typed
	copied.model = model
	return &copied
}

type rooted struct {
	response string
	err      error
}

func (r *rooted) Error() string { return r.err.Error() }
func (r *rooted) Unwrap() error { return r.err }

func Root(err error, response string) error {
	if err == nil {
		return nil
	}
	if _, known := classify(err); !known {
		err = Invariant(err)
	}
	var existing *rooted
	if errors.As(err, &existing) {
		return err
	}
	return &rooted{response: response, err: err}
}

func RootOf(err error) (string, bool) {
	var found *rooted
	if errors.As(err, &found) {
		return found.response, true
	}
	return "", false
}
