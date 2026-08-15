package event

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	testPostModel      ir.ModelID = "01010101010101010101010101010101"
	testPostID         ir.FieldID = "02020202020202020202020202020202"
	testPostKey        ir.KeyID   = "03030303030303030303030303030303"
	testFriendModel    ir.ModelID = "11111111111111111111111111111111"
	testFriendUserID   ir.FieldID = "12121212121212121212121212121212"
	testFriendFriendID ir.FieldID = "13131313131313131313131313131313"
	testFriendKey      ir.KeyID   = "14141414141414141414141414141414"
)

func TestGeneratedArtifactsByteIdenticalAcrossShuffleAndRepeat(t *testing.T) {
	request := eventRequest(t, "example.test/models", t.TempDir())
	first, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Compilation.Model.Models[0], request.Compilation.Model.Models[1] = request.Compilation.Model.Models[1], request.Compilation.Model.Models[0]
	request.Compilation.Contract.Models[0], request.Compilation.Contract.Models[1] = request.Compilation.Contract.Models[1], request.Compilation.Contract.Models[0]
	second, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || !bytes.Equal(first[0].Source, second[0].Source) {
		t.Fatalf("shuffle/repeat changed generated event source\n%s\n%s", sourceAt(first), sourceAt(second))
	}
	source := string(first[0].Source)
	for _, fragment := range []string{
		"type PostEventIdentity = golem.UUID", "type FriendshipEventIdentity struct", "func (identity FriendshipEventIdentity) UserID() golem.UUID",
		"type PostEvent struct", "func (event PostEvent) Metadata() golem.EventMetadata", "func (event PostEvent) Entity() (golem.Row[Post], bool)",
		"type golemGeneratedPostEventFactory struct{}", "Build(input golemruntime.ValidatedEvent)", "GolemGeneratedEventModels", "GolemGeneratedEventFactories",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("generated source lacks %q:\n%s", fragment, source)
		}
	}
}

func TestFreshP7EventModuleCompilesAndFactoriesBuildValidatedScalarAndCompositeValues(t *testing.T) {
	moduleRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	modelsDir := filepath.Join(root, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	request := eventRequest(t, "example.test/p7events/models", modelsDir)
	files, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/p7events\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => "+moduleRoot+"\n")
	writeFile(t, filepath.Join(modelsDir, "models.go"), generatedSupportSource())
	writeFile(t, files[0].Path, string(files[0].Source))
	runGo(t, root, "test", "-mod=mod", "./...")
	testGeneratedFactoryConstructionInsideModule(t, moduleRoot)

	invalidDir := filepath.Join(root, "invalid")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(invalidDir, "invalid.go"), `package invalid
import (
  "github.com/eleven-am/golem/go/golem"
  "github.com/eleven-am/golem/go/runtime"
)
var _ = runtime.ValidatedEvent{metadata: golem.EventMetadata{}, identity: []any{golem.UUID{1}}}
`)
	output, err := runGoFailure(root, "test", "-mod=mod", "./invalid")
	if err == nil || !strings.Contains(output, "cannot refer to unexported field") {
		t.Fatalf("external raw validated-event construction was not rejected:\n%s", output)
	}
}

func testGeneratedFactoryConstructionInsideModule(t *testing.T, moduleRoot string) {
	t.Helper()
	directory, err := os.MkdirTemp(".", "p7eventtmp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	base := filepath.Base(directory)
	importPath := "github.com/eleven-am/golem/go/internal/codegen/event/" + base
	request := eventRequest(t, importPath, directory)
	files, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, "models.go"), generatedSupportSource())
	writeFile(t, files[0].Path, string(files[0].Source))
	writeFile(t, filepath.Join(directory, "events_test.go"), generatedFactoryTestSource())
	runGo(t, moduleRoot, "test", "./internal/codegen/event/"+base)
}

