package golem

import (
	"bytes"
	"testing"
	"time"
)

func identityCells(t *testing.T, id UUID, at time.Time) []RuntimeReadCell {
	t.Helper()
	return []RuntimeReadCell{
		RuntimePresentReadCell(readPostID, id, nil),
		RuntimePresentReadCell(readPostTitle, at, nil),
	}
}

func TestRowIdentityRejectsInvalidKeys(t *testing.T) {
	present := RuntimePresentReadCell(readPostID, UUID{15: 1}, nil)
	for name, test := range map[string]struct {
		fields []FieldID
		cells  []RuntimeReadCell
	}{
		"no fields":        {nil, nil},
		"missing cell":     {[]FieldID{readPostID, readPostTitle}, []RuntimeReadCell{present}},
		"zero field":       {[]FieldID{{}}, []RuntimeReadCell{RuntimePresentReadCell(FieldID{}, UUID{15: 1}, nil)}},
		"wrong field":      {[]FieldID{readPostTitle}, []RuntimeReadCell{present}},
		"null component":   {[]FieldID{readPostID}, []RuntimeReadCell{RuntimeNullReadCell(readPostID)}},
		"unencodable type": {[]FieldID{readPostID}, []RuntimeReadCell{RuntimePresentReadCell(readPostID, struct{}{}, nil)}},
	} {
		if identity, err := RuntimeNewRowIdentity(test.fields, test.cells); err == nil || identity.Present() {
			t.Fatalf("%s: identity=%v err=%v", name, identity, err)
		}
	}
}

