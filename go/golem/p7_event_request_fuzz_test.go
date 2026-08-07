package golem

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

// FuzzP7FrozenEventRequestRejectionAndClone exercises the closed event-option
// boundary and verifies that neither authoring inputs nor getter results share
// mutable storage with the frozen request.
func FuzzP7FrozenEventRequestRejectionAndClone(f *testing.F) {
	f.Add(uint8(0), "visible", []byte{1, 2, 3})
	f.Add(uint8(1), "nil", []byte(nil))
	f.Add(uint8(6), "unknown-kind", []byte{9})
	f.Add(uint8(8), "foreign-selection", []byte{4, 5})
	f.Add(uint8(9), "foreign-predicate", []byte{6, 7})
	f.Add(uint8(10), "zero-descriptor", []byte{8})

	f.Fuzz(func(t *testing.T, shape uint8, text string, payload []byte) {
		if len(text) > 256 {
			text = text[:256]
		}
		if len(payload) > 256 {
			payload = payload[:256]
		}
		payload = append([]byte(nil), payload...)

		predicate := readPosts.Title.Eq(text).And(readPosts.Payload.Eq(payload))
		selection := EventSelect[readPost](readPosts.Title, readPosts.Payload, readPosts.Comments)
		descriptor := GeneratedModelDescriptor[readPost](readPostModel, GeneratedDescriptorShape(
			[]FieldID{readPostID, readPostTitle, readPostBody, readPostPayload}, nil, nil,
			[]RelationMetadata{GeneratedRelationMetadata(readPostModel, readCommentModel, readPostComments, readRelation, RelationInverse, RelationToMany)},
		))
		options := []EventOption[readPost]{EventWhere(predicate), selection}
		valid := false

		switch shape % 11 {
		case 0:
			valid = utf8.ValidString(text)
		case 1:
			options = []EventOption[readPost]{nil}
		case 2:
			options = []EventOption[readPost]{EventWhere(predicate), EventWhere(readPosts.Title.Eq("duplicate"))}
		case 3:
			options = []EventOption[readPost]{selection, EventSelect[readPost](readPosts.ID)}
		case 4:
			options = []EventOption[readPost]{EventSelect[readPost]()}
		case 5:
			options = []EventOption[readPost]{EventSelect[readPost](readPosts.Title, readPosts.Title)}
		case 6:
			options = []EventOption[readPost]{eventOption[readPost]{node: eventOptionNode[readPost]{kind: eventOptionKind(255)}}}
		case 7:
			options = []EventOption[readPost]{eventOption[readPost]{node: eventOptionNode[readPost]{kind: eventOptionSelect, fields: []FieldID{{}}}}}
		case 8:
			foreign := GeneratedTextField[readPost, string](readCommentBody)
			options = []EventOption[readPost]{EventSelect[readPost](foreign)}
		case 9:
			foreign := GeneratedTextField[readPost, string](readCommentBody)
			options = []EventOption[readPost]{EventWhere(foreign.Eq(text))}
		case 10:
			descriptor = ModelDescriptor[readPost]{}
		}

		frozen, err := RuntimeFreezeEventOptions(descriptor, options...)
		if !valid {
			if err == nil {
				t.Fatalf("invalid event request was accepted: shape=%d", shape%11)
			}
			return
		}
		if err != nil {
			t.Fatalf("valid event request was rejected: %v", err)
		}
		if frozen.ModelID() != readPostModel {
			t.Fatalf("model=%x want=%x", frozen.ModelID(), readPostModel)
		}

		wantSelection := []FieldID{readPostTitle, readPostPayload, readPostComments}
		gotSelection := frozen.Selection()
		if len(gotSelection) != len(wantSelection) {
			t.Fatalf("selection=%x", gotSelection)
		}
		gotSelection[0] = FieldID{0xff}
		if again := frozen.Selection(); !equalEventFieldIDs(again, wantSelection) {
			t.Fatalf("Selection exposed mutable frozen storage: %x", again)
		}

		where, present := frozen.Where()
		if !present {
			t.Fatal("valid request lost where predicate")
		}
		wantCanonical := where.CanonicalBytes()
		if len(payload) != 0 {
			payload[0] ^= 0xff
		}
		predicate.node = nil
		if concrete, ok := selection.(eventOption[readPost]); ok && len(concrete.node.fields) != 0 {
			concrete.node.fields[0] = FieldID{0xee}
		}
		where.canonical = append(where.canonical, 0xff)
		where.root = nil

		again, present := frozen.Where()
		if !present || !bytes.Equal(again.CanonicalBytes(), wantCanonical) {
			t.Fatalf("Where or authoring input exposed mutable frozen storage")
		}
		if !equalEventFieldIDs(frozen.Selection(), wantSelection) {
			t.Fatalf("authoring option mutated frozen selection: %x", frozen.Selection())
		}
	})
}

func equalEventFieldIDs(left, right []FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
