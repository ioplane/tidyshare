package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type cleanupFile struct {
	file, parent *os.File
	info         os.FileInfo
	name         string
}

func openCleanupFile(r row) (*cleanupFile, error) {
	pfd, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(r.path), &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pfd), filepath.Dir(r.path))
	fd, err := unix.Openat(pfd, filepath.Base(r.path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	f := os.NewFile(uintptr(fd), r.path)
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		_ = parent.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() != r.size || st.ModTime().Unix() != r.mt || st.Sys().(*syscall.Stat_t).Nlink != 1 {
		_ = f.Close()
		_ = parent.Close()
		return nil, errors.New("file changed, is not regular or has multiple links")
	}
	return &cleanupFile{f, parent, st, filepath.Base(r.path)}, nil
}

func (f *cleanupFile) close() { _ = f.file.Close(); _ = f.parent.Close() }

func (f *cleanupFile) stable() bool {
	var st unix.Stat_t
	if unix.Fstatat(int(f.parent.Fd()), f.name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return false
	}
	old := f.info.Sys().(*syscall.Stat_t)
	return st.Dev == uint64(old.Dev) && st.Ino == old.Ino && st.Nlink == 1 && st.Size == old.Size &&
		st.Mtim.Sec == old.Mtim.Sec && st.Mtim.Nsec == old.Mtim.Nsec && st.Ctim.Sec == old.Ctim.Sec && st.Ctim.Nsec == old.Ctim.Nsec
}

func equalCleanupFiles(ctx context.Context, a, b *os.File) (bool, error) {
	if _, err := a.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := b.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	left, right := make([]byte, 128<<10), make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, ae := io.ReadFull(a, left)
		m, be := io.ReadFull(b, right)
		if ae != nil && ae != io.EOF && ae != io.ErrUnexpectedEOF {
			return false, ae
		}
		if be != nil && be != io.EOF && be != io.ErrUnexpectedEOF {
			return false, be
		}
		if n != m || !bytes.Equal(left[:n], right[:m]) {
			return false, nil
		}
		if ae != nil || be != nil {
			return errors.Is(ae, be), nil
		}
	}
}

// Claim the name into a private directory before unlinking; a replaced source is restored.
func deleteCleanupFile(ctx context.Context, keep, dup *cleanupFile) (bool, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, err
	}
	name := ".tidyshare-delete-" + hex.EncodeToString(random[:])
	pfd := int(dup.parent.Fd())
	if err := unix.Mkdirat(pfd, name, 0o700); err != nil {
		return false, err
	}
	defer func() { _ = unix.Unlinkat(pfd, name, unix.AT_REMOVEDIR) }()
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !keep.stable() || !dup.stable() {
		return false, errors.New("file identity changed before claim")
	}
	if err := unix.Renameat2(pfd, dup.name, fd, "duplicate", unix.RENAME_NOREPLACE); err != nil {
		return false, err
	}
	restore := func(cause error) (bool, error) {
		if err := unix.Renameat2(fd, "duplicate", pfd, dup.name, unix.RENAME_NOREPLACE); err != nil {
			return false, fmt.Errorf("%w; restore failed; retained file at %s: %w", cause, filepath.Join(dup.parent.Name(), name, "duplicate"), err)
		}
		return false, cause
	}
	var claimed unix.Stat_t
	if err := unix.Fstatat(fd, "duplicate", &claimed, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return restore(err)
	}
	old := dup.info.Sys().(*syscall.Stat_t)
	if claimed.Ino != old.Ino || claimed.Dev != uint64(old.Dev) || claimed.Nlink != 1 || claimed.Size != old.Size || claimed.Mtim.Sec != old.Mtim.Sec || claimed.Mtim.Nsec != old.Mtim.Nsec {
		return restore(errors.New("claimed file does not match reviewed duplicate"))
	}
	equal, err := equalCleanupFiles(ctx, keep.file, dup.file)
	if err != nil {
		return restore(err)
	}
	if !equal || !keep.stable() {
		return restore(errors.New("content or keeper changed after claim"))
	}
	var final unix.Stat_t
	if err := unix.Fstatat(fd, "duplicate", &final, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return restore(err)
	}
	if final.Ino != claimed.Ino || final.Dev != claimed.Dev || final.Nlink != claimed.Nlink || final.Size != claimed.Size || final.Mtim != claimed.Mtim || final.Ctim != claimed.Ctim {
		return restore(errors.New("duplicate changed during comparison"))
	}
	if err := ctx.Err(); err != nil {
		return restore(err)
	}
	if err := unix.Unlinkat(fd, "duplicate", 0); err != nil {
		return restore(err)
	}
	if err := unix.Fsync(fd); err != nil {
		return true, err
	}
	if err := unix.Fsync(pfd); err != nil {
		return true, err
	}
	return true, nil
}

