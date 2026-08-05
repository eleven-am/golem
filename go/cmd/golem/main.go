package main

import (
	"context"
	"io"
	"os"
)

func main() {
	os.Exit(run(context.Background(), ".", os.Args[1:], os.Stdout, os.Stderr))
}

func writeError(writer io.Writer, err error) {
	_, _ = io.WriteString(writer, err.Error()+"\n")
}
