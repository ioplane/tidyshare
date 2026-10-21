package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitFixture(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, path, "init", "-b", "fixture")
	fixtureGit(t, path, "commit", "--allow-empty", "-m", "fixture")
	return path
}

func gitFixtureWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGitTreesMainLinkedAndMissing(t *testing.T) {
	root := t.TempDir()
	main := gitFixture(t, root, "main\nsource")
	linked := filepath.Join(root, "linked\ttree")
	missing := filepath.Join(root, "missing")
	locked := filepath.Join(root, "locked-missing")
	for _, path := range []string{linked, missing, locked} {
		fixtureGit(t, main, "worktree", "add", "--detach", path)
	}
	fixtureGit(t, main, "worktree", "lock", "--reason", "portable", locked)
	fixtureGit(t, main, "worktree", "lock", linked)
	for _, path := range []string{missing, locked} {
		if err := os.Rename(path, path+"-moved"); err != nil {
			t.Fatal(err)
		}
	}
	wantHead := fixtureGit(t, main, "rev-parse", "HEAD")
	trees, err := gitTreesAt(context.Background(), main, root)
	if err != nil || len(trees) != 4 {
		t.Fatalf("trees=%+v, err=%v", trees, err)
	}
	byPath := make(map[string]gitTree)
	for _, tree := range trees {
		byPath[tree.Path] = tree
	}
	if tree := byPath[main]; tree.Kind != "main" || tree.Branch != "refs/heads/fixture" || tree.Head != wantHead {
		t.Fatalf("main tree: %+v", tree)
	}
	if tree := byPath[linked]; tree.Kind != "linked" || tree.Missing || !tree.Locked || tree.Head != wantHead {
		t.Fatalf("linked tree: %+v", tree)
	}
	if tree := byPath[missing]; !tree.Missing || !tree.Prunable || tree.Locked {
		t.Fatalf("missing tree: %+v", tree)
	}
	if tree := byPath[locked]; !tree.Missing || !tree.Locked || tree.Prunable {
		t.Fatalf("locked missing tree: %+v", tree)
	}
	linkedTrees, err := gitTreesAt(context.Background(), linked, root)
	if err != nil || len(linkedTrees) == 0 || linkedTrees[0].Kind != "linked" || !linkedTrees[0].Locked {
		t.Fatalf("linked scan: %+v, %v", linkedTrees, err)
	}
}

