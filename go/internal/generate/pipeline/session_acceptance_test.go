package pipeline

import (
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

const sessionConsumerPondVersion = "v0.2.9"

// Disposable shared-session integration with Frames' pinned PondSocket release.
// Frames' production credential verification and session store remain application work.
func TestFreshGeneratedLoginCookieAndSessionRevocation(t *testing.T) {
	runGeneratedDTOConsumerArtifacts(t, "objecttypes", func(source string) string {
		source = strings.Replace(source, `"context"`, `"context"
 "fmt"
 "net/http"
 "sync/atomic"`, 1)
		source = strings.Replace(source, "type Actor struct{}", "type Actor struct{ Authenticated bool }", 1)
		source = strings.Replace(source, "type Principal struct{}", "type Principal string", 1)
		source = strings.Replace(source, "func (User) DefinePolicy(rules *golem.Rules[User], _ Actor) { rules.CanRead(golem.All[User]()) }", "func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) { if actor.Authenticated { rules.CanRead(golem.All[User]()) } }", 1)
		source = strings.Replace(source, "func DefineGraphQL(schema *golem.GraphQLSchema) {", "func DefineGraphQL(schema *golem.GraphQLSchema) {\n golem.Mutation(schema, \"login\", Login)", 1)
		return source + sessionFixtureSource
	}, sessionConsumerTest, generatedDTOExecution{consumerModulePins: []generatedDTOConsumerModulePin{{Path: "github.com/eleven-am/pondsocket/go/pondsocket", Version: sessionConsumerPondVersion}}}, compilerir.SQLite)
}

const sessionFixtureSource = `
// The writer is supplied by actual HTTP middleware, never by resolver setup.
type SessionWriterKey struct{}
var sessionActive atomic.Bool
func SessionValid(principal Principal) bool { return principal == "fixture-session" && sessionActive.Load() }
func RevokeSession() { sessionActive.Store(false) }
func (User) GolemModel() golem.ModelSpec[User] { return golem.DefineModel(golem.Subscriptions[User]()) }
type LoginArgs struct { Password string }
func Login(ctx context.Context, caller *Caller[Principal], args LoginArgs) (bool, error) {
 if caller == nil || args.Password != "fixture-password" { return false, fmt.Errorf("invalid fixture login") }
 writer, ok := ctx.Value(SessionWriterKey{}).(http.ResponseWriter)
 if !ok { return false, fmt.Errorf("middleware response writer missing from generated custom caller context") }
 sessionActive.Store(true)
 http.SetCookie(writer, &http.Cookie{Name:"session", Value:"fixture-session", Path:"/", HttpOnly:true, SameSite:http.SameSiteLaxMode})
 return true,nil
}
`

const sessionConsumerTest = `package acceptance_test

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net/http"
 "net/http/cookiejar"
 "net/http/httptest"
 "net/url"
 "os"
 "strings"
 "testing"
 "time"
 app "example.test/home"
 "github.com/eleven-am/golem/go/events"
 "github.com/eleven-am/golem/go/golem"
 providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
 "github.com/gorilla/websocket"
 "github.com/eleven-am/pondsocket/go/pondsocket"
)

type principalKey struct{}
type sessionFrame struct { ID string ` + "`json:\"id\"`" + `; Type string ` + "`json:\"type\"`" + `; Payload json.RawMessage ` + "`json:\"payload\"`" + ` }

func TestGeneratedLoginCookieAndRevocation(t *testing.T) { runSharedSession(t, false) }
func TestGeneratedLoginCookieAndDeadline(t *testing.T) { runSharedSession(t, true) }

func runSharedSession(t *testing.T, expire bool) {
 ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
 defer cancel()
 database,err:=providersqlite.Open(ctx,providersqlite.Config{DataSourceName:os.Getenv("GOLEM_OBJECT_DB")})
 if err!=nil { t.Fatal(err) }; defer database.Close()
 transport,err:=events.NewMemoryTransport(events.MemoryLimits{});if err!=nil{t.Fatal(err)}
 const revalidate = 3*time.Second
 // Initialized before login/handshakes and immutable afterwards. The shared
 // session lifetime is independent of the HTTP upgrade request lifetime.
 var sessionCtx context.Context
 valid := func(p app.Principal) bool { return sessionCtx != nil && sessionCtx.Err()==nil && app.SessionValid(p) }
 application,err:=app.Open(ctx,app.Config[app.Principal]{Database:database,EventTransport:transport,
  EventLimits:events.Limits{RevalidateEvery:revalidate,RetentionEvery:events.RetentionDisabled,PublisherConcurrency:1,EvaluationConcurrency:1},
  ReportEventOperator:func(context.Context,events.OperatorAuditRecord){},
  ResolvePrincipal:func(_ context.Context,principal app.Principal)(app.Actor,error){
   if principal=="" { return app.Actor{},nil } // Anonymous actor can invoke login, but cannot read User.
   if !valid(principal){return app.Actor{},errors.New("session revoked")}
   return app.Actor{Authenticated:true},nil
  },
 })
 if err!=nil{t.Fatal(err)}
 server,err:=application.GraphQL(app.GraphQLConfig[app.Principal]{
  PrincipalFromContext:func(ctx context.Context)(app.Principal,bool){p,ok:=ctx.Value(principalKey{}).(app.Principal);return p,ok},
  WebSocketInit:func(ctx context.Context,_ json.RawMessage)(context.Context,error){
   p,_:=ctx.Value(principalKey{}).(app.Principal)
   if !valid(p){return nil,errors.New("session revoked")}
   if expire { return context.WithValue(sessionCtx,principalKey{},p),nil }
   // Preserve Golem's documented rolling-session contract: reconnect at
   // open+RevalidateEvery. Callback cancellation does not close this transport.
   return golem.WithoutCredentialExpiry(ctx),nil
  },
  ReportInternalError:func(_ context.Context,err error){t.Errorf("internal GraphQL error: %v",err)},
 })
 if err!=nil{t.Fatal(err)}
 pond:=pondsocket.NewManager(ctx)
 var endpoint *pondsocket.Endpoint
 endpoint=pond.CreateEndpoint("/pond",func(c *pondsocket.ConnectionContext)error{
  principal:=app.Principal("")
  for _,cookie:=range c.Cookies(){if cookie.Name=="session"{principal=app.Principal(cookie.Value)}}
  if !valid(principal){return c.Decline(http.StatusUnauthorized,"session invalid")}
  if err:=c.Accept();err!=nil{return err}
  id:=c.GetUser().UserID
  clients:=endpoint.GetClients(id)
  if len(clients)!=1{return errors.New("accepted Pond connection is absent")}
  // Pond v0.2.9 parents WebSocket lifetime to its manager, not the request.
  // This explicit application adapter owns deadline and revocation closure.
  stop:=context.AfterFunc(sessionCtx,func(){_ = endpoint.CloseConnection(id)})
  clients[0].OnClose(func(pondsocket.Transport)error{stop();return nil})
  if !valid(principal){_ = endpoint.CloseConnection(id);return nil}
  return clients[0].SendJSON(pondsocket.Event{Action:"SYSTEM",ChannelName:"GATEWAY",RequestId:"ready",Event:"session-ready",Payload:true})
 })
 host:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path=="/pond"{pond.HTTPHandler().ServeHTTP(w,r);return}
  principal:=app.Principal("")
  if cookie,err:=r.Cookie("session");err==nil{principal=app.Principal(cookie.Value)}
  requestContext:=context.WithValue(r.Context(),principalKey{},principal)
  requestContext=context.WithValue(requestContext,app.SessionWriterKey{},w)
  // Rolling sessions deliberately reconnect at the configured revalidation
  // period. Explicit credential deadlines have separate transport coverage.
  requestContext=golem.WithoutCredentialExpiry(requestContext)
  server.Handler().ServeHTTP(w,r.WithContext(requestContext))
 }))
 defer host.Close()
 jar,err:=cookiejar.New(nil);if err!=nil{t.Fatal(err)}
 client:=&http.Client{Jar:jar,Timeout:5*time.Second}
 post:=func(query string) map[string]any {
  t.Helper();body,_:=json.Marshal(map[string]any{"query":query})
  request,err:=http.NewRequestWithContext(ctx,http.MethodPost,host.URL,strings.NewReader(string(body)));if err!=nil{t.Fatal(err)}
  request.Header.Set("Content-Type","application/json")
  response,err:=client.Do(request);if err!=nil{t.Fatal(err)};defer response.Body.Close()
  raw,err:=io.ReadAll(io.LimitReader(response.Body,65537));if err!=nil||len(raw)>65536{t.Fatalf("response: %v",err)}
  var result map[string]any;if err:=json.Unmarshal(raw,&result);err!=nil{t.Fatalf("response %s: %v",raw,err)};return result
 }
 create:=func(name string){t.Helper();if _,err:=application.System().Users.Create(ctx,app.Users.Create(app.Users.Name.Create(name)));err!=nil{t.Fatal(err)}}
 create("private-before-login")
 anonymous:=post("{ users { name } }")
 if strings.Contains(fmt.Sprint(anonymous),"private-before-login"){t.Fatalf("anonymous disclosure: %#v",anonymous)}
 lifetime:=30*time.Second
 if expire { lifetime=12*time.Second }
 var stopSession context.CancelFunc
 sessionCtx,stopSession=context.WithTimeout(ctx,lifetime)
 defer stopSession()
 login:=post("mutation { login(password: \"fixture-password\") }")
 if login["errors"]!=nil||login["data"].(map[string]any)["login"]!=true{t.Fatalf("login: %#v",login)}
 cookieURL,_:=url.Parse(host.URL)
 cookies:=jar.Cookies(cookieURL)
 if len(cookies)!=1||cookies[0].Name!="session"||cookies[0].Value!="fixture-session"{t.Fatalf("missing middleware login cookie: %#v",cookies)}
 authenticated:=post("{ users { name } }")
 if authenticated["errors"]!=nil||!strings.Contains(fmt.Sprint(authenticated),"private-before-login"){t.Fatalf("authenticated rows: %#v",authenticated)}
 bundle:=app.GolemGeneratedSchemaBundle()
 // This registered-object fixture requires the object-aware client manifest.
 protocol:=map[string]any{"golem":map[string]any{"protocol":1,"manifestVersion":2,"contract":bundle.Contract().Fingerprint().String()}}
 dial:=func()*websocket.Conn{
  t.Helper();headers:=http.Header{}
  for _,cookie:=range jar.Cookies(cookieURL){headers.Add("Cookie",cookie.String())}
  dialer:=websocket.Dialer{Subprotocols:[]string{"graphql-transport-ws"},HandshakeTimeout:3*time.Second}
  connection,_,err:=dialer.DialContext(ctx,"ws"+strings.TrimPrefix(host.URL,"http"),headers);if err!=nil{t.Fatal(err)}
  t.Cleanup(func(){connection.Close()})
  if err:=connection.WriteJSON(map[string]any{"type":"connection_init","payload":protocol});err!=nil{t.Fatal(err)}
  return connection
 }
 pondHeaders:=http.Header{}
 for _,cookie:=range jar.Cookies(cookieURL){pondHeaders.Add("Cookie",cookie.String())}
 pondDialer:=websocket.Dialer{HandshakeTimeout:3*time.Second}
 pondConn,_,err:=pondDialer.DialContext(ctx,"ws"+strings.TrimPrefix(host.URL,"http")+"/pond",pondHeaders)
 if err!=nil{t.Fatal(err)}
 defer pondConn.Close()
 pondConn.SetReadDeadline(time.Now().Add(4*time.Second))
 // Pond may batch newline-delimited events into one WebSocket message.
 // Decode every event rather than discarding the remainder after CONNECT.
 connected,ready:=false,false
 for !ready {
  _,reader,err:=pondConn.NextReader();if err!=nil{t.Fatalf("Pond session-adapter readiness: %v",err)}
  decoder:=json.NewDecoder(reader)
  for {
   var event pondsocket.Event
   if err:=decoder.Decode(&event);errors.Is(err,io.EOF){break}else if err!=nil{t.Fatalf("Pond event: %v",err)}
   if !connected&&event.Action=="CONNECT"{connected=true;continue}
   if connected&&!ready&&event.Event=="session-ready"{ready=true;continue}
   t.Fatalf("unexpected Pond setup event: %+v",event)
  }
 }
 type pondClosure struct { at time.Time; err error }
 pondClosed:=make(chan pondClosure,1)
 pondReadDone:=make(chan struct{})
 pondDeadline,_:=sessionCtx.Deadline()
 pondConn.SetReadDeadline(pondDeadline.Add(time.Second))
 go func(){defer close(pondReadDone);_,_,err:=pondConn.ReadMessage();pondClosed<-pondClosure{time.Now(),err}}()
 defer func(){pondConn.Close();select{case <-pondReadDone:case <-time.After(time.Second):t.Error("Pond reader did not stop")}}()
 connection:=dial()
 read:=func()sessionFrame{t.Helper();connection.SetReadDeadline(time.Now().Add(4*time.Second));var frame sessionFrame;if err:=connection.ReadJSON(&frame);err!=nil{t.Fatal(err)};return frame}
 if frame:=read();frame.Type!="connection_ack"{t.Fatalf("connection ack: %+v",frame)}
 if err:=connection.WriteJSON(map[string]any{"id":"users","type":"subscribe","payload":map[string]any{"query":"subscription { userEvents { entity { name } } }"}});err!=nil{t.Fatal(err)}
 ack:=read()
 if ack.Type!="next"||string(ack.Payload)!=` + "`" + `{"data":null,"extensions":{"golem":{"live":true}}}` + "`" + `{t.Fatalf("subscription barrier: %+v",ack)}
 publisherCtx,stopPublisher:=context.WithCancel(ctx)
 published:=make(chan error,1)
 go func(){published<-application.RunEventPublisher(publisherCtx)}()
 defer func(){stopPublisher();select{case err:=<-published:if err!=nil&&!errors.Is(err,context.Canceled){t.Errorf("publisher: %v",err)};case <-time.After(3*time.Second):t.Error("publisher did not stop")}}()
 create("visible-live-row")
 for { frame:=read();if frame.Type!="next"{t.Fatalf("live result: %+v",frame)};if strings.Contains(string(frame.Payload),"visible-live-row"){break} }
 deadline,_:=sessionCtx.Deadline()
 if time.Until(deadline)<2*time.Second{t.Fatal("session setup consumed the deadline readiness margin")}
 revokedAt:=time.Now()
 closureBound:=revokedAt.Add(revalidate+time.Second)
 if expire {
  closureBound=deadline.Add(time.Second)
 } else {
  app.RevokeSession()
  stopSession()
  create("private-after-revocation")
 }
 connection.SetReadDeadline(closureBound)
 for {
  var frame sessionFrame
  err:=connection.ReadJSON(&frame)
  if err!=nil {
   var closed *websocket.CloseError
   if !errors.As(err,&closed)||closed.Code!=4401{t.Fatalf("revoked connection must close 4401: %v",err)}
   break
  }
  if strings.Contains(string(frame.Payload),"private-after-revocation"){t.Fatalf("revoked row disclosed: %+v",frame)}
 }
 if time.Now().After(closureBound){t.Fatal("Golem socket exceeded session closure bound")}
 if expire&&time.Now().Before(deadline){t.Fatal("Golem closed before session deadline")}
 var pondResult pondClosure
 select{case pondResult=<-pondClosed:case <-time.After(time.Until(closureBound)):t.Fatal("Pond exceeded session closure bound")}
 var pondClose *websocket.CloseError
 if !errors.As(pondResult.err,&pondClose){t.Fatalf("Pond must close, not time out: %v",pondResult.err)}
 if expire&&pondResult.at.Before(deadline){t.Fatal("Pond closed before session deadline")}
 if pondResult.at.After(closureBound){t.Fatal("Pond exceeded session closure bound")}
 rejected,handshake,err:=pondDialer.DialContext(ctx,"ws"+strings.TrimPrefix(host.URL,"http")+"/pond",pondHeaders)
 if rejected!=nil{rejected.Close()}
 if handshake!=nil{defer handshake.Body.Close()}
 if err==nil||handshake==nil||handshake.StatusCode!=http.StatusUnauthorized{t.Fatalf("invalid-session Pond reconnect: %v",err)}
 if response:=post("{ users { name } }");response["errors"]==nil{t.Fatalf("revoked HTTP session admitted: %#v",response)}
 reconnect:=dial();reconnect.SetReadDeadline(time.Now().Add(3*time.Second))
 var frame sessionFrame
 err=reconnect.ReadJSON(&frame)
 var closed *websocket.CloseError
 if !errors.As(err,&closed)||closed.Code!=4403{t.Fatalf("revoked reconnect admitted: frame=%+v err=%v",frame,err)}
}
`
