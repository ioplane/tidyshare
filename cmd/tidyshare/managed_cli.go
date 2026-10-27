package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func readManagedPlanData(path string) (managedPlan, []byte, error) {
	var p managedPlan
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return p, nil, errors.New("plan path must be clean and absolute")
	}
	parent, err := openPlainDirectory(filepath.Dir(path), false)
	if err != nil {
		return p, nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return p, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > managedPlanLimit {
		return p, nil, errors.New("managed plan must be a regular file of at most 64 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, managedPlanLimit+1))
	if err != nil || len(data) > managedPlanLimit {
		return p, nil, errors.New("managed plan exceeds the read bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return p, nil, errors.New("managed plan contains trailing data")
	}
	if err := validateManagedPlan(p, storagePolicy{}); err != nil {
		return p, nil, err
	}
	return p, data, nil
}

func managedDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func readManagedPlan(path string) (managedPlan, string, error) {
	p, data, err := readManagedPlanData(path)
	if err != nil {
		return p, "", err
	}
	return p, managedDigest(data), nil
}

func managedReportDirectory(out string, p managedPlan) error {
	if !filepath.IsAbs(out) || filepath.Clean(out) != out || cacheWithin(out, p.Root) {
		return errors.New("report directory must be clean and must not contain the managed root")
	}
	for _, target := range p.Targets {
		if target.Kind != "docker_build" && (cacheWithin(target.Path, out) || cacheWithin(out, target.Path)) {
			return errors.New("report directory overlaps a managed target")
		}
	}
	if st, err := plainPath(out, true); err != nil {
		return err
	} else if st.Name() == filepath.Base(out) {
		entries, err := os.ReadDir(out)
		if err != nil || !st.IsDir() || st.Mode().Perm()&0o077 != 0 || len(entries) != 0 {
			return errors.New("report directory must be new or empty and private")
		}
	}
	return nil
}

func writeManagedPlanReports(out string, p managedPlan) (string, error) {
	if err := managedReportDirectory(out, p); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if len(data) > managedPlanLimit {
		return "", errors.New("serialized managed plan exceeds 64 MiB")
	}
	files, closeFiles, err := reserveReports(out, []string{"managed-plan.json", "managed-plan.md"})
	if err != nil {
		return "", err
	}
	defer closeFiles()
	if _, err := files["managed-plan.json"].Write(data); err != nil {
		return "", err
	}
	if err := files["managed-plan.json"].Sync(); err != nil {
		return "", err
	}
	digest := managedDigest(data)
	w := files["managed-plan.md"]
	if _, err := fmt.Fprintf(w, "# Managed cleanup plan\n\nSHA-256: `%s`\n\nRoot: %q. Platform: %s. Contract: managed.\n\nThis plan does not establish global inactivity. A new consumer can appear after observation.\nNative operations use manager scope. File manifests use best-effort revalidation.\nSessions and applications are not stopped automatically.\n\n", digest, p.Root, p.Platform); err != nil {
		return "", err
	}
	for _, target := range p.Targets {
		if _, err := fmt.Fprintf(w, "- Kind: %s. Target: %q. State: %s. Entries: %d. Skipped during planning: %d. Scope: %s. Reason: %s\n", target.Kind, target.Path, target.State, len(target.Entries), target.Skipped, target.Scope, target.Reason); err != nil {
			return "", err
		}
		if target.Tool != nil {
			if _, err := fmt.Fprintf(w, "  Manager: %q. Version: %q. SHA-256: %s. Node: %q, SHA-256: %s.\n", target.Tool.Path, target.Tool.Version, target.Tool.SHA256, target.Tool.NodePath, target.Tool.NodeSHA256); err != nil {
				return "", err
			}
		}
	}
	return digest, nil
}

func managedCleanReports(ctx context.Context, out string, p managedPlan, data []byte, o managedExecuteOptions) (managedExecutionReport, error) {
	if err := managedReportDirectory(out, p); err != nil {
		return managedExecutionReport{}, err
	}
	files, closeFiles, err := reserveReports(out, []string{"managed-cleanup.json", "managed-cleanup.md", "managed-results.jsonl", "reviewed-plan.json"})
	if err != nil {
		return managedExecutionReport{}, err
	}
	defer closeFiles()
	if _, err := files["reviewed-plan.json"].Write(data); err != nil {
		return managedExecutionReport{}, err
	}
	if err := files["reviewed-plan.json"].Sync(); err != nil {
		return managedExecutionReport{}, err
	}
	if err := syncManagedReportDirectory(out); err != nil {
		return managedExecutionReport{}, err
	}
	o.Journal = files["managed-results.jsonl"]
	r, runErr := executeManagedPlan(ctx, p, managedDigest(data), o)
	if err := json.NewEncoder(files["managed-cleanup.json"]).Encode(r); err != nil {
		return r, errors.Join(runErr, err)
	}
	if err := files["managed-cleanup.json"].Sync(); err != nil {
		return r, errors.Join(runErr, err)
	}
	if err := writeManagedCleanupMarkdown(files["managed-cleanup.md"], r); err != nil {
		return r, errors.Join(runErr, err)
	}

	return r, runErr
}

func runManagedPlan(args []string) int {
	f := flag.NewFlagSet("managed-plan", flag.ContinueOnError)
	profile, protected := policyFlags(f)
	root := f.String("root", "", "user data root (required)")
	out := f.String("out", "", "new private report directory (required)")
	engine := f.String("engine", "", "optional runtime observation: docker")
	dockerContext := f.String("docker-context", "", "local Docker context; requires engine=docker")
	var kinds pathFlags
	f.Var(&kinds, "kind", "managed category; repeat to select categories")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	if *root == "" || *out == "" || f.NArg() != 0 {
		return fail(errors.New("managed-plan requires -root and -out without positional arguments"))
	}
	var err error
	*root, err = filepath.Abs(*root)
	if err != nil {
		return fail(err)
	}
	*out, err = filepath.Abs(*out)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *engine != "" && *engine != "docker" || *dockerContext != "" && *engine != "docker" {
		return fail(errors.New("managed runtime observation requires engine=docker"))
	}
	options := managedPlanOptions{Root: *root, Kinds: kinds, Policy: storagePolicy{Profile: *profile, Protected: *protected}}
	if *engine == "docker" {
		options.Docker, err = resolveDockerStore(ctx, *dockerContext)
		if err != nil {
			return fail(err)
		}
	}
	p, err := createManagedPlan(ctx, options)
	if err != nil {
		return fail(err)
	}
	digest, err := writeManagedPlanReports(*out, p)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "managed plan SHA-256: %s; inspect target states before execution\n", digest)
	return 0
}

func runManagedClean(args []string) int {
	f := flag.NewFlagSet("managed-clean", flag.ContinueOnError)
	plan := f.String("plan", "", "reviewed managed-plan.json (required)")
	out := f.String("out", "", "new private report directory (required)")
	o := managedExecuteOptions{}
	f.BoolVar(&o.Execute, "yes", false, "execute explicitly selected targets; default is dry run")
	f.StringVar(&o.ExpectedDigest, "plan-sha256", "", "exact SHA-256 of the reviewed plan")
	f.IntVar(&o.Limit, "limit", 0, "maximum manifest entries or native operations; zero means all")
	var targets pathFlags
	f.Var(&targets, "target", "exact reviewed target path; repeat to select targets")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	if *plan == "" || *out == "" || f.NArg() != 0 {
		return fail(errors.New("managed-clean requires -plan and -out without positional arguments"))
	}
	var err error
	*plan, err = filepath.Abs(*plan)
	if err != nil {
		return fail(err)
	}
	*out, err = filepath.Abs(*out)
	if err != nil {
		return fail(err)
	}
	p, data, err := readManagedPlanData(*plan)
	if err != nil {
		return fail(err)
	}
	o.Targets = targets
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	_, err = managedCleanReports(ctx, *out, p, data, o)
	if err != nil {
		return fail(err)
	}
	return 0
}

func managedReportNames(directory string, names ...string) []string {
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(directory, strings.TrimSpace(name)))
	}
	return paths
}

