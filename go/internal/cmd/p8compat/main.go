package main

import (
	"context"
	"io"
	"os"

	"github.com/eleven-am/golem/go/internal/completion"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout))
}

func run(ctx context.Context, arguments []string, output io.Writer) int {
	return completion.Execute(ctx, "p8compat", arguments, output)
}
