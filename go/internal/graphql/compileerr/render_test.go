package compileerr

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

type renderCase struct {
	kind    Kind
	detail  Detail
	options []Option
	want    string
}

func renderCases() []renderCase {
	return []renderCase{
		{KindNullNotAllowed, DetailNone, []Option{Path("where"), Model("Article")}, "Argument where on Article cannot be null"},
		{KindNullNotAllowed, DetailNone, []Option{Path("where.id"), Model("Article")}, "Input where.id on Article cannot be null"},
		{KindNullNotAllowed, DetailNone, []Option{Path("where")}, "Argument where cannot be null"},
		{KindNullNotAllowed, DetailNone, nil, "Value cannot be null"},
		{KindEmpty, DetailNone, []Option{Path("orderBy"), Model("Article")}, "Argument orderBy on Article must not be empty"},
		{KindEmpty, EmptySelection, nil, "Selection must not be empty"},
		{KindExactlyOne, DetailNone, []Option{Path("where"), Model("Article")}, "Argument where on Article must set exactly one member"},
		{KindRequired, DetailNone, []Option{Path("cursor.id"), Model("Article")}, "Input cursor.id on Article is required"},
		{KindIntegerRange, IntegerSkip, []Option{Path("skip"), Model("Article")}, "Argument skip on Article must be at least 0"},
		{KindIntegerRange, IntegerTake, []Option{Path("take"), Model("Article"), Bounds(-500, 500)}, "Argument take on Article must be between -500 and 500, excluding 0"},
		{KindIntegerRange, IntegerTakeNonZero, []Option{Path("take"), Model("Article")}, "Argument take on Article must not be 0"},
		{KindIntegerRange, IntegerPositive, []Option{Path("take"), Model("search"), Bounds(1, 50)}, "Argument take on search must be between 1 and 50"},
		{KindIntegerRange, IntegerPlatform, []Option{Path("take"), Model("Article")}, "Argument take on Article is outside the supported integer range"},
		{KindNegativeTakeRequiresOrderBy, DetailNone, []Option{Path("take"), Model("Article")}, "Argument take on Article is negative and requires orderBy"},
		{KindInvalidValue, ValueMalformed, []Option{Path("where.id.equals"), Model("Article"), Type("UUID")}, "Input where.id.equals on Article is not a valid UUID"},
		{KindInvalidValue, ValueNotCanonical, []Option{Path("where.id.equals"), Model("Article"), Type("UUID")}, "Input where.id.equals on Article is not a canonical UUID"},
		{KindInvalidValue, ValueOutOfRange, []Option{Path("where.views.gt"), Model("Article"), Type("Int")}, "Input where.views.gt on Article is outside the Int range"},
		{KindInvalidValue, ValueWrongKind, []Option{Path("where.views.gt"), Model("Article"), Type("Int")}, "Input where.views.gt on Article must be of type Int"},
		{KindInvalidValue, ValueWrongShape, []Option{Path("where"), Model("Article")}, "Argument where on Article has the wrong kind of value"},
		{KindInvalidValue, ValueNotFinite, []Option{Path("where.score.gt"), Model("Article"), Type("Float")}, "Input where.score.gt on Article must be a finite Float"},
		{KindInvalidValue, ValueTooLong, []Option{Path("where.title.equals"), Model("Article"), Type("String"), Max(120)}, "Input where.title.equals on Article exceeds the maximum length of 120"},
		{KindInvalidValue, ValueNotOneOf, []Option{Path("orderBy[0].id"), Model("Article")}, "Input orderBy[0].id on Article is not one of the allowed values"},
		{KindInvalidValue, ValueUnknownEnum, []Option{Path("where.state.equals"), Model("Article"), Type("ArticleState")}, "Input where.state.equals on Article is not a value of enum ArticleState"},
		{KindInvalidValue, ValueContainsNUL, []Option{Path("where.title.equals"), Model("Article")}, "Input where.title.equals on Article must not contain NUL characters"},
		{KindOperatorUnsupported, OperatorNotSupported, []Option{Path("where.title.contains"), Model("Article")}, "Input where.title.contains on Article is not supported for this field or provider"},
		{KindDuplicate, DuplicateField, []Option{Path("orderBy"), Model("Article")}, "Argument orderBy on Article names a field more than once"},
		{KindDuplicate, DuplicateArgument, []Option{Path("prefix"), Model("greeting")}, "Argument prefix on greeting is given more than once"},
		{KindNotAllowedHere, NotAllowedAllAlone, []Option{Path("where.all"), Model("Article")}, "Input where.all on Article must be the only member and Boolean"},
		{KindFieldUnavailable, UnavailableArgument, []Option{Path("extra"), Model("greeting")}, "Argument extra on greeting is not available for this operation"},
		{KindFieldUnavailable, UnavailableInput, []Option{Path("where.secret"), Model("Account")}, "Input where.secret on Account is not available for this operation"},
		{KindFieldUnavailable, UnavailableField, []Option{Path("title"), Model("Article")}, "Field title on Article is not available for this operation"},
		{KindSelectionShape, ShapeNeedsSelection, []Option{Path("author"), Model("Article")}, "Field author on Article requires a selection set"},
		{KindSelectionShape, ShapeForbidsSelection, []Option{Path("title"), Model("Article")}, "Field title on Article must not have a selection set"},
		{KindSelectionShape, ShapeSingleRoot, nil, "A subscription must select exactly one root field"},
		{KindSelectionShape, ShapeEventField, []Option{Path("eventID"), Model("Article")}, "Field eventID on Article has an invalid shape for this event"},
		{KindDirectiveInvalid, DirectiveNeedsBoolean, []Option{Path("skip")}, "Directive @skip requires a Boolean if"},
		{KindFragmentInvalid, FragmentIncompatible, nil, "Fields with the same response name cannot be merged"},
		{KindFragmentInvalid, FragmentMissing, nil, "Fragment spreads in this operation cannot be resolved"},
		{KindFragmentInvalid, FragmentCycle, nil, "Fragment spreads in this operation form a cycle"},
		{KindInputLimit, DetailNone, []Option{Limit("MaxInputNodes", 32768)}, "Input exceeds MaxInputNodes (32768)"},
		{KindInputLimit, LimitJSONDepth, []Option{Path("value"), Model("Article"), Max(32)}, "Argument value on Article exceeds the JSON depth limit (32)"},
		{KindInputLimit, LimitJSONNodes, []Option{Path("value"), Model("Article"), Max(32768)}, "Argument value on Article exceeds the JSON node limit (32768)"},
		{KindInputLimit, LimitJSONBytes, []Option{Path("value"), Model("Article"), Max(524288)}, "Argument value on Article exceeds the JSON size limit (524288)"},
		{KindSelectionLimit, DetailNone, []Option{Limit("MaxDepth", 32)}, "Selection exceeds MaxDepth (32)"},
	}
}

