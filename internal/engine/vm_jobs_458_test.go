//go:build linux

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

func typedU64(field string, v uint64) libvirt.TypedParam {
	return libvirt.TypedParam{Field: field, Value: libvirt.TypedParamValue{I: v}}
}

func typedStr(field, v string) libvirt.TypedParam {
	return libvirt.TypedParam{Field: field, Value: libvirt.TypedParamValue{I: v}}
}

// jobStats builds a DomainGetJobStatsFn that answers current-job queries
// (flags == 0) from current, in order, repeating the last entry, and
// completed-job queries from completed.
type jobAnswer struct {
	typ    libvirt.DomainJobType
	params []libvirt.TypedParam
	err    error
}

func jobStats(completed jobAnswer, current ...jobAnswer) func(libvirt.Domain, libvirt.DomainGetJobStatsFlags) (int32, []libvirt.TypedParam, error) {
	i := 0
	return func(_ libvirt.Domain, flags libvirt.DomainGetJobStatsFlags) (int32, []libvirt.TypedParam, error) {
		a := completed
		if flags == 0 {
			a = current[i]
			if i < len(current)-1 {
				i++
			}
		}
		return int32(a.typ), a.params, a.err
	}
}

func TestBackupDisks458(t *testing.T) {
	fastVMPolling(t, time.Minute)

	t.Run("missing target", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{}}
		_, err := h.backupDisks(context.Background(), libvirt.Domain{}, "vm1", []domainDisk{{Path: "/x.img"}}, t.TempDir(), "", false, noProgress, &BackupResult{})
		if err == nil || !strings.HasPrefix(err.Error(), "building backup XML: disk /x.img has no target device") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("job wait error", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{DomainGetJobStatsFn: jobStats(jobAnswer{}, jobAnswer{
			typ: libvirt.DomainJobFailed, params: []libvirt.TypedParam{typedStr("errmsg", "disk full")},
		})}}
		_, err := h.backupDisks(context.Background(), libvirt.Domain{}, "vm1", []domainDisk{{Path: "/x.img", Target: "vda"}}, t.TempDir(), "", false, noProgress, &BackupResult{})
		if err == nil || err.Error() != "backup job failed: disk full" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("success records artifacts", func(t *testing.T) {
		dest := t.TempDir()
		res := &BackupResult{}
		h := &VMHandler{conn: &fakeLibvirt{}}
		artifacts, err := h.backupDisks(context.Background(), libvirt.Domain{}, "vm1",
			[]domainDisk{{Index: 0, Path: "/a.qcow2", Target: "vda"}, {Index: 1, Path: "/b.img", Target: "vdb"}}, dest, "", false, noProgress, res)
		if err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 2 || len(res.Files) != 2 || res.Files[0].Name != "vdisk0.qcow2" || res.Files[1].Name != "vdisk1.img" {
			t.Fatalf("artifacts=%+v files=%+v", artifacts, res.Files)
		}
	})
}

