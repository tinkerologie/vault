package engine

import "errors"

// errShutdownTimeout marks the case where a guest simply did not honour a
// graceful shutdown request in time — as opposed to a libvirt RPC failure or a
// cancelled context.
//
// Lives here, untagged, alongside the decision it drives: vm.go is linux-only
// and its libvirt handle has no seam for a fake, so keeping the policy pure
// and free of libvirt types is the only way to test it on any host.
var errShutdownTimeout = errors.New("domain did not shut down within the timeout")

// escalateToForceStop decides what to do after asking a domain to shut down
// gracefully.
//
// shutdownErr is the result of the shutdown request itself, waitErr the result
// of waiting for the domain to reach a shut-off state, and shutOff whether it
// actually got there.
//
// A guest that accepts the request and then ignores it — no ACPI handler, a
// login prompt, an application blocking shutdown — used to fail the whole
// backup, because the timeout returned before reaching the forced-stop branch
// that exists for exactly this case (issue #255). A timeout now escalates.
//
// Any other wait failure is returned as fatal. That distinction matters: a
// cancelled context must never be converted into a hard power-off of a running
// VM.
func escalateToForceStop(shutdownErr, waitErr error, shutOff bool) (force bool, fatal error) {
	if waitErr != nil && !errors.Is(waitErr, errShutdownTimeout) {
		return false, waitErr
	}
	return shutdownErr != nil || !shutOff, nil
}
