package model

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestOptimisticConcurrencyModelAuthoredBatchAndUnsafeNestedSurfacesAreAbsent(t *testing.T) {
	compilation := socialCompilation()
	root := t.TempDir()
	var post *ir.ModelDeclIR
	for index := range compilation.Model.Models {
		if compilation.Model.Models[index].LogicalName == "Post" {
			post = &compilation.Model.Models[index]
			break
		}
	}
	if post == nil {
		t.Fatal("post fixture missing")
	}
	var user *ir.ModelDeclIR
	for index := range compilation.Model.Models {
		if compilation.Model.Models[index].LogicalName == "User" {
			user = &compilation.Model.Models[index]
			break
		}
	}
	if user == nil {
		t.Fatal("user fixture missing")
	}
	postVersion := scalarField(id(90), "Version", 20, ir.TypeInt64)
	userVersion := scalarField(id(91), "Version", 20, ir.TypeInt64)
	post.Fields = append(post.Fields, postVersion)
	post.OptimisticConcurrency = pointer(postVersion.ID)
	user.Fields = append(user.Fields, userVersion)
	user.OptimisticConcurrency = pointer(userVersion.ID)
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		switch contract.ModelID {
		case post.ID:
			contract.OptimisticConcurrency = pointer(postVersion.ID)
			contract.Fields = append(contract.Fields, ir.FieldContractIR{FieldID: postVersion.ID, Modes: []ir.FieldMode{ir.ModeVisible}})
		case user.ID:
			contract.OptimisticConcurrency = pointer(userVersion.ID)
			contract.Fields = append(contract.Fields, ir.FieldContractIR{FieldID: userVersion.ID, Modes: []ir.FieldMode{ir.ModeVisible}})
		}
	}

	result, err := Emit(Request{
		Compilation: compilation,
		Packages:    []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social", Directory: root}},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := string(result.Files[0].Source)
	for _, required := range []string{
		"type PostCreateInput = golem.CreateInput[Post]",
		"type PostUpdateInput = golem.UpdateInput[Post]",
		"golemGeneratedPostVersionAnalyticsField",
		"func (golemGeneratedPostFields) Create(values ...golem.CreateValue[Post]) PostCreateInput",
		"func (golemGeneratedPostFields) Update(first golem.UpdateValue[Post], rest ...golem.UpdateValue[Post]) PostUpdateInput",
		"func (golemGeneratedUserPostsMutationRelation) Create(first golem.CreateInput[Post], rest ...golem.CreateInput[Post]) golem.NestedCreateValue[User]",
		"func (golemGeneratedUserPostsMutationRelation) CreateMany(first golem.CreateInput[Post], rest ...golem.CreateInput[Post]) golem.NestedCreateValue[User]",
		"func (golemGeneratedPostAuthorMutationRelation) Connect(target golem.MutationTarget[User]) golem.NestedCreateValue[Post]",
		"func (golemGeneratedPostAuthorMutationRelation) ConnectOrCreate(target golem.MutationTarget[User], create golem.CreateInput[User]) golem.NestedCreateValue[Post]",
		"func (golemGeneratedUserManagerMutationRelation) Connect(target golem.MutationTarget[User]) golem.NestedCreateValue[User]",
		"func (golemGeneratedUserManagerMutationRelation) ConnectOrCreate(target golem.MutationTarget[User], create golem.CreateInput[User]) golem.NestedCreateValue[User]",
		"func (golemGeneratedUserReportsMutationRelation) Create(first golem.CreateInput[User], rest ...golem.CreateInput[User]) golem.NestedCreateValue[User]",
		"func (golemGeneratedUserReportsMutationRelation) CreateMany(first golem.CreateInput[User], rest ...golem.CreateInput[User]) golem.NestedCreateValue[User]",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("versioned model source missing %q:\n%s", required, source)
		}
	}
	for _, forbidden := range []string{
		"type PostUpdateManyInput",
		"type PostUpdateManyHookRequest",
		"type PostUpdateManyHookResult",
		"type PostDeleteManyHookRequest",
		"type PostDeleteManyHookResult",
		"func (golemGeneratedPostFields) UpdateMany(",
		"func (field golemGeneratedPostVersionMutationField) Create(",
		"func (field golemGeneratedPostVersionMutationField) Set(",
		"func (field golemGeneratedPostVersionMutationField) Increment(",
		"func (field golemGeneratedPostVersionMutationField) Decrement(",
		"func (golemGeneratedUserPostsMutationRelation) Connect(",
		"func (golemGeneratedUserPostsMutationRelation) ConnectOrCreate(",
		"func (golemGeneratedUserPostsMutationRelation) Disconnect(",
		"func (golemGeneratedUserPostsMutationRelation) Set(",
		"func (golemGeneratedUserPostsMutationRelation) Update(",
		"func (golemGeneratedUserPostsMutationRelation) UpdateMany(",
		"func (golemGeneratedUserPostsMutationRelation) Upsert(",
		"func (golemGeneratedUserPostsMutationRelation) Delete(",
		"func (golemGeneratedUserPostsMutationRelation) DeleteMany(",
		"func (golemGeneratedUserReportsMutationRelation) Connect(",
		"func (golemGeneratedUserReportsMutationRelation) ConnectOrCreate(",
		"func (golemGeneratedUserReportsMutationRelation) Disconnect(",
		"func (golemGeneratedUserReportsMutationRelation) Set(",
		"func (golemGeneratedUserReportsMutationRelation) Update(",
		"func (golemGeneratedUserReportsMutationRelation) UpdateMany(",
		"func (golemGeneratedUserReportsMutationRelation) Upsert(",
		"func (golemGeneratedUserReportsMutationRelation) Delete(",
		"func (golemGeneratedUserReportsMutationRelation) DeleteMany(",
		"func (golemGeneratedPostAuthorMutationRelation) Update(",
		"func (golemGeneratedPostAuthorMutationRelation) Upsert(",
		"func (golemGeneratedPostAuthorMutationRelation) Delete(",
		"func (golemGeneratedUserManagerMutationRelation) Update(",
		"func (golemGeneratedUserManagerMutationRelation) Upsert(",
		"func (golemGeneratedUserManagerMutationRelation) Delete(",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("versioned model source contains forbidden capability %q:\n%s", forbidden, source)
		}
	}

	compileGenerated(t, root, map[string]string{
		"models.go": "package social\n\ntype User struct{}\ntype Post struct{}\n",
		"usage.go": `package social

import golem "github.com/eleven-am/golem/go/golem"

var legalUserTarget = Users.ByID.Value(golem.NewUUID([16]byte{1}))
var legalVersionedCreate PostCreateInput = Posts.Create(Posts.ID.Create("post"), Posts.Author.Connect(legalUserTarget))
var legalVersionedUpdate PostUpdateInput = Posts.Update(Posts.ID.Set("post-next"))
var legalVersionRead golem.Projection[Post] = Posts.Select(Posts.Version)
var legalNestedVersionedCreate UserCreateInput = Users.Create(Users.Posts.Create(legalVersionedCreate))
`,
	}, result.Files)

	for _, test := range []struct {
		name, usage, want string
	}{
		{"create token", "var _ = Posts.Version.Create(int64(1))\n", "Posts.Version.Create undefined"},
		{"update token", "var _ = Posts.Version.Set(int64(2))\n", "Posts.Version.Set undefined"},
		{"root update many", "var _ = Posts.UpdateMany(Posts.ID.Set(\"post\"))\n", "Posts.UpdateMany undefined"},
		{"versioned root relation value is create only", "var _ = Posts.Update(Posts.Author.Connect(Users.ByID.Value(golem.NewUUID([16]byte{1}))))\n", "does not implement golem.UpdateValue[Post]"},
		{"versioned self source relation value is create only", "var _ = Users.Update(Users.Manager.Connect(Users.ByID.Value(golem.NewUUID([16]byte{1}))))\n", "does not implement golem.UpdateValue[User]"},
		{"nested connect without owner expectation", "var _ = Users.Posts.Connect(Posts.ByID.Value(\"post\"))\n", "Users.Posts.Connect undefined"},
		{"nested update without target expectation", "var _ = Users.Posts.Update(Posts.ByID.Value(\"post\"), Posts.Update(Posts.ID.Set(\"next\")))\n", "Users.Posts.Update undefined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			compileGeneratedFailure(t, result.Files, test.usage, test.want)
		})
	}

	// Keep the two independent nested hazards observable: a non-versioned
	// parent still cannot mutate an existing versioned target, while a
	// versioned root cannot reuse relation values in Update even when its target
	// is non-versioned.
	mixed := socialCompilation()
	var mixedPost *ir.ModelDeclIR
	for index := range mixed.Model.Models {
		if mixed.Model.Models[index].LogicalName == "Post" {
			mixedPost = &mixed.Model.Models[index]
			break
		}
	}
	if mixedPost == nil {
		t.Fatal("mixed post fixture missing")
	}
	mixedVersion := scalarField(id(92), "Version", 20, ir.TypeInt64)
	mixedPost.Fields = append(mixedPost.Fields, mixedVersion)
	mixedPost.OptimisticConcurrency = pointer(mixedVersion.ID)
	for index := range mixed.Contract.Models {
		if mixed.Contract.Models[index].ModelID == mixedPost.ID {
			mixed.Contract.Models[index].OptimisticConcurrency = pointer(mixedVersion.ID)
			mixed.Contract.Models[index].Fields = append(mixed.Contract.Models[index].Fields, ir.FieldContractIR{FieldID: mixedVersion.ID, Modes: []ir.FieldMode{ir.ModeVisible}})
		}
	}
	mixedResult, err := Emit(Request{
		Compilation: mixed,
		Packages:    []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social", Directory: t.TempDir()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mixedSource := string(mixedResult.Files[0].Source)
	for _, forbidden := range []string{
		"func (golemGeneratedUserPostsMutationRelation) Update(",
		"func (golemGeneratedUserPostsMutationRelation) UpdateMany(",
		"func (golemGeneratedPostAuthorMutationRelation) Update(",
		"func (golemGeneratedPostAuthorMutationRelation) Upsert(",
		"func (golemGeneratedPostAuthorMutationRelation) Delete(",
	} {
		if strings.Contains(mixedSource, forbidden) {
			t.Errorf("mixed versioned model source contains forbidden capability %q:\n%s", forbidden, mixedSource)
		}
	}
	if !strings.Contains(mixedSource, "func (golemGeneratedPostAuthorMutationRelation) Connect(target golem.MutationTarget[User]) golem.NestedCreateValue[Post]") {
		t.Fatalf("mixed versioned root did not retain create-only connect:\n%s", mixedSource)
	}
	compileGeneratedFailure(t, mixedResult.Files, "var _ = Users.Posts.Update(Posts.ByID.Value(\"post\"), Posts.Update(Posts.ID.Set(\"next\")))\n", "Users.Posts.Update undefined")
	compileGeneratedFailure(t, mixedResult.Files, "var _ = Posts.Update(Posts.Author.Connect(Users.ByID.Value(golem.NewUUID([16]byte{1}))))\n", "does not implement golem.UpdateValue[Post]")
}

func TestNonVersionedModelEmissionBytesRemainFrozenAcrossConcurrencyBranch(t *testing.T) {
	result, err := Emit(Request{
		Compilation: socialCompilation(),
		Packages:    []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files=%d; want 1", len(result.Files))
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(result.Files[0].Source))
	const want = "b83d676a8eb61633f4eb99f314609664fe0257d7d655d3215264c3abefee0cc7"
	if digest != want {
		t.Fatalf("non-versioned model source digest=%s; want %s", digest, want)
	}
}