func TestRenderTableGivesEveryKindAndDetailItsLiteralMessage(t *testing.T) {
	covered := map[templateKey]bool{}
	for _, test := range renderCases() {
		covered[templateKey{test.kind, test.detail}] = true
		failure := New(test.kind, test.detail, test.options...)
		if got := failure.Message(); got != test.want {
			t.Errorf("kind %d detail %d message=%q want %q", test.kind, test.detail, got, test.want)
		}
		if got := failure.Error(); got != test.want {
			t.Errorf("kind %d detail %d Error()=%q want the public message %q", test.kind, test.detail, got, test.want)
		}
		if failure.Internal() {
			t.Errorf("kind %d detail %d is internal", test.kind, test.detail)
		}
	}
	for key := range templates {
		if !covered[key] {
			t.Errorf("template kind %d detail %d has no literal render case", key.kind, key.detail)
		}
	}
}

func TestInvariantAndUnconvertedRenderFixedPublicTextAndKeepTheCause(t *testing.T) {
	cause := errors.New("relation 0123 is absent")
	invariant := Invariant(cause)
	if invariant.Message() != "internal server error" || !invariant.Internal() || invariant.Error() != "internal compile failure: relation 0123 is absent" || invariant.Cause() != cause {
		t.Fatalf("invariant message=%q error=%q internal=%t cause=%v", invariant.Message(), invariant.Error(), invariant.Internal(), invariant.Cause())
	}
	if got := Invariantf("model %s is absent", "m1").Error(); got != "internal compile failure: model m1 is absent" {
		t.Fatalf("Invariantf=%q", got)
	}
	if got := Invariant(nil).Error(); got != "internal compile failure: unclassified compile failure" {
		t.Fatalf("Invariant(nil)=%q", got)
	}
	converted := Unconverted(cause).(*Error)
	if converted.Message() != "GraphQL operation could not be bound" || converted.Internal() || converted.Error() != "GraphQL operation could not be bound: relation 0123 is absent" {
		t.Fatalf("unconverted message=%q internal=%t error=%q", converted.Message(), converted.Internal(), converted.Error())
	}
	if Unconverted(nil) != nil {
		t.Fatal("Unconverted(nil) is not nil")
	}
	if got := New(KindInvalidValue, Detail(200)).Message(); got != "GraphQL operation is invalid" {
		t.Fatalf("unknown template message=%q", got)
	}
}

