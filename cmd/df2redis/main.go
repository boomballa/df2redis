package main

import (
	"os"

	"df2redis/internal/cli"
)

func main() {
	code := cli.Execute(os.Args[1:])
	os.Exit(code)
}
