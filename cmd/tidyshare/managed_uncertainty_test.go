package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Losing a typed error or removing either root destroys diagnostics of a live consumer.
func TestManagedQualificationRetainsNestedRoots(t *testing.T) {
	cause := errors.New("fixture stop uncertainty")
	runner := func(ctx context.Context, path string, args, env []string, dir string) (managedCommandResult, error) {
		if len(args) == 1 && args[0] == "version" {
			return managedCommandResult{ExitCode: 0, Stdout: "go fixture"}, nil
		}
		return managedCommandResult{ExitCode: -1}, &managedStopError{Cause: cause, PID: 123}
	}
	_, err := qualifyManagedToolWithRunner(context.Background(), "go_build", runner)
	var stop *managedStopError
	if !errors.As(err, &stop) || !errors.Is(err, cause) || len(stop.Roots) != 2 {
		t.Fatalf("typed nested roots lost: %v", err)
	}
	for _, root := range stop.Roots {
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		st, e := os.Stat(root)
		if e != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("retained root unavailable: %s %v", root, e)
		}
		if !strings.Contains(err.Error(), root) {
			t.Fatalf("root absent from error: %s", root)
		}
	}
}

func TestManagedPlanningUncertaintyStopsBatch(t *testing.T) {
	root, _ := inventoryFixture(t)
	write(t, filepath.Join(root, ".cache/go-build/00/fixture"), "cache", 48*time.Hour)
	write(t, filepath.Join(root, ".npm/_cacache/content-v2/fixture"), "cache", 48*time.Hour)
	cause := &managedStopError{Cause: errors.New("fixture"), Roots: []string{"retained-owned-fixture"}, PID: 123}
	calls := 0
	p, err := createManagedPlanWithQualifier(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"go_build", "npm"}}, func(context.Context, string) (*managedTool, error) { calls++; return nil, cause })
	if !errors.Is(err, cause) || calls != 1 || len(p.Targets) != 1 || p.Targets[0].State != "blocked" {
		t.Fatalf("batch continued or error lost: calls=%d plan=%+v err=%v", calls, p, err)
	}
}

func TestManagedExecutionUncertaintyStopsBeforeLaterTargets(t *testing.T) {
	root, _ := inventoryFixture(t)
	write(t, filepath.Join(root, ".cache/go-build/00/fixture"), "cache", 48*time.Hour)
	later := filepath.Join(root, "Library/Caches/Homebrew/downloads/preserve")
	write(t, later, "preserve", 48*time.Hour)
	program, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	path, digest, identity, err := managedExecutable(program)
	if err != nil {
		t.Fatal(err)
	}
	tool := &managedTool{Path: path, SHA256: digest, Identity: identity, Version: "go fixture"}
	p, err := createManagedPlanWithQualifier(context.Background(), managedPlanOptions{Root: root, Kinds: []string{"go_build", "homebrew"}}, func(context.Context, string) (*managedTool, error) { return tool, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, when := range []string{"before-action", "after-possible-action"} {
		t.Run(when, func(t *testing.T) {
			cause := &managedStopError{Cause: errors.New("fixture stop failure"), Roots: []string{"retained-owned-fixture"}, PID: 123}
			calls := 0
			native := managedNativeOps{
				busy: func(context.Context, managedTarget) error {
					calls++
					if when == "before-action" {
						return cause
					}
					return nil
				},
				qualify: func(context.Context, string) (*managedTool, error) { return tool, nil },
				run: func(context.Context, managedTarget, *managedTool) (managedCommandResult, error) {
					return managedCommandResult{ExitCode: -1}, cause
				},
			}
			selected := []string{}
			for _, target := range p.Targets {
				if target.State == "planned" {
					selected = append(selected, target.Path)
				}
			}
			digest := strings.Repeat("a", 64)
			report, err := executeManagedPlanWithNative(context.Background(), p, digest, managedExecuteOptions{Execute: true, ExpectedDigest: digest, Targets: selected, Journal: managedFixtureJournal(t)}, native)
			want := "blocked"
			if when == "after-possible-action" {
				want = "partial"
			}
			if !errors.Is(err, cause) || calls != 1 || len(report.Results) != 1 || report.Results[0].Status != want || len(report.FilesystemAfter) != 0 {
				t.Fatalf("uncertainty not fail-fast: %+v calls=%d err=%v", report, calls, err)
			}
			if _, err := os.Stat(later); err != nil {
				t.Fatal("later target changed")
			}
		})
	}
}
