package schematest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestSemanticFixtureMarksPostIndexed(t *testing.T) {
	fixture := NewSemanticIndexed(t)
	post, ok := fixture.Registry.Model(golem.ModelID(fixture.Post))
	if !ok || !post.SemanticIndexed() || !post.SubscriptionsEnabled() {
		t.Fatalf("post indexed=%v subscribed=%v ok=%v", post.SemanticIndexed(), post.SubscriptionsEnabled(), ok)
	}
	user, ok := fixture.Registry.Model(golem.ModelID(fixture.User))
	if !ok || user.SemanticIndexed() {
		t.Fatalf("user indexed=%v", user.SemanticIndexed())
	}
	plain := NewSubscribedIndexed(t)
	plainPost, _ := plain.Registry.Model(golem.ModelID(plain.Post))
	if plainPost.SemanticIndexed() {
		t.Fatal("non-semantic fixture reported an index")
	}
	if len(fixture.SQLite.Extensions) != 1 || len(fixture.PostgreSQL.Extensions) != 1 {
		t.Fatalf("physical extensions sqlite=%d postgresql=%d", len(fixture.SQLite.Extensions), len(fixture.PostgreSQL.Extensions))
	}
	unsubscribed := NewSemanticIndexedUnsubscribed(t)
	other, _ := unsubscribed.Registry.Model(golem.ModelID(unsubscribed.Post))
	if !other.SemanticIndexed() || other.SubscriptionsEnabled() {
		t.Fatalf("unsubscribed semantic post indexed=%v subscribed=%v", other.SemanticIndexed(), other.SubscriptionsEnabled())
	}
}