func TestWaitForBackupCompletion458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	dom := libvirt.Domain{Name: "vm1"}
	rpcErr := errors.New("rpc")

	presentDir := t.TempDir()
	present := []vmBackupArtifact{{BackupFile: "vdisk0.img", TargetPath: filepath.Join(presentDir, "vdisk0.img")}}
	writeTestFile(t, present[0].TargetPath, "data")
	missing := []vmBackupArtifact{{BackupFile: "vdisk0.img", TargetPath: filepath.Join(t.TempDir(), "vdisk0.img")}}

	tests := []struct {
		name      string
		stats     func(libvirt.Domain, libvirt.DomainGetJobStatsFlags) (int32, []libvirt.TypedParam, error)
		artifacts []vmBackupArtifact
		wantErr   string
		wantPct   int
	}{
		{
			name:  "completed",
			stats: jobStats(jobAnswer{}, jobAnswer{typ: libvirt.DomainJobCompleted}),
		},
		{
			name:    "cancelled job",
			stats:   jobStats(jobAnswer{}, jobAnswer{typ: libvirt.DomainJobCancelled}),
			wantErr: "backup job cancelled: ",
		},
		{
			name: "progress then completed",
			stats: jobStats(jobAnswer{},
				jobAnswer{typ: libvirt.DomainJobUnbounded, params: []libvirt.TypedParam{typedU64("fileprocessed", 50), typedU64("filetotal", 100)}},
				jobAnswer{typ: libvirt.DomainJobUnbounded, params: []libvirt.TypedParam{typedU64("fileprocessed", 50), typedU64("filetotal", 100)}},
				jobAnswer{typ: libvirt.DomainJobCompleted},
			),
			wantPct: 60,
		},
		{
			name:    "vanished job with completed record",
			stats:   jobStats(jobAnswer{typ: libvirt.DomainJobCompleted}, jobAnswer{typ: libvirt.DomainJobNone}),
			wantPct: 85,
		},
		{
			name:    "vanished job with failed record",
			stats:   jobStats(jobAnswer{typ: libvirt.DomainJobFailed, params: []libvirt.TypedParam{typedStr("errmsg", "io")}}, jobAnswer{typ: libvirt.DomainJobNone}),
			wantErr: "backup job failed: io",
		},
		{
			name:      "vanished job inferred from artifacts",
			stats:     jobStats(jobAnswer{typ: libvirt.DomainJobNone}, jobAnswer{typ: libvirt.DomainJobNone}),
			artifacts: present,
			wantPct:   85,
		},
		{
			name:      "vanished job without artifacts",
			stats:     jobStats(jobAnswer{typ: libvirt.DomainJobNone}, jobAnswer{typ: libvirt.DomainJobNone}),
			artifacts: missing,
			wantErr:   "backup job for vm1 ended without a completion record; missing or empty backup artifacts: vdisk0.img",
		},
		{
			name:      "stats error inferred from artifacts",
			stats:     jobStats(jobAnswer{err: rpcErr}, jobAnswer{err: rpcErr}),
			artifacts: present,
			wantPct:   85,
		},
		{
			name:    "stats error with failed record",
			stats:   jobStats(jobAnswer{typ: libvirt.DomainJobFailed}, jobAnswer{err: rpcErr}),
			wantErr: "backup job failed: ",
		},
		{
			name:      "stats error without artifacts",
			stats:     jobStats(jobAnswer{err: rpcErr}, jobAnswer{err: rpcErr}),
			artifacts: missing,
			wantErr:   "getting backup job stats: rpc; missing or empty backup artifacts: vdisk0.img",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeLibvirt{DomainGetJobStatsFn: tt.stats}
			progress := &progressLog{}
			err := (&VMHandler{conn: fake}).waitForBackupCompletion(context.Background(), dom, "vm1", tt.artifacts, progress.fn)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want prefix %q", err, tt.wantErr)
			}
			if tt.wantPct != 0 && !progress.hasPct(tt.wantPct) {
				t.Fatalf("progress %d not reported: %+v", tt.wantPct, progress.entries)
			}
		})
	}
}

func TestWaitForBackupCompletionCancelled458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeLibvirt{}
	err := (&VMHandler{conn: fake}).waitForBackupCompletion(ctx, libvirt.Domain{}, "vm1", nil, noProgress)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if fake.count("DomainAbortJob") != 1 {
		t.Fatalf("abort calls = %d, want 1", fake.count("DomainAbortJob"))
	}
}