func TestRowIdentityKeepsComponentOrderAndTokenFollowsIt(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 0, 123456000, time.UTC)
	forward, err := RuntimeNewRowIdentity([]FieldID{readPostID, readPostTitle}, identityCells(t, UUID{15: 7}, at))
	if err != nil {
		t.Fatal(err)
	}
	reversed, err := RuntimeNewRowIdentity([]FieldID{readPostTitle, readPostID}, []RuntimeReadCell{
		RuntimePresentReadCell(readPostTitle, at, nil), RuntimePresentReadCell(readPostID, UUID{15: 7}, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := forward.Fields(); len(got) != 2 || got[0] != readPostID || got[1] != readPostTitle {
		t.Fatalf("forward fields=%v", got)
	}
	if got := reversed.Fields(); len(got) != 2 || got[0] != readPostTitle || got[1] != readPostID {
		t.Fatalf("reversed fields=%v", got)
	}
	if forward.Token() == reversed.Token() || forward.Token() == (RuntimeSemanticIdentityToken{}) {
		t.Fatal("component order does not distinguish identities")
	}
	zone := time.FixedZone("offset", 5*3600)
	sameInstant, err := RuntimeNewRowIdentity([]FieldID{readPostID, readPostTitle}, identityCells(t, UUID{15: 7}, at.In(zone)))
	if err != nil {
		t.Fatal(err)
	}
	if sameInstant.Token() != forward.Token() {
		t.Fatal("one datetime instant produced two identities")
	}
	other, err := RuntimeNewRowIdentity([]FieldID{readPostID, readPostTitle}, identityCells(t, UUID{15: 7}, at.Add(time.Microsecond)))
	if err != nil {
		t.Fatal(err)
	}
	if other.Token() == forward.Token() {
		t.Fatal("two datetime instants produced one identity")
	}
	values := forward.Values()
	if values[0] != (UUID{15: 7}) || !values[1].(time.Time).Equal(at) {
		t.Fatalf("values=%v", values)
	}
}

func TestRowIdentityOwnsMutableValues(t *testing.T) {
	data := []byte{1, 2, 3}
	identity, err := RuntimeNewRowIdentity([]FieldID{readPostPayload}, []RuntimeReadCell{
		RuntimePresentReadCell(readPostPayload, data, func(value []byte) []byte { return bytes.Clone(value) }),
	})
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 9
	got := identity.Values()
	if !bytes.Equal(got[0].([]byte), []byte{1, 2, 3}) {
		t.Fatalf("source mutation reached the identity: %v", got[0])
	}
	got[0].([]byte)[1] = 9
	if again := identity.Values(); !bytes.Equal(again[0].([]byte), []byte{1, 2, 3}) {
		t.Fatalf("returned value mutation reached the identity: %v", again[0])
	}
}

func TestRowIdentityTravelsWithEveryRowRepresentation(t *testing.T) {
	identity, err := RuntimeNewRowIdentity([]FieldID{readPostID}, []RuntimeReadCell{RuntimePresentReadCell(readPostID, UUID{15: 4}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := RuntimeModelReadRow(readPostModel, RuntimePresentReadCell(readPostTitle, "hello", func(value string) string { return value }))
	if err != nil {
		t.Fatal(err)
	}
	if RuntimeModelRowIdentity(bare).Present() {
		t.Fatal("a hand-built row has an identity")
	}
	runtimeRow := RuntimeModelRowWithIdentity(bare, identity)
	typed, err := RuntimeTypedReadRow(readPostDescriptor, runtimeRow)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]RuntimeRowIdentity{
		"runtime row": RuntimeModelRowIdentity(runtimeRow),
		"typed row":   RuntimeTypedRowIdentity(typed),
		"typed clone": RuntimeTypedRowIdentity(cloneRow(typed)),
		"erased":      RuntimeModelRowIdentity(RuntimeModelRowFromTyped(typed)),
		"row clone":   RuntimeModelRowIdentity(cloneRuntimeModelRow(runtimeRow)),
		"hook row": func() RuntimeRowIdentity {
			hooked, hookErr := runtimeTypedHookRow[readPost](readPostModel, runtimeRow)
			if hookErr != nil {
				t.Fatal(hookErr)
			}
			return RuntimeTypedRowIdentity(hooked)
		}(),
	} {
		if got.Token() != identity.Token() || len(got.Fields()) != 1 || got.Fields()[0] != readPostID {
			t.Fatalf("%s lost the identity: %v", name, got)
		}
	}
	if _, selected := typedKeyCell(typed); selected {
		t.Fatal("the identity became a cell")
	}
	if RuntimeModelRowIdentity(bare).Present() {
		t.Fatal("attaching an identity mutated the source row")
	}
}

func typedKeyCell(row Row[readPost]) (any, bool) {
	value, selected := row.cells[readPostID]
	return value, selected
}

func TestRelationRowsKeepTheirIdentityThroughEveryAccessor(t *testing.T) {
	identity, err := RuntimeNewRowIdentity([]FieldID{readCommentID}, []RuntimeReadCell{RuntimePresentReadCell(readCommentID, UUID{15: 8}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	child, err := RuntimeModelReadRow(readCommentModel, RuntimePresentReadCell(readCommentBody, "first", func(value string) string { return value }))
	if err != nil {
		t.Fatal(err)
	}
	child = RuntimeModelRowWithIdentity(child, identity)
	toMany := RuntimeToManyReadCell(readPostComments, []RuntimeModelRow{child})
	toOne := RuntimeToOneReadCell(readPostComments, child)
	for name, cell := range map[string]RuntimeReadCell{"to-many": toMany, "to-one": toOne} {
		runtimeRow, rowErr := RuntimeModelReadRow(readPostModel, cell)
		if rowErr != nil {
			t.Fatal(rowErr)
		}
		post, rowErr := RuntimeTypedReadRow(readPostDescriptor, runtimeRow)
		if rowErr != nil {
			t.Fatal(rowErr)
		}
		var rows []Row[readComment]
		if name == "to-many" {
			rows, _ = Many(post, readPosts.Comments).Get()
		} else {
			one, _ := One(post, GeneratedToOne[readPost, readComment](readPostComments, readRelation, readCommentModel)).Get()
			rows = []Row[readComment]{one}
		}
		if len(rows) != 1 || RuntimeTypedRowIdentity(rows[0]).Token() != identity.Token() {
			t.Fatalf("%s accessor lost the relation row identity", name)
		}
	}
}
