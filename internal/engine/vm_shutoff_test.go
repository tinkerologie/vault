package engine

import (
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
)

// TestLibvirtDomainIsShutOff pins the distinction that a cold backup depends
// on. DomainShutdown means "being shut down" — the guest is still running.
// Treating it as stopped made waitForLibvirtDomainShutOff return the moment a
// guest began shutting down, so the cold path immediately tried to start its
// paused session on a live domain and failed with "domain is already
// running", leaving the backup failed and the guest powered off (issue #265).
// It only struck when a poll landed inside that window, so nothing but an
// explicit check keeps it from creeping back.
func TestLibvirtDomainIsShutOff(t *testing.T) {
	cases := []struct {
		name  string
		state libvirt.DomainState
		want  bool
	}{
		{"shutoff", libvirt.DomainShutoff, true},
		{"shutting down is NOT shut off", libvirt.DomainShutdown, false},
		{"running", libvirt.DomainRunning, false},
		{"paused", libvirt.DomainPaused, false},
		{"crashed", libvirt.DomainCrashed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := libvirtDomainIsShutOff(tc.state); got != tc.want {
				t.Errorf("libvirtDomainIsShutOff(%v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestSelectBackupDiskXMLUsesLiveXMLWhileShuttingDown guards the same
// distinction at the other call site: a guest that is still shutting down is
// running, so its live XML is the accurate description of its disks.
func TestSelectBackupDiskXMLUsesLiveXMLWhileShuttingDown(t *testing.T) {
	const live, inactive = "<live/>", "<inactive/>"
	if got := selectBackupDiskXML(libvirt.DomainShutdown, live, inactive); got != live {
		t.Errorf("selectBackupDiskXML(shutting down) = %q, want the live XML", got)
	}
	if got := selectBackupDiskXML(libvirt.DomainShutoff, live, inactive); got != inactive {
		t.Errorf("selectBackupDiskXML(shut off) = %q, want the inactive XML", got)
	}
}
