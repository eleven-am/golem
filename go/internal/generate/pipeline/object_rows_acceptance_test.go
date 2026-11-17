package pipeline

import "testing"

func TestFreshGeneratedDTOEmbeddedRowsAreCallerHydratedAndGated(t *testing.T) {
	runGeneratedDTOConsumer(t, "objectrows", nil, objectRowsConsumerTest)
}

const objectRowsConsumerTest = `package acceptance_test
import (
 "context"
 "encoding/json"
 stderrors "errors"
 "net/http"
 "net/http/httptest"
 "os"
 "strings"
 "testing"
 app "example.test/home"
 "github.com/eleven-am/golem/go/golem"
 golemgraphql "github.com/eleven-am/golem/go/graphql"
 providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)
func TestEmbeddedRows(t *testing.T) {
 ctx:=context.Background()
 database,err:=providersqlite.Open(ctx,providersqlite.Config{DataSourceName:os.Getenv("GOLEM_OBJECT_DB")});if err!=nil{t.Fatal(err)};t.Cleanup(func(){_ = database.Close()})
 application,err:=app.Open(ctx,app.Config[string]{Database:database,ResolvePrincipal:func(_ context.Context,id string)(app.Actor,error){return app.Actor{ID:id},nil}});if err!=nil{t.Fatal(err)}
 system:=application.System()
 id1:=golem.UUID{1};id2:=golem.UUID{2};id3:=golem.UUID{3};episodeID:=golem.UUID{4};id5:=golem.UUID{5}
 for _,input:=range []golem.CreateInput[app.Movie]{
  app.Movies.Create(app.Movies.ID.Create(id1),app.Movies.Owner.Create("alice"),app.Movies.Title.Create("visible")),
  app.Movies.Create(app.Movies.ID.Create(id2),app.Movies.Owner.Create("bob"),app.Movies.Title.Create("secret")),
  app.Movies.Create(app.Movies.ID.Create(id3),app.Movies.Owner.Create("alice"),app.Movies.Title.Create("deleted")),
  app.Movies.Create(app.Movies.ID.Create(id5),app.Movies.Owner.Create("alice"),app.Movies.Title.Create("second parent")),
 } {if _,err:=system.Movies.Create(ctx,input);err!=nil{t.Fatal(err)}}
 if _,err:=system.Episodes.Create(ctx,app.Episodes.Create(app.Episodes.ID.Create(episodeID),app.Episodes.Owner.Create("bob"),app.Episodes.Name.Create("private episode")));err!=nil{t.Fatal(err)}
 first,err:=system.Movies.FindUnique(ctx,app.Movies.ByID.Value(id1));if err!=nil{t.Fatal(err)}
 second,err:=system.Movies.FindUnique(ctx,app.Movies.ByID.Value(id2));if err!=nil{t.Fatal(err)}
 deleted,err:=system.Movies.FindUnique(ctx,app.Movies.ByID.Value(id3));if err!=nil{t.Fatal(err)}
 episode,err:=system.Episodes.FindUnique(ctx,app.Episodes.ByID.Value(episodeID));if err!=nil{t.Fatal(err)}
 if _,err:=system.Movies.Delete(ctx,app.Movies.ByID.Value(id3));err!=nil{t.Fatal(err)}
 if _,err:=system.Movies.Update(ctx,app.Movies.ByID.Value(id1),app.Movies.Update(app.Movies.Title.Set("fresh")));err!=nil{t.Fatal(err)}
 app.Results=[]app.ContinueWatchingItem{{Playhead:99,Media:second,Episode:&episode},{Playhead:7,Media:first,Episode:&episode},{Playhead:88,Media:deleted},{Playhead:9,Media:first}}
 app.RequiredResult=app.RequiredItem{Episode:episode}
 principal:="alice"
 server,err:=application.GraphQL(app.GraphQLConfig[string]{PrincipalFromContext:func(context.Context)(string,bool){return principal,true},ReportInternalError:func(_ context.Context,err error){t.Errorf("internal error: %v",err)}});if err!=nil{t.Fatal(err)}
 validInput:=request(t,server.Handler(),"{ narrow(input: {value: 32767}) }")
 if validInput["errors"]!=nil || validInput["data"].(map[string]any)["narrow"]!=float64(32767){t.Fatalf("valid narrow input: %#v",validInput)}
 invalidInput:=request(t,server.Handler(),"{ narrow(input: {value: 32768}) }")
 inputErrors,ok:=invalidInput["errors"].([]any)
 if !ok || len(inputErrors)!=1 || inputErrors[0].(map[string]any)["extensions"].(map[string]any)["code"]!="BAD_USER_INPUT"{t.Fatalf("input overflow: %#v",invalidInput)}
 query:="{ watching { playhead a: media { title } b: media { id } episode { name } } }"
 response:=request(t,server.Handler(),query)
 rows:=response["data"].(map[string]any)["watching"].([]any)
 if len(rows)!=2{t.Fatalf("gate/order: %#v",response)}
 for i,expected:=range []string{"7","9"} {
  item:=rows[i].(map[string]any)
  if item["playhead"]!=expected || item["a"].(map[string]any)["title"]!="fresh" || item["episode"]!=nil {t.Fatalf("caller hydration: %#v",item)}
  if _,leaked:=item["b"].(map[string]any)["title"];leaked{t.Fatal("alias projection leaked title")}
 }
 if response["errors"]!=nil{t.Fatalf("nullable denial: %#v",response)}
 computedQuery:="{ movies(take: 10) { title metadata(input: {value: 7}) { text: label } optionalMetadata { label } watching { playhead media { title } episode { name } } batchWatching { playhead media { title } } } }"
 computed:=request(t,server.Handler(),computedQuery)
 if computed["errors"]!=nil{t.Fatalf("computed DTOs: %#v",computed)}
 parents:=computed["data"].(map[string]any)["movies"].([]any)
 if len(parents)!=2{t.Fatalf("computed parent authorization: %#v",computed)}
 for _,raw:=range parents{
  parent:=raw.(map[string]any)
  if parent["metadata"].(map[string]any)["text"]!=parent["title"].(string)+":7" || parent["optionalMetadata"]!=nil{t.Fatalf("computed scalar DTO/input/null: %#v",parent)}
  for _,field:=range []string{"watching","batchWatching"}{
   items:=parent[field].([]any)
   if len(items)!=2{t.Fatalf("computed item gate: %#v",parent)}
   for i,playhead:=range []string{"7","9"}{item:=items[i].(map[string]any);if item["playhead"]!=playhead || item["media"].(map[string]any)["title"]!="fresh" || field=="watching" && item["episode"]!=nil{t.Fatalf("computed caller hydration/order: %#v",item)}}
  }
 }
 scalarOnly:=request(t,server.Handler(),"{ watching { playhead } }")
 if got:=scalarOnly["data"].(map[string]any)["watching"].([]any);len(got)!=2{t.Fatalf("unselected gate disclosed scalars: %#v",scalarOnly)}
 boundedHandler:=func(limits golemgraphql.Limits)http.Handler{
  limited,err:=application.GraphQL(app.GraphQLConfig[string]{PrincipalFromContext:func(context.Context)(string,bool){return principal,true},Limits:limits,ReportInternalError:func(_ context.Context,err error){var limit *golem.Error;if !stderrors.As(err,&limit) || limit.Code!=golem.CodeLimitExceeded || limit.Limit!=golem.LimitHydratedRows && limit.Limit!=golem.LimitResultBytes{t.Errorf("internal bounded result error: %v",err)}}});if err!=nil{t.Fatal(err)}
  return limited.Handler()
 }
 bounded:=boundedHandler(golemgraphql.Limits{MaxHydratedRows:3})
 for i:=0;i<2;i++{single:=request(t,bounded,"{watching{playhead}}");if single["errors"]!=nil || len(single["data"].(map[string]any)["watching"].([]any))!=2{t.Fatalf("hydration budget did not reset per request: %#v",single)}}
 aggregate:=request(t,bounded,"{first:watching{playhead} second:watching{playhead}}")
 budgetErrors,ok:=aggregate["errors"].([]any)
 if !ok || len(budgetErrors)!=1{t.Fatalf("aggregate hidden-gate budget: %#v",aggregate)}
 budgetError:=budgetErrors[0].(map[string]any);budgetExtensions:=budgetError["extensions"].(map[string]any)
 if budgetExtensions["code"]!="LIMIT_EXCEEDED" || budgetExtensions["limit"]!="hydrated_rows" || budgetExtensions["max"]!=float64(3) || budgetError["path"].([]any)[0]!="second"{t.Fatalf("aggregate hydration reporting: %#v",aggregate)}
 required:=request(t,server.Handler(),"{ required { episode { name } } }")
 if required["errors"]==nil || required["data"].(map[string]any)["required"]!=nil{t.Fatalf("required member denial did not propagate: %#v",required)}
 mutation:=request(t,server.Handler(),"mutation { touch(id: \""+id1.String()+"\") { episode { name } } }")
 errors,ok:=mutation["errors"].([]any)
 if !ok || len(errors)!=1 || errors[0].(map[string]any)["extensions"].(map[string]any)["code"]!="COMMITTED_RESULT_UNAVAILABLE" {t.Fatalf("committed DTO denial: %#v",mutation)}
 commitExtensions:=errors[0].(map[string]any)["extensions"].(map[string]any)
 if commitExtensions["committed"]!=true || commitExtensions["reason"]!="RESULT_NOT_READABLE" || commitExtensions["unreadable"]!=float64(1){t.Fatalf("commit-state reporting: %#v",mutation)}
 persisted,err:=system.Movies.FindUnique(ctx,app.Movies.ByID.Value(id1));if err!=nil{t.Fatal(err)}
 if title,present:=golem.Value(persisted,app.Movies.Title).Get();!present || title!="committed"{t.Fatalf("successful write was rolled back: %#v",persisted)}
 ownEpisodeID:=golem.UUID{6}
 if _,err:=system.Episodes.Create(ctx,app.Episodes.Create(app.Episodes.ID.Create(ownEpisodeID),app.Episodes.Owner.Create("alice"),app.Episodes.Name.Create("own episode")));err!=nil{t.Fatal(err)}
 ownEpisode,err:=system.Episodes.FindUnique(ctx,app.Episodes.ByID.Value(ownEpisodeID));if err!=nil{t.Fatal(err)}
 app.RequiredResult=app.RequiredItem{Episode:ownEpisode}
 committedBudget:=request(t,boundedHandler(golemgraphql.Limits{MaxHydratedRows:1}),"mutation {first:touch(id:\""+id1.String()+"\"){episode{name}} second:touch(id:\""+id5.String()+"\"){episode{name}}}")
 budgetErrors,ok=committedBudget["errors"].([]any)
 if !ok || len(budgetErrors)!=1{t.Fatalf("post-commit hydration budget: %#v",committedBudget)}
 budgetError=budgetErrors[0].(map[string]any);budgetExtensions=budgetError["extensions"].(map[string]any)
 if budgetExtensions["code"]!="COMMITTED_RESULT_UNAVAILABLE" || budgetExtensions["committed"]!=true || budgetExtensions["reason"]!="LIMIT_EXCEEDED" || budgetExtensions["limit"]!="hydrated_rows" || budgetExtensions["max"]!=float64(1) || budgetError["path"].([]any)[0]!="second"{t.Fatalf("post-commit budget reporting: %#v",committedBudget)}
 persisted,err=system.Movies.FindUnique(ctx,app.Movies.ByID.Value(id5));if err!=nil{t.Fatal(err)}
 if title,present:=golem.Value(persisted,app.Movies.Title).Get();!present || title!="committed"{t.Fatalf("budget failure lost the successful second write: %#v",persisted)}
 if _,err:=system.Movies.Update(ctx,app.Movies.ByID.Value(id5),app.Movies.Update(app.Movies.Title.Set("before-result-limit")));err!=nil{t.Fatal(err)}
 oversized:=request(t,boundedHandler(golemgraphql.Limits{MaxResultBytes:8}),"mutation{touch(id:\""+id5.String()+"\"){episode{name}}}")
 app.RequiredResult=app.RequiredItem{Episode:episode}
 budgetErrors,ok=oversized["errors"].([]any)
 if !ok || len(budgetErrors)!=1{t.Fatalf("post-commit result size: %#v",oversized)}
 budgetExtensions=budgetErrors[0].(map[string]any)["extensions"].(map[string]any)
 if oversized["data"]!=nil || budgetExtensions["code"]!="COMMITTED_RESULT_UNAVAILABLE" || budgetExtensions["committed"]!=true || budgetExtensions["reason"]!="LIMIT_EXCEEDED" || budgetExtensions["limit"]!="result_bytes" || budgetExtensions["max"]!=float64(8) || oversized["extensions"].(map[string]any)["golem"].(map[string]any)["commit"]!="PARTIAL"{t.Fatalf("post-commit result size reporting: %#v",oversized)}
 persisted,err=system.Movies.FindUnique(ctx,app.Movies.ByID.Value(id5));if err!=nil{t.Fatal(err)}
 if title,present:=golem.Value(persisted,app.Movies.Title).Get();!present || title!="committed"{t.Fatalf("result-size failure lost the successful write: %#v",persisted)}
 principal="bob"
 other:=request(t,server.Handler(),query)
 if got:=other["data"].(map[string]any)["watching"].([]any);len(got)!=1 || got[0].(map[string]any)["playhead"]!="99"{t.Fatalf("actor isolation: %#v",other)}
 otherComputed:=request(t,server.Handler(),computedQuery)
 if otherComputed["errors"]!=nil{t.Fatalf("other actor computed DTO: %#v",otherComputed)}
 otherParents:=otherComputed["data"].(map[string]any)["movies"].([]any)
 if len(otherParents)!=1{t.Fatalf("computed parent actor isolation: %#v",otherComputed)}
 otherParent:=otherParents[0].(map[string]any)
 for _,field:=range []string{"watching","batchWatching"}{items:=otherParent[field].([]any);if len(items)!=1 || items[0].(map[string]any)["playhead"]!="99"{t.Fatalf("computed row actor isolation: %#v",otherComputed)}}
 if otherParent["watching"].([]any)[0].(map[string]any)["episode"].(map[string]any)["name"]!="private episode"{t.Fatalf("computed nullable row authorization: %#v",otherComputed)}
 if _,err:=system.Episodes.Delete(ctx,app.Episodes.ByID.Value(episodeID));err!=nil{t.Fatal(err)}
 principal="alice"
 missing:=request(t,server.Handler(),query)
 if missing["errors"]!=nil{t.Fatalf("nullable missing row: %#v",missing)}
 for _,item:=range missing["data"].(map[string]any)["watching"].([]any){if item.(map[string]any)["episode"]!=nil{t.Fatalf("missing row disclosed: %#v",item)}}
 missingRequired:=request(t,server.Handler(),"{ required { episode { name } } }")
 deniedJSON,_:=json.Marshal(required);missingJSON,_:=json.Marshal(missingRequired)
 if string(deniedJSON)!=string(missingJSON){t.Fatalf("required denied/missing disclosure differs: denied=%s missing=%s",deniedJSON,missingJSON)}
}
func request(t *testing.T,handler http.Handler,query string)map[string]any{
 t.Helper();payload,_:=json.Marshal(map[string]any{"query":query});recorder:=httptest.NewRecorder();req:=httptest.NewRequest(http.MethodPost,"/graphql",strings.NewReader(string(payload)));req.Header.Set("Content-Type","application/json");handler.ServeHTTP(recorder,req)
 var response map[string]any;if err:=json.Unmarshal(recorder.Body.Bytes(),&response);err!=nil{t.Fatal(err)};return response
}
`
