package schema

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestExtractSocialRawDeclarations(t *testing.T) {
	result := Extract(context.Background(), Config{Dir: "testdata/social", Pattern: "."})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("Extract diagnostics: %#v", result.Diagnostics)
	}
	if result.Raw.FormatVersion != ir.RawDeclFormatVersion {
		t.Fatalf("FormatVersion = %d", result.Raw.FormatVersion)
	}
	root := result.Raw.Root
	if root.FunctionName != "DefineSchema" || root.ParameterName != "schema" || root.SchemaName != "social" {
		t.Fatalf("unexpected root: %#v", root)
	}
	if root.Actor == nil || root.Actor.GoName != "Actor" {
		t.Fatalf("actor = %#v", root.Actor)
	}
	if got := []ir.Provider{root.Providers[0].Provider, root.Providers[1].Provider}; !reflect.DeepEqual(got, []ir.Provider{ir.SQLite, ir.PostgreSQL}) {
		t.Fatalf("providers = %#v", got)
	}
	if len(root.Models) != 2 || root.Models[0].GoName != "User" || root.Models[1].GoName != "Post" {
		t.Fatalf("model registrations = %#v", root.Models)
	}
	if len(result.Raw.Models) != 2 || result.Raw.Models[0].GoName != "Post" || result.Raw.Models[1].GoName != "User" {
		t.Fatalf("normalized declaration order = %#v", result.Raw.Models)
	}
	post := result.Raw.Models[0]
	if value(post.Marker, "table") != "posts" || len(post.Directives) != 1 || post.Directives[0].Name != "idx_posts_author" {
		t.Fatalf("post marker/directive = %#v / %#v", post.Marker, post.Directives)
	}
	if len(post.Fields) != 5 || post.Fields[3].GoName != "CreatedAt" || post.Fields[3].TypeSyntax != "time.Time" {
		t.Fatalf("post fields = %#v", post.Fields)
	}
	if post.Fields[0].GoType.PackagePath != "github.com/eleven-am/golem/go/golem" || post.Fields[0].GoType.GoName != "UUID" {
		t.Fatalf("aliased UUID type was not canonicalized: %#v", post.Fields[0].GoType)
	}
	if len(result.Raw.Methods) != 1 || result.Raw.Methods[0].Name != "GolemModel" || !strings.Contains(result.Raw.Methods[0].BodySyntax, "DefineModel") {
		t.Fatalf("methods = %#v", result.Raw.Methods)
	}
	for _, model := range result.Raw.Models {
		if strings.HasPrefix(model.Span.RelativeFile, "/") || model.Span.ModulePath == "" {
			t.Fatalf("non-portable source span: %#v", model.Span)
		}
	}
}

func TestExtractRejectsOpenRootAndBadTagDeterministically(t *testing.T) {
	result := Extract(context.Background(), Config{Dir: "testdata/invalid", Pattern: "."})
	if len(result.Diagnostics) < 2 {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	codes := make([]string, len(result.Diagnostics))
	for i, diagnostic := range result.Diagnostics {
		codes[i] = diagnostic.Code
		if strings.HasPrefix(diagnostic.Span.RelativeFile, "/") {
			t.Fatalf("absolute diagnostic path: %#v", diagnostic)
		}
	}
	if !contains(codes, "P1_SCHEMA_BODY_CALL") || !contains(codes, "P1_GOLEM_TAG_UNKNOWN") {
		t.Fatalf("diagnostic codes = %v", codes)
	}
	for i := 1; i < len(result.Diagnostics); i++ {
		left, right := result.Diagnostics[i-1], result.Diagnostics[i]
		if left.Span.RelativeFile > right.Span.RelativeFile || left.Span.RelativeFile == right.Span.RelativeFile && left.Span.StartLine > right.Span.StartLine {
			t.Fatalf("diagnostics are not sorted: %#v", result.Diagnostics)
		}
	}
}

func value(attrs []ir.RawAttribute, name string) string {
	for _, attr := range attrs {
		if attr.Name == name && attr.RawValue != nil {
			return *attr.RawValue
		}
	}
	return ""
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
