package pipeline

import "testing"

func TestFreshGeneratedInputPresenceAndOneOfHTTP(t *testing.T) {
	runGeneratedDTOConsumer(t, "objecttypes", nil, inputPresenceConsumerTest)
}

const inputPresenceConsumerTest = `package acceptance_test
import (
 "context"
 "encoding/json"
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
func TestInputSemantics(t *testing.T){
 ctx:=context.Background()
 database,err:=providersqlite.Open(ctx,providersqlite.Config{DataSourceName:os.Getenv("GOLEM_OBJECT_DB")});if err!=nil{t.Fatal(err)};t.Cleanup(func(){_ = database.Close()})
 application,err:=app.Open(ctx,app.Config[app.Principal]{Database:database,ResolvePrincipal:func(context.Context,app.Principal)(app.Actor,error){return app.Actor{},nil}});if err!=nil{t.Fatal(err)}
 if _,err:=application.System().Users.Create(ctx,app.Users.Create(app.Users.Name.Create("viewer")));err!=nil{t.Fatal(err)}
 server,err:=application.GraphQL(app.GraphQLConfig[app.Principal]{PrincipalFromContext:func(context.Context)(app.Principal,bool){return app.Principal{},true},ReportInternalError:func(_ context.Context,err error){t.Errorf("internal input failure: %v",err)}});if err!=nil{t.Fatal(err)}
 handler:=server.Handler()
 fields:="noteState note countState count tagState childState childNoteState valuesState valueStates"
 for _,test:=range []struct{name,arguments string;variables map[string]any;states map[string]string;note string;valueStates []string}{
  {name:"omitted",arguments:"patch: {}",states:map[string]string{"noteState":"omitted","countState":"omitted","tagState":"omitted","childState":"omitted","valuesState":"omitted"}},
  {name:"null",arguments:"patch: {note: null,count: null,child: null,values: null}, tag: null",states:map[string]string{"noteState":"null","countState":"null","tagState":"null","childState":"null","valuesState":"null"}},
  {name:"values",arguments:"patch: {note: \"\",count: 0,child: {note: \"child\"},values: [null,\"\"]}, tag: \"\"",states:map[string]string{"noteState":"value","countState":"value","tagState":"value","childState":"value","childNoteState":"value","valuesState":"value"},valueStates:[]string{"null","value"}},
 }{
  response:=inputRequest(t,handler,"{ inputState("+test.arguments+") {"+fields+"} }",test.variables)
  if response["errors"]!=nil{t.Fatalf("%s: %#v",test.name,response)}
  view:=response["data"].(map[string]any)["inputState"].(map[string]any)
  for field,state:=range test.states{if view[field]!=state{t.Fatalf("%s %s: %#v",test.name,field,view)}}
  if view["note"]!=test.note || view["count"]!=float64(0){t.Fatalf("zero value: %#v",view)}
  values:=view["valueStates"].([]any);if len(values)!=len(test.valueStates){t.Fatalf("nested list presence: %#v",view)};for i,state:=range test.valueStates{if values[i]!=state{t.Fatalf("nested list presence: %#v",view)}}
 }
 for _,test:=range []struct{variables map[string]any;state string}{{map[string]any{},"omitted"},{map[string]any{"note":nil},"null"},{map[string]any{"note":""},"value"}}{
  response:=inputRequest(t,handler,"query($note: String){inputState(patch: {note: $note},tag: $note){noteState tagState}}",test.variables)
  if response["errors"]!=nil{t.Fatalf("variable presence: %#v",response)}
  view:=response["data"].(map[string]any)["inputState"].(map[string]any)
  if view["noteState"]!=test.state || view["tagState"]!=test.state{t.Fatalf("variable omission/null: %#v",response)}
 }
 exactDocument:=inputRequest(t,handler,"query($document: JSON){inputState(patch: {document: $document}){documentState document}}",map[string]any{"document":map[string]any{"exact":json.Number("9007199254740993")}})
 if exactDocument["errors"]!=nil{t.Fatalf("native JSON input: %#v",exactDocument)}
 documentView:=exactDocument["data"].(map[string]any)["inputState"].(map[string]any)
 if documentView["documentState"]!="value" || documentView["document"]!="{\"exact\":9007199254740993}"{t.Fatalf("native JSON precision: %#v",exactDocument)}
 computed:=inputRequest(t,handler,"{ users { a: inputState(patch: {}) {noteState tagState} b: inputState(patch: {note: null},tag: null) {noteState tagState} c: batchInputState(patch: {}) {tagState} d: batchInputState(patch: {},tag: null) {tagState} e: batchInputState(patch: {},tag: \"\") {tagState} } }",nil)
 if computed["errors"]!=nil{t.Fatalf("computed input presence: %#v",computed)}
 row:=computed["data"].(map[string]any)["users"].([]any)[0].(map[string]any)
 if row["a"].(map[string]any)["noteState"]!="omitted" || row["b"].(map[string]any)["noteState"]!="null" || row["c"].(map[string]any)["tagState"]!="omitted" || row["d"].(map[string]any)["tagState"]!="null" || row["e"].(map[string]any)["tagState"]!="value"{t.Fatalf("computed cache mixed omission/null/value: %#v",row)}
 for _,test:=range []struct{query string;variables map[string]any}{
  {query:"{inputState(patch:{values:\"\"}){valueStates}}"},
  {query:"query($values:[String]){inputState(patch:{values:$values}){valueStates}}",variables:map[string]any{"values":""}},
  {query:"{users{inputState(patch:{values:\"\"}){valueStates} batchInputState(patch:{values:\"\"}){valueStates}}}"},
 }{
  response:=inputRequest(t,handler,test.query,test.variables)
  if response["errors"]!=nil{t.Fatalf("singleton list input: %#v",response)}
  views:=[]any{response["data"].(map[string]any)["inputState"]}
  if users,present:=response["data"].(map[string]any)["users"];present{user:=users.([]any)[0].(map[string]any);views=[]any{user["inputState"],user["batchInputState"]}}
  for _,value:=range views{states:=value.(map[string]any)["valueStates"].([]any);if len(states)!=1 || states[0]!="value"{t.Fatalf("singleton list materialization: %#v",response)}}
 }
 id:=golem.UUID{1}.String()
 mutation:="mutation($target: PlaybackTarget!){choosePlayback(target: $target)}"
 for _,member:=range []string{"videoId","mediaId","playlistId","playlistVideoId","playQueueItemId"}{
  response:=inputRequest(t,handler,mutation,map[string]any{"target":map[string]any{member:id}})
  if response["errors"]!=nil || response["data"].(map[string]any)["choosePlayback"]!=member{t.Fatalf("valid target %s: %#v",member,response)}
 }
 calls:=app.InputChoiceCalls
 for _,target:=range []any{map[string]any{},map[string]any{"videoId":nil},map[string]any{"videoId":id,"mediaId":id},map[string]any{"unknown":id},map[string]any{"videoId":"not-a-uuid"}}{
  response:=inputRequest(t,handler,mutation,map[string]any{"target":target})
  failures,ok:=response["errors"].([]any)
  if !ok || len(failures)==0 || failures[0].(map[string]any)["extensions"].(map[string]any)["code"]!="BAD_USER_INPUT" || app.InputChoiceCalls!=calls{t.Fatalf("invalid target reached mutation: target=%#v response=%#v calls=%d",target,response,app.InputChoiceCalls)}
 }
 overflow:=inputRequest(t,handler,"{inputState(patch: {count: 32768}) {count}}",nil)
 if failures,ok:=overflow["errors"].([]any);!ok || len(failures)!=1 || failures[0].(map[string]any)["extensions"].(map[string]any)["code"]!="BAD_USER_INPUT"{t.Fatalf("wrapped numeric overflow: %#v",overflow)}
 bounded,err:=application.GraphQL(app.GraphQLConfig[app.Principal]{PrincipalFromContext:func(context.Context)(app.Principal,bool){return app.Principal{},true},Limits:golemgraphql.Limits{MaxInputNodes:2},ReportInternalError:func(_ context.Context,err error){t.Errorf("internal budget failure: %v",err)}});if err!=nil{t.Fatal(err)}
 boundedCalls:=app.InputChoiceCalls
 refused:=inputRequest(t,bounded.Handler(),"mutation{choosePlayback(target:{videoId:\""+id+"\"})}",nil)
 failures,ok:=refused["errors"].([]any)
 if !ok || len(failures)!=1 || failures[0].(map[string]any)["extensions"].(map[string]any)["code"]!="INPUT_LIMIT_EXCEEDED" || app.InputChoiceCalls!=boundedCalls{t.Fatalf("inline input budget reached mutation: %#v calls=%d",refused,app.InputChoiceCalls)}
}
func inputRequest(t *testing.T,handler http.Handler,query string,variables map[string]any)map[string]any{
 t.Helper();payload,_:=json.Marshal(map[string]any{"query":query,"variables":variables});recorder:=httptest.NewRecorder();request:=httptest.NewRequest(http.MethodPost,"/graphql",strings.NewReader(string(payload)));request.Header.Set("Content-Type","application/json");handler.ServeHTTP(recorder,request)
 var response map[string]any;if err:=json.Unmarshal(recorder.Body.Bytes(),&response);err!=nil{t.Fatal(err)};return response
}
`
