package state

import (
	"errors"
	"testing"
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
)

func TestSnapshotIsImmutable(t *testing.T) {
	repos := []RepoState{
		{Owner: "ericdahl-dev", Name: "git-green", Stoplight: aggregator.StoplightGreen},
	}
	snap := New(repos)
	repos[0].Stoplight = aggregator.StoplightRed

	if snap.Repos[0].Stoplight != aggregator.StoplightGreen {
		t.Error("snapshot was mutated by external change to source slice")
	}
}

func TestSnapshotUpdatedAt(t *testing.T) {
	before := time.Now()
	snap := New(nil)
	after := time.Now()

	if snap.UpdatedAt.Before(before) || snap.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt %v not within [%v, %v]", snap.UpdatedAt, before, after)
	}
}

func TestRepoStateFullName(t *testing.T) {
	r := RepoState{Owner: "ericdahl-dev", Name: "git-green"}
	if got := r.FullName(); got != "ericdahl-dev/git-green" {
		t.Errorf("expected ericdahl-dev/git-green, got %q", got)
	}
}

func TestRepoStateStale(t *testing.T) {
	r := RepoState{}
	if r.IsStale() {
		t.Error("expected not stale when StaleAt is nil")
	}

	now := time.Now()
	r.StaleAt = &now
	if !r.IsStale() {
		t.Error("expected stale when StaleAt is set")
	}
}

func TestRepoStateErrorCleared(t *testing.T) {
	now := time.Now()
	stale := RepoState{
		Stoplight: aggregator.StoplightGreen,
		StaleAt:   &now,
		Err:       errors.New("api error"),
	}
	// Simulate successful fetch producing a new snapshot
	fresh := RepoState{
		Owner:     stale.Owner,
		Name:      stale.Name,
		Stoplight: aggregator.StoplightGreen,
		StaleAt:   nil,
		Err:       nil,
	}
	snap := New([]RepoState{fresh})
	if snap.Repos[0].IsStale() {
		t.Error("expected staleness cleared after successful fetch")
	}
	if snap.Repos[0].Err != nil {
		t.Error("expected error cleared after successful fetch")
	}
}
