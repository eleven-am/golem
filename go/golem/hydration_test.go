package golem

import (
	"bytes"
	"testing"
)

func TestIdentityHydrationKeepsCorrelationSeparateAcrossFrozenClones(t *testing.T) {
	identity, err := RuntimeNewRowIdentity([]FieldID{readPostID}, []RuntimeReadCell{RuntimePresentReadCell(readPostID, UUID{15: 1}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	base, err := FreezeFindMany(readPostDescriptor, Select[readPost](readPosts.Title), Take[readPost](1), Where(readPosts.Title.Eq("visible")))
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := RuntimeIdentityHydrationRequest(base, []RuntimeRowIdentity{identity})
	if err != nil {
		t.Fatal(err)
	}
	callerWhere, _ := base.Where()
	nested := FrozenReadSelection{request: &lookup}
	child, ok := nested.clone().Request()
	if !ok {
		t.Fatal("nested frozen request absent")
	}
	for _, value := range []FrozenReadRequest{lookup, lookup.clone(), child} {
		correlation, fields, present := value.RuntimeIdentityCorrelation()
		if !present || len(fields) != 1 || fields[0] != readPostID || correlation.View().RootModelID() != readPostModel {
			t.Fatal("private identity lost across frozen clone")
		}
		fields[0] = readPostTitle
		_, again, _ := value.RuntimeIdentityCorrelation()
		if again[0] != readPostID {
			t.Fatal("private field inventory aliases its getter")
		}
		where, present := value.Where()
		if !present || !bytes.Equal(where.CanonicalBytes(), callerWhere.CanonicalBytes()) {
			t.Fatal("private key changed caller where")
		}
	}
	if _, _, present := base.RuntimeIdentityCorrelation(); present {
		t.Fatal("constructing lookup mutated source request")
	}
}
