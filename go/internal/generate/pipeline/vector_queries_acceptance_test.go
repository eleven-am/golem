package pipeline

import "testing"

func TestFreshGeneratedVectorRankingPreservesAuthorizationAndTransactionSnapshots(t *testing.T) {
	runGeneratedDTOConsumer(t, "vectorqueries", nil, vectorQueriesConsumerTest)
}

const vectorQueriesConsumerTest = `package acceptance_test

import (
 "context"
 "errors"
 "fmt"
 "os"
 "strings"
 "sync/atomic"
 "testing"
 "time"

 app "example.test/home"
 "github.com/eleven-am/golem/go/embedding"
 "github.com/eleven-am/golem/go/golem"
 "github.com/eleven-am/golem/go/observe"
 providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
 "github.com/eleven-am/golem/go/queue"
 golemruntime "github.com/eleven-am/golem/go/runtime"
)

type embedder struct { specification embedding.Specification; calls atomic.Int64 }
type vectorTrace struct { reads, statements atomic.Int64 }
func (trace *vectorTrace) ObserveGolem(_ context.Context, value observe.Observation) { if value.Operation()==observe.OperationSemanticVectors { trace.reads.Add(1);trace.statements.Add(int64(value.StatementCount())) } }
func (p *embedder) Specification() embedding.Specification { return p.specification }
func (p *embedder) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
 p.calls.Add(int64(len(inputs)))
 vectors := make([]embedding.Vector, len(inputs))
 for i, input := range inputs {
  values := []float32{1,0,0}
  if strings.Contains(input.Text(), "beta") { values = []float32{0,1,0} }
  var err error
  vectors[i], err = embedding.NewVector(values)
  if err != nil { return nil, err }
 }
 return vectors, nil
}

func TestPreparedVectorRanking(t *testing.T) {
 ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
 defer cancel()
 // Local disposable storage exercises the one-connection profile; this is
 // not a NAS latency or durability measurement.
 database, err := providersqlite.OpenNetworkFilesystem(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
 if err != nil { t.Fatal(err) }
 t.Cleanup(func() { _ = database.Close() })
 specification, err := embedding.NewSpecification("fixture", "model", "r1", 3, 8)
 if err != nil { t.Fatal(err) }
 provider := &embedder{specification: specification}
 embeddings, err := embedding.NewRegistry(map[string]embedding.Provider{"content": provider})
 if err != nil { t.Fatal(err) }
 trace:=&vectorTrace{}
 config:=app.Config[string]{Database: database, Embeddings: embeddings, Observer:trace, Queue: &golemruntime.QueueConfig{Registry: queue.NewRegistry()}, ResolvePrincipal: func(_ context.Context, principal string)(app.Actor,error){ return app.Actor{Viewer:"alice",ReadContent:principal=="reader" || principal=="vector_only",HideOwner:principal=="vector_only"},nil }}
 application, err := app.Open(ctx, config)
 if err != nil { t.Fatal(err) }
 system := application.System()
 for _, row := range []struct{id,owner,content string; visible bool}{{"a","alice","alpha",true},{"b","alice","beta",true},{"c","alice","beta",false},{"d","bob","beta",true}} {
  if _,err := system.Documents.Create(ctx,app.Documents.Create(app.Documents.ID.Create(row.id),app.Documents.Owner.Create(row.owner),app.Documents.Content.Create(row.content),app.Documents.Visible.Create(row.visible))); err != nil { t.Fatal(err) }
 }
 if err := application.RefreshSemanticIndexes(ctx); err != nil { t.Fatal(err) }
 if provider.calls.Load()!=4 { t.Fatalf("seed embedded %d records",provider.calls.Load()) }
 if _,err:=system.Credits.Create(ctx,app.Credits.Create(app.Credits.ID.Create("credit-a"),app.Credits.DocumentID.Create("a"),app.Credits.Name.Create("alpha")));err!=nil{t.Fatal(err)}
 if _,err:=system.Companies.Create(ctx,app.Companies.Create(app.Companies.ID.Create("company-a"),app.Companies.DocumentID.Create("a"),app.Companies.Name.Create("")));err!=nil{t.Fatal(err)}
 vector, err := embedding.NewVector([]float32{0,1,0})
 if err != nil { t.Fatal(err) }
 query, err := embedding.NewQueryVector(specification,vector)
 if err != nil { t.Fatal(err) }
 caller,err := application.ForPrincipal(ctx,"reader")
 if err != nil { t.Fatal(err) }
 assertRanks := func(results []golem.SemanticResult[app.Document], expected ...string) {
  t.Helper()
  if len(results)!=len(expected) { t.Fatalf("ranked %d rows want %v",len(results),expected) }
  for i,result := range results { id,present:=golem.Value(result.Row(),app.Documents.ID).Get(); if !present || id!=expected[i] { t.Fatalf("rank %d=%q present=%v want=%q",i,id,present,expected[i]) } }
 }
 ranked,err := caller.Documents.SearchByVectorContent(ctx,query,10)
 if err != nil { t.Fatal(err) }
 assertRanks(ranked,"b","a")
 if ranked[0].Similarity()!=1 || ranked[1].Distance()!=1 { t.Fatalf("known-vector distances=%v,%v",ranked[0].Distance(),ranked[1].Distance()) }
 trusted,err := system.Documents.SearchByVectorContent(ctx,query,10)
 if err != nil { t.Fatal(err) }
 if len(trusted)!=4 { t.Fatalf("system ranking lost rows: %d",len(trusted)) }
 narrowed,err := caller.Documents.SearchByVectorContent(ctx,query,10,app.Documents.ID.Eq("a"))
 if err != nil { t.Fatal(err) }
 assertRanks(narrowed,"a")
 masked,err := application.ForPrincipal(ctx,"masked")
 if err != nil { t.Fatal(err) }
 ordinary,err := masked.Documents.FindMany(ctx,golem.Select[app.Document](app.Documents.ID))
 if err != nil || len(ordinary)!=3 { t.Fatalf("masked actor should still read its three rows: rows=%d err=%v",len(ordinary),err) }
 hidden,err := masked.Documents.SearchByVectorContent(ctx,query,10)
 var denied *golem.Error
 if len(hidden)!=0 || !errors.As(err,&denied) || denied.Code!=golem.CodeForbidden { t.Fatalf("denied source field disclosed vector results: rows=%d err=%v",len(hidden),err) }
 selectors:=[]golem.UniqueSelectorValue[app.Document]{app.Documents.ByID.Value("b"),app.Documents.ByID.Value("c"),app.Documents.ByID.Value("d"),app.Documents.ByID.Value("missing"),app.Documents.ByID.Value("a"),app.Documents.ByID.Value("b")}
 cells,err:=caller.Documents.VectorsContent(ctx,selectors...);if err!=nil{t.Fatal(err)}
 assertCells:=func(cells []golem.ReadValue[embedding.QueryVector], present ...bool){t.Helper();if len(cells)!=len(present){t.Fatalf("vector cells=%d want=%d",len(cells),len(present))};for i,cell:=range cells{vector,found:=cell.Get();if found!=present[i] || !found && !cell.IsNull() || found && vector.Specification()!=specification {t.Fatalf("vector source %d presence=%v state=%v",i,found,cell.State())}}}
 assertCells(cells,true,false,false,false,true,true)
 stored,_:=cells[0].Get();values:=stored.Vector().Values();values[1]=99
 if stored.Vector().Values()[1]!=1 || stored.Validate()!=nil{t.Fatal("stored query vector was mutable")}
 trustedCells,err:=system.Documents.VectorsContent(ctx,selectors...);if err!=nil{t.Fatal(err)}
 assertCells(trustedCells,true,true,true,false,true,true)
 maskedCells,err:=masked.Documents.VectorsContent(ctx,app.Documents.ByID.Value("a"));if err!=nil{t.Fatal(err)}
 assertCells(maskedCells,false)
 vectorOnly,err:=application.ForPrincipal(ctx,"vector_only");if err!=nil{t.Fatal(err)}
 vectorOnlyCells,err:=vectorOnly.Documents.VectorsContent(ctx,app.Documents.ByID.Value("a"));if err!=nil{t.Fatal(err)}
 assertCells(vectorOnlyCells,true)
 if empty,err:=caller.Documents.VectorsContent(ctx);err!=nil || len(empty)!=0{t.Fatalf("empty vector sources: rows=%d err=%v",len(empty),err)}
 batch:=make([]golem.UniqueSelectorValue[app.Document],1000);for i:=range batch{batch[i]=app.Documents.ByID.Value("b")}
 beforeReads,beforeStatements:=trace.reads.Load(),trace.statements.Load()
 many,err:=caller.Documents.VectorsContent(ctx,batch...);if err!=nil{t.Fatal(err)}
 if len(many)!=len(batch){t.Fatalf("large vector batch returned %d cells",len(many))};for _,cell:=range many{if _,ok:=cell.Get();!ok{t.Fatal("duplicate input lost its ready vector")}}
 if reads,statements:=trace.reads.Load()-beforeReads,trace.statements.Load()-beforeStatements;reads!=10 || statements!=10{t.Fatalf("1000 vector sources used reads=%d statements=%d; want 10 bounded batches",reads,statements)}
 if _,err:=caller.Documents.VectorsContent(ctx,append(batch,app.Documents.ByID.Value("a"))...);err==nil{t.Fatal("vector source count exceeded 1000")}
 config.ReadLimits=golemruntime.ReadLimits{MaxStatementParameters:12}
 bounded,err:=app.Open(ctx,config);if err!=nil{t.Fatal(err)}
 boundedCaller,err:=bounded.ForPrincipal(ctx,"reader");if err!=nil{t.Fatal(err)}
 beforeStatements=trace.statements.Load()
 narrowCells,err:=boundedCaller.Documents.VectorsContent(ctx,batch[:23]...);if err!=nil{t.Fatal(err)}
 if len(narrowCells)!=23{t.Fatalf("capacity-split vector batch returned %d cells",len(narrowCells))};for _,cell:=range narrowCells{if _,ok:=cell.Get();!ok{t.Fatal("capacity split lost a vector")}}
 if statements:=trace.statements.Load()-beforeStatements;statements<=1 || statements>=23{t.Fatalf("capacity fallback did not preserve batching: statements=%d",statements)}
 rollback := errors.New("roll back fixture writes")
 err = caller.Transaction(ctx,func(tx *app.CallerTx[string])error{
  if _,err:=tx.Documents.SearchContent(ctx,"beta",10);err==nil || !strings.Contains(err.Error(),"single-connection transaction") { return fmt.Errorf("text search called provider inside caller transaction: %v",err) }
  if _,err:=tx.Documents.Delete(ctx,app.Documents.ByID.Value("b"));err!=nil{return err}
  insideCells,err:=tx.Documents.VectorsContent(ctx,app.Documents.ByID.Value("b"),app.Documents.ByID.Value("a"));if err!=nil{return err};assertCells(insideCells,false,true)
  inside,err:=tx.Documents.SearchByVectorContent(ctx,query,10);if err!=nil{return err}
  assertRanks(inside,"a")
  return rollback
 })
 if !errors.Is(err,rollback) { t.Fatalf("caller transaction: %v",err) }
 err = system.Transaction(ctx,func(tx *app.SystemTx[string])error{
  if _,err:=tx.Documents.SearchContent(ctx,"beta",10);err==nil || !strings.Contains(err.Error(),"single-connection transaction") { return fmt.Errorf("text search called provider inside system transaction: %v",err) }
  if _,err:=tx.Documents.Delete(ctx,app.Documents.ByID.Value("d"));err!=nil{return err}
  insideCells,err:=tx.Documents.VectorsContent(ctx,app.Documents.ByID.Value("d"),app.Documents.ByID.Value("c"));if err!=nil{return err};assertCells(insideCells,false,true)
  inside,err:=tx.Documents.SearchByVectorContent(ctx,query,10);if err!=nil{return err}
  if len(inside)!=3 { t.Fatalf("system transaction snapshot rows=%d",len(inside)) }
  return rollback
 })
 if !errors.Is(err,rollback) { t.Fatalf("system transaction: %v",err) }
 err=caller.Transaction(ctx,func(tx *app.CallerTx[string])error{
  if _,err:=tx.Documents.Update(ctx,app.Documents.ByID.Value("a"),app.Documents.Update(app.Documents.Content.Set("beta pending")));err!=nil{return err}
  pending,err:=tx.Documents.VectorsContent(ctx,app.Documents.ByID.Value("a"));if err!=nil{return err};assertCells(pending,false)
  if _,err:=tx.Documents.SimilarContent(ctx,app.Documents.ByID.Value("a"),10);err==nil{return fmt.Errorf("similarity used a vector for a transaction-local changed document")}
  current,err:=tx.Documents.SearchByVectorContent(ctx,query,10);if err!=nil{return err};assertRanks(current,"b")
  return rollback
 })
 if !errors.Is(err,rollback){t.Fatalf("transaction-local document staleness: %v",err)}
 ranked,err = caller.Documents.SearchByVectorContent(ctx,query,10)
 if err != nil { t.Fatal(err) }
 assertRanks(ranked,"b","a")
 changed,err:=embedding.NewSpecification("fixture","model","r2",3,8);if err!=nil{t.Fatal(err)}
 mismatch,err:=embedding.NewQueryVector(changed,vector);if err!=nil{t.Fatal(err)}
 if _,err:=caller.Documents.SearchByVectorContent(ctx,mismatch,10);err==nil{t.Fatal("index accepted another embedding revision")}
 if provider.calls.Load()!=4 { t.Fatalf("prepared-vector operations called provider: %d",provider.calls.Load()) }
 outside,err:=caller.Documents.SearchContent(ctx,"beta",10)
 if err!=nil{t.Fatalf("ordinary text search on one-connection profile refused: %v",err)}
 assertRanks(outside,"b","a")
 if provider.calls.Load()!=5{t.Fatalf("ordinary text search did not invoke provider once: %d",provider.calls.Load())}
 if _,err:=system.Documents.Update(ctx,app.Documents.ByID.Value("b"),app.Documents.Update(app.Documents.Content.Set("alpha replacement")));err!=nil{t.Fatal(err)}
 pending,err:=caller.Documents.VectorsContent(ctx,app.Documents.ByID.Value("b"));if err!=nil{t.Fatal(err)};assertCells(pending,false)
 if provider.calls.Load()!=5{t.Fatal("reading a pending vector invoked the provider")}
 if err:=application.RefreshSemanticIndexes(ctx);err!=nil{t.Fatal(err)}
 refreshed,err:=caller.Documents.VectorsContent(ctx,app.Documents.ByID.Value("b"));if err!=nil{t.Fatal(err)};assertCells(refreshed,true)
 latest,_:=refreshed[0].Get();if latest.Vector().Values()[0]!=1 || provider.calls.Load()!=6{t.Fatalf("document change did not refresh vector: calls=%d",provider.calls.Load())}
 // An application command owns the explicitly maintained internal document.
 // Its related-row changes and document update share the same transaction.
 changeCreditsAndCompanies:=func(tx *app.SystemTx[string],creditName,companyName string)error{
  if _,err:=tx.Credits.Update(ctx,app.Credits.ByID.Value("credit-a"),app.Credits.Update(app.Credits.Name.Set(creditName)));err!=nil{return err}
  if _,err:=tx.Companies.Update(ctx,app.Companies.ByID.Value("company-a"),app.Companies.Update(app.Companies.Name.Set(companyName)));err!=nil{return err}
  credit,err:=tx.Credits.FindUnique(ctx,app.Credits.ByID.Value("credit-a"));if err!=nil{return err}
  company,err:=tx.Companies.FindUnique(ctx,app.Companies.ByID.Value("company-a"));if err!=nil{return err}
  creditText,creditPresent:=golem.Value(credit,app.Credits.Name).Get();companyText,companyPresent:=golem.Value(company,app.Companies.Name).Get()
  if !creditPresent || !companyPresent{return fmt.Errorf("related document inputs absent")}
  if _,err:=tx.Documents.Update(ctx,app.Documents.ByID.Value("a"),app.Documents.Update(app.Documents.Content.Set(strings.TrimSpace(creditText+" "+companyText))));err!=nil{return err}
  unavailable,err:=tx.Documents.VectorsContent(ctx,app.Documents.ByID.Value("a"));if err!=nil{return err};assertCells(unavailable,false)
  return nil
 }
 if err:=system.Transaction(ctx,func(tx *app.SystemTx[string])error{return changeCreditsAndCompanies(tx,"beta","studio")});err!=nil{t.Fatal(err)}
 derived,err:=caller.Documents.VectorsContent(ctx,app.Documents.ByID.Value("a"));if err!=nil{t.Fatal(err)};assertCells(derived,false)
 if provider.calls.Load()!=6{t.Fatal("document-maintenance transaction called embedding provider")}
 if err:=application.RefreshSemanticIndexes(ctx);err!=nil{t.Fatal(err)}
 derived,err=caller.Documents.VectorsContent(ctx,app.Documents.ByID.Value("a"));if err!=nil{t.Fatal(err)};assertCells(derived,true)
 derivedVector,_:=derived[0].Get();if derivedVector.Vector().Values()[1]!=1 || provider.calls.Load()!=7{t.Fatalf("related-row changes did not refresh document: calls=%d",provider.calls.Load())}
 err=system.Transaction(ctx,func(tx *app.SystemTx[string])error{if err:=changeCreditsAndCompanies(tx,"alpha","rollback");err!=nil{return err};return rollback})
 if !errors.Is(err,rollback){t.Fatalf("related document rollback: %v",err)}
 derived,err=caller.Documents.VectorsContent(ctx,app.Documents.ByID.Value("a"));if err!=nil{t.Fatal(err)};assertCells(derived,true)
 credit,err:=system.Credits.FindUnique(ctx,app.Credits.ByID.Value("credit-a"));if err!=nil{t.Fatal(err)}
 company,err:=system.Companies.FindUnique(ctx,app.Companies.ByID.Value("company-a"));if err!=nil{t.Fatal(err)}
 if value,_:=golem.Value(credit,app.Credits.Name).Get();value!="beta"{t.Fatalf("credit escaped rollback: %q",value)}
 if value,_:=golem.Value(company,app.Companies.Name).Get();value!="studio"{t.Fatalf("company escaped rollback: %q",value)}
 tokenID:=golem.UUID{9}
 if _,err:=system.Tokens.Create(ctx,app.Tokens.Create(app.Tokens.ID.Create(tokenID),app.Tokens.Content.Create("alpha")));err!=nil{t.Fatal(err)}
 if _,err:=system.Smalls.Create(ctx,app.Smalls.Create(app.Smalls.ID.Create(int16(7)),app.Smalls.Serial.Create(int32(8)),app.Smalls.Content.Create("alpha")));err!=nil{t.Fatal(err)}
 var smallSelector golem.UniqueSelectorValue[app.Small]
 for _,identity:=range app.GolemGeneratedSmallDescriptor.Metadata().Identities(){if identity.Kind()==golem.PrimaryIdentity{fields:=identity.Fields();if len(fields)!=2{t.Fatal("small primary identity has wrong width")};smallSelector=golem.GeneratedUniqueSelectorValue[app.Small](app.GolemGeneratedSmallDescriptor.Metadata().ModelID(),identity.KeyID(),golem.GeneratedSelectorComponent(fields[0],int16(7)),golem.GeneratedSelectorComponent(fields[1],int32(8)))}}
 if err:=application.RefreshSemanticIndexes(ctx);err!=nil{t.Fatal(err)}
 if provider.calls.Load()!=9{t.Fatalf("identity fixtures embedded unexpected documents: %d",provider.calls.Load())}
 err=system.Transaction(ctx,func(tx *app.SystemTx[string])error{
  if _,err:=tx.Tokens.Update(ctx,app.Tokens.ByID.Value(tokenID),app.Tokens.Update(app.Tokens.Content.Set("beta")));err!=nil{return err}
  if _,err:=tx.Smalls.Update(ctx,smallSelector,app.Smalls.Update(app.Smalls.Content.Set("beta")));err!=nil{return err}
  tokens,err:=tx.Tokens.VectorsContent(ctx,app.Tokens.ByID.Value(tokenID));if err!=nil{return err};assertCells(tokens,false)
  smalls,err:=tx.Smalls.VectorsContent(ctx,smallSelector);if err!=nil{return err};assertCells(smalls,false)
  unchanged,err:=tx.Documents.VectorsContent(ctx,app.Documents.ByID.Value("a"));if err!=nil{return err};assertCells(unchanged,true)
  return rollback
 })
 if !errors.Is(err,rollback){t.Fatalf("transaction identity snapshot: %v",err)}
 tokens,err:=caller.Tokens.VectorsContent(ctx,app.Tokens.ByID.Value(tokenID));if err!=nil{t.Fatal(err)};assertCells(tokens,true)
 smalls,err:=caller.Smalls.VectorsContent(ctx,smallSelector);if err!=nil{t.Fatal(err)};assertCells(smalls,true)
 if provider.calls.Load()!=9{t.Fatal("identity snapshot reads invoked the provider")}
}
`
