package main

import (
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
)

func TestEveryCommandRequestSharesOneGQLGenCacheAcrossItsBuilds(t *testing.T) {
	first := pipelineRequest(t.TempDir(), commonOptions{schemaPattern: ".", root: "DefineSchema"}, modelcodegen.PackageSpec{}, nil)
	second := pipelineRequest(t.TempDir(), commonOptions{schemaPattern: ".", root: "DefineSchema"}, modelcodegen.PackageSpec{}, nil)
	if first.GraphQLExecutables == nil || second.GraphQLExecutables == nil {
		t.Fatal("command pipeline request carries no gqlgen executable cache")
	}
	if first.GraphQLExecutables == second.GraphQLExecutables {
		t.Fatal("separate commands share one gqlgen executable cache")
	}
}