func TestAbortBackupJobWithRetry458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	busy := errors.New("busy")
	tests := []struct {
		name      string
		fake      *fakeLibvirt
		wantAbort int
	}{
		{name: "abort ok", fake: &fakeLibvirt{}, wantAbort: 1},
		{name: "domain gone", fake: &fakeLibvirt{DomainAbortJobFn: func(libvirt.Domain) error { return noDomainErr }}, wantAbort: 1},
		{name: "job already finished", fake: &fakeLibvirt{
			DomainAbortJobFn:    func(libvirt.Domain) error { return busy },
			DomainGetJobStatsFn: jobStats(jobAnswer{}, jobAnswer{typ: libvirt.DomainJobNone}),
		}, wantAbort: 1},
		{name: "job stats error", fake: &fakeLibvirt{
			DomainAbortJobFn:    func(libvirt.Domain) error { return busy },
			DomainGetJobStatsFn: jobStats(jobAnswer{}, jobAnswer{err: busy}),
		}, wantAbort: 1},
		{name: "job still active", fake: &fakeLibvirt{
			DomainAbortJobFn:    func(libvirt.Domain) error { return busy },
			DomainGetJobStatsFn: jobStats(jobAnswer{}, jobAnswer{typ: libvirt.DomainJobUnbounded}),
		}, wantAbort: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			(&VMHandler{conn: tt.fake}).abortBackupJobWithRetry(libvirt.Domain{}, "vm1")
			if got := tt.fake.count("DomainAbortJob"); got != tt.wantAbort {
				t.Fatalf("abort calls = %d, want %d", got, tt.wantAbort)
			}
		})
	}
}

// blockJobs answers DomainGetBlockJobInfo from a scripted sequence, repeating
// the last entry.
type blockJob struct {
	found    bool
	typ      libvirt.DomainBlockJobType
	cur, end uint64
	err      error
}

func blockJobSeq(jobs ...blockJob) func(libvirt.Domain, string) (int32, int32, uint64, uint64, uint64, error) {
	i := 0
	return func(libvirt.Domain, string) (int32, int32, uint64, uint64, uint64, error) {
		j := jobs[i]
		if i < len(jobs)-1 {
			i++
		}
		found := int32(0)
		if j.found {
			found = 1
		}
		return found, int32(j.typ), 0, j.cur, j.end, j.err
	}
}