func TestAccessorsExposeOnlyTheTypedFields(t *testing.T) {
	failure := New(KindIntegerRange, IntegerTake, Path("take"), Model("Article"), Bounds(-5, 5))
	minimum, hasMin := failure.Min()
	maximum, hasMax := failure.Max()
	if failure.Kind() != KindIntegerRange || failure.Detail() != IntegerTake || failure.Argument() != "take" || !hasMin || minimum != -5 || !hasMax || maximum != 5 || failure.Reason() != golem.ReasonInvalidValue {
		t.Fatalf("accessors kind=%d detail=%d argument=%q min=%d/%t max=%d/%t reason=%q", failure.Kind(), failure.Detail(), failure.Argument(), minimum, hasMin, maximum, hasMax, failure.Reason())
	}
	limit := New(KindSelectionLimit, DetailNone, Limit("MaxDepth", 32))
	if _, ok := limit.Min(); ok || limit.Limit() != "MaxDepth" || limit.Reason() != golem.ReasonLimitExceeded {
		t.Fatalf("limit accessors limit=%q reason=%q", limit.Limit(), limit.Reason())
	}
	if New(KindEmpty, DetailNone).Reason() != "" {
		t.Fatal("a plain refusal carries a reason")
	}
	for _, selection := range []*Error{
		New(KindSelectionShape, ShapeNeedsSelection, Path("author")),
		New(KindDirectiveInvalid, DirectiveNeedsBoolean, Path("skip")),
		New(KindFieldUnavailable, UnavailableField, Path("title")),
	} {
		if selection.Argument() != "" {
			t.Errorf("kind %d detail %d reports field name %q as an argument", selection.Kind(), selection.Detail(), selection.Argument())
		}
	}
}

func TestPathHelpersJoinSchemaNamesAndIndexes(t *testing.T) {
	for _, test := range []struct{ parent, child, want string }{
		{"", "where", "where"}, {"where", "", "where"}, {"where", "id", "where.id"}, {"where.AND", "[2]", "where.AND[2]"},
	} {
		if got := Join(test.parent, test.child); got != test.want {
			t.Errorf("Join(%q,%q)=%q want %q", test.parent, test.child, got, test.want)
		}
	}
	if got := Index("orderBy", 3); got != "orderBy[3]" {
		t.Fatalf("Index=%q", got)
	}
}

func TestAtOnRootAndEnsureClassifyEveryErrorOnce(t *testing.T) {
	inner := New(KindInvalidValue, ValueMalformed, Path("equals"), Type("UUID"))
	at := At(inner, "where.id").(*Error)
	if at.Argument() != "where.id.equals" || inner.Argument() != "equals" {
		t.Fatalf("At mutated its input or lost the path: at=%q inner=%q", at.Argument(), inner.Argument())
	}
	on := On(at, "Article").(*Error)
	if on.Message() != "Input where.id.equals on Article is not a valid UUID" {
		t.Fatalf("On message=%q", on.Message())
	}
	if again := On(on, "Other").(*Error); again.Message() != on.Message() {
		t.Fatalf("On replaced an existing model: %q", again.Message())
	}
	public := golem.RuntimeOperationError(golem.CodeNotFound, "read", golem.ModelID{}, golem.FieldID{}, "record not found", nil)
	for name, err := range map[string]error{"At": At(public, "where"), "On": On(public, "Article"), "Ensure": Ensure(public)} {
		if err != public {
			t.Errorf("%s changed a public golem error", name)
		}
	}
	untyped := errors.New("sql: connection refused")
	for name, err := range map[string]error{"At": At(untyped, "where"), "On": On(untyped, "Article"), "Ensure": Ensure(untyped), "Root": Root(untyped, "items")} {
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind() != KindInvariant || typed.Cause() != untyped {
			t.Errorf("%s did not make an untyped error an Invariant: %#v", name, err)
		}
	}
	invariant := Invariant(untyped)
	if At(invariant, "where") != error(invariant) || On(invariant, "Article") != error(invariant) {
		t.Fatal("an Invariant was rewritten")
	}
	for name, err := range map[string]error{"At": At(nil, "where"), "On": On(nil, "Article"), "Ensure": Ensure(nil), "Root": Root(nil, "items")} {
		if err != nil {
			t.Errorf("%s(nil)=%v", name, err)
		}
	}
	rooted := Root(inner, "items")
	if name, ok := RootOf(rooted); !ok || name != "items" {
		t.Fatalf("RootOf=%q/%t", name, ok)
	}
	if again := Root(rooted, "other"); again != rooted {
		t.Fatal("Root re-rooted an already rooted error")
	}
	if _, ok := RootOf(inner); ok {
		t.Fatal("RootOf found a root on an unrooted error")
	}
	var typed *Error
	if !errors.As(rooted, &typed) || typed != inner || rooted.Error() != inner.Error() {
		t.Fatal("Root hid the typed error")
	}
	rootedPublic := Root(public, "items")
	var failure *golem.Error
	if !errors.As(rootedPublic, &failure) || failure != public {
		t.Fatal("Root hid a public golem error")
	}
}
