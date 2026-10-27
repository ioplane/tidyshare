package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type managedExecuteOptions struct {
	Execute         bool
	ExpectedDigest  string
	Targets         []string
	Limit           int
	MandatoryPolicy storagePolicy
	Journal         *os.File
}

type managedResult struct {
	Target       string                `json:"target"`
	Name         string                `json:"name,omitempty"`
	Status       string                `json:"status"`
	Reason       string                `json:"reason,omitempty"`
	RecoveryPath string                `json:"recovery_path,omitempty"`
	Command      *managedCommandResult `json:"command,omitempty"`
	BeforeFiles  *int64                `json:"before_files,omitempty"`
	AfterFiles   *int64                `json:"after_files,omitempty"`
}

type managedExecutionReport struct {
	SchemaVersion    int                     `json:"schema_version"`
	Contract         string                  `json:"contract"`
	PlanSHA256       string                  `json:"plan_sha256"`
	DryRun           bool                    `json:"dry_run"`
	Changed          int                     `json:"changed_actions"`
	Results          []managedResult         `json:"results"`
	FilesystemBefore []workstationFilesystem `json:"filesystem_before"`
	FilesystemAfter  []workstationFilesystem `json:"filesystem_after"`
	Limitation       string                  `json:"limitation"`
	Error            string                  `json:"error,omitempty"`
}

func managedDirectoryMatches(st unix.Stat_t, id managedIdentity) bool {
	return uint64(st.Dev) == id.Device && st.Ino == id.Inode && st.Uid == id.UID && uint32(st.Mode) == id.Mode && st.Mode&unix.S_IFMT == unix.S_IFDIR
}

func managedOpenTarget(t managedTarget) (*os.File, error) {
	dir, err := openPlainDirectory(t.Path, false)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil || !managedDirectoryMatches(st, t.Identity) {
		_ = dir.Close()
		return nil, errors.New("target directory identity changed")
	}
	return dir, nil
}

// These observations cover visible processes and open FDs, not all memory consumers or future opens.
func managedBusy(ctx context.Context, target managedTarget) error {
	if target.Kind == "chrome" || target.Kind == "xcode" {
		result, err := managedCommand(ctx, "/bin/ps", []string{"-axo", "pid=,uid=,comm="}, []string{"PATH=/usr/bin:/bin", "LANG=C"}, "/")
		if err != nil {
			if managedUncertain(err) {
				return fmt.Errorf("application process observation: %w", err)
			}
			return errors.New("application process observation is unavailable")
		}
		for _, line := range strings.Split(result.Stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			uid, err := strconv.ParseUint(fields[1], 10, 32)
			if err != nil {
				return errors.New("application process observation is malformed")
			}
			if uint32(uid) != target.Identity.UID {
				continue
			}
			name := strings.ToLower(strings.Join(fields[2:], " "))
			if target.Kind == "chrome" && strings.Contains(name, "google chrome") || target.Kind == "xcode" && (strings.Contains(name, "xcode.app") || strings.HasSuffix(name, "/xcodebuild")) {
				return errors.New("application consumer is running; preserve this category")
			}
		}
	}
	lsof := "/usr/bin/lsof"
	if runtime.GOOS == "darwin" {
		lsof = "/usr/sbin/lsof"
	}
	if st, err := os.Stat(lsof); err != nil || !st.Mode().IsRegular() {
		return errors.New("required system open-file observer is unavailable")
	}
	result, runErr := managedCommand(ctx, lsof, []string{"-nP", "-a", "-u" + strconv.FormatUint(uint64(target.Identity.UID), 10), "+D", target.Path, "-F", "p"}, []string{"PATH=/usr/bin:/bin:/usr/sbin", "LANG=C"}, "/")
	if managedUncertain(runErr) {
		return fmt.Errorf("open-file observation: %w", runErr)
	}
	if result.Stderr != "" || runErr != nil && result.ExitCode != 1 {
		return errors.New("open-file observation is incomplete")
	}
	if err := managedLsofProcesses(result.Stdout, os.Getpid()); err != nil {
		return err
	}
	return ctx.Err()
}

func managedLsofProcesses(output string, ownPID int) error {
	seenProcess := false
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		// lsof includes descriptor records even when only process IDs are requested.
		if strings.HasPrefix(line, "f") && len(line) > 1 && seenProcess {
			continue
		}
		if !strings.HasPrefix(line, "p") {
			return errors.New("open-file observation is malformed")
		}
		pid, err := strconv.Atoi(line[1:])
		if err != nil || pid <= 0 {
			return errors.New("open-file observation has invalid process identity")
		}
		seenProcess = true
		if pid != ownPID {
			return fmt.Errorf("visible open-file consumer %d uses the target", pid)
		}
	}
	return nil
}

