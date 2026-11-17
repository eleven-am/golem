package golem

import (
	"bytes"
	"strings"
	"testing"
)

func TestBoundFactFreezesExactOwnedArgumentsAndPreservesConstantBodyIdentity(t *testing.T) {
	var previous []byte
	for _, value := range []byte{7, 8} {
		blob := []byte{value}
		bound := GeneratedBoundFact(freezeModelID, strings.Repeat("bc", 16), scopeBindingTestDigest(), "ready", None[freezeModel](), GeneratedTemplateArgument("Blob", blob))
		rules := NewRules[freezeModel]()
		rules.BindFactScope(GeneratedBoundFactScope(freezeModelID, scopeBindingTestID(), scopeBindingTestDigest(), All[freezeModel]()))
		ref := rules.BindFact(bound)
		rules.CanRead(ref.Not())
		frozen, err := rules.Freeze(freezeModelID)
		if err != nil {
			t.Fatal(err)
		}
		view, present := frozen.View().Facts()[0].TemplateBinding()
		if !present || frozen.Version() != 4 || len(view.Arguments()) != 1 {
			t.Fatal("frozen fact lost its exact template inputs")
		}
		before := frozen.CanonicalBytes()
		blob[0] = 99
		argument, _ := view.Arguments()[0].Operand().One()
		copied, _ := argument.Bytes()
		if !bytes.Equal(copied, []byte{value}) {
			t.Fatal("generated fact retained a borrowed argument")
		}
		copied[0] = 100
		canonical := view.CanonicalBytes()
		canonical[0] ^= 0xff
		again, err := rules.Freeze(freezeModelID)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, again.CanonicalBytes()) {
			t.Fatal("fact input/accessor mutation changed an immutable policy")
		}
		if previous != nil && bytes.Equal(previous, before) {
			t.Fatal("constant body erased distinct fact arguments")
		}
		previous = before
	}
}

func TestBoundFactRequiresItsPolicyOwnerAndGeneratedAuthority(t *testing.T) {
	bound := GeneratedBoundFact(freezeModelID, strings.Repeat("bc", 16), scopeBindingTestDigest(), "ready", All[freezeModel]())
	for _, name := range []string{"inline-scope", "foreign-owner", "duplicate", "zero"} {
		t.Run(name, func(t *testing.T) {
			rules := NewRules[freezeModel]()
			if name == "inline-scope" {
				rules.FactScope(All[freezeModel]())
			} else {
				rules.BindFactScope(GeneratedBoundFactScope(freezeModelID, scopeBindingTestID(), scopeBindingTestDigest(), All[freezeModel]()))
			}
			ref := rules.BindFact(bound)
			switch name {
			case "foreign-owner":
				other := NewRules[freezeModel]()
				ref = other.BindFact(bound)
			case "duplicate":
				rules.Fact("ready", All[freezeModel]())
			case "zero":
				rules.BindFact(BoundFact[freezeModel]{})
			}
			rules.CanRead(ref)
			if _, err := rules.Freeze(freezeModelID); err == nil {
				t.Fatal("invalid generated fact crossed policy ownership/freezing")
			}
		})
	}
}
