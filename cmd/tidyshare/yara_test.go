package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func yaraFixture(t *testing.T, script string) ([]entry, opts) {
	t.Helper()
	root, out := t.TempDir(), t.TempDir()
	if err := os.Chmod(out, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fixture.bin")
	if err := os.WriteFile(target, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(root, "rules.yar")
	if err := os.WriteFile(rules, []byte("rule fixture { condition: true }"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "yara")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIDYSHARE_YARA_TARGET", target)
	return []entry{{Path: target, Size: 5, Cat: "binary"}}, opts{out: out, yaraRules: rules, yaraBin: binary, yaraCats: "binary", yaraMax: 1 << 20, workers: 1}
}

func TestYaraFailuresKeepPartialEvidence(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"hit-and-nonzero", `printf 'fixture %s\n' "$TIDYSHARE_YARA_TARGET"; echo owned-diagnostic >&2; exit 9`},
		{"oversized-line", `printf 'fixture %s\n' "$TIDYSHARE_YARA_TARGET"; echo owned-diagnostic >&2; printf '%1048577s\n' x`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, o := yaraFixture(t, tc.script)
			hits, err := runYara(context.Background(), entries, o)
			if err == nil || len(hits) != 1 {
				t.Fatalf("partial failure lost: hits=%d error=%v", len(hits), err)
			}
			data, err := os.ReadFile(filepath.Join(o.out, "yara.tsv"))
			if err != nil || !strings.Contains(string(data), "fixture") {
				t.Fatalf("partial hits lost: %s %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(o.out, "yara.err"))
			if err != nil || !strings.Contains(string(data), "owned-diagnostic") {
				t.Fatalf("stderr lost: %s %v", data, err)
			}
		})
	}
}

func TestYaraCancellationStopsOwnedGroupAndKeepsHits(t *testing.T) {
	entries, o := yaraFixture(t, `printf 'fixture %s\n' "$TIDYSHARE_YARA_TARGET"; echo owned-diagnostic >&2; sleep 1 & echo $! > "$TIDYSHARE_YARA_CHILD"; : > "$TIDYSHARE_YARA_READY"; wait`)
	childfile, ready := filepath.Join(t.TempDir(), "child"), filepath.Join(t.TempDir(), "ready")
	t.Setenv("TIDYSHARE_YARA_CHILD", childfile)
	t.Setenv("TIDYSHARE_YARA_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		until := time.Now().Add(2 * time.Second)
		for time.Now().Before(until) {
			if _, err := os.Stat(ready); err == nil {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	hits, err := runYara(ctx, entries, o)
	<-done
	if !errors.Is(err, context.Canceled) || len(hits) != 1 {
		t.Fatalf("cancellation or hits lost: hits=%d err=%v", len(hits), err)
	}
	data, err := os.ReadFile(childfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("YARA descendant still present: %d %v", pid, err)
	}
	for name, marker := range map[string]string{"yara.tsv": "fixture", "yara.err": "owned-diagnostic"} {
		data, err := os.ReadFile(filepath.Join(o.out, name))
		if err != nil || !strings.Contains(string(data), marker) {
			t.Fatalf("partial evidence missing: %s %v", data, err)
		}
	}
}

func TestYaraScanDoesNotUseNativeOutputBound(t *testing.T) {
	entries, o := yaraFixture(t, `i=0; while [ "$i" -lt 2000 ]; do printf 'fixture %s\n' "$TIDYSHARE_YARA_TARGET"; i=$((i+1)); done`)
	hits, err := runYara(context.Background(), entries, o)
	if err != nil || len(hits) != 2000 {
		t.Fatalf("scan inherited native output bound: hits=%d err=%v", len(hits), err)
	}
}

func TestYaraRuleCompilerStopsDescendant(t *testing.T) {
	_, o := yaraFixture(t, `sleep 1 >/dev/null 2>&1 & echo $! > "$TIDYSHARE_YARA_CHILD"`)
	rules := t.TempDir()
	if err := os.WriteFile(filepath.Join(rules, "fixture.yar"), []byte("owned rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	o.yaraRules = rules
	childfile := filepath.Join(t.TempDir(), "child")
	t.Setenv("TIDYSHARE_YARA_CHILD", childfile)
	if _, err := rulesFile(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(childfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("rule compiler descendant remains: %v", err)
	}
}

func TestYaraDiagnosticWriteFailureCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cause := errors.New("owned stderr write failure")
	writer := &yaraDiagnosticWriter{Writer: &failingReportSink{writeErr: cause}, cancel: cancel}
	if _, err := writer.Write([]byte("owned diagnostic")); !errors.Is(err, cause) || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("stderr write failure did not cancel: %v %v", err, ctx.Err())
	}
}

func TestYaraStopUncertaintyRetainsStage(t *testing.T) {
	entries, o := yaraFixture(t, `GOCACHE="$TIDYSHARE_YARA_CACHE" HOME="$TIDYSHARE_YARA_HOME" exec "$TIDYSHARE_NATIVE_FIXTURE" clean -cache`)
	bin, mode, await := managedFailureTool(t)
	defer await()
	mode("qualification", "")
	t.Setenv("TIDYSHARE_NATIVE_FIXTURE", filepath.Join(bin, "go"))
	t.Setenv("TIDYSHARE_YARA_CACHE", t.TempDir())
	t.Setenv("TIDYSHARE_YARA_HOME", o.out)
	_, err := runYara(context.Background(), entries, o)
	var stop *managedStopError
	if !errors.As(err, &stop) || len(stop.Roots) != 1 || stop.Roots[0] != o.out {
		t.Fatalf("YARA stop error lost stage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(o.out, "yara.tsv")); err != nil {
		t.Fatal("partial report lost")
	}
}

func TestYaraStderrCreateFailureReturnsOriginalCause(t *testing.T) {
	entries, o := yaraFixture(t, "exit 0")
	path := filepath.Join(o.out, "yara.err")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := runYara(context.Background(), entries, o)
	var cause *os.PathError
	if !errors.As(err, &cause) || cause.Path != path {
		t.Fatalf("stderr error lost: %v", err)
	}
}
