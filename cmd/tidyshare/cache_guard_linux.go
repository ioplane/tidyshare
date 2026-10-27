package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func cacheMountGuard(path string) error {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return errors.New("cannot inspect cache mount boundaries")
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			return errors.New("invalid mount inventory")
		}
		point := cacheEscapedProcPath(fields[4])
		if point != path && cacheWithin(path, point) {
			return errors.New("cache contains a nested mount")
		}
	}
	return scanner.Err()
}

func cacheEscapedProcPath(path string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(path)
}

func cacheLinkWithin(root, path string) bool {
	// readlink returns literal names; only proc text formats escape octal sequences.
	return cacheWithin(root, path) || cacheWithin(root, strings.TrimSuffix(path, " (deleted)"))
}

func cacheBusy(ctx context.Context, path string, ownFDs []uintptr) error {
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return errors.New("cannot inspect cache users")
	}
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := strconv.Atoi(process.Name()); err != nil {
			continue
		}
		base := filepath.Join("/proc", process.Name())
		checkLink := func(name string) error {
			link, err := os.Readlink(name)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return errors.New("cannot inspect a potentially relevant process")
			}
			if cacheLinkWithin(path, link) {
				return errors.New("cache is in use by a process")
			}
			return nil
		}
		for _, name := range []string{"cwd", "exe", "root"} {
			if err := checkLink(filepath.Join(base, name)); err != nil {
				return err
			}
		}
		descriptors, err := os.ReadDir(filepath.Join(base, "fd"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("cannot inspect process file descriptors")
		}
		for _, fd := range descriptors {
			ignore := false
			if process.Name() == strconv.Itoa(os.Getpid()) {
				for _, own := range ownFDs {
					if fd.Name() == strconv.FormatUint(uint64(own), 10) {
						ignore = true
						break
					}
				}
			}
			if !ignore {
				if err := checkLink(filepath.Join(base, "fd", fd.Name())); err != nil {
					return err
				}
			}
		}
		maps, err := os.Open(filepath.Join(base, "maps"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("cannot inspect process memory maps")
		}
		scanner := bufio.NewScanner(maps)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		busy := false
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				_ = maps.Close()
				return err
			}
			line := scanner.Text()
			if start := strings.Index(line, "/"); start >= 0 {
				mapped := line[start:]
				if cacheLinkWithin(path, mapped) || cacheLinkWithin(path, cacheEscapedProcPath(mapped)) {
					busy = true
					break
				}
			}
		}
		scanErr := scanner.Err()
		_ = maps.Close()
		if scanErr != nil {
			return errors.New("cannot inspect complete process memory maps")
		}
		if busy {
			return errors.New("cache has a memory-mapped payload")
		}
	}
	return nil
}
