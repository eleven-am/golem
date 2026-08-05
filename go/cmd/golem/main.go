package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	compiler "github.com/eleven-am/golem/go/internal/compiler/compile"
)

type inspectOutput struct {
	Model               any    `json:"model"`
	Contract            any    `json:"contract"`
	ModelFingerprint    string `json:"modelFingerprint"`
	ContractFingerprint string `json:"contractFingerprint"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "inspect" {
		fmt.Fprintln(os.Stderr, "usage: golem inspect --schema <pattern> [--root <name>]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	schemaPattern := flags.String("schema", "", "Go package pattern containing the schema root")
	root := flags.String("root", "DefineSchema", "schema root function name")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	if *schemaPattern == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: golem inspect --schema <pattern> [--root <name>]")
		os.Exit(2)
	}

	result := compiler.Compile(context.Background(), compiler.Config{Dir: ".", Pattern: *schemaPattern, Root: *root})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if len(result.Diagnostics) != 0 {
		if err := encoder.Encode(struct {
			Diagnostics any `json:"diagnostics"`
		}{Diagnostics: result.Diagnostics}); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
	output := inspectOutput{
		Model: result.Compilation.Model, Contract: result.Compilation.Contract,
		ModelFingerprint: string(result.ModelFingerprint), ContractFingerprint: string(result.ContractFingerprint),
	}
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
