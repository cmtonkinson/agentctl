// Command agentctl manages agent assets from ~/.agents/.
package main

import (
	"os"

	"github.com/cmtonkinson/agentctl/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version, os.Args[1:], os.Stdout, os.Stderr))
}
