package compileerr

type templateKey struct {
	kind   Kind
	detail Detail
}

var templates = map[templateKey]string{
	{KindNullNotAllowed, DetailNone}: "{Subject} on {model} cannot be null",
	{KindEmpty, DetailNone}:          "{Subject} on {model} must not be empty",
	{KindEmpty, EmptySelection}:      "Selection must not be empty",
	{KindExactlyOne, DetailNone}:     "{Subject} on {model} must set exactly one member",
	{KindRequired, DetailNone}:       "{Subject} on {model} is required",

	{KindIntegerRange, IntegerSkip}:        "{Subject} on {model} must be at least 0",
	{KindIntegerRange, IntegerTake}:        "{Subject} on {model} must be between {min} and {max}, excluding 0",
	{KindIntegerRange, IntegerPositive}:    "{Subject} on {model} must be between {min} and {max}",
	{KindIntegerRange, IntegerTakeNonZero}: "{Subject} on {model} must not be 0",
	{KindIntegerRange, IntegerPlatform}:    "{Subject} on {model} is outside the supported integer range",
	{KindIntegerRange, IntegerVersion}:     "{Subject} on {model} must be at least 1",

	{KindNegativeTakeRequiresOrderBy, DetailNone}: "{Subject} on {model} is negative and requires orderBy",

	{KindInvalidValue, ValueMalformed}:    "{Subject} on {model} is not a valid {scalar}",
	{KindInvalidValue, ValueNotCanonical}: "{Subject} on {model} is not a canonical {scalar}",
	{KindInvalidValue, ValueOutOfRange}:   "{Subject} on {model} is outside the {scalar} range",
	{KindInvalidValue, ValueWrongKind}:    "{Subject} on {model} must be of type {scalar}",
	{KindInvalidValue, ValueWrongShape}:   "{Subject} on {model} has the wrong kind of value",
	{KindInvalidValue, ValueNotFinite}:    "{Subject} on {model} must be a finite {scalar}",
	{KindInvalidValue, ValueTooLong}:      "{Subject} on {model} exceeds the maximum length of {max}",
	{KindInvalidValue, ValueNotOneOf}:     "{Subject} on {model} is not one of the allowed values",
	{KindInvalidValue, ValueUnknownEnum}:  "{Subject} on {model} is not a value of enum {scalar}",
	{KindInvalidValue, ValueContainsNUL}:  "{Subject} on {model} must not contain NUL characters",

	{KindOperatorUnsupported, OperatorNotSupported}: "{Subject} on {model} is not supported for this field or provider",

	{KindDuplicate, DuplicateField}:    "{Subject} on {model} names a field more than once",
	{KindDuplicate, DuplicateArgument}: "{Subject} on {model} is given more than once",

	{KindNotAllowedHere, NotAllowedAllAlone}:                    "{Subject} on {model} must be the only member and Boolean",
	{KindNotAllowedHere, NotAllowedCreateWhere}:                 "{Subject} on {model} is not accepted by create",
	{KindNotAllowedHere, NotAllowedBatchRelation}:               "{Subject} on {model} cannot be a relation in an update-many input",
	{KindNotAllowedHere, NotAllowedVersionedRelation}:           "{Subject} on {model} cannot carry relation writes without per-row expectations",
	{KindNotAllowedHere, NotAllowedNestedInCreate}:              "{Subject} on {model} is not allowed while creating",
	{KindNotAllowedHere, NotAllowedNestedToOne}:                 "{Subject} on {model} is not allowed on a to-one relation",
	{KindNotAllowedHere, NotAllowedNestedNeedsExpectation}:      "{Subject} on {model} requires an expectation because the target model is versioned",
	{KindNotAllowedHere, NotAllowedMustBeTrue}:                  "{Subject} on {model} must be true",
	{KindNotAllowedHere, NotAllowedNumericOnly}:                 "{Subject} on {model} is only allowed on numeric fields",
	{KindNotAllowedHere, NotAllowedVersionedBatch}:              "Batch writes are not available on {model}",
	{KindNotAllowedHere, NotAllowedAggregateNeedsMeasure}:       "An aggregate on {model} must select at least one measure",
	{KindNotAllowedHere, NotAllowedKeyNotInBy}:                  "Key {path} on {model} must be listed in by",
	{KindNotAllowedHere, NotAllowedRelationGroupNeedsDimension}: "Relation grouping on {model} requires a relation dimension in by",

	{KindFieldUnavailable, UnavailableArgument}: "{Subject} on {model} is not available for this operation",
	{KindFieldUnavailable, UnavailableInput}:    "{Subject} on {model} is not available for this operation",
	{KindFieldUnavailable, UnavailableField}:    "Field {path} on {model} is not available for this operation",

	{KindSelectionShape, ShapeNeedsSelection}:   "Field {path} on {model} requires a selection set",
	{KindSelectionShape, ShapeForbidsSelection}: "Field {path} on {model} must not have a selection set",
	{KindSelectionShape, ShapeSingleRoot}:       "A subscription must select exactly one root field",
	{KindSelectionShape, ShapeEventField}:       "Field {path} on {model} has an invalid shape for this event",

	{KindDirectiveInvalid, DirectiveNeedsBoolean}: "Directive @{path} requires a Boolean if",

	{KindFragmentInvalid, FragmentIncompatible}: "Fields with the same response name cannot be merged",
	{KindFragmentInvalid, FragmentMissing}:      "Fragment spreads in this operation cannot be resolved",
	{KindFragmentInvalid, FragmentCycle}:        "Fragment spreads in this operation form a cycle",

	{KindInputLimit, DetailNone}:     "Input exceeds {limit} ({max})",
	{KindInputLimit, LimitJSONDepth}: "{Subject} on {model} exceeds the JSON depth limit ({max})",
	{KindInputLimit, LimitJSONNodes}: "{Subject} on {model} exceeds the JSON node limit ({max})",
	{KindInputLimit, LimitJSONBytes}: "{Subject} on {model} exceeds the JSON size limit ({max})",

	{KindSelectionLimit, DetailNone}: "Selection exceeds {limit} ({max})",
}