func TestWaitForDomainBlockJobReady458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	commit := libvirt.DomainBlockJobTypeActiveCommit
	tests := []struct {
		name    string
		jobs    []blockJob
		timeout time.Duration
		wantErr string
	}{
		{name: "no job", jobs: []blockJob{{}}, timeout: time.Minute},
		{name: "info error", jobs: []blockJob{{err: errors.New("rpc")}}, timeout: time.Minute, wantErr: "getting block job info for vda: rpc"},
		{name: "unsupported job", jobs: []blockJob{{found: true, typ: libvirt.DomainBlockJobTypeCopy}}, timeout: time.Minute, wantErr: "disk vda has unsupported block job"},
		{name: "ready after poll", jobs: []blockJob{{found: true, typ: commit, cur: 1, end: 2}, {found: true, typ: commit, cur: 2, end: 2}}, timeout: time.Minute},
		{name: "times out", jobs: []blockJob{{found: true, typ: commit, cur: 1, end: 2}}, timeout: -time.Second, wantErr: "waiting for stale block job on vda: timed out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &VMHandler{conn: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(tt.jobs...)}}
			err := h.waitForDomainBlockJobReady(context.Background(), "vm1", libvirt.Domain{}, "vda", tt.timeout, noProgress)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want prefix %q", err, tt.wantErr)
			}
		})
	}

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := &VMHandler{conn: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(blockJob{found: true, typ: commit, cur: 1, end: 2})}}
		if err := h.waitForDomainBlockJobReady(ctx, "vm1", libvirt.Domain{}, "vda", time.Minute, noProgress); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPivotDomainBlockJob458(t *testing.T) {
	commit := libvirt.DomainBlockJobTypeActiveCommit

	t.Run("abort error", func(t *testing.T) {
		fastVMPolling(t, time.Minute)
		h := &VMHandler{conn: &fakeLibvirt{DomainBlockJobAbortFn: func(libvirt.Domain, string, libvirt.DomainBlockJobAbortFlags) error {
			return errors.New("rpc")
		}}}
		if err := h.pivotDomainBlockJob(context.Background(), "vm1", libvirt.Domain{}, "vda", noProgress); err == nil || err.Error() != "pivoting stale block job on vda: rpc" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("job disappears", func(t *testing.T) {
		fastVMPolling(t, time.Minute)
		var flags libvirt.DomainBlockJobAbortFlags
		h := &VMHandler{conn: &fakeLibvirt{
			DomainBlockJobAbortFn: func(_ libvirt.Domain, _ string, f libvirt.DomainBlockJobAbortFlags) error {
				flags = f
				return nil
			},
			DomainGetBlockJobInfoFn: blockJobSeq(blockJob{found: true, typ: commit}, blockJob{}),
		}}
		if err := h.pivotDomainBlockJob(context.Background(), "vm1", libvirt.Domain{}, "vda", noProgress); err != nil {
			t.Fatal(err)
		}
		if flags != libvirt.DomainBlockJobAbortPivot {
			t.Fatalf("abort flags = %v, want pivot", flags)
		}
	})

	t.Run("info error", func(t *testing.T) {
		fastVMPolling(t, time.Minute)
		h := &VMHandler{conn: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(blockJob{err: errors.New("rpc")})}}
		if err := h.pivotDomainBlockJob(context.Background(), "vm1", libvirt.Domain{}, "vda", noProgress); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("times out", func(t *testing.T) {
		fastVMPolling(t, 5*time.Millisecond)
		h := &VMHandler{conn: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(blockJob{found: true, typ: commit})}}
		err := h.pivotDomainBlockJob(context.Background(), "vm1", libvirt.Domain{}, "vda", noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "waiting for pivoted block job on vda to disappear: timed out") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		fastVMPolling(t, time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := &VMHandler{conn: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(blockJob{found: true, typ: commit})}}
		if err := h.pivotDomainBlockJob(ctx, "vm1", libvirt.Domain{}, "vda", noProgress); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestResolveStaleSnapshotDisk458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	commit := libvirt.DomainBlockJobTypeCommit
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "vdisk1.img.snap")
	writeTestFile(t, existing, "overlay")

	tests := []struct {
		name         string
		path         string
		fake         *fakeLibvirt
		wantResolved bool
		wantErr      string
		wantCommit   int
	}{
		{
			name:    "info error",
			path:    existing,
			fake:    &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(blockJob{err: errors.New("rpc")})},
			wantErr: "getting block job info for vda: rpc",
		},
		{
			name:    "foreign block job",
			path:    existing,
			fake:    &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(blockJob{found: true, typ: libvirt.DomainBlockJobTypePull})},
			wantErr: "disk vda has active block job",
		},
		{
			name: "ready commit job is pivoted",
			path: existing,
			fake: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(
				blockJob{found: true, typ: commit, cur: 5, end: 5}, // resolve
				blockJob{}, // pivot poll
			)},
			wantResolved: true,
		},
		{
			name: "commit job waited on then pivoted",
			path: existing,
			fake: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(
				blockJob{found: true, typ: commit, cur: 1, end: 5}, // resolve
				blockJob{found: true, typ: commit, cur: 5, end: 5}, // wait: ready
				blockJob{}, // pivot poll
			)},
			wantResolved: true,
		},
		{
			name: "commit job wait fails",
			path: existing,
			fake: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(
				blockJob{found: true, typ: commit, cur: 1, end: 5},
				blockJob{err: errors.New("rpc")},
			)},
			wantErr: "getting block job info for vda: rpc",
		},
		{
			name: "overlay missing on disk",
			path: filepath.Join(tmp, "gone.img.snap"),
			fake: &fakeLibvirt{},
		},
		{
			name:         "overlay committed",
			path:         existing,
			fake:         &fakeLibvirt{},
			wantResolved: true,
			wantCommit:   1,
		},
		{
			name: "commit start fails",
			path: existing,
			fake: &fakeLibvirt{DomainBlockCommitFn: func(libvirt.Domain, string) error {
				return errors.New("locked")
			}},
			wantErr:    "starting block commit for stale overlay on vda: locked",
			wantCommit: 1,
		},
		{
			name: "commit wait fails",
			path: existing,
			fake: &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(
				blockJob{},
				blockJob{err: errors.New("rpc")},
			)},
			wantErr:    "getting block job info for vda: rpc",
			wantCommit: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := (&VMHandler{conn: tt.fake}).resolveStaleSnapshotDisk(context.Background(), "vm1", libvirt.Domain{},
				domainDisk{Path: tt.path, Target: "vda"}, noProgress)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want prefix %q", err, tt.wantErr)
			}
			if tt.wantErr == "" && resolved != tt.wantResolved {
				t.Fatalf("resolved = %v, want %v", resolved, tt.wantResolved)
			}
			if got := tt.fake.count("DomainBlockCommit"); got != tt.wantCommit {
				t.Fatalf("commit calls = %d, want %d", got, tt.wantCommit)
			}
		})
	}
}

