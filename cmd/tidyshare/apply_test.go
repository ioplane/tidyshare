package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const planHeader = "group\taction\tsize\tmtime\tpath\n"

func applyFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	d := t.TempDir()
	keep, dup, plan, quarantine := d+"/keep", d+"/dup", d+"/plan.tsv", d+"/quarantine"
	write(t, keep, "same-content", time.Hour)
	write(t, dup, "same-content", time.Hour)
	st, err := os.Stat(keep)
	if err != nil {
		t.Fatal(err)
	}
	write(t, plan, planHeader+fmt.Sprintf("1\tkeep\t12\t%d\t%q\n1\tdup\t12\t%d\t%q\n", st.ModTime().Unix(), keep, st.ModTime().Unix(), dup), 0)
	return keep, dup, plan, quarantine
}

func TestApplyDryRunDoesNotWrite(t *testing.T) {
	_, dup, plan, q := applyFixture(t)
	if rc := runApply([]string{"-plan", plan, "-quarantine", q}); rc != 0 {
		t.Fatalf("dry run returned %d", rc)
	}
	if _, err := os.Lstat(q); !os.IsNotExist(err) {
		t.Fatalf("dry run created quarantine: %v", err)
	}
	if body, err := os.ReadFile(dup); err != nil || string(body) != "same-content" {
		t.Fatalf("dry run changed duplicate: %q %v", body, err)
	}
}

func TestApplyDryRunPreservesExistingManifest(t *testing.T) {
	_, dup, plan, q := applyFixture(t)
	manifest := q + "/MANIFEST.tsv"
	write(t, manifest, "preserve-manifest", time.Hour)
	st, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	wantCode := 0
	if hostPlatform() == "darwin" {
		wantCode = 1
	}
	if rc := runApply([]string{"-plan", plan, "-quarantine", q, "-mode", "dedupe"}); rc != wantCode {
		t.Fatalf("dry run returned %d", rc)
	}
	after, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(manifest); err != nil || string(b) != "preserve-manifest" || !after.ModTime().Equal(st.ModTime()) {
		t.Fatalf("dry run modified manifest: %q %v", b, err)
	}
	if b, err := os.ReadFile(dup); err != nil || string(b) != "same-content" {
		t.Fatalf("dry run modified duplicate: %q %v", b, err)
	}
}

func TestApplyRejectsInvalidPlanBeforeWrites(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(string, string, string) string
	}{
		{"header", func(s, _, _ string) string { return strings.Replace(s, "group\t", "wrong\t", 1) }},
		{"truncated row", func(s, _, _ string) string { return s + "2\tkeep\t12\t0\t\"unfinished" }},
		{"missing final newline", func(s, _, _ string) string { return strings.TrimSuffix(s, "\n") }},
		{"oversize row", func(s, _, _ string) string { return s + strings.Repeat("x", 1<<20) + "\n" }},
		{"missing fields", func(s, _, _ string) string { return s + "2\tdup\n" }},
		{"unknown action", func(s, _, _ string) string { return strings.Replace(s, "\tdup\t", "\tdrop\t", 1) }},
		{"bad size", func(s, _, _ string) string { return strings.Replace(s, "\t12\t", "\tbogus\t", 1) }},
		{"negative size", func(s, _, _ string) string { return strings.ReplaceAll(s, "\t12\t", "\t-12\t") }},
		{"bad mtime", func(s, _, _ string) string { return strings.Replace(s, "\t12\t", "\t12\tbroken", 1) }},
		{"empty group", func(s, _, _ string) string { return strings.TrimPrefix(s, planHeader+"1") }},
		{"multiple keepers", func(s, _, _ string) string { return strings.Replace(s, "\tdup\t", "\tkeep\t", 1) }},
		{"missing keeper", func(s, _, _ string) string { return strings.Replace(s, "\tkeep\t", "\tdup\t", 1) }},
		{"group size mismatch", func(s, _, _ string) string { return strings.Replace(s, "\tdup\t12\t", "\tdup\t11\t", 1) }},
		{"same path", func(s, keep, dup string) string {
			return strings.Replace(s, strconv.Quote(dup), strconv.Quote(keep), 1)
		}},
		{"relative path", func(s, _, dup string) string { return strings.Replace(s, strconv.Quote(dup), `"relative"`, 1) }},
		{"traversal", func(s, _, dup string) string {
			return strings.Replace(s, strconv.Quote(dup), strconv.Quote(filepath.Dir(dup)+"/../outside"), 1)
		}},
		{"nul path", func(s, _, dup string) string { return strings.Replace(s, strconv.Quote(dup), `"/bad\x00path"`, 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			keep, dup, plan, q := applyFixture(t)
			body, err := os.ReadFile(plan)
			if err != nil {
				t.Fatal(err)
			}
			write(t, plan, test.edit(string(body), keep, dup), 0)
			if rc := runApply([]string{"-plan", plan, "-quarantine", q, "-yes"}); rc == 0 {
				t.Error("invalid plan accepted")
			}
			if _, err := os.Lstat(q); !os.IsNotExist(err) {
				t.Errorf("invalid plan created quarantine: %v", err)
			}
			if body, err := os.ReadFile(dup); err != nil || string(body) != "same-content" {
				t.Errorf("invalid plan changed duplicate: %q %v", body, err)
			}
		})
	}
}

