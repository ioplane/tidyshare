package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupDuplicatesRequiresExecutionAndKeepsKeeper(t *testing.T) {
	keep, dup, plan, _ := applyFixture(t)
	r, err := cleanupDuplicates(context.Background(), plan, false, 0)
	if err != nil || len(r.Results) != 1 || r.Results[0].Status != "would_delete" {
		t.Fatalf("dry run: %+v %v", r, err)
	}
	if _, err := os.Stat(dup); err != nil {
		t.Fatal("dry run removed duplicate", err)
	}
	r, err = cleanupDuplicates(context.Background(), plan, true, 1)
	if err != nil || r.Changed != 1 {
		t.Fatalf("execute: %+v %v", r, err)
	}
	if _, err := os.Lstat(dup); !os.IsNotExist(err) {
		t.Fatalf("duplicate remains: %v", err)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "same-content" {
		t.Fatalf("keeper changed: %q %v", b, err)
	}
	r, err = cleanupDuplicates(context.Background(), plan, true, 0)
	if err != nil || r.Changed != 0 || r.Results[0].Status != "absent" {
		t.Fatalf("repeat: %+v %v", r, err)
	}
}

func TestCleanupDuplicatesRefusesDifferentBytesAndUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"content", "missing keeper", "symlink", "hardlink", "cancelled", "bad plan"} {
		t.Run(kind, func(t *testing.T) {
			keep, dup, plan, _ := applyFixture(t)
			ctx := context.Background()
			switch kind {
			case "content":
				st, _ := os.Stat(dup)
				if err := os.WriteFile(dup, []byte("evil-content"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(dup, st.ModTime(), st.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "missing keeper":
				if err := os.Remove(keep); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(dup); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(keep, dup); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(dup, filepath.Join(filepath.Dir(dup), "extra")); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "bad plan":
				f, err := os.OpenFile(plan, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString("bad row\n"); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			r, err := cleanupDuplicates(ctx, plan, true, 0)
			if r.Changed != 0 {
				t.Fatalf("unsafe change: %+v", r)
			}
			if kind == "bad plan" || kind == "cancelled" {
				if err == nil {
					t.Fatal("expected refusal")
				}
			}
			if _, err := os.Lstat(dup); err != nil {
				t.Fatalf("refused duplicate lost: %v", err)
			}
			paths, err := filepath.Glob(filepath.Join(filepath.Dir(dup), ".tidyshare-delete-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(paths) != 0 {
				t.Fatalf("refused cleanup wrote staging dirs: %v", paths)
			}
		})
	}
}

func TestCleanupDuplicatePlanRejectsKeeperSymlinkParent(t *testing.T) {
	keep, dup, plan, _ := applyFixture(t)
	dir := filepath.Dir(keep)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, []byte(strings.ReplaceAll(string(b), keep, alias+"/keep")), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := cleanupDuplicates(context.Background(), plan, true, 0)
	if r.Changed != 0 {
		t.Fatalf("symlink parent accepted: %+v", r)
	}
	if _, err := os.Stat(dup); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupCLIReportsAndPreflight(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected reports", true: "existing report"}[blocked], func(t *testing.T) {
			keep, dup, plan, _ := applyFixture(t)
			out := filepath.Join(t.TempDir(), "reports")
			if blocked {
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(keep, filepath.Join(out, "cleanup.json")); err != nil {
					t.Fatal(err)
				}
			}
			rc := runCleanup([]string{"-kind", "duplicates", "-plan", plan, "-out", out, "-formats", "json,jsonl", "-yes"})
			if blocked {
				if rc == 0 {
					t.Fatal("unsafe report accepted")
				}
				if _, err := os.Stat(dup); err != nil {
					t.Fatal("report preflight did not block delete", err)
				}
				return
			}
			if rc != 0 {
				t.Fatalf("cleanup exit %d", rc)
			}
			body, err := os.ReadFile(filepath.Join(out, "cleanup.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Changed int  `json:"changed"`
				DryRun  bool `json:"dry_run"`
			}
			if err := json.Unmarshal(body, &report); err != nil || report.Changed != 1 || report.DryRun {
				t.Fatalf("wrong report: %s %v", body, err)
			}
			if _, err := os.Stat(filepath.Join(out, "cleanup.jsonl")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(out, "cleanup.md")); !os.IsNotExist(err) {
				t.Fatal("unselected Markdown report created", err)
			}
		})
	}
}

func TestCleanupAcceptsRelativePlans(t *testing.T) {
	_, _, plan, _ := applyFixture(t)
	t.Chdir(filepath.Dir(plan))
	r, err := cleanupDuplicates(context.Background(), filepath.Base(plan), false, 0)
	if err != nil || len(r.Results) != 1 {
		t.Fatalf("relative duplicate plan: %+v %v", r, err)
	}
	imagePlan := `{"schema_version":1,"owner":"root","endpoint":"unix:///run/podman/podman.sock","graphroot":"/fixture/storage","runroot":"/fixture/runtime","exact_ids":[],"candidates":[]}`
	if err := os.WriteFile("image-plan.json", []byte(imagePlan), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readImagePlan("image-plan.json"); err != nil {
		t.Fatal(err)
	}
}

type cancelAfterClaim struct {
	context.Context
	cancel      context.CancelFunc
	target      string
	replacement bool
}

func (c *cancelAfterClaim) Err() error {
	if _, err := os.Lstat(c.target); os.IsNotExist(err) && c.Context.Err() == nil {
		if c.replacement {
			_ = os.WriteFile(c.target, []byte("new-file"), 0o600)
		}
		c.cancel()
	}
	return c.Context.Err()
}

func TestCleanupRestoresClaimOnCancellation(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "retain on collision"}[replacement], func(t *testing.T) {
			keepPath, dupPath, _, _ := applyFixture(t)
			st, err := os.Stat(keepPath)
			if err != nil {
				t.Fatal(err)
			}
			keep, err := openCleanupFile(row{path: keepPath, size: 12, mt: st.ModTime().Unix()})
			if err != nil {
				t.Fatal(err)
			}
			defer keep.close()
			dup, err := openCleanupFile(row{path: dupPath, size: 12, mt: st.ModTime().Unix()})
			if err != nil {
				t.Fatal(err)
			}
			defer dup.close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deleted, err := deleteCleanupFile(&cancelAfterClaim{ctx, cancel, dupPath, replacement}, keep, dup)
			if deleted || !errors.Is(err, context.Canceled) {
				t.Fatalf("wrong cancellation result: %t %v", deleted, err)
			}
			paths, globErr := filepath.Glob(filepath.Join(filepath.Dir(dupPath), ".tidyshare-delete-*", "duplicate"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if replacement {
				if len(paths) != 1 || !strings.Contains(err.Error(), paths[0]) {
					t.Fatalf("missing recovery location: %v %v", paths, err)
				}
				if b, readErr := os.ReadFile(paths[0]); readErr != nil || string(b) != "same-content" {
					t.Fatalf("retained bytes: %q %v", b, readErr)
				}
				if b, readErr := os.ReadFile(dupPath); readErr != nil || string(b) != "new-file" {
					t.Fatalf("replacement overwritten: %q %v", b, readErr)
				}
			} else {
				if len(paths) != 0 {
					t.Fatalf("staging retained after restore: %v", paths)
				}
				if b, readErr := os.ReadFile(dupPath); readErr != nil || string(b) != "same-content" {
					t.Fatalf("restore lost data: %q %v", b, readErr)
				}
			}
			if b, readErr := os.ReadFile(keepPath); readErr != nil || string(b) != "same-content" {
				t.Fatalf("keeper lost: %q %v", b, readErr)
			}
		})
	}
}
