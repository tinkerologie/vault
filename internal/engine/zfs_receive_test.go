//go:build linux

package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestZFSReceivePropertyArgs(t *testing.T) {
	t.Run("a new dataset ignores the stream's mount properties", func(t *testing.T) {
		f := &fakeZFS{}
		args, err := newFakeZFSHandler(f).receivePropertyArgs(t.Context(), "tank/app")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"-x", "mountpoint", "-x", "canmount", "-x", "overlay"}; !slices.Equal(args, want) {
			t.Fatalf("args = %v", args)
		}
	})
	t.Run("values set locally on the existing dataset survive", func(t *testing.T) {
		f := &fakeZFS{mountProps: "mountpoint\t/srv/app\tlocal\ncanmount\ton\tdefault\noverlay\ton\tinherited from tank\n"}
		args, err := newFakeZFSHandler(f).receivePropertyArgs(t.Context(), "tank/app")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"-o", "mountpoint=/srv/app", "-x", "canmount", "-x", "overlay"}; !slices.Equal(args, want) {
			t.Fatalf("args = %v", args)
		}
	})
	t.Run("an unreadable dataset stops the restore", func(t *testing.T) {
		f := &fakeZFS{mountGetErr: errors.New("permission denied")}
		if _, err := newFakeZFSHandler(f).receivePropertyArgs(t.Context(), "tank/app"); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestZFSMountReceived(t *testing.T) {
	cases := []struct {
		name, state string
		wantMount   bool
	}{
		{"unmounted filesystem is mounted", "filesystem\nno\non\n/mnt/tank/app\n", true},
		{"already mounted", "filesystem\nyes\non\n/mnt/tank/app\n", false},
		{"volume", "volume\n-\n-\n-\n", false},
		{"canmount off", "filesystem\nno\noff\n/mnt/tank/app\n", false},
		{"noauto", "filesystem\nno\nnoauto\n/mnt/tank/app\n", false},
		{"legacy", "filesystem\nno\non\nlegacy\n", false},
		{"none", "filesystem\nno\non\nnone\n", false},
		{"mountpoint with spaces", "filesystem\nno\non\n/mnt/My Vault\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeZFS{mountState: c.state}
			if err := newFakeZFSHandler(f).mountReceived(t.Context(), "tank/app"); err != nil {
				t.Fatal(err)
			}
			if got := len(f.commands("mount")) == 1; got != c.wantMount {
				t.Errorf("mounted = %v", got)
			}
		})
	}
	t.Run("errors say the data arrived", func(t *testing.T) {
		for _, f := range []*fakeZFS{
			{mountState: "filesystem\nno\non\n/mnt/x\n", mountErr: errors.New("boom")},
			{stateGetErr: errors.New("boom")},
		} {
			err := newFakeZFSHandler(f).mountReceived(t.Context(), "tank/app")
			if err == nil || !strings.Contains(err.Error(), "data was received") {
				t.Errorf("err = %v", err)
			}
		}
	})
}