func TestApplyQuarantineRefusesExistingDestination(t *testing.T) {
	_, dup, plan, q := applyFixture(t)
	dst := filepath.Join(q, dup)
	write(t, dst, "preserve-existing", 0)
	runApply([]string{"-plan", plan, "-quarantine", q, "-yes"})
	for p, want := range map[string]string{dup: "same-content", dst: "preserve-existing"} {
		if body, err := os.ReadFile(p); err != nil || string(body) != want {
			t.Errorf("%s overwritten or removed: %q %v", p, body, err)
		}
	}
}

func TestApplyRefusesSymlinkDirectories(t *testing.T) {
	for _, location := range []string{"quarantine", "destination", "source"} {
		t.Run(location, func(t *testing.T) {
			keep, dup, plan, q := applyFixture(t)
			outside := t.TempDir()
			switch location {
			case "quarantine":
				if err := os.Symlink(outside, q); err != nil {
					t.Fatal(err)
				}
			case "destination":
				if err := os.Mkdir(q, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, q+"/"+strings.Split(strings.TrimPrefix(dup, "/"), "/")[0]); err != nil {
					t.Fatal(err)
				}
			case "source":
				alias := filepath.Dir(dup) + "/alias"
				if err := os.Symlink(filepath.Dir(dup), alias); err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(plan)
				if err != nil {
					t.Fatal(err)
				}
				write(t, plan, strings.Replace(string(body), strconv.Quote(dup), strconv.Quote(alias+"/dup"), 1), 0)
			}
			runApply([]string{"-plan", plan, "-quarantine", q, "-yes"})
			for _, p := range []string{keep, dup} {
				if body, err := os.ReadFile(p); err != nil || string(body) != "same-content" {
					t.Errorf("symlink path changed source: %q %v", body, err)
				}
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Errorf("symlink target modified: %v %v", entries, err)
			}
		})
	}
}

