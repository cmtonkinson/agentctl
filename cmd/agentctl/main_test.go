package main

import (
	"runtime/debug"
	"testing"
)

// TestBuildVersion checks direct builds, injected releases, and no-VCS builds.
func TestBuildVersion(t *testing.T) {
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "8e023ea96cdd"},
		{Key: "vcs.modified", Value: "true"},
	}}
	if got := buildVersion("dev", info); got != "8e023ea-dirty" {
		t.Fatalf("direct build version: %s", got)
	}
	if got := buildVersion("v1.2.3", info); got != "v1.2.3" {
		t.Fatalf("injected version: %s", got)
	}
	if got := buildVersion("dev", nil); got != "dev" {
		t.Fatalf("no-VCS fallback: %s", got)
	}
}
