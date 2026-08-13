package schematest

import "testing"

func TestFixtureBuildsExactTwoProviderRegistry(t *testing.T) {
	fixture := New(t)
	if fixture.Registry == nil || fixture.User == ([16]byte{}) || fixture.Post == ([16]byte{}) || len(fixture.SQLite.Tables) == 0 || len(fixture.PostgreSQL.Tables) == 0 {
		t.Fatalf("incomplete fixture: user=%x post=%x sqlite=%d postgres=%d", fixture.User, fixture.Post, len(fixture.SQLite.Tables), len(fixture.PostgreSQL.Tables))
	}
}