// Synchronize new directory entries up to the existing filesystem boundary.
func syncManagedReportDirectory(out string) error {
	var device uint64
	for current := out; ; current = filepath.Dir(current) {
		dir, err := openPlainDirectory(current, false)
		if err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
			return errors.Join(err, dir.Close())
		}
		if device != 0 && uint64(st.Dev) != device {
			return dir.Close()
		}
		device = uint64(st.Dev)
		err = errors.Join(dir.Sync(), dir.Close())
		if err != nil {
			return fmt.Errorf("synchronize managed report directory: %w", err)
		}
		if current == "/" {
			return nil
		}
	}
}

func writeManagedCleanupMarkdown(w io.Writer, r managedExecutionReport) error {
	if _, err := fmt.Fprintf(w, "# Managed cleanup results\n\nPlan SHA-256: `%s`. Dry run: %t. Changed actions: %d.\n\n%s\n\nOperation error: %s\n\n", r.PlanSHA256, r.DryRun, r.Changed, r.Limitation, r.Error); err != nil {
		return err
	}
	count := func(v *int64) string {
		if v == nil {
			return "unknown"
		}
		return fmt.Sprint(*v)
	}
	for _, item := range r.Results {
		if _, err := fmt.Fprintf(w, "- Target: %q. Entry: %q. Status: %s. Reason: %s. Recovery: %q.\n", item.Target, item.Name, item.Status, item.Reason, item.RecoveryPath); err != nil {
			return err
		}
		if item.Command != nil {
			if _, err := fmt.Fprintf(w, "  Native exit code: %d. Files before: %s. Files after: %s.\n", item.Command.ExitCode, count(item.BeforeFiles), count(item.AfterFiles)); err != nil {
				return err
			}
		}
	}
	for _, set := range []struct {
		name    string
		entries []workstationFilesystem
	}{{"before", r.FilesystemBefore}, {"after", r.FilesystemAfter}} {
		if _, err := fmt.Fprintf(w, "\nFilesystem observations %s:\n", set.name); err != nil {
			return err
		}
		for _, f := range set.entries {
			value := "unknown"
			if f.AvailableBytes != nil {
				value = fmt.Sprint(*f.AvailableBytes)
			}
			if _, err := fmt.Fprintf(w, "- Device: %d. Path: %q. Available bytes: %s. Reason: %s.\n", f.Device, f.Path, value, f.Reason); err != nil {
				return err
			}
		}
	}
	return nil
}