func cleanupDuplicate(ctx context.Context, keeper, duplicate row, execute bool) (cleanupResult, error) {
	r := cleanupResult{Target: duplicate.path, Keeper: keeper.path, Bytes: duplicate.size, Status: "skipped"}
	keep, err := openCleanupFile(keeper)
	if err != nil {
		r.Reason = "keeper unavailable or changed"
		return r, nil
	}
	defer keep.close()
	dup, err := openCleanupFile(duplicate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			r.Status = "absent"
		} else {
			r.Reason = "duplicate unavailable, changed or unsafe"
		}
		return r, nil
	}
	defer dup.close()
	if keep.info.Sys().(*syscall.Stat_t).Dev != dup.info.Sys().(*syscall.Stat_t).Dev || os.SameFile(keep.info, dup.info) {
		r.Reason = "files must be distinct single-link files on one filesystem"
		return r, nil
	}
	for _, f := range []*cleanupFile{keep, dup} {
		if err := unix.Flock(int(f.file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			r.Reason = "file lock unavailable"
			return r, nil
		}
		defer func() { _ = unix.Flock(int(f.file.Fd()), unix.LOCK_UN) }()
	}
	equal, err := equalCleanupFiles(ctx, keep.file, dup.file)
	if err != nil {
		return r, err
	}
	if !equal || !keep.stable() || !dup.stable() {
		r.Reason = "content or file identity differs"
		return r, nil
	}
	if !execute {
		r.Status = "would_delete"
		return r, nil
	}
	deleted, err := deleteCleanupFile(ctx, keep, dup)
	if deleted {
		r.Status = "deleted"
	}
	if err != nil {
		r.Reason = err.Error()
	}
	return r, err
}

func cleanupDuplicates(ctx context.Context, plan string, execute bool, limit int) (cleanupReport, error) {
	r := cleanupReport{SchemaVersion: 1, Operation: "cleanup_duplicates", DryRun: !execute, Results: []cleanupResult{}}
	if limit < 0 {
		return r, errors.New("limit must be non-negative")
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	groups, order, err := readDuplicatePlan(plan)
	if err != nil {
		return r, err
	}
	for _, id := range order {
		var keeper row
		for _, v := range groups[id] {
			if v.act == "keep" {
				keeper = v
			}
		}
		for _, v := range groups[id] {
			if v.act != "dup" {
				continue
			}
			if limit > 0 && len(r.Results) >= limit {
				return r, nil
			}
			if err := ctx.Err(); err != nil {
				return r, err
			}
			result, err := cleanupDuplicate(ctx, keeper, v, execute)
			r.Results = append(r.Results, result)
			if result.Status == "deleted" {
				r.Changed++
				r.LogicalBytes += v.size
			}
			if result.Status == "skipped" || result.Status == "absent" {
				r.Skipped++
			}
			if err != nil {
				return r, err
			}
		}
	}
	return r, nil
}

func readDuplicatePlan(path string) (map[string][]row, []string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	if _, err := plainPath(path, false); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 64<<20 {
		return nil, nil, errors.New("duplicate plan must be a regular file no larger than 64 MiB")
	}
	return readPlan(f)
}
