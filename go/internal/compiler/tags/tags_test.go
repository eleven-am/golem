package tags

import "testing"

func TestParseGolemPreservesOrderAndQuotedSemicolon(t *testing.T) {
	allowed := Allowed{"id": true, "default": true, "pk": true}
	attrs, errs := ParseGolem(` id=social.Post.ID ; default="a;b" ; pk `, allowed)
	if len(errs) != 0 {
		t.Fatalf("ParseGolem errors: %v", errs)
	}
	if len(attrs) != 3 || attrs[0].Name != "id" || attrs[1].Value != `"a;b"` || attrs[2].Name != "pk" {
		t.Fatalf("unexpected attributes: %#v", attrs)
	}
}

func TestParseGolemRejectsUnknownDuplicateAndEmpty(t *testing.T) {
	_, errs := ParseGolem("pk;wat=x;pk;", Allowed{"pk": true})
	want := []string{"P1_GOLEM_TAG_UNKNOWN", "P1_GOLEM_TAG_DUPLICATE", "P1_GOLEM_TAG_EMPTY_TOKEN"}
	if len(errs) != len(want) {
		t.Fatalf("got %d errors (%v), want %d", len(errs), errs, len(want))
	}
	for i := range want {
		if errs[i].Code != want[i] {
			t.Errorf("error %d code = %q, want %q", i, errs[i].Code, want[i])
		}
	}
}

func TestParseDB(t *testing.T) {
	for _, test := range []struct {
		value   string
		ignored bool
		bad     bool
	}{
		{value: "post_id"},
		{value: "-", ignored: true},
		{value: "post_id,omitempty", bad: true},
		{value: `"post_id"`, bad: true},
	} {
		got, err := ParseDB(test.value)
		if (err != nil) != test.bad {
			t.Errorf("ParseDB(%q) error = %v, bad=%v", test.value, err, test.bad)
		}
		if got.Ignored != test.ignored {
			t.Errorf("ParseDB(%q).Ignored = %v", test.value, got.Ignored)
		}
	}
}

func TestParseDirective(t *testing.T) {
	got, err := ParseDirective(Attribute{Name: "unique", HasValue: true, Value: "uq_post_author(author_id, slug)"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "uq_post_author" || len(got.Components) != 2 || got.Components[1] != "slug" {
		t.Fatalf("unexpected directive: %#v", got)
	}

	_, err = ParseDirective(Attribute{Name: "index", HasValue: true, Value: "ix(a,a)"})
	if err == nil || err.Code != "P1_DIRECTIVE_DUPLICATE_COMPONENT" {
		t.Fatalf("duplicate component error = %v", err)
	}
}