func eventRequest(t *testing.T, importPath, directory string) Request {
	t.Helper()
	field := func(id ir.FieldID, name string) ir.FieldIR {
		return ir.FieldIR{ID: id, GoName: name, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}}
	}
	post := ir.ModelDeclIR{ID: testPostModel, Go: ir.GoNamedTypeIR{PackagePath: importPath, Name: "Post"}, LogicalName: "Post", Fields: []ir.FieldIR{field(testPostID, "ID")}, PrimaryKey: &ir.KeyIR{ID: testPostKey, Kind: ir.KeyPrimary, Fields: []ir.FieldID{testPostID}}}
	friend := ir.ModelDeclIR{ID: testFriendModel, Go: ir.GoNamedTypeIR{PackagePath: importPath, Name: "Friendship"}, LogicalName: "Friendship", Fields: []ir.FieldIR{field(testFriendUserID, "UserID"), field(testFriendFriendID, "FriendID")}, PrimaryKey: &ir.KeyIR{ID: testFriendKey, Kind: ir.KeyPrimary, Fields: []ir.FieldID{testFriendUserID, testFriendFriendID}}}
	contract := func(model ir.ModelDeclIR, payload, identity string) ir.ModelContractIR {
		identityFields := make([]ir.EventFieldSchemaIR, len(model.PrimaryKey.Fields))
		fields := make([]ir.FieldContractIR, len(model.Fields))
		for index, id := range model.PrimaryKey.Fields {
			identityFields[index] = ir.EventFieldSchemaIR{FieldID: id, Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}
			fields[index] = ir.FieldContractIR{FieldID: id, GraphQLName: model.Fields[index].GoName, Modes: []ir.FieldMode{ir.ModeVisible}}
		}
		fingerprint := ir.Fingerprint(strings.Repeat("4", 64))
		return ir.ModelContractIR{ModelID: model.ID, GraphQLName: model.Go.Name, GraphQLPlural: model.Go.Name + "s", Fields: fields, Exposed: true, Subscriptions: true, Event: &ir.EventContractIR{PayloadTypeName: payload, IdentityTypeName: identity, DeleteSnapshotFull: true, SchemaFingerprint: fingerprint, Schema: ir.EventSchemaShapeIR{FormatVersion: ir.EventSchemaFormatVersion, ModelID: model.ID, PrimaryKeyID: model.PrimaryKey.ID, IdentityFields: identityFields, SnapshotFields: identityFields, Enums: []ir.EventEnumSchemaIR{}}}}
	}
	return Request{
		Compilation: ir.CompilationIR{Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{post, friend}}, Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{contract(post, "PostEvent", ""), contract(friend, "FriendshipEvent", "FriendshipEventIdentity")}}},
		Packages:    []modelcodegen.PackageSpec{{ImportPath: importPath, PackageName: "models", Directory: directory}},
		FinalStamp:  modelcodegen.FinalStamp{GenerationDigest: strings.Repeat("1", 64), GeneratorVersion: "p7-test", TemplateABIVersion: "p7-event-abi-v1"},
	}
}

func generatedSupportSource() string {
	return `package models
import "github.com/eleven-am/golem/go/golem"
type Post struct{}
type Friendship struct{}
var GolemGeneratedPostDescriptor = golem.GeneratedModelDescriptor[Post](golem.ModelID{0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01}, golem.GeneratedDescriptorShape(nil,nil,nil,nil))
var GolemGeneratedFriendshipDescriptor = golem.GeneratedModelDescriptor[Friendship](golem.ModelID{0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11}, golem.GeneratedDescriptorShape(nil,nil,nil,nil))
`
}

