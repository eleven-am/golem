package fixtures

import (
	"testing"

	"github.com/eleven-am/golem/go/phase0"
)

func TestSocialFixtureOwnsTypedFieldAndPolicyBoundaries(t *testing.T) {
	if got := PostTitle.Descriptor(); got.Model != "Post" || got.Name != "title" || got.Kind != phase0.ScalarString {
		t.Fatalf("post title descriptor=%#v", got)
	}
	if got := UserEmail.Descriptor(); got.Model != "User" || got.Name != "email" || got.Kind != phase0.ScalarString {
		t.Fatalf("user email descriptor=%#v", got)
	}
	var postRules phase0.Rules[Post]
	PostPolicy{}.Define(&postRules, Actor{ID: "actor"})
	if _, err := phase0.Canonical(postRules.Effective(phase0.Read)); err != nil {
		t.Fatalf("post policy is not canonical: %v", err)
	}
}