func TestApplySkipsChangedOrMissingFiles(t *testing.T) {
	for _, change := range []string{"size", "mtime", "missing keeper", "symlink"} {
		t.Run(change, func(t *testing.T) {
			keep, dup, plan, q := applyFixture(t)
			switch change {
			case "size":
				write(t, dup, "changed", 0)
			case "mtime":
				write(t, dup, "same-content", 0)
			case "missing keeper":
				if err := os.Rename(keep, keep+".away"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(dup, dup+".away"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dup+".away", dup); err != nil {
					t.Fatal(err)
				}
			}
			runApply([]string{"-plan", plan, "-quarantine", q, "-yes"})
			if _, err := os.Lstat(dup); err != nil {
				t.Fatalf("unsafe duplicate moved: %v", err)
			}
		})
	}
}

func TestSameDevRejectsMissingQuarantineInfo(t *testing.T) {
	defer func() {
		if value := recover(); value != nil {
			t.Errorf("nil quarantine info panicked: %v", value)
		}
	}()
	if sameDev(t.TempDir(), nil) {
		t.Fatal("nil quarantine info accepted")
	}
}

func TestApplyManifestWriteFailure(t *testing.T) {
	_, dup, plan, q := applyFixture(t)
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
			t.Fatal(err)
		}
	}()
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: old.Max}); err != nil {
		t.Fatal(err)
	}
	if rc := runApply([]string{"-plan", plan, "-quarantine", q, "-yes"}); rc == 0 {
		t.Fatal("manifest write error reported as success")
	}
	if _, err := os.Lstat(dup); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestApplyRefusesUnsafeManifest(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			_, dup, plan, q := applyFixture(t)
			if err := os.Mkdir(q, 0o755); err != nil {
				t.Fatal(err)
			}
			target, manifest := filepath.Dir(q)+"/target", q+"/MANIFEST.tsv"
			write(t, target, "preserve-target", 0)
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, manifest)
			case "hardlink":
				err = os.Link(target, manifest)
			case "directory":
				err = os.Mkdir(manifest, 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if rc := runApply([]string{"-plan", plan, "-quarantine", q, "-yes"}); rc == 0 {
				t.Fatal("unsafe manifest accepted")
			}
			for p, want := range map[string]string{target: "preserve-target", dup: "same-content"} {
				if b, err := os.ReadFile(p); err != nil || string(b) != want {
					t.Fatalf("unsafe manifest modified files: %q %v", b, err)
				}
			}
		})
	}
}

func TestApplyDedupeRefusesDifferentContent(t *testing.T) {
	keep, dup, plan, q := applyFixture(t)
	st, err := os.Stat(dup)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dup, "DIFF-content", 0)
	if err := os.Chtimes(dup, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := dedupe(keep, dup, 12); err != nil && (errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP)) {
		t.Skip("filesystem has no dedupe support")
	}
	if rc := runApply([]string{"-plan", plan, "-quarantine", q, "-mode", "dedupe", "-yes"}); rc != 0 {
		t.Fatalf("apply returned %d", rc)
	}
	if b, err := os.ReadFile(dup); err != nil || string(b) != "DIFF-content" {
		t.Fatalf("different content changed: %q %v", b, err)
	}
	if b, err := os.ReadFile(q + "/MANIFEST.tsv"); err != nil || len(b) != 0 {
		t.Fatalf("refused dedupe recorded as success: %q %v", b, err)
	}
}

func TestApplyDedupeCanRepeat(t *testing.T) {
	keep, dup, plan, q := applyFixture(t)
	if err := dedupe(keep, dup, 12); err != nil {
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
			t.Skip("filesystem has no dedupe support")
		}
		t.Fatal(err)
	}
	for range 2 {
		if rc := runApply([]string{"-plan", plan, "-quarantine", q, "-mode", "dedupe", "-yes"}); rc != 0 {
			t.Fatalf("repeat dedupe returned %d", rc)
		}
	}
	for _, p := range []string{keep, dup} {
		if b, err := os.ReadFile(p); err != nil || string(b) != "same-content" {
			t.Fatalf("repeat dedupe changed content: %q %v", b, err)
		}
	}
	if b, err := os.ReadFile(q + "/MANIFEST.tsv"); err != nil || strings.Count(string(b), "\tdedupe\t") != 2 {
		t.Fatalf("repeat dedupe missing manifest entries: %q %v", b, err)
	}
}

func TestApplySkipsDifferentFilesystem(t *testing.T) {
	_, dup, plan, _ := applyFixture(t)
	other, err := os.MkdirTemp("/dev/shm", "tidyshare-apply-")
	if err != nil {
		t.Skipf("separate temporary filesystem unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(other); err != nil {
			t.Error(err)
		}
	})
	qst, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if sameDev(dup, qst) {
		t.Skip("temporary directories use the same filesystem")
	}
	if rc := runApply([]string{"-plan", plan, "-quarantine", other, "-yes"}); rc != 0 {
		t.Fatalf("apply returned %d", rc)
	}
	if b, err := os.ReadFile(dup); err != nil || string(b) != "same-content" {
		t.Fatalf("cross-filesystem duplicate moved: %q %v", b, err)
	}
}
