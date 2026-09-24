package core

import "github.com/cmtonkinson/agentctl/internal/fsx"

func hashMatches(full, given string) bool { return fsx.MatchHash(full, given) }

func shortHash(h string) string { return fsx.Short(h) }

// ShortHash abbreviates a content hash for display.
func ShortHash(h string) string { return fsx.Short(h) }
