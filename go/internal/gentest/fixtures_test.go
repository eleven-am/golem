package gentest

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestSocialCompilationIRIsCanonicalAndFresh(t *testing.T) {
	first := SocialCompilationIR()
	second := SocialCompilationIR()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated fixture builds differ")
	}
	if first.Model.FormatVersion != ir.ModelFormatVersion || first.Contract.FormatVersion != ir.ContractFormatVersion {
		t.Fatalf("fixture versions = model %d contract %d", first.Model.FormatVersion, first.Contract.FormatVersion)
	}
	if len(first.Model.Models) != 2 || len(first.Model.Relations) != 1 {
		t.Fatalf("fixture shape = %d models, %d relations", len(first.Model.Models), len(first.Model.Relations))
	}
	if first.Model.Schema.Actor.Name != "Actor" {
		t.Fatalf("fixture actor = %#v", first.Model.Schema.Actor)
	}

	first.Model.Models[0].LogicalName = "mutated"
	first.Model.Relations[0].LocalFields[0] = "mutated"
	first.Contract.Methods[0].Actor.Name = "MutatedActor"
	third := SocialCompilationIR()
	if third.Model.Models[0].LogicalName != "User" {
		t.Fatal("builder retained a model mutation")
	}
	if third.Model.Relations[0].LocalFields[0] != postAuthorIDFieldID {
		t.Fatal("builder retained a nested slice mutation")
	}
	if third.Contract.Methods[0].Actor.Name != "Actor" {
		t.Fatal("builder retained a nested pointer mutation")
	}
}

func TestSocialCompilationIRJSONIsByteStable(t *testing.T) {
	want, err := json.MarshalIndent(SocialCompilationIR(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 20; iteration++ {
		got, err := json.MarshalIndent(SocialCompilationIR(), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("iteration %d produced different JSON", iteration)
		}
	}
}
