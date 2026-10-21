package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type gitTree struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	GitDir    string `json:"git_dir,omitempty"`
	CommonDir string `json:"common_dir,omitempty"`
	Head      string `json:"head,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Locked    bool   `json:"locked"`
	Prunable  bool   `json:"prunable"`
	Missing   bool   `json:"missing"`
	Reason    string `json:"reason,omitempty"`
}

var errGitOutside = errors.New("metadata outside scan root")

// gitTreesAt reads metadata only; repository configuration and hooks are never loaded.
func gitTreesAt(ctx context.Context, path, scanRoot string) ([]gitTree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	scanRoot, err = filepath.Abs(scanRoot)
	if err != nil {
		return nil, err
	}
	tree := gitTree{Path: path, Kind: "main", GitDir: filepath.Join(path, ".git")}
	metadata, err := gitOpenMetadata(ctx, tree.GitDir, scanRoot)
	if errors.Is(err, os.ErrNotExist) {
		tree.Kind, tree.GitDir = "bare", path
		for _, name := range []string{"objects", "refs"} {
			if err := gitMetadataDirectory(ctx, filepath.Join(path, name), scanRoot); err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOTDIR) {
					return nil, nil
				}
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	} else {
		info, statErr := metadata.Stat()
		_ = metadata.Close()
		if statErr != nil {
			return nil, statErr
		}
		if !info.IsDir() {
			text, err := gitReadMetadata(ctx, tree.GitDir, scanRoot)
			if err != nil {
				return nil, err
			}
			if !strings.HasPrefix(text, "gitdir: ") || len(text) == len("gitdir: ") {
				return nil, errors.New("invalid Git directory pointer")
			}
			tree.Kind = "gitfile"
			tree.GitDir = gitMetadataPath(path, strings.TrimPrefix(text, "gitdir: "))
		}
	}
	if !mcpWithin(scanRoot, tree.GitDir) {
		tree.GitDir, tree.Reason = "", errGitOutside.Error()
		return []gitTree{tree}, nil
	}
	tree.CommonDir = tree.GitDir
	common, err := gitReadMetadata(ctx, filepath.Join(tree.GitDir, "commondir"), scanRoot)
	if err == nil {
		if common == "" {
			return nil, errors.New("empty Git common directory pointer")
		}
		tree.Kind = "linked"
		tree.CommonDir = gitMetadataPath(tree.GitDir, common)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !mcpWithin(scanRoot, tree.CommonDir) {
		tree.CommonDir, tree.Reason = "", errGitOutside.Error()
		return []gitTree{tree}, nil
	}
	for _, name := range []string{"objects", "refs"} {
		if err := gitMetadataDirectory(ctx, filepath.Join(tree.CommonDir, name), scanRoot); err != nil {
			return nil, fmt.Errorf("invalid Git repository metadata: %w", err)
		}
	}
	if err := gitTreeHead(ctx, &tree, scanRoot); err != nil {
		return nil, err
	}
	trees := []gitTree{tree}
	registered, outside, err := gitRegisteredTrees(ctx, tree.CommonDir, scanRoot)
	if err != nil {
		return nil, err
	}
	if outside {
		trees[0].Reason = "registered worktree outside scan root"
	}
	for _, registration := range registered {
		if registration.Path == path {
			trees[0].Locked = registration.Locked
		} else {
			trees = append(trees, registration)
		}
	}
	return trees, nil
}

func gitMetadataPath(base, pointer string) string {
	if filepath.IsAbs(pointer) {
		return filepath.Clean(pointer)
	}
	return filepath.Join(base, pointer)
}

func gitOpenMetadata(ctx context.Context, path, root string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !mcpWithin(root, path) {
		return nil, errGitOutside
	}
	parent, err := openPlainDirectory(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open Git metadata without symlinks: %w", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func gitReadMetadata(ctx context.Context, path, root string) (string, error) {
	file, err := gitOpenMetadata(ctx, path, root)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("git metadata is not a regular file")
	}
	const limit = 64 * 1024
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if len(data) > limit {
		return "", errors.New("git metadata exceeds 64 KiB")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}

func gitMetadataDirectory(ctx context.Context, path, root string) error {
	file, err := gitOpenMetadata(ctx, path, root)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err == nil && !info.IsDir() {
		return unix.ENOTDIR
	}
	return err
}

func gitTreeHead(ctx context.Context, tree *gitTree, root string) error {
	head, err := gitReadMetadata(ctx, filepath.Join(tree.GitDir, "HEAD"), root)
	if err != nil {
		return fmt.Errorf("read Git HEAD: %w", err)
	}
	if gitObjectID(head) {
		tree.Head = head
		return nil
	}
	branch := strings.TrimPrefix(head, "ref: ")
	if branch == head || !gitBranchRef(branch) {
		return errors.New("invalid Git HEAD reference")
	}
	tree.Branch = branch
	object, err := gitReadMetadata(ctx, filepath.Join(tree.CommonDir, branch), root)
	if errors.Is(err, os.ErrNotExist) {
		return nil // Unborn and packed branch references need no object traversal.
	}
	if err != nil {
		return err
	}
	if !gitObjectID(object) {
		return errors.New("invalid Git branch object identifier")
	}
	tree.Head = object
	return nil
}

func gitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

func gitBranchRef(value string) bool {
	if !strings.HasPrefix(value, "refs/heads/") || len(value) > 1024 || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return false
		}
	}
	for component := range strings.SplitSeq(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func gitRegisteredTrees(ctx context.Context, common, root string) ([]gitTree, bool, error) {
	dir := filepath.Join(common, "worktrees")
	file, err := gitOpenMetadata(ctx, dir, root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	var trees []gitTree
	outside, count := false, 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		names, err := file.Readdirnames(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		if len(names) == 0 {
			return trees, outside, nil
		}
		for _, name := range names {
			count++
			if count > 65536 {
				return nil, false, errors.New("git worktree registrations exceed 65536 entries")
			}
			metadata := filepath.Join(dir, name)
			pointer, err := gitReadMetadata(ctx, filepath.Join(metadata, "gitdir"), root)
			if err != nil {
				return nil, false, err
			}
			if !filepath.IsAbs(pointer) || filepath.Base(pointer) != ".git" {
				return nil, false, errors.New("invalid registered Git worktree pointer")
			}
			path := filepath.Dir(pointer)
			if !mcpWithin(root, path) {
				outside = true
				continue
			}
			tree := gitTree{Path: path, Kind: "linked", GitDir: metadata, CommonDir: common}
			lock, err := gitOpenMetadata(ctx, filepath.Join(metadata, "locked"), root)
			if err == nil {
				tree.Locked = true
				_ = lock.Close()
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, false, err
			}
			worktree, err := gitOpenMetadata(ctx, pointer, root)
			if errors.Is(err, os.ErrNotExist) {
				tree.Missing, tree.Prunable = true, !tree.Locked
				tree.Reason = "registered worktree missing"
			} else if err != nil {
				return nil, false, err
			} else {
				_ = worktree.Close()
			}
			if err := gitTreeHead(ctx, &tree, root); err != nil {
				return nil, false, err
			}
			trees = append(trees, tree)
		}
	}
}
