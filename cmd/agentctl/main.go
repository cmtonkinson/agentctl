// Command agentctl manages agent assets from ~/.agents/.
package main

import (
	"os"
	"runtime/debug"
)

// version may be set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	info, _ := debug.ReadBuildInfo()
	os.Exit(execute(buildVersion(version, info), os.Args[1:], os.Stdout, os.Stderr))
}

// buildVersion uses Go's embedded VCS metadata when no version was injected.
func buildVersion(explicit string, info *debug.BuildInfo) string {
	if explicit != "" && explicit != "dev" {
		return explicit
	}
	if info == nil {
		return "dev"
	}
	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision != "" {
		if len(revision) > 7 {
			revision = revision[:7]
		}
		if modified {
			revision += "-dirty"
		}
		return revision
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