func TestResolveStaleSnapshotDiskStatError458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tmp := t.TempDir()
	// A path *through* a regular file fails stat with ENOTDIR, not ENOENT.
	file := filepath.Join(tmp, "file")
	writeTestFile(t, file, "x")
	_, err := (&VMHandler{conn: &fakeLibvirt{}}).resolveStaleSnapshotDisk(context.Background(), "vm1", libvirt.Domain{},
		domainDisk{Path: filepath.Join(file, "x.snap"), Target: "vda"}, noProgress)
	if err == nil || !strings.HasPrefix(err.Error(), "stat stale overlay") {
		t.Fatalf("err = %v", err)
	}
}

func TestCleanStaleSnapshots458(t *testing.T) {
	fastVMPolling(t, time.Minute)

	t.Run("nothing stale", func(t *testing.T) {
		fake := &fakeLibvirt{}
		disks := []domainDisk{{Path: "/a.img", Target: "vda"}}
		got, err := (&VMHandler{conn: fake}).cleanStaleSnapshots(context.Background(), "vm1", libvirt.Domain{}, disks, noProgress)
		if err != nil || !sameDomainDisks(got, disks) || len(fake.calls) != 0 {
			t.Fatalf("got=%v err=%v calls=%v", got, err, fake.calls)
		}
	})

	t.Run("missing overlay redefines domain", func(t *testing.T) {
		tmp := t.TempDir()
		base := filepath.Join(tmp, "vdisk1.img")
		stale := base + ".snap.snap"
		writeTestFile(t, base+".snap", "intermediate overlay")
		current := testDomainXML("raw", "", stale, filepath.Join(tmp, "vdisk2.img"))
		var defined string
		fake := &fakeLibvirt{
			DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) { return current, nil },
			DomainDefineXMLFlagsFn: func(xml string) (libvirt.Domain, error) {
				defined = xml
				current = xml
				return libvirt.Domain{}, nil
			},
		}
		disks, _, err := parseDomainDisksWithTargets(current)
		if err != nil {
			t.Fatal(err)
		}
		got, err := (&VMHandler{conn: fake}).cleanStaleSnapshots(context.Background(), "vm1", libvirt.Domain{}, disks, noProgress)
		if err != nil {
			t.Fatalf("cleanStaleSnapshots: %v", err)
		}
		if strings.Contains(defined, ".snap") || !strings.Contains(defined, base) {
			t.Fatalf("redefined XML = %s", defined)
		}
		if len(got) != 2 || got[0].Path != base {
			t.Fatalf("disks = %+v", got)
		}
		if _, err := os.Stat(base + ".snap"); !os.IsNotExist(err) {
			t.Fatalf("intermediate overlay not removed: %v", err)
		}
	})

	t.Run("committed overlay skips redefine", func(t *testing.T) {
		tmp := t.TempDir()
		stale := filepath.Join(tmp, "vdisk1.img.snap")
		writeTestFile(t, stale, "overlay")
		fake := &fakeLibvirt{DomainGetXMLDescFn: xmlReturning(testDomainXML("raw", "", filepath.Join(tmp, "vdisk1.img")))}
		got, err := (&VMHandler{conn: fake}).cleanStaleSnapshots(context.Background(), "vm1", libvirt.Domain{},
			[]domainDisk{{Path: stale, Target: "vda"}}, noProgress)
		if err != nil || len(got) != 1 || strings.HasSuffix(got[0].Path, ".snap") {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if fake.count("DomainDefineXMLFlags") != 0 || fake.count("DomainBlockCommit") != 1 {
			t.Fatalf("calls = %v", fake.calls)
		}
	})

	staleDisks := func(t *testing.T) []domainDisk {
		return []domainDisk{{Path: filepath.Join(t.TempDir(), "gone.img.snap"), Target: "vda"}}
	}
	boom := errors.New("boom")
	tests := []struct {
		name string
		fake func() *fakeLibvirt
		want string
	}{
		{
			name: "resolve error",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetBlockJobInfoFn: blockJobSeq(blockJob{err: boom})}
			},
			want: "resolving stale snapshot disk vda: getting block job info for vda: boom",
		},
		{
			name: "read xml for redefine",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) { return "", boom }}
			},
			want: "reading domain XML for redefine: boom",
		},
		{
			name: "redefine",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainDefineXMLFlagsFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, boom }}
			},
			want: "redefining domain with patched XML: boom",
		},
		{
			name: "lookup after cleanup",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, boom }}
			},
			want: "re-reading domain after cleanup: boom",
		},
		{
			name: "xml after cleanup",
			fake: func() *fakeLibvirt {
				n := 0
				return &fakeLibvirt{DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
					n++
					if n == 2 {
						return "", boom
					}
					return "<domain/>", nil
				}}
			},
			want: "re-reading domain XML after cleanup: boom",
		},
		{
			name: "unparsable xml after cleanup",
			fake: func() *fakeLibvirt {
				n := 0
				return &fakeLibvirt{DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
					n++
					if n == 2 {
						return "<domain", nil
					}
					return "<domain/>", nil
				}}
			},
			want: "re-parsing domain XML after cleanup",
		},
		{
			name: "still stale after cleanup",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetXMLDescFn: xmlReturning(testDomainXML("raw", "", "/mnt/x.img.snap"))}
			},
			want: "disk /mnt/x.img.snap still has .snap suffix after cleanup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (&VMHandler{conn: tt.fake()}).cleanStaleSnapshots(context.Background(), "vm1", libvirt.Domain{}, staleDisks(t), noProgress)
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("err = %v, want prefix %q", err, tt.want)
			}
		})
	}
}

func TestVMBackupCleansStaleSnapshot458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tmp := t.TempDir()
	base := filepath.Join(tmp, "src", "vdisk1.img")
	writeTestFile(t, base, "base")
	current := testDomainXML("raw", "", base+".snap")
	fake := &fakeLibvirt{
		DomainGetXMLDescFn:     func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) { return current, nil },
		DomainDefineXMLFlagsFn: func(xml string) (libvirt.Domain, error) { current = xml; return libvirt.Domain{}, nil },
	}
	res, err := (&VMHandler{conn: fake}).Backup(context.Background(), BackupItem{Name: "vm1", Settings: map[string]any{}}, filepath.Join(tmp, "dest"), noProgress)
	if err != nil || !res.Success {
		t.Fatalf("Backup = %+v, %v", res, err)
	}
	if fake.count("DomainDefineXMLFlags") != 1 {
		t.Fatalf("redefine calls = %d", fake.count("DomainDefineXMLFlags"))
	}
}
