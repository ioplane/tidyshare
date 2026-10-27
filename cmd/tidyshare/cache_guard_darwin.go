//go:build darwin && cgo

package main

/*
#include "cache_guard_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	darwinPIDs = iota
	darwinFDs
	darwinFileports
	darwinIdentity
	darwinDirectories
	darwinExecutable
	darwinFDPath
	darwinFileportPath
	darwinRegion
)

type darwinProcResult struct {
	path, root                       string
	start, startMicro, address, size uint64
	flags                            uint32
}

type darwinProcAPI struct {
	query func(int, int, uint64) (darwinProcResult, error)
	list  func(int, int, []uint64) (int, error)
}

var nativeDarwinAPI = darwinProcAPI{
	query: func(kind, pid int, arg uint64) (darwinProcResult, error) {
		var out C.struct_tidy_proc_result
		if code := C.tidy_proc_query(C.int(kind), C.int(pid), C.uint64_t(arg), &out); code != 0 {
			return darwinProcResult{}, unix.Errno(code)
		}
		return darwinProcResult{
			path: C.GoString(&out.path[0]), root: C.GoString(&out.root[0]),
			start: uint64(out.start), startMicro: uint64(out.start_micro),
			address: uint64(out.address), size: uint64(out.size), flags: uint32(out.flags),
		}, nil
	},
	list: func(kind, pid int, out []uint64) (int, error) {
		var count C.int
		if code := C.tidy_proc_list(C.int(kind), C.int(pid), (*C.uint64_t)(unsafe.Pointer(&out[0])), C.int(len(out)), &count); code != 0 {
			return 0, unix.Errno(code)
		}
		return int(count), nil
	},
}

func darwinReadList(kind, pid int, call func(int, int, []uint64) (int, error)) ([]uint64, error) {
	for capacity := 64; capacity <= 1<<20; capacity *= 2 {
		out := make([]uint64, capacity)
		n, err := call(kind, pid, out)
		if err != nil {
			return nil, err
		}
		if n < 0 || n > len(out) {
			return nil, errors.New("invalid process inventory size")
		}
		if n < len(out) {
			return out[:n], nil
		}
	}
	return nil, errors.New("cannot inspect complete process inventory")
}

func darwinPathWithin(root, path string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

func darwinCheckProcess(ctx context.Context, path string, pid int, ownFDs []uintptr, api darwinProcAPI) error {
	query := func(kind int, arg uint64) (darwinProcResult, error) {
		if err := ctx.Err(); err != nil {
			return darwinProcResult{}, err
		}
		out, err := api.query(kind, pid, arg)
		if err != nil {
			return out, fmt.Errorf("cannot inspect process %d (query %d): %w", pid, kind, err)
		}
		if darwinPathWithin(path, out.path) || (out.root != "" && darwinPathWithin(path, out.root)) {
			return out, fmt.Errorf("cache is in use by process %d (query %d)", pid, kind)
		}
		return out, nil
	}
	before, err := query(darwinIdentity, 0)
	if err != nil {
		return err
	}
	if before.flags&0x100 != 0 {
		return fmt.Errorf("cannot inspect thread working directories for process %d", pid)
	}
	for _, kind := range []int{darwinDirectories, darwinExecutable} {
		if _, err := query(kind, 0); err != nil {
			return err
		}
	}
	for _, kind := range []int{darwinFDs, darwinFileports} {
		entries, err := darwinReadList(kind, pid, api.list)
		if err != nil {
			return fmt.Errorf("cannot inspect process %d descriptors (list %d): %w", pid, kind, err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			fd, fdType := uint32(entry), uint32(entry>>32)
			if fdType != 1 {
				if fdType > 11 || fdType == 8 {
					return fmt.Errorf("cannot inspect descriptor type %d for process %d", fdType, pid)
				}
				continue
			}
			if kind == darwinFDs && pid == os.Getpid() && slices.Contains(ownFDs, uintptr(fd)) {
				continue
			}
			queryKind := darwinFDPath
			if kind == darwinFileports {
				queryKind = darwinFileportPath
			}
			if _, err := query(queryKind, uint64(fd)); err != nil {
				return err
			}
		}
	}
	var address uint64
	unresolved := false
	for regions := 0; ; regions++ {
		if regions == 1<<20 {
			return errors.New("cannot inspect complete process memory maps")
		}
		region, err := query(darwinRegion, address)
		// libproc returns EINVAL when no region remains. An initial EINVAL proves no traversal.
		if errors.Is(err, unix.EINVAL) && regions > 0 {
			break
		}
		if err != nil {
			return err
		}
		unresolved = unresolved || region.flags != 0
		next := region.address + region.size
		if region.size == 0 || region.address < address || next <= region.address {
			return errors.New("cannot inspect complete process memory maps")
		}
		address = next
	}
	after, err := query(darwinIdentity, 0)
	if err != nil {
		return err
	}
	if before.start != after.start || before.startMicro != after.startMicro || before.flags != after.flags {
		return fmt.Errorf("process %d changed during cache inspection", pid)
	}
	if unresolved {
		return fmt.Errorf("cannot resolve all mapped files for process %d", pid)
	}
	return nil
}

func cacheBusy(ctx context.Context, path string, ownFDs []uintptr) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("cannot resolve cache path: %w", err)
	}
	pids, err := darwinReadList(darwinPIDs, 0, nativeDarwinAPI.list)
	if err != nil || len(pids) == 0 {
		return errors.Join(errors.New("cannot inspect all cache users"), err)
	}
	for _, pid := range pids {
		if pid == 0 { // PID zero is the kernel, not a user process.
			continue
		}
		if pid > 1<<31-1 {
			return errors.New("invalid process identifier")
		}
		if err := darwinCheckProcess(ctx, path, int(pid), ownFDs, nativeDarwinAPI); err != nil {
			return err
		}
	}
	current, err := darwinReadList(darwinPIDs, 0, nativeDarwinAPI.list)
	if err != nil {
		return fmt.Errorf("cannot recheck all cache users: %w", err)
	}
	slices.Sort(pids)
	slices.Sort(current)
	if !slices.Equal(pids, current) {
		return errors.New("process inventory changed during cache inspection")
	}
	return ctx.Err()
}

func cacheMountGuard(path string) error {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("cannot resolve cache path: %w", err)
	}
	return darwinMountGuard(path, unix.Getfsstat)
}

func darwinMountGuard(path string, list func([]unix.Statfs_t, int) (int, error)) error {
	count, err := list(nil, unix.MNT_NOWAIT)
	if err != nil || count <= 0 || count > 1<<14 {
		return errors.Join(errors.New("cannot inspect cache mount boundaries"), err)
	}
	for capacity := count + 16; capacity <= 1<<14; capacity *= 2 {
		mounts := make([]unix.Statfs_t, capacity)
		n, err := list(mounts, unix.MNT_NOWAIT)
		if err != nil {
			return fmt.Errorf("cannot inspect cache mount boundaries: %w", err)
		}
		if n <= 0 || n > len(mounts) {
			return errors.New("invalid mount inventory size")
		}
		if n == len(mounts) {
			continue
		}
		for _, mount := range mounts[:n] {
			point := unix.ByteSliceToString(mount.Mntonname[:])
			if !filepath.IsAbs(point) || len(point) >= len(mount.Mntonname)-1 {
				return errors.New("invalid mount path")
			}
			if point != path && darwinPathWithin(path, point) {
				return errors.New("cache contains a nested mount")
			}
		}
		return nil
	}
	return errors.New("cannot inspect complete mount inventory")
}
