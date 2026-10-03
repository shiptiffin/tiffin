// Package provider creates and destroys the machine a Tiffin box runs on.
// Every provider ends in the same place — a Linux machine Tiffin can run
// commands on and copy files to — so installing and updating Tiffin is
// provider-agnostic (internal/install). Phase 1 has Lima (local VM); Phase 2
// adds Hetzner and plain SSH.
package provider

import (
	"context"
	"errors"
)

// Machine is a running Linux box.
type Machine interface {
	// Exec runs a shell script on the machine as the login user (use sudo for root).
	Exec(ctx context.Context, script string) (stdout, stderr string, err error)
	// Copy copies a local file to a path on the machine.
	Copy(ctx context.Context, local, remote string) error
	// Arch is the machine's Go architecture ("arm64", "amd64").
	Arch() string
}

// State of a box's machine.
type State string

const (
	StateAbsent  State = "absent"
	StateStopped State = "stopped"
	StateRunning State = "running"
)

// Provider manages one box's machine.
type Provider interface {
	Name() string
	// State reports whether the machine exists and runs.
	State(ctx context.Context) (State, error)
	// Up creates the machine if needed and starts it. progress gets short,
	// human-readable steps.
	Up(ctx context.Context, progress func(string)) (Machine, error)
	// Destroy deletes the machine and its data disk. Idempotent.
	Destroy(ctx context.Context) error
	// HostPort is the Mac/host port that reaches the box's HTTPS port.
	HostPort() int
}

// ErrUnavailable means the provider's tooling is missing on this host.
var ErrUnavailable = errors.New("provider unavailable")
