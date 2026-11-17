package pipeline

import "testing"

func TestFreshGeneratedScalarSetsPreserveCallerReadsMasksAndUpdates(t *testing.T) {
	runGeneratedDTOConsumer(t, "scalarsets", nil, scalarSetsConsumerTest)
}

const scalarSetsConsumerTest = `package acceptance_test

import (
 "context"
 "encoding/binary"
 "errors"
 "fmt"
 "os"
 "strings"
 "testing"

 app "example.test/home"
 "github.com/eleven-am/golem/go/golem"
 providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestLargeActorGrantSnapshot(t *testing.T) {
 ctx := context.Background()
 database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
 if err != nil { t.Fatal(err) }
 t.Cleanup(func() { _ = database.Close() })
 uuid := func(number uint32) golem.UUID { var id golem.UUID; binary.BigEndian.PutUint32(id[12:], number); return id }
 grants := make([]golem.UUID, 2000)
 for i := range grants { grants[i] = uuid(uint32(i+1)) }
 first, last, filtered, outside := grants[0], grants[1999], grants[1998], uuid(2001)
 actor := app.Actor{AccountID: first, ProfileID: uuid(3001), GrantRevision: 1, Allowed: grants}
 application, err := app.Open(ctx, app.Config[string]{
  Database: database,
  ResolvePrincipal: func(context.Context, string) (app.Actor, error) { return actor, nil },
  SnapshotActor: func(source app.Actor) (app.Actor, error) { source.Allowed = append([]golem.UUID(nil), source.Allowed...); return source, nil },
  PolicyCacheKey: func(snapshot app.Actor) (string, bool) { return fmt.Sprintf("%s/%s/%d", snapshot.AccountID, snapshot.ProfileID, snapshot.GrantRevision), true },
 })
 if err != nil { t.Fatal(err) }
 system := application.System()
 for _, owner := range []golem.UUID{first, last, filtered, outside} {
  if _, err := system.Items.Create(ctx, app.Items.Create(app.Items.ID.Create(owner), app.Items.Owner.Create(owner), app.Items.Label.Create("before"), app.Items.Secret.Create("private"))); err != nil { t.Fatal(err) }
 }
 caller, err := application.ForPrincipal(ctx, "viewer")
 if err != nil { t.Fatalf("principal policy: %v; cause: %v", err, errors.Unwrap(err)) }
 // Mutate the resolver's slice before this caller first builds a policy.
 // Its policy and row projection must still use the owned actor snapshot.
 grants[0] = outside
 actor.GrantRevision++
 where := app.Items.Owner.NotIn(filtered)
 projection := golem.Select[app.Item](app.Items.ID, app.Items.Owner, app.Items.Label, app.Items.Secret)
 rows, err := caller.Items.FindMany(ctx, golem.Where(where), projection)
 if err != nil { t.Fatalf("caller read: %v; cause: %v", err, errors.Unwrap(err)) }
 assertRows := func(rows []golem.Row[app.Item], expected ...golem.UUID) {
  t.Helper()
  if len(rows) != len(expected) { t.Fatalf("rows=%d want=%d", len(rows), len(expected)) }
  got := make(map[golem.UUID]bool)
  for _, row := range rows {
   id, present := golem.Value(row, app.Items.ID).Get()
   if !present { t.Fatal("selected authorized ID missing") }
   got[id] = true
   secret, visible := golem.Value(row, app.Items.Secret).Get()
   if visible != (id == first) || visible && secret != "private" { t.Fatalf("field mask for %s: value=%q visible=%v", id, secret, visible) }
  }
  for _, id := range expected { if !got[id] { t.Fatalf("missing authorized row %s", id) } }
 }
 assertRows(rows, first, last)
 count, err := caller.Items.Count(ctx, golem.Where(where))
 if err != nil || count != 2 { t.Fatalf("count=%d err=%v", count, err) }
 fresh, err := application.ForPrincipal(ctx, "viewer")
 if err != nil { t.Fatal(err) }
 rows, err = fresh.Items.FindMany(ctx, golem.Where(where), projection)
 if err != nil { t.Fatal(err) }
 assertRows(rows, last, outside)
 // The same account and grant revision can have a different active profile.
 actor.ProfileID = uuid(3002)
 actor.Allowed = []golem.UUID{filtered}
 otherProfile, err := application.ForPrincipal(ctx, "viewer")
 if err != nil { t.Fatal(err) }
 rows, err = otherProfile.Items.FindMany(ctx, projection)
 if err != nil { t.Fatal(err) }
 assertRows(rows, filtered)
 updated, err := caller.Items.UpdateMany(ctx, where, app.Items.UpdateMany(app.Items.Label.Set("after")))
 if err != nil || updated != 2 { t.Fatalf("updated=%d err=%v", updated, err) }
 updated, err = caller.Items.UpdateMany(ctx, app.Items.Owner.Eq(outside), app.Items.UpdateMany(app.Items.Label.Set("leaked")))
 if err != nil || updated != 0 { t.Fatalf("out-of-scope updated=%d err=%v", updated, err) }
 persisted, err := system.Items.FindMany(ctx, golem.Select[app.Item](app.Items.ID, app.Items.Label))
 if err != nil || len(persisted) != 4 { t.Fatalf("persisted=%d err=%v", len(persisted), err) }
 for _, row := range persisted {
  id, _ := golem.Value(row, app.Items.ID).Get()
  label, present := golem.Value(row, app.Items.Label).Get()
  expected := "before"
  if id == first || id == last { expected = "after" }
  if !present || label != expected { t.Fatalf("persisted %s label=%q want=%q", id, label, expected) }
 }
 // A later principal resolution must not alter a retained caller's snapshot.
 rows, err = caller.Items.FindMany(ctx, golem.Where(where), projection)
 if err != nil { t.Fatal(err) }
 assertRows(rows, first, last)
 oversized := app.Items.Label.In(strings.Repeat("x", 1<<20))
 assertCapacity := func(operation string, err error) {
  t.Helper()
  var failure *golem.Error
  if !errors.As(err, &failure) || failure.Code != golem.CodeLimitExceeded || failure.Reason != golem.ReasonLimitExceeded || failure.Limit != golem.LimitScalarSetBytes || failure.LimitMax != 1<<20 || failure.Operation != operation {
   t.Fatalf("%s oversized set did not return bounded public metadata: %v", operation, err)
  }
 }
 _, err = caller.Items.FindMany(ctx, golem.Where(oversized))
 assertCapacity("findMany", err)
 _, err = caller.Items.Count(ctx, golem.Where(oversized))
 assertCapacity("count", err)
 _, err = caller.Items.UpdateMany(ctx, oversized, app.Items.UpdateMany(app.Items.Label.Set("refused")))
 assertCapacity("updateMany", err)
 predicates := make([]golem.Predicate[app.Item], 1000)
 for i := range predicates { predicates[i] = app.Items.Label.Contains(fmt.Sprintf("name-%04d", i)) }
 _, err = system.Items.Count(ctx, golem.Where(golem.Or(predicates...)))
 var parameterFailure *golem.Error
 if !errors.As(err, &parameterFailure) || parameterFailure.Code != golem.CodeLimitExceeded || parameterFailure.Limit != golem.LimitStatementParameters || parameterFailure.LimitMax != 999 || parameterFailure.Operation != "count" {
  t.Fatalf("independent comparisons bypassed 999-bind limit: %v", err)
 }
}
`