func managedRelativeParent(root *os.File, rel string, device uint64) (*os.File, error) {
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	for _, part := range strings.Split(filepath.Dir(rel), string(os.PathSeparator)) {
		if part == "." {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
		var st unix.Stat_t
		if e := unix.Fstat(fd, &st); e != nil || uint64(st.Dev) != device {
			_ = unix.Close(fd)
			return nil, errors.New("manifest parent changed filesystem")
		}
	}
	return os.NewFile(uintptr(fd), filepath.Join(root.Name(), filepath.Dir(rel))), nil
}

var errManagedEntryChanged = errors.New("reviewed entry changed; preserved")

func managedEntryOpenError(err error) error {
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("%w: %w", errManagedEntryChanged, err)
	}
	return err
}

func managedRemoveEntry(ctx context.Context, root *os.File, entry managedEntry, claimName string) (bool, string, error) {
	parent, err := managedRelativeParent(root, entry.Name, entry.Identity.Device)
	if err != nil {
		return false, "", managedEntryOpenError(err)
	}
	defer parent.Close()
	pfd := int(parent.Fd())
	name := filepath.Base(entry.Name)
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, "", managedEntryOpenError(err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var current unix.Stat_t
	if err := unix.Fstat(fd, &current); err != nil {
		return false, "", err
	}
	if managedStat(current) != entry.Identity {
		return false, "", errManagedEntryChanged
	}
	if err := platformDuplicateGuard(f); err != nil {
		return false, "", err
	}
	if err := unix.Mkdirat(pfd, claimName, 0o700); err != nil {
		return false, "", err
	}
	defer func() { _ = unix.Unlinkat(pfd, claimName, unix.AT_REMOVEDIR) }()
	claim, err := unix.Openat(pfd, claimName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, "", err
	}
	defer unix.Close(claim)
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	if err := unix.Fstatat(pfd, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, "", managedEntryOpenError(err)
	}
	if managedStat(current) != entry.Identity {
		return false, "", errManagedEntryChanged
	}
	if err := renameNoReplace(pfd, name, claim, "entry"); err != nil {
		return false, "", managedEntryOpenError(err)
	}
	restore := func(cause error) (bool, string, error) {
		if err := renameNoReplace(claim, "entry", pfd, name); err != nil {
			return false, filepath.Join(parent.Name(), claimName, "entry"), fmt.Errorf("%w; restore failed: %w", cause, err)
		}
		if err := unix.Fsync(claim); err != nil {
			return false, filepath.Join(parent.Name(), name), fmt.Errorf("restore synchronization failed: %w", err)
		}
		if err := unix.Fsync(pfd); err != nil {
			return false, filepath.Join(parent.Name(), name), fmt.Errorf("restore synchronization failed: %w", err)
		}
		return false, "", cause
	}
	var claimed unix.Stat_t
	if err := unix.Fstatat(claim, "entry", &claimed, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return restore(err)
	}
	got, want := managedStat(claimed), entry.Identity
	// Rename changes ctime. Every other reviewed field must still match.
	got.Ctime = want.Ctime
	if got != want {
		return restore(errManagedEntryChanged)
	}
	if err := unix.Fsync(claim); err != nil {
		return restore(err)
	}
	if err := unix.Fsync(pfd); err != nil {
		return restore(err)
	}
	if err := ctx.Err(); err != nil {
		return restore(err)
	}
	var final unix.Stat_t
	if err := unix.Fstatat(claim, "entry", &final, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return restore(err)
	}
	if managedStat(final) != managedStat(claimed) {
		return restore(errManagedEntryChanged)
	}
	if err := platformDuplicateGuard(f); err != nil {
		return restore(err)
	}
	if err := unix.Unlinkat(claim, "entry", 0); err != nil {
		return restore(err)
	}
	if err := unix.Fsync(claim); err != nil {
		return true, "", err
	}
	return true, "", unix.Fsync(pfd)
}

func managedFilesystemObservations(paths map[uint64]string) []workstationFilesystem {
	result := []workstationFilesystem{}
	for device, path := range paths {
		r := workstationFilesystem{Device: device, Path: path}
		var identity unix.Stat_t
		var fs unix.Statfs_t
		if err := unix.Stat(path, &identity); err != nil || uint64(identity.Dev) != device {
			r.Reason = "filesystem path is missing or changed"
		} else if err := unix.Statfs(path, &fs); err != nil || fs.Bsize <= 0 || uint64(fs.Bavail) > math.MaxUint64/uint64(fs.Bsize) {
			r.Reason = "filesystem observation unavailable"
		} else {
			free := uint64(fs.Bavail) * uint64(fs.Bsize)
			r.AvailableBytes = &free
		}
		result = append(result, r)
	}
	return result
}

type managedNativeOps struct {
	busy    func(context.Context, managedTarget) error
	qualify func(context.Context, string) (*managedTool, error)
	run     func(context.Context, managedTarget, *managedTool) (managedCommandResult, error)
}

func executeManagedPlan(ctx context.Context, p managedPlan, digest string, o managedExecuteOptions) (managedExecutionReport, error) {
	return executeManagedPlanWithNative(ctx, p, digest, o, managedNativeOps{managedBusy, qualifyManagedTool, runManagedNative})
}

func executeManagedPlanWithNative(ctx context.Context, p managedPlan, digest string, o managedExecuteOptions, native managedNativeOps) (r managedExecutionReport, resultErr error) {
	r = managedExecutionReport{SchemaVersion: 1, Contract: "managed", PlanSHA256: digest, DryRun: !o.Execute, Results: []managedResult{}, Limitation: "Managed observations do not prove global inactivity. New consumers and concurrent writes remain possible. Filesystem changes are not attributed reclaim. Native operations have no per-file deletion journal. Journal synchronization does not guarantee recovery after power loss."}
	devices := map[uint64]string{}
	defer func() {
		if resultErr != nil {
			r.Error = resultErr.Error()
		}
		if len(devices) > 0 && !managedUncertain(resultErr) {
			r.FilesystemAfter = managedFilesystemObservations(devices)
		}
	}()
	persist := func(item managedResult) error {
		if o.Journal == nil {
			return nil
		}
		if err := json.NewEncoder(o.Journal).Encode(item); err != nil {
			return fmt.Errorf("write managed journal: %w", err)
		}
		if err := o.Journal.Sync(); err != nil {
			return fmt.Errorf("sync managed journal: %w", err)
		}
		return nil
	}
	record := func(item managedResult) error { r.Results = append(r.Results, item); return persist(item) }

	if err := validateManagedPlan(p, o.MandatoryPolicy); err != nil {
		return r, err
	}
	if o.Limit < 0 {
		return r, errors.New("limit cannot be negative")
	}
	if o.Execute && (len(digest) != 64 || digest != o.ExpectedDigest || len(o.Targets) == 0 || o.Journal == nil) {
		return r, errors.New("execution requires the exact reviewed SHA-256 and explicit target selections and a private journal")
	}
	selected := map[string]bool{}
	for _, path := range o.Targets {
		if selected[path] {
			return r, errors.New("duplicate target selection")
		}
		selected[path] = true
	}
	for path := range selected {
		found := false
		for _, target := range p.Targets {
			if target.Path == path {
				found = true
			}
		}
		if !found {
			return r, errors.New("selected target is absent from the reviewed plan")
		}
	}
	root, err := openPlainDirectory(p.Root, false)
	if err != nil {
		return r, err
	}
	defer root.Close()
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &rootStat); err != nil || !managedDirectoryMatches(rootStat, p.RootIdentity) {
		return r, errors.New("managed root identity changed")
	}
	devices[p.RootIdentity.Device] = p.Root
	for _, t := range p.Targets {
		if t.Identity.Inode != 0 && (len(selected) == 0 || selected[t.Path]) {
			if _, ok := devices[t.Identity.Device]; !ok {
				devices[t.Identity.Device] = t.Path
			}
		}
	}
	r.FilesystemBefore = managedFilesystemObservations(devices)
	if o.Execute {
		if err := persist(managedResult{Status: "begin", Reason: "managed contract; reviewed plan SHA-256=" + digest}); err != nil {
			return r, err
		}
	}
	actions := 0
	failed := false
	for _, target := range p.Targets {
		if len(selected) != 0 && !selected[target.Path] {
			continue
		}
		if o.Limit > 0 && actions >= o.Limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if target.State != "planned" {
			if err := record(managedResult{Target: target.Path, Status: target.State, Reason: target.Reason}); err != nil {
				return r, err
			}
			failed = failed || target.State == "blocked"
			continue
		}
		dir, err := managedOpenTarget(target)
		if err != nil {
			if e := record(managedResult{Target: target.Path, Status: "blocked", Reason: err.Error()}); e != nil {
				return r, e
			}
			failed = true
			continue
		}
		if !o.Execute {
			if err := record(managedResult{Target: target.Path, Status: "would_apply", Reason: "dry run; consumer observation and execution remain required"}); err != nil {
				_ = dir.Close()
				return r, err
			}
			_ = dir.Close()
			continue
		}
		if err := native.busy(ctx, target); err != nil {
			_ = dir.Close()
			if managedUncertain(err) {
				return r, errors.Join(err, record(managedResult{Target: target.Path, Status: "blocked", Reason: err.Error()}))
			}
			if e := record(managedResult{Target: target.Path, Status: "blocked", Reason: err.Error()}); e != nil {
				return r, e
			}
			failed = true
			continue
		}
		if target.Kind == "go_build" || target.Kind == "npm" {
			if err := verifyManagedTool(target.Kind, target.Tool); err != nil {
				_ = dir.Close()
				failed = true
				if e := record(managedResult{Target: target.Path, Status: "blocked", Reason: err.Error()}); e != nil {
					return r, e
				}
				continue
			}
			qualified, err := native.qualify(ctx, target.Kind)
			if managedUncertain(err) {
				_ = dir.Close()
				return r, errors.Join(err, record(managedResult{Target: target.Path, Status: "blocked", Reason: err.Error()}))
			}
			if err != nil || !reflect.DeepEqual(qualified, target.Tool) {
				if err := record(managedResult{Target: target.Path, Status: "blocked", Reason: "native qualification or executable identity differs"}); err != nil {
					_ = dir.Close()
					return r, err
				}
				_ = dir.Close()
				failed = true
				continue
			}
			current, err := managedOpenTarget(target)
			if err != nil {
				_ = dir.Close()
				return r, err
			}
			_ = current.Close()
			_, before, measureErr := cacheWalk(ctx, dir, target.Identity.Device, cacheGuardEntry(target.Kind, target.Path))
			if measureErr != nil {
				if err := record(managedResult{Target: target.Path, Status: "blocked", Reason: measureErr.Error()}); err != nil {
					_ = dir.Close()
					return r, err
				}
				_ = dir.Close()
				failed = true
				continue
			}
			if err := persist(managedResult{Target: target.Path, Status: "intent", Reason: "native manager operation, not a file whitelist"}); err != nil {
				_ = dir.Close()
				return r, err
			}
			result, runErr := native.run(ctx, target, target.Tool)
			actions++
			item := managedResult{Target: target.Path, Status: "native_completed", Command: &result, BeforeFiles: &before}
			if managedUncertain(runErr) {
				item.Status = "partial"
				item.Reason = runErr.Error() + "; changes may be partial"
				_ = dir.Close()
				return r, errors.Join(runErr, record(item))
			}
			afterDir, afterErr := openPlainDirectory(target.Path, false)
			var after int64
			if afterErr == nil {
				_, after, afterErr = cacheWalk(ctx, afterDir, target.Identity.Device, nil)
				_ = afterDir.Close()
			} else if errors.Is(afterErr, os.ErrNotExist) {
				afterErr = nil
			}
			if afterErr == nil {
				item.AfterFiles = &after
				if after < before {
					r.Changed++
				}
			}
			if runErr != nil || afterErr != nil {
				item.Status = "partial"
				item.Reason = fmt.Sprintf("native error: %v; post-measurement: %v; changes may be partial", runErr, afterErr)
				failed = true
			}
			if err := record(item); err != nil {
				_ = dir.Close()
				return r, err
			}
			_ = dir.Close()
			continue
		}
		if _, _, err := cacheWalk(ctx, dir, target.Identity.Device, managedProjectGuard); err != nil {
			_ = dir.Close()
			failed = true
			if e := record(managedResult{Target: target.Path, Status: "blocked", Reason: err.Error()}); e != nil {
				return r, e
			}
			continue
		}
		for _, entry := range target.Entries {
			if o.Limit > 0 && actions >= o.Limit {
				break
			}
			actions++
			if err := ctx.Err(); err != nil {
				_ = dir.Close()
				return r, err
			}
			current, err := managedOpenTarget(target)
			if err != nil {
				_ = dir.Close()
				return r, err
			}
			_ = current.Close()
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				_ = dir.Close()
				return r, err
			}
			claimName := ".tidyshare-managed-" + hex.EncodeToString(random[:])
			claimPath := filepath.Join(target.Path, filepath.Dir(entry.Name), claimName, "entry")
			if err := persist(managedResult{Target: target.Path, Name: entry.Name, Status: "intent", RecoveryPath: claimPath}); err != nil {
				_ = dir.Close()
				return r, err
			}
			changed, recovery, err := managedRemoveEntry(ctx, dir, entry, claimName)
			item := managedResult{Target: target.Path, Name: entry.Name, Status: "skipped", RecoveryPath: recovery}
			if changed {
				item.Status = "removed"
				r.Changed++
			}
			if err != nil {
				item.Reason = err.Error()
				if changed || recovery != "" || !errors.Is(err, errManagedEntryChanged) {
					item.Status = "partial"
					failed = true
				}
			}
			if err := record(item); err != nil {
				_ = dir.Close()
				return r, err
			}
		}
		_ = dir.Close()
	}
	if failed {
		return r, errors.New("selected managed operations were blocked or partial; inspect the report")
	}
	return r, nil
}