func generatedFactoryTestSource() string {
	return `package models
import (
  "testing"
  "time"
  "github.com/eleven-am/golem/go/golem"
  typedvalue "github.com/eleven-am/golem/go/internal/event/typedvalue"
  golemruntime "github.com/eleven-am/golem/go/runtime"
)
func TestGeneratedFactories(t *testing.T) {
  registry, err := golemruntime.GeneratedEventFactoryRegistry(golem.SchemaDigest{0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11}, GolemGeneratedEventFactories())
  if err != nil { t.Fatal(err) }
  postModel := golem.ModelID{0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01,0x01}
  schema := golem.EventSchemaDigest{0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44,0x44}
  runtimeRow, err := golem.RuntimeModelReadRow(postModel)
  if err != nil { t.Fatal(err) }
  input, err := typedvalue.New(typedvalue.Metadata{EventID:golem.EventID{1}, Action:golem.EventCreated, CausationID:golem.CausationID{2}, Ordinal:1, RecordedAt:time.Unix(1,123456789), Generation:golem.SchemaDigest{9}, EventSchema:schema, HasEventSchema:true, ResolvedEventSchema:schema, ModelID:postModel}, []any{golem.UUID{3}}, &runtimeRow)
  if err != nil { t.Fatal(err) }
  built, err := golemruntime.RuntimeBuildValidatedEvent(registry, input)
  event, ok := built.(PostEvent)
  if err != nil || !ok || event.ID() != (golem.UUID{3}) { t.Fatalf("event=%#v ok=%v err=%v", built, ok, err) }
  if _, present := event.Entity(); !present { t.Fatal("validated entity was not retained") }
  if _, err := typedvalue.New(typedvalue.Metadata{EventID:golem.EventID{1}, Action:golem.EventCreated, CausationID:golem.CausationID{2}, Ordinal:1, RecordedAt:time.Unix(1,0), Generation:golem.SchemaDigest{9}, EventSchema:golem.EventSchemaDigest{5}, HasEventSchema:true, ResolvedEventSchema:schema, ModelID:postModel}, []any{golem.UUID{3}}, nil); err == nil { t.Fatal("different wire and resolved event schemas were accepted") }
  incompatible, _ := typedvalue.New(typedvalue.Metadata{EventID:golem.EventID{1}, Action:golem.EventCreated, CausationID:golem.CausationID{2}, Ordinal:1, RecordedAt:time.Unix(1,0), Generation:golem.SchemaDigest{9}, EventSchema:golem.EventSchemaDigest{5}, HasEventSchema:true, ResolvedEventSchema:golem.EventSchemaDigest{5}, ModelID:postModel}, []any{golem.UUID{3}}, nil)
  if _, err := golemruntime.RuntimeBuildValidatedEvent(registry, incompatible); err == nil { t.Fatal("incompatible resolved event schema was accepted") }
  historicalV1, err := typedvalue.New(typedvalue.Metadata{EventID:golem.EventID{8}, Action:golem.EventCreated, CausationID:golem.CausationID{9}, Ordinal:1, RecordedAt:time.Unix(3,0), Generation:golem.SchemaDigest{7}, ResolvedEventSchema:schema, ModelID:postModel}, []any{golem.UUID{10}}, nil)
  if err != nil { t.Fatal(err) }
  built, err = golemruntime.RuntimeBuildValidatedEvent(registry, historicalV1)
  historical, ok := built.(PostEvent)
  if err != nil || !ok || historical.ID() != (golem.UUID{10}) { t.Fatalf("historical=%#v ok=%v err=%v", built, ok, err) }
  friendModel := golem.ModelID{0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11,0x11}
  composite, _ := typedvalue.New(typedvalue.Metadata{EventID:golem.EventID{4}, Action:golem.EventCreated, CausationID:golem.CausationID{5}, Ordinal:1, RecordedAt:time.Unix(2,0), Generation:golem.SchemaDigest{8}, EventSchema:schema, HasEventSchema:true, ResolvedEventSchema:schema, ModelID:friendModel}, []any{golem.UUID{6},golem.UUID{7}}, nil)
  built, err = golemruntime.RuntimeBuildValidatedEvent(registry, composite)
  friendship, ok := built.(FriendshipEvent)
  if err != nil || !ok || friendship.ID().UserID() != (golem.UUID{6}) || friendship.ID().FriendID() != (golem.UUID{7}) { t.Fatalf("composite=%#v ok=%v err=%v", built, ok, err) }
}
`
}

func sourceAt(files []File) []byte {
	if len(files) == 0 {
		return nil
	}
	return files[0].Source
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGo(t *testing.T, directory string, args ...string) {
	t.Helper()
	output, err := runGoFailure(directory, args...)
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func runGoFailure(directory string, args ...string) (string, error) {
	command := exec.Command("go", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	return string(output), err
}