func TestGitTreesOutsideRegistrationAndConfigAreOpaque(t *testing.T) {
	root := t.TempDir()
	main := gitFixture(t, root, "main")
	outside := filepath.Join(t.TempDir(), "outside")
	fixtureGit(t, main, "worktree", "add", "--detach", outside)
	marker := filepath.Join(root, "executed")
	gitFixtureWrite(t, filepath.Join(main, ".git", "config"), "[core]\n\tfsmonitor = touch "+marker+"\n[include]\n\tpath = /not-read\n")
	trees, err := gitTreesAt(context.Background(), main, root)
	if err != nil || len(trees) != 1 || trees[0].Reason != "registered worktree outside scan root" {
		t.Fatalf("outside registration: %+v, %v", trees, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository code executed: %v", err)
	}
}

func TestGitTreesBareAndRelativeGitfile(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	fixtureGit(t, root, "init", "--bare", bare)
	trees, err := gitTreesAt(context.Background(), bare, root)
	if err != nil || len(trees) != 1 || trees[0].Kind != "bare" {
		t.Fatalf("bare trees=%+v, err=%v", trees, err)
	}
	work := gitFixture(t, root, "work")
	metadata := filepath.Join(root, "metadata")
	if err := os.Rename(filepath.Join(work, ".git"), metadata); err != nil {
		t.Fatal(err)
	}
	gitFixtureWrite(t, filepath.Join(work, ".git"), "gitdir: ../metadata\n")
	trees, err = gitTreesAt(context.Background(), work, root)
	if err != nil || len(trees) != 1 || trees[0].Kind != "gitfile" {
		t.Fatalf("gitfile trees=%+v, err=%v", trees, err)
	}
}

func TestGitTreesSeparateGitDirUsesNeutralGitfileKind(t *testing.T) {
	root := t.TempDir()
	work, metadata := filepath.Join(root, "work"), filepath.Join(root, "metadata")
	fixtureGit(t, root, "init", "-b", "fixture", "--separate-git-dir", metadata, work)
	fixtureGit(t, work, "commit", "--allow-empty", "-m", "fixture")
	wantHead := fixtureGit(t, work, "rev-parse", "HEAD")
	trees, err := gitTreesAt(context.Background(), work, root)
	if err != nil || len(trees) != 1 {
		t.Fatalf("separate Git directory: %+v, %v", trees, err)
	}
	if tree := trees[0]; tree.Kind != "gitfile" || tree.GitDir != metadata || tree.CommonDir != metadata || tree.Head != wantHead || tree.Branch != "refs/heads/fixture" {
		t.Fatalf("separate Git directory misclassified: %+v", tree)
	}
}

func TestGitTreesRejectFalsePositiveAndCancellation(t *testing.T) {
	root := t.TempDir()
	gitFixtureWrite(t, filepath.Join(root, "HEAD"), "ref: refs/heads/fixture\n")
	trees, err := gitTreesAt(context.Background(), root, root)
	if err != nil || len(trees) != 0 {
		t.Fatalf("ordinary folder: %+v, %v", trees, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gitTreesAt(ctx, root, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestGitTreesOutsideMetadataIsOpaque(t *testing.T) {
	root := t.TempDir()
	outside := gitFixture(t, t.TempDir(), "outside")
	gitFixtureWrite(t, filepath.Join(root, ".git"), "gitdir: "+filepath.Join(outside, ".git")+"\n")
	trees, err := gitTreesAt(context.Background(), root, root)
	if err != nil || len(trees) != 1 {
		t.Fatalf("outside pointer: %+v, %v", trees, err)
	}
	if tree := trees[0]; tree.Head != "" || tree.Branch != "" || tree.GitDir != "" || tree.CommonDir != "" || tree.Reason != "metadata outside scan root" {
		t.Fatalf("outside metadata disclosed: %+v", tree)
	}
}

func TestGitTreesOutsideCommonDirIsOpaque(t *testing.T) {
	root := t.TempDir()
	main := gitFixture(t, root, "main")
	gitFixtureWrite(t, filepath.Join(main, ".git", "commondir"), t.TempDir()+"\n")
	trees, err := gitTreesAt(context.Background(), main, root)
	if err != nil || len(trees) != 1 || trees[0].Head != "" || trees[0].CommonDir != "" || trees[0].Reason != "metadata outside scan root" {
		t.Fatalf("outside common: %+v, %v", trees, err)
	}
}

func TestGitTreesSymlinkMetadataAndOversizeAreRejected(t *testing.T) {
	for _, target := range []string{"gitdir", "commondir", "HEAD", "objects", "refs"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			main := gitFixture(t, root, "main")
			metadata := filepath.Join(main, ".git")
			switch target {
			case "gitdir":
				if err := os.Rename(metadata, filepath.Join(root, "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "moved"), metadata); err != nil {
					t.Fatal(err)
				}
			case "commondir":
				if err := os.Symlink(root, filepath.Join(metadata, "commondir")); err != nil {
					t.Fatal(err)
				}
			default:
				path := filepath.Join(metadata, target)
				if err := os.Rename(path, path+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-moved", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := gitTreesAt(context.Background(), main, root); err == nil {
				t.Fatal("symlink metadata accepted")
			}
		})
	}
	root := t.TempDir()
	gitFixtureWrite(t, filepath.Join(root, ".git"), "gitdir: "+strings.Repeat("x", 65536))
	if _, err := gitTreesAt(context.Background(), root, root); err == nil {
		t.Fatal("unbounded gitfile accepted")
	}
}

func TestGitTreesDetachedSHA256AndUnsafeBranch(t *testing.T) {
	root := t.TempDir()
	main := gitFixture(t, root, "main")
	gitFixtureWrite(t, filepath.Join(main, ".git", "HEAD"), strings.Repeat("a", 64)+"\n")
	trees, err := gitTreesAt(context.Background(), main, root)
	if err != nil || len(trees) != 1 || trees[0].Head != strings.Repeat("a", 64) {
		t.Fatalf("SHA256 HEAD: %+v, %v", trees, err)
	}
	gitFixtureWrite(t, filepath.Join(main, ".git", "HEAD"), "ref: refs/heads/../../secret\n")
	if _, err := gitTreesAt(context.Background(), main, root); err == nil {
		t.Fatal("unsafe symbolic ref accepted")
	}
}
