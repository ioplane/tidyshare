package main

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

type failingReportSink struct {
	writeErr, closeErr error
	closed             bool
}

func (f *failingReportSink) Write([]byte) (int, error) { return 0, f.writeErr }
func (f *failingReportSink) Close() error              { f.closed = true; return f.closeErr }

func TestReportFinalizationKeepsFlushAndCloseErrors(t *testing.T) {
	writeErr, closeErr := errors.New("fixture write failure"), errors.New("fixture close failure")
	file := &failingReportSink{writeErr: writeErr, closeErr: closeErr}
	buffer := bufio.NewWriter(file)
	_, _ = buffer.WriteString("owned report")
	err := finishReport(buffer, file)
	if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) || !file.closed {
		t.Fatalf("finalization lost causes or close: %v", err)
	}
}

// The process limit applies only to this child. No host limit or cache changes.
func TestReportWriterFailureHelper(t *testing.T) {
	kind := os.Getenv("TIDYSHARE_WRITER_CASE")
	if kind == "" {
		return
	}
	out := os.Getenv("TIDYSHARE_WRITER_ROOT")
	if kind == "candidates" || kind == "images" {
		name := "candidates.tsv"
		if kind == "images" {
			name = "images.tsv"
		}
		if err := os.Symlink(os.DevNull, filepath.Join(out, name)); err != nil {
			t.Fatal(err)
		}
	}
	signal.Ignore(syscall.SIGXFSZ)
	var before unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &before); err != nil {
		t.Fatal(err)
	}
	limit := before
	limit.Cur = 0
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &before); err != nil {
			t.Errorf("restore fixture file limit: %v", err)
		}
	}()
	if kind == "mcp-exec" {
		app := os.Getenv("TIDYSHARE_WRITER_APP")
		if err := syscall.Exec(app, []string{app, "mcp", "-root", os.Getenv("TIDYSHARE_WRITER_DATA"), "-out", out}, os.Environ()); err != nil {
			t.Fatal(err)
		}
		return
	}
	o := opts{root: out, out: out, minSize: 1, depth: 1, staleDays: 180}
	var err error
	switch kind {
	case "catalog":
		err = writeReports(nil, nil, o)
	case "summary":
		err = writeSummary(nil, o, 0, 0, 0, 0, 0)
	case "candidates":
		err = writeCandidates(nil, nil, imageReport{}, o)
	case "images":
		err = writeImages(imageReport{}, o)
	case "yara":
		err = writeYara([]yaraHit{{Rule: "fixture", Path: "owned", Size: 1}}, o)
	default:
		t.Fatal("unknown writer fixture")
	}
	if !errors.Is(err, unix.EFBIG) {
		t.Fatalf("writer swallowed disk error: %v", err)
	}
}

func TestReportWritersReturnDiskErrors(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"catalog", "summary", "candidates", "images", "yara"} {
		t.Run(kind, func(t *testing.T) {
			child := exec.Command(executable, "-test.run=^TestReportWriterFailureHelper$")
			child.Env = append(os.Environ(), "TIDYSHARE_WRITER_CASE="+kind, "TIDYSHARE_WRITER_ROOT="+t.TempDir())
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("writer did not propagate failure: %v\n%s", err, output)
			}
		})
	}
}
