package core

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// State is machine-local bookkeeping in the XDG state directory: what
// agentctl deployed where, and which manual uploads were acknowledged.
type State struct {
	Version     int           `json:"version"`
	Deployments []*Deployment `json:"deployments"`
	Acks        []*Ack        `json:"acknowledgements"`
}

// Deployment records one thing agentctl put in place.
type Deployment struct {
	Target     string `json:"target"`
	Project    string `json:"project,omitempty"`
	Asset      string `json:"asset"`
	Method     string `json:"method"`
	Dest       string `json:"dest,omitempty"`
	Key        string `json:"key,omitempty"`
	SourceHash string `json:"source_hash,omitempty"`
	DestHash   string `json:"dest_hash,omitempty"`
	// Value is the config entry agentctl wrote or asked for (with ${VAR}
	// placeholders); a client entry matching it is agentctl's.
	Value  json.RawMessage `json:"value,omitempty"`
	Manual bool            `json:"manual,omitempty"`
	At     time.Time       `json:"at"`
}

// Ack records a manual step the user confirmed.
type Ack struct {
	Target  string    `json:"target"`
	Project string    `json:"project,omitempty"`
	Asset   string    `json:"asset"`
	Hash    string    `json:"hash"`
	At      time.Time `json:"at"`
}

// LoadState reads the state file; a missing file yields empty state.
func LoadState(p string) (*State, error) {
	s := &State{Version: 1}
	b, err := os.ReadFile(p)
	if fsx.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Save writes the state file.
func (s *State) Save(p string) error {
	sort.SliceStable(s.Deployments, func(i, j int) bool {
		a, b := s.Deployments[i], s.Deployments[j]
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Asset < b.Asset
	})
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(p, append(b, '\n'), 0o644)
}

// Find returns the deployment for an exact (target, project, asset, dest, key).
func (s *State) Find(target, project, asset, dest, key string) *Deployment {
	for _, d := range s.Deployments {
		if d.Target == target && d.Project == project && d.Asset == asset && d.Dest == dest && d.Key == key {
			return d
		}
	}
	return nil
}

// ForDest returns every deployment that owns dest (and key).
func (s *State) ForDest(dest, key string) []*Deployment {
	var out []*Deployment
	for _, d := range s.Deployments {
		if d.Dest == dest && d.Key == key {
			out = append(out, d)
		}
	}
	return out
}

// ForAsset returns deployments of asset, optionally limited to a target.
func (s *State) ForAsset(asset, target, project string) []*Deployment {
	var out []*Deployment
	for _, d := range s.Deployments {
		if d.Asset == asset && (target == "" || d.Target == target) && (target == "" || d.Project == project) {
			out = append(out, d)
		}
	}
	return out
}

// Upsert inserts or replaces a deployment.
func (s *State) Upsert(d *Deployment) {
	if old := s.Find(d.Target, d.Project, d.Asset, d.Dest, d.Key); old != nil {
		*old = *d
		return
	}
	s.Deployments = append(s.Deployments, d)
}

// Drop removes a deployment record.
func (s *State) Drop(d *Deployment) {
	for i, x := range s.Deployments {
		if x == d {
			s.Deployments = append(s.Deployments[:i], s.Deployments[i+1:]...)
			return
		}
	}
}

// LatestAck returns the most recent acknowledgement.
func (s *State) LatestAck(target, project, asset string) *Ack {
	var best *Ack
	for _, a := range s.Acks {
		if a.Target == target && a.Project == project && a.Asset == asset && (best == nil || !a.At.Before(best.At)) {
			best = a
		}
	}
	return best
}

// AddAck records an acknowledgement, replacing older ones for the asset.
func (s *State) AddAck(a *Ack) {
	s.DropAcks(a.Target, a.Project, a.Asset)
	s.Acks = append(s.Acks, a)
}

// DropAcks forgets acknowledgements for an asset on a target.
func (s *State) DropAcks(target, project, asset string) {
	keep := s.Acks[:0]
	for _, a := range s.Acks {
		if !(a.Target == target && a.Project == project && a.Asset == asset) {
			keep = append(keep, a)
		}
	}
	s.Acks = keep
}
