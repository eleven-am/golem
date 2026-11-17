package pipeline

import (
	"os"
	"strings"
	"testing"
)

func TestFreshGeneratedRelatedFactTemplateUsesPhysicalTruth(t *testing.T) {
	if os.Getenv("GOLEM_POLICY_HISTORY_WRITER_REHEARSAL") != "4" {
		t.Skip("run scripts/verify.sh writer4-database for historical policy facts")
	}
	runGeneratedDTOConsumer(t, "policyfacts", func(source string) string {
		source = strings.Replace(source, "golem.Providers(schema, golem.SQLite, golem.PostgreSQL)", "golem.Providers(schema, golem.SQLite)", 1)
		return source + `
func (Video) DefinePolicy(rules *golem.Rules[Video], actor Actor) { rules.CanRead(golem.None[Video]()) }
func (StorageObject) DefinePolicy(rules *golem.Rules[StorageObject], actor Actor) { rules.CanRead(golem.None[StorageObject]()) }
`
	}, factTemplatePhysicalConsumerTest)
}

const factTemplatePhysicalConsumerTest = `package acceptance_test

import (
 "context"
 "errors"
 "os"
 "testing"

 app "example.test/home"
 "github.com/eleven-am/golem/go/events"
 "github.com/eleven-am/golem/go/golem"
 providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestPhysicalDataFactsRemainFreshAndDoNotGrantDependencyReads(t *testing.T) {
 ctx:=context.Background()
 database,err:=providersqlite.Open(ctx,providersqlite.Config{DataSourceName:os.Getenv("GOLEM_OBJECT_DB")})
 if err!=nil { t.Fatal(err) }
 t.Cleanup(func(){ _=database.Close() })
 transport,err:=events.NewMemoryTransport(events.MemoryLimits{})
 if err!=nil { t.Fatal(err) }
 application,err:=app.Open(ctx,app.Config[string]{Database:database,EventTransport:transport,
  ReportEventOperator:func(context.Context,events.OperatorAuditRecord){},
  ResolvePrincipal:func(context.Context,string)(app.Actor,error){return app.Actor{},nil},
 })
 if err!=nil { t.Fatal(err) }
 system:=application.System()
 id:=func(value byte)golem.UUID{return golem.UUID{15:value}}
 for _,value:=range []byte{1,2,3,4} {
  if _,err:=system.Medias.Create(ctx,app.Medias.Create(app.Medias.ID.Create(id(value))));err!=nil { t.Fatal(err) }
 }
 for _,value:=range []byte{1,2,3} {
  height:=int64(5); if value==3 {height=20}
  if _,err:=system.Videos.Create(ctx,app.Videos.Create(app.Videos.ID.Create(id(value+10)),app.Videos.MediaID.Create(id(value)),app.Videos.Height.Create(height)));err!=nil {t.Fatal(err)}
 }
 for _,value:=range []byte{1,2} {
  state:="other";if value==1 {state="ready"}
  if _,err:=system.StorageObjects.Create(ctx,app.StorageObjects.Create(app.StorageObjects.ID.Create(id(value+20)),app.StorageObjects.VideoID.Create(id(value+10)),app.StorageObjects.State.Create(state)));err!=nil {t.Fatal(err)}
 }
 caller,err:=application.ForPrincipal(ctx,"viewer")
 if err!=nil {t.Fatalf("principal policy: %v; cause: %v",err,errors.Unwrap(err))}
 assertRows:=func(expected ...byte){
  t.Helper()
  rows,err:=caller.Medias.FindMany(ctx,golem.Select[app.Media](app.Medias.ID))
  if err!=nil {t.Fatalf("caller read: %v; cause: %v",err,errors.Unwrap(err))}
  seen:=map[golem.UUID]bool{}
  for _,row:=range rows {value,present:=golem.Value(row,app.Medias.ID).Get();if !present {t.Fatal("authorized media ID missing")};seen[value]=true}
  if len(rows)!=len(expected) {t.Fatalf("media rows=%v expected=%v",seen,expected)}
  for _,value:=range expected {if !seen[id(value)] {t.Fatalf("media %d missing from %v",value,seen)}}
  count,err:=caller.Medias.Count(ctx)
  if err!=nil||count!=int64(len(expected)) {t.Fatalf("count=%d err=%v expected=%v",count,err,expected)}
 }
 // Readability is a physical fact: a tall video or a ready copy is enough.
 // Empty Media.Videos and an empty StorageObjects extent retain their
 // specified None/Every semantics instead of borrowing target read grants.
 assertRows(1,3)
 assertDenied:=func(count int64,err error){
  t.Helper()
  var failure *golem.Error
  if count!=0 || !errors.As(err,&failure) || failure.Code!=golem.CodeForbidden {t.Fatalf("dependency reads were not forbidden: count=%d err=%v",count,err)}
 }
 assertDenied(caller.Videos.Count(ctx))
 assertDenied(caller.StorageObjects.Count(ctx))
 if _,err:=system.StorageObjects.UpdateMany(ctx,app.StorageObjects.ID.Eq(id(21)),app.StorageObjects.UpdateMany(app.StorageObjects.State.Set("other")));err!=nil {t.Fatal(err)}
 assertRows(3)
 if _,err:=system.Videos.UpdateMany(ctx,app.Videos.ID.Eq(id(12)),app.Videos.UpdateMany(app.Videos.Height.Set(int64(20))));err!=nil {t.Fatal(err)}
 assertRows(2,3)
 if _,err:=system.Videos.UpdateMany(ctx,app.Videos.ID.Eq(id(13)),app.Videos.UpdateMany(app.Videos.Height.Set(int64(5))));err!=nil {t.Fatal(err)}
 assertRows(2)
 if _,err:=system.StorageObjects.Create(ctx,app.StorageObjects.Create(app.StorageObjects.ID.Create(id(25)),app.StorageObjects.VideoID.Create(id(11)),app.StorageObjects.State.Create("ready")));err!=nil {t.Fatal(err)}
 assertRows(1,2)
 if _,err:=system.StorageObjects.DeleteMany(ctx,app.StorageObjects.ID.Eq(id(25)));err!=nil {t.Fatal(err)}
 assertRows(2)
}
`
