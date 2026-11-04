package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

func (fixture *p10OperationFixture) rawGraphQL(t *testing.T, principal, query string) map[string]json.RawMessage {
	t.Helper()
	ctx := context.WithValue(context.Background(), p10OperationPrincipalKey{}, p10operations.Principal{ID: principal, Valid: true})
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(recorder, request)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode GraphQL status=%d: %v", recorder.Code, err)
	}
	return envelope
}

func commitReportOf(t *testing.T, envelope map[string]json.RawMessage) (string, bool) {
	t.Helper()
	raw, present := envelope["extensions"]
	if !present {
		return "", false
	}
	var extensions struct {
		Golem struct {
			Commit string `json:"commit"`
		} `json:"golem"`
	}
	if err := json.Unmarshal(raw, &extensions); err != nil {
		t.Fatal(err)
	}
	return extensions.Golem.Commit, true
}

func inviteMemberDocument(alias string, id, team any, email string) string {
	return fmt.Sprintf(`%s: inviteMember(id: "%v", teamID: "%v", owner: "alpha", email: "%s", fail: false)`, alias, id, team, email)
}

func TestPartialCommitIsReportedAcrossProviders(t *testing.T) {
	t.Run("second root fails after the first committed", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 801)
			envelope := fixture.rawGraphQL(t, "alpha", "mutation { "+inviteMemberDocument("first", id, fixture.alpha, "g@example.test")+" "+inviteMemberDocument("second", id, fixture.alpha, "g@example.test")+" }")
			if _, failed := envelope["errors"]; !failed {
				t.Fatalf("the duplicate root did not fail: %s", envelope)
			}
			if commit, present := commitReportOf(t, envelope); !present || commit != "PARTIAL" {
				t.Fatalf("commit report = %q present=%v, envelope=%v", commit, present, envelope)
			}
			if exists, _, _ := fixture.inviteExists(t, id); !exists {
				t.Fatal("the first root did not commit")
			}
		})
	})

	t.Run("custom mutation commits once and then errors", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			var creates atomic.Int32
			p10operations.SetInviteHook(func(context.Context) error {
				if creates.Add(1) == 2 {
					return errors.New("second create refused")
				}
				return nil
			})
			first, second, third := p10OperationID(t, 811), p10OperationID(t, 812), p10OperationID(t, 813)
			envelope := fixture.rawGraphQL(t, "alpha", fmt.Sprintf(`mutation { submitInvites(first: "%s", second: "%s", third: "%s", teamID: "%s", foreign: "alpha") { id } }`, first, second, third, fixture.alpha))
			if _, failed := envelope["errors"]; !failed {
				t.Fatalf("the custom mutation did not fail: %s", envelope)
			}
			if commit, present := commitReportOf(t, envelope); !present || commit != "PARTIAL" {
				t.Fatalf("commit report = %q present=%v, envelope=%v", commit, present, envelope)
			}
			if exists, _, _ := fixture.inviteExists(t, first); !exists {
				t.Fatal("the first create did not commit")
			}
			if exists, _, _ := fixture.inviteExists(t, second); exists {
				t.Fatal("the refused create committed")
			}
		})
	})

	t.Run("a single failing root without commits carries no extension", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			missing := p10OperationID(t, 821)
			envelope := fixture.rawGraphQL(t, "alpha", "mutation { "+inviteMemberDocument("only", missing, fixture.alpha, "not-an-email")+" }")
			if _, failed := envelope["errors"]; !failed {
				t.Fatalf("the update of a missing row did not fail: %s", envelope)
			}
			if raw, present := envelope["extensions"]; present {
				t.Fatalf("a clean failure carries extensions: %s", raw)
			}
		})
	})

	t.Run("a no-op update commits nothing", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 831)
			fixture.seedInvite(t, id)
			envelope := fixture.rawGraphQL(t, "alpha", fmt.Sprintf(`mutation {
  noop: reviewInvite(id: "%s", alsoEmail: false)
  failing: %s
}`, id, inviteMemberDocument("x", p10OperationID(t, 832), fixture.alpha, "not-an-email")[3:]))
			if _, failed := envelope["errors"]; !failed {
				t.Fatalf("the second root did not fail: %s", envelope)
			}
			if raw, present := envelope["extensions"]; present {
				t.Fatalf("a no-op update reported a commit: %s", raw)
			}
		})
	})

	t.Run("a commit whose connection drops is unknown", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 841)
			fixture.gate.dropNextCommit()
			envelope := fixture.rawGraphQL(t, "alpha", "mutation { "+inviteMemberDocument("only", id, fixture.alpha, "g@example.test")+" }")
			if _, failed := envelope["errors"]; !failed {
				t.Fatalf("the dropped commit did not surface an error: %s", envelope)
			}
			if commit, present := commitReportOf(t, envelope); !present || commit != "UNKNOWN" {
				t.Fatalf("commit report = %q present=%v, envelope=%v", commit, present, envelope)
			}
			if exists, _, _ := fixture.inviteExists(t, id); !exists {
				t.Fatal("the forwarded commit did not reach the database")
			}
		})
	})

	t.Run("a committed result that is unavailable agrees with its error", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			id := p10OperationID(t, 851)
			envelope := fixture.rawGraphQL(t, "alpha", fmt.Sprintf(`mutation { submitBlank(id: "%s", teamID: "%s", owner: "alpha", email: "b@example.test") { id } }`, id, fixture.alpha))
			var failures []struct {
				Extensions map[string]any `json:"extensions"`
			}
			if err := json.Unmarshal(envelope["errors"], &failures); err != nil || len(failures) != 1 || failures[0].Extensions["committed"] != true {
				t.Fatalf("errors = %s (%v)", envelope["errors"], err)
			}
			if commit, present := commitReportOf(t, envelope); !present || commit != "PARTIAL" {
				t.Fatalf("commit report = %q present=%v", commit, present)
			}
		})
	})
}
