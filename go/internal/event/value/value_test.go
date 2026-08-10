package value

import (
	"bytes"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestP7SealedNoticeValidationAndOwnership(t *testing.T) {
	valid := func(action golem.EventAction, ordinal uint32, encoded []byte) error {
		_, err := NewNotice(golem.EventID{1}, golem.SchemaDigest{2}, golem.ModelID{3}, action, golem.CausationID{4}, ordinal, encoded)
		return err
	}
	if err := valid(golem.EventCreated, 1, []byte{1}); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"action":   valid("unknown", 1, []byte{1}),
		"ordinal":  valid(golem.EventCreated, 0, []byte{1}),
		"encoding": valid(golem.EventCreated, 1, nil),
	} {
		if err == nil {
			t.Fatalf("invalid %s accepted", name)
		}
	}
	encoded := []byte{1, 2}
	notice, err := NewNotice(golem.EventID{1}, golem.SchemaDigest{2}, golem.ModelID{3}, golem.EventCreated, golem.CausationID{4}, 1, encoded)
	if err != nil {
		t.Fatal(err)
	}
	encoded[0] = 9
	if !bytes.Equal(notice.Encoded(), []byte{1, 2}) {
		t.Fatal("notice retained caller bytes")
	}
}

func TestP7SealedBatchRequiresOneContiguousCausation(t *testing.T) {
	causation := golem.CausationID{4}
	notice := func(event byte, ordinal uint32, owner golem.CausationID) Notice {
		result, err := NewNotice(golem.EventID{event}, golem.SchemaDigest{2}, golem.ModelID{3}, golem.EventCreated, owner, ordinal, []byte{event})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if _, err := NewEventBatch(causation, []Notice{notice(1, 1, causation), notice(2, 2, causation)}); err != nil {
		t.Fatal(err)
	}
	tests := map[string][]Notice{
		"gap":               {notice(1, 1, causation), notice(2, 3, causation)},
		"foreign causation": {notice(1, 1, golem.CausationID{9})},
		"duplicate event":   {notice(1, 1, causation), notice(1, 2, causation)},
	}
	for name, notices := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewEventBatch(causation, notices); err == nil {
				t.Fatal("invalid batch accepted")
			}
		})
	}
}

func TestP7SealedSubscriptionRejectsAbsentRoutingIdentity(t *testing.T) {
	if _, err := NewSubscription(golem.SchemaDigest{}, golem.ModelID{1}); err == nil {
		t.Fatal("zero generation accepted")
	}
	if _, err := NewSubscription(golem.SchemaDigest{1}, golem.ModelID{}); err == nil {
		t.Fatal("zero model accepted")
	}
}
