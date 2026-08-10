package runtime

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"unsafe"
)

type immutableSnapshotActor struct {
	Subject string
	Flags   [2]bool
	Nested  struct{ Tenant uint64 }
	Claim   any
}

type mutableSnapshotActor struct {
	Subject string
	Roles   map[string]bool
}

func TestSnapshotActorAcceptsDeeplyImmutableValueData(t *testing.T) {
	actor := immutableSnapshotActor{Subject: "user-1", Flags: [2]bool{true, false}, Claim: "editor"}
	actor.Nested.Tenant = 42
	got, err := snapshotActor(actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != actor.Subject || got.Nested.Tenant != 42 || got.Claim != "editor" {
		t.Fatalf("snapshot=%#v", got)
	}
}

func TestSnapshotActorRejectsEveryMutableOrAliasingShapeWithoutSnapshotter(t *testing.T) {
	value := 1
	tests := []struct {
		name  string
		actor any
		kind  string
	}{
		{name: "pointer", actor: &value, kind: "ptr"},
		{name: "map", actor: map[string]bool{"admin": true}, kind: "map"},
		{name: "slice", actor: []string{"admin"}, kind: "slice"},
		{name: "channel", actor: make(chan struct{}), kind: "chan"},
		{name: "function", actor: func() {}, kind: "func"},
		{name: "unsafe pointer", actor: unsafe.Pointer(&value), kind: "unsafe.Pointer"},
		{name: "interface containing map", actor: struct{ Claim any }{Claim: map[string]string{"role": "admin"}}, kind: "map"},
		{name: "nil pointer", actor: (*int)(nil), kind: "ptr"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := snapshotActor(test.actor, nil)
			if err == nil || !strings.Contains(err.Error(), test.kind) || !strings.Contains(err.Error(), "SnapshotActor") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSnapshotActorCallsSnapshotterOnceAndDetachesConcurrentMutation(t *testing.T) {
	original := &mutableSnapshotActor{Subject: "user-1", Roles: map[string]bool{"reader": true}}
	calls := 0
	snapshot, err := snapshotActor(original, func(input *mutableSnapshotActor) (*mutableSnapshotActor, error) {
		calls++
		roles := make(map[string]bool, len(input.Roles))
		for role, allowed := range input.Roles {
			roles[role] = allowed
		}
		return &mutableSnapshotActor{Subject: input.Subject, Roles: roles}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("snapshot calls=%d", calls)
	}

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for index := 0; index < 10_000; index++ {
			original.Roles["writer"] = index%2 == 0
		}
	}()
	go func() {
		defer wait.Done()
		for index := 0; index < 10_000; index++ {
			if !snapshot.Roles["reader"] {
				t.Errorf("stable snapshot changed at iteration %d", index)
				return
			}
		}
	}()
	wait.Wait()
	if snapshot.Roles["writer"] {
		t.Fatal("source mutation reached actor snapshot")
	}
}

func TestSnapshotActorPropagatesSnapshotterFailureWithoutReturningActor(t *testing.T) {
	want := errors.New("clone failed")
	got, err := snapshotActor(&mutableSnapshotActor{}, func(*mutableSnapshotActor) (*mutableSnapshotActor, error) {
		return nil, want
	})
	if got != nil || !errors.Is(err, want) {
		t.Fatalf("actor=%#v error=%v", got, err)
	}
}
