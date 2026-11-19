package sandbox

import (
	"fmt"
	"strings"
)

// Backend identifies a sandbox execution backend. It is the configuration-level
// skeleton for the backends described in the design (host, docker, ssh,
// landlock); this package only ships HostSandbox today. Docker and SSH
// backends are expected to implement Sandbox (and optionally DirLister) later
// without changing this identifier set.
type Backend string

const (
	// BackendHost runs commands and file access directly on the host process.
	// It is not an isolation boundary.
	BackendHost Backend = "host"
	// BackendDocker runs tools inside a container (not implemented yet).
	BackendDocker Backend = "docker"
	// BackendSSH runs tools on a remote host (not implemented yet).
	BackendSSH Backend = "ssh"
	// BackendLandlock confines file and network access on Linux (not
	// implemented yet).
	BackendLandlock Backend = "landlock"
)

// ParseBackend normalizes and validates a configured backend name. Empty names
// are rejected; callers that want a default should supply BackendHost
// explicitly.
func ParseBackend(name string) (Backend, error) {
	switch b := Backend(strings.ToLower(strings.TrimSpace(name))); b {
	case BackendHost, BackendDocker, BackendSSH, BackendLandlock:
		return b, nil
	default:
		return "", fmt.Errorf("%w: unknown sandbox backend %q", ErrInvalidSpec, name)
	}
}

// String returns the backend name.
func (b Backend) String() string { return string(b) }
