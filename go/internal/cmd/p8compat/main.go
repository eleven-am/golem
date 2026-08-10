package main

import (
	"context"
	"os"

	"github.com/eleven-am/golem/go/internal/completion"
)

func main() {
	os.Exit(completion.Execute(context.Background(), "p8compat", os.Args[1:], os.Stdout))
}
