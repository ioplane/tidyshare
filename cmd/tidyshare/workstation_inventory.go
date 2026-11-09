package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type workstationOptions struct {
	Root         string
	ProjectRoots []string
	Policy       storagePolicy
	MinFreeBytes uint64
	Previous     *workstationReport
	Runtime      *workstationRuntimeSource
}

type workstationRuntimeSource struct {
	Engine string
	Docker *dockerStore
}

func (s workstationRuntimeSource) observe(ctx context.Context) (workstationRuntime, error) {
	r := workstationRuntime{Engine: s.Engine, State: "partial"}
	var err error
	switch s.Engine {
	case "docker":
		if s.Docker == nil {
			err = errors.New("the Docker source is unavailable")
			break
		}
		_, r.Summary, err = s.Docker.inventory(ctx)
		s.Docker.mu.Lock()
		r.Identity = s.Docker.DaemonID
		s.Docker.mu.Unlock()
		if err == nil && strings.Contains(r.Summary, "disk usage: unavailable") {
			err = errors.New("the Docker disk usage is unavailable")
		}
	case "podman":
		r.State = "unsupported"
		err = errors.New("the Podman workstation runtime is unsupported until socket identity and caller cancellation are qualified. Use the existing images command")
	default:
		err = errors.New("unsupported workstation runtime")
	}
	if err != nil {
		r.Reason = err.Error()
		return r, err
	}
	r.State = "complete"
	return r, nil
}

type workstationCategory struct {
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	State          string `json:"state"`
	Reason         string `json:"reason,omitempty"`
	Operation      string `json:"proposed_operation"`
	LogicalBytes   *int64 `json:"logical_bytes"`
	AllocatedBytes *int64 `json:"allocated_bytes"`
	Files          int64  `json:"files"`
	Device         uint64 `json:"device,omitempty"`
	Inode          uint64 `json:"inode,omitempty"`
	LogicalDelta   *int64 `json:"logical_delta,omitempty"`
	AllocatedDelta *int64 `json:"allocated_delta,omitempty"`
}

type workstationFilesystem struct {
	Device         uint64  `json:"device"`
	Path           string  `json:"path"`
	AvailableBytes *uint64 `json:"available_bytes"`
	BudgetBytes    uint64  `json:"budget_bytes,omitempty"`
	BelowBudget    *bool   `json:"below_budget,omitempty"`
	Reason         string  `json:"reason,omitempty"`
	AvailableDelta *int64  `json:"available_delta,omitempty"`
}

type workstationComparison struct {
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason"`
}

type workstationRuntime struct {
	Engine   string `json:"engine"`
	State    string `json:"state"`
	Identity string `json:"identity,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type workstationReport struct {
	SchemaVersion int                     `json:"schema_version"`
	Operation     string                  `json:"operation"`
	Host          string                  `json:"host"`
	Platform      string                  `json:"platform"`
	Architecture  string                  `json:"architecture"`
	ObservedAt    time.Time               `json:"observed_at"`
	Root          string                  `json:"root"`
	RootDevice    uint64                  `json:"root_device"`
	RootInode     uint64                  `json:"root_inode"`
	ProjectRoots  []string                `json:"project_roots"`
	Policy        storagePolicy           `json:"policy"`
	Categories    []workstationCategory   `json:"categories"`
	GitTrees      []gitTree               `json:"git_trees"`
	Filesystems   []workstationFilesystem `json:"filesystems"`
	Runtime       workstationRuntime      `json:"runtime"`
	Comparison    workstationComparison   `json:"comparison"`
	Complete      bool                    `json:"complete"`
	Warnings      []string                `json:"warnings"`
}

func collectWorkstation(ctx context.Context, o workstationOptions) (r workstationReport, resultErr error) {
	r = workstationReport{SchemaVersion: 1, Operation: "workstation", Platform: runtime.GOOS, Architecture: runtime.GOARCH, ObservedAt: time.Now().UTC(), Root: o.Root, ProjectRoots: append([]string{}, o.ProjectRoots...), Categories: []workstationCategory{}, GitTrees: []gitTree{}, Filesystems: []workstationFilesystem{}, Warnings: []string{}, Runtime: workstationRuntime{State: "not_requested"}, Comparison: workstationComparison{Reason: "no previous report"}}
	defer func() {
		if resultErr != nil {
			r.Complete = false
			if len(r.Warnings) < 1000 {
				r.Warnings = append(r.Warnings, resultErr.Error())
			}
		}
	}()
	r.Host, _ = os.Hostname()
	if !filepath.IsAbs(o.Root) || filepath.Clean(o.Root) != o.Root {
		return r, errors.New("workstation root must be a clean absolute directory")
	}
	root, err := openPlainDirectory(o.Root, false)
	if err != nil {
		return r, err
	}
	defer root.Close()
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &rootStat); err != nil {
		return r, err
	}
	r.RootDevice, r.RootInode = uint64(rootStat.Dev), rootStat.Ino
	if o.Policy.Profile == "" {
		o.Policy.Profile = "workstation"
	}
	r.Policy, err = newStoragePolicy(o.Policy.Profile, o.Policy.Protected)
	if err != nil {
		return r, err
	}
	// The data anchor can describe another user root, independently of this process HOME.
	for _, suffix := range workstationProtectedSuffixes {
		r.Policy.Protected = append(r.Policy.Protected, filepath.Join(o.Root, suffix))
	}
	sort.Strings(r.Policy.Protected)
	r.Policy.Protected = uniqueStrings(r.Policy.Protected)
	sort.Strings(r.ProjectRoots)
	if err := validateWorkstationProjects(o.Root, r.ProjectRoots, r.Policy); err != nil {
		return r, err
	}
	collectWorkstationObservations(ctx, o, &r)
	compareWorkstation(&r, o.Previous)
	if err := ctx.Err(); err != nil {
		workstationWarning(&r, err.Error())
		return r, err
	}
	if !r.Complete {
		return r, errors.New("workstation inventory is partial; inspect report warnings")
	}
	return r, nil
}

func uniqueStrings(values []string) []string {
	out := values[:0]
	for _, v := range values {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

func compareWorkstation(r, previous *workstationReport) {
	if previous == nil {
		return
	}
	if previous.SchemaVersion != 1 || previous.Operation != "workstation" || previous.Host != r.Host || previous.Platform != r.Platform || previous.Architecture != r.Architecture || previous.Root != r.Root || previous.RootDevice != r.RootDevice || previous.RootInode != r.RootInode || !reflect.DeepEqual(previous.ProjectRoots, r.ProjectRoots) || !reflect.DeepEqual(previous.Policy, r.Policy) || !previous.ObservedAt.Before(r.ObservedAt) {
		r.Comparison = workstationComparison{Reason: "previous report has incompatible identity, roots, policy or time"}
		return
	}
	r.Comparison = workstationComparison{Compatible: true, Reason: "compatible observations; changes are not attributed to cleanup"}
	compareWorkstationCategories(r.Categories, previous.Categories)
	compareWorkstationFilesystems(r.Filesystems, previous.Filesystems)
}

func readWorkstationPrevious(path string) (*workstationReport, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("previous report must have a clean absolute path")
	}
	parent, err := openPlainDirectory(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 8<<20 {
		return nil, errors.New("previous report must be a regular file of at most 8 MiB")
	}
	var r workstationReport
	decoder := json.NewDecoder(io.LimitReader(f, 8<<20))
	if err := decoder.Decode(&r); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("previous report contains trailing data")
	}
	return &r, nil
}

func writeWorkstationReports(out string, r workstationReport) error {
	files, closeFiles, err := reserveReports(out, []string{"workstation.json", "workstation.md"})
	if err != nil {
		return err
	}
	defer closeFiles()
	if err := json.NewEncoder(files["workstation.json"]).Encode(r); err != nil {
		return err
	}
	var text strings.Builder
	escape := func(s string) string { return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(s) }
	value := func(v *int64) string {
		if v == nil {
			return "unknown"
		}
		return fmt.Sprint(*v)
	}
	fmt.Fprintf(&text, "# Workstation inventory\n\nHost: %s. Platform: %s/%s. Observed: %s. Complete: %t.\n\nRoot: %s. Policy: %s.\n\nSizes are observations, not predicted recovered space. Categories can overlap.\nDo not add filesystem and Docker figures. Consumer inactivity is not established.\n\n", escape(r.Host), r.Platform, r.Architecture, r.ObservedAt.Format(time.RFC3339), r.Complete, escape(r.Root), r.Policy.Profile)
	fmt.Fprintln(&text, "## Protected roots")
	for _, path := range r.Policy.Protected {
		fmt.Fprintf(&text, "\n- %s", escape(path))
	}
	fmt.Fprintln(&text, "\n\n## Explicit project roots")
	for _, path := range r.ProjectRoots {
		fmt.Fprintf(&text, "\n- %s", escape(path))
	}
	fmt.Fprintln(&text, "\n\n## Git trees\n\n| Path | Kind | Branch | HEAD | Locked | Prunable | Missing | Reason |\n|---|---|---|---|---|---|---|---|")
	for _, tree := range r.GitTrees {
		fmt.Fprintf(&text, "| %s | %s | %s | %s | %t | %t | %t | %s |\n", escape(tree.Path), tree.Kind, escape(tree.Branch), escape(tree.Head), tree.Locked, tree.Prunable, tree.Missing, escape(tree.Reason))
	}
	fmt.Fprintln(&text, "\n## Cache categories\n\n| Kind | Path | State | Files | Logical bytes | Allocated bytes | Logical delta | Allocated delta | Reason |\n|---|---|---|---:|---:|---:|---:|---:|---|")
	for _, c := range r.Categories {
		fmt.Fprintf(&text, "| %s | %s | %s | %d | %s | %s | %s | %s | %s |\n", c.Kind, escape(c.Path), c.State, c.Files, value(c.LogicalBytes), value(c.AllocatedBytes), value(c.LogicalDelta), value(c.AllocatedDelta), escape(c.Reason))
	}
	fmt.Fprintln(&text, "\n## Filesystems\n\n| Device | Path | Available bytes | Budget bytes | Below budget | Available delta | Reason |\n|---|---|---:|---:|---|---:|---|")
	for _, f := range r.Filesystems {
		free, below := "unknown", "not configured"
		if f.AvailableBytes != nil {
			free = fmt.Sprint(*f.AvailableBytes)
		}
		if f.BelowBudget != nil {
			below = fmt.Sprint(*f.BelowBudget)
		}
		fmt.Fprintf(&text, "| %d | %s | %s | %d | %s | %s | %s |\n", f.Device, escape(f.Path), free, f.BudgetBytes, below, value(f.AvailableDelta), escape(f.Reason))
	}
	fmt.Fprintf(&text, "\nComparison: %s\n\n## Runtime\n\nEngine: %s. State: %s. Identity: %s.\n\n%s\n\n%s\n\n## Warnings\n", escape(r.Comparison.Reason), r.Runtime.Engine, r.Runtime.State, escape(r.Runtime.Identity), escape(r.Runtime.Reason), escape(r.Runtime.Summary))
	for _, warning := range r.Warnings {
		fmt.Fprintf(&text, "\n- %s", escape(warning))
	}
	_, err = io.WriteString(files["workstation.md"], text.String()+"\n")
	return err
}

func runWorkstation(args []string) int {
	f := flag.NewFlagSet("workstation", flag.ContinueOnError)
	profile, protected := policyFlags(f)
	var o workstationOptions
	var projects pathFlags
	f.StringVar(&o.Root, "root", "", "user data anchor (required)")
	f.Var(&projects, "project-root", "explicit project directory; repeat for multiple roots")
	out := f.String("out", "", "new private report directory (required)")
	previous := f.String("previous", "", "previous workstation.json to compare")
	f.Uint64Var(&o.MinFreeBytes, "min-free-bytes", 0, "optional filesystem budget; zero disables the budget")
	engine := f.String("engine", "", "optional runtime: docker or podman")
	dockerContext := f.String("docker-context", "", "local Docker context")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	if o.Root == "" || *out == "" || f.NArg() != 0 {
		return fail(errors.New("workstation requires -root and -out without positional arguments"))
	}
	var err error
	o.Root, err = filepath.Abs(o.Root)
	if err != nil {
		return fail(err)
	}
	*out, err = filepath.Abs(*out)
	if err != nil {
		return fail(err)
	}
	if cacheWithin(*out, o.Root) {
		return fail(errors.New("report directory must not contain the data root"))
	}
	if err := prepareWorkstationPaths(&o, projects, *out); err != nil {
		return fail(err)
	}
	if err := configureWorkstationOptions(&o, *profile, *protected, *previous, *engine, *dockerContext); err != nil {
		return fail(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	o.Runtime = configureWorkstationRuntime(ctx, *engine, *dockerContext)
	r, scanErr := collectWorkstation(ctx, o)
	if err := validateWorkstationCategoryOutput(*out, r.Categories); err != nil {
		return fail(err)
	}
	if err := writeWorkstationReports(*out, r); err != nil {
		return fail(errors.Join(scanErr, err))
	}
	if scanErr != nil {
		return fail(scanErr)
	}
	return 0
}

func validateWorkstationProjects(root string, projects []string, policy storagePolicy) error {
	for i, path := range projects {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || !cacheWithin(root, path) || policy.overlaps(path) && policy.excludes(path) {
			return errors.New("project root is outside the anchor or protected")
		}
		for _, ancestor := range projects[:i] {
			if cacheWithin(ancestor, path) || cacheWithin(path, ancestor) {
				return errors.New("project roots overlap")
			}
		}
		if st, err := plainPath(path, false); err != nil || !st.IsDir() {
			return errors.New("project root has unsafe or unavailable components")
		}
	}
	return nil
}

func observeWorkstationFilesystem(path string, device, budget uint64, warn func(string)) workstationFilesystem {
	fs := workstationFilesystem{Device: device, Path: path, BudgetBytes: budget}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		fs.Reason = err.Error()
		warn(path + ": filesystem observation unavailable")
	} else if uint64(st.Bavail) > math.MaxUint64/uint64(st.Bsize) {
		fs.Reason = "filesystem byte count overflows"
		warn(fs.Reason)
	} else {
		free := uint64(st.Bavail) * uint64(st.Bsize)
		fs.AvailableBytes = &free
		if budget != 0 {
			below := free < budget
			fs.BelowBudget = &below
		}
	}
	return fs
}

func workstationCachePaths(root string) map[string]string {
	paths := map[string]string{}
	for _, layout := range cacheLayouts {
		if runtime.GOOS != "darwin" && strings.HasPrefix(layout.suffix, "Library/") {
			continue
		}
		paths[filepath.Join(root, layout.suffix)] = layout.kind
	}
	if runtime.GOOS == "darwin" {
		paths[filepath.Join(root, "Library/Caches/Google/Chrome")] = "chrome"
	}
	addWorkstationEnvironmentCaches(root, paths)
	return paths
}

func addWorkstationEnvironmentCaches(root string, paths map[string]string) {
	// Only recognized current environment settings can supply additional cache roots.
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "PIP_CACHE_DIR", "UV_CACHE_DIR", "NPM_CONFIG_CACHE", "npm_config_cache", "CARGO_HOME", "GRADLE_USER_HOME", "NUGET_PACKAGES", "NUGET_HTTP_CACHE_PATH", "XDG_CACHE_HOME", "XDG_DATA_HOME", "GOPATH"} {
		for _, base := range filepath.SplitList(os.Getenv(key)) {
			if !filepath.IsAbs(base) {
				continue
			}
			addWorkstationEnvironmentBase(root, base, paths)
		}
	}
}

func discoverWorkstationProjects(ctx context.Context, r *workstationReport, paths map[string]string, observeFS func(string, uint64), warn func(string)) {
	for _, path := range r.ProjectRoots {
		if ctx.Err() != nil {
			break
		}
		project, err := openPlainDirectory(path, false)
		if err != nil {
			warn(path + ": " + err.Error())
			continue
		}
		var st unix.Stat_t
		err = unix.Fstat(int(project.Fd()), &st)
		_ = project.Close()
		if err != nil {
			warn(path + ": " + err.Error())
			continue
		}
		observeFS(path, uint64(st.Dev))
		d, err := discoverStorageWithPolicy(ctx, path, "", r.Policy)
		r.GitTrees = append(r.GitTrees, d.GitTrees...)
		for _, w := range d.Warnings {
			warn(w)
		}
		if err != nil {
			warn(path + ": " + err.Error())
		}
		for _, c := range d.Caches {
			paths[c.Path] = c.Kind
		}
	}
}

func observeWorkstationCategory(ctx context.Context, path, kind string, policy storagePolicy, observeFS func(string, uint64), warn func(string)) workstationCategory {
	c := workstationCategory{Kind: kind, Path: path, State: "absent", Operation: "inventory_only"}
	switch c.Kind {
	case "go_build", "npm", "homebrew", "chrome", "xcode":
		c.Operation = "managed_plan"
	}
	if policy.overlaps(path) {
		c.State = "protected"
		c.Reason = "storage policy excludes this target"
		return c
	}
	if err := ctx.Err(); err != nil {
		c.State = "partial"
		c.Reason = err.Error()
		warn(c.Reason)
		return c
	}
	if _, err := plainPath(path, false); errors.Is(err, os.ErrNotExist) {
		return c
	}
	dir, err := openPlainDirectory(path, false)
	if err != nil {
		c.State = "partial"
		c.Reason = err.Error()
		warn(path + ": " + c.Reason)
		return c
	}
	var st unix.Stat_t
	err = unix.Fstat(int(dir.Fd()), &st)
	if err == nil {
		c.Device, c.Inode = uint64(st.Dev), st.Ino
		observeFS(path, c.Device)
		err = measureWorkstationCategory(ctx, dir, &c)
	}
	_ = dir.Close()
	if err != nil {
		c.State = "partial"
		c.Reason = err.Error()
		warn(path + ": " + c.Reason)
	}
	return c
}

func measureWorkstationCategory(ctx context.Context, dir *os.File, c *workstationCategory) error {
	var err error
	var logical, allocated int64
	seen := map[[2]uint64]bool{}
	_, _, err = cacheWalk(ctx, dir, c.Device, func(_ *os.File, _ string, s unix.Stat_t) error {
		if s.Mode&unix.S_IFMT != unix.S_IFREG {
			return nil
		}
		c.Files++
		key := [2]uint64{uint64(s.Dev), s.Ino}
		if seen[key] {
			return nil
		}
		seen[key] = true
		if s.Size < 0 || s.Blocks < 0 || s.Blocks > math.MaxInt64/512 || logical > math.MaxInt64-s.Size || allocated > math.MaxInt64-s.Blocks*512 {
			return errors.New("category byte count overflows")
		}
		logical += s.Size
		allocated += s.Blocks * 512
		return nil
	})
	if err == nil {
		c.LogicalBytes = &logical
		c.AllocatedBytes = &allocated
		c.State = "complete"
	}
	return err
}

func compareWorkstationCategories(categories, previous []workstationCategory) {
	for i := range categories {
		c := &categories[i]
		for _, p := range previous {
			if c.Path == p.Path && c.Kind == p.Kind && c.Device == p.Device && c.Inode == p.Inode && c.State == "complete" && p.State == "complete" && c.LogicalBytes != nil && p.LogicalBytes != nil && c.AllocatedBytes != nil && p.AllocatedBytes != nil && *c.LogicalBytes >= 0 && *p.LogicalBytes >= 0 && *c.AllocatedBytes >= 0 && *p.AllocatedBytes >= 0 {
				d := *c.LogicalBytes - *p.LogicalBytes
				a := *c.AllocatedBytes - *p.AllocatedBytes
				c.LogicalDelta = &d
				c.AllocatedDelta = &a
				break
			}
		}
	}
}

func compareWorkstationFilesystems(filesystems, previous []workstationFilesystem) {
	for i := range filesystems {
		f := &filesystems[i]
		for _, p := range previous {
			if f.Device == p.Device && f.AvailableBytes != nil && p.AvailableBytes != nil && *f.AvailableBytes <= math.MaxInt64 && *p.AvailableBytes <= math.MaxInt64 {
				d := int64(*f.AvailableBytes) - int64(*p.AvailableBytes)
				f.AvailableDelta = &d
				break
			}
		}
	}
}

func prepareWorkstationPaths(o *workstationOptions, projects []string, out string) error {
	for _, path := range projects {
		p, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if cacheWithin(p, out) || cacheWithin(out, p) {
			return errors.New("report directory overlaps a project root")
		}
		o.ProjectRoots = append(o.ProjectRoots, p)
	}
	for _, layout := range cacheLayouts {
		p := filepath.Join(o.Root, layout.suffix)
		if cacheWithin(p, out) || cacheWithin(out, p) {
			return errors.New("report directory overlaps a cache root")
		}
	}
	if _, err := plainPath(out, true); err != nil {
		return err
	}
	if st, err := os.Stat(out); err == nil {
		entries, e := os.ReadDir(out)
		if e != nil || !st.IsDir() || st.Mode().Perm()&0o077 != 0 || len(entries) != 0 {
			return errors.New("report directory must be new or empty and private")
		}
	}
	return nil
}

func collectWorkstationObservations(ctx context.Context, o workstationOptions, r *workstationReport) {
	var err error
	r.Complete = true
	warn := func(reason string) { workstationWarning(r, reason) }
	devices := map[uint64]bool{}
	observeFS := func(path string, device uint64) {
		if devices[device] {
			return
		}
		devices[device] = true
		fs := observeWorkstationFilesystem(path, device, o.MinFreeBytes, warn)
		r.Filesystems = append(r.Filesystems, fs)
	}
	observeFS(o.Root, r.RootDevice)
	paths := workstationCachePaths(o.Root)
	discoverWorkstationProjects(ctx, r, paths, observeFS, warn)
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		r.Categories = append(r.Categories, observeWorkstationCategory(ctx, path, paths[path], r.Policy, observeFS, warn))
	}
	sort.SliceStable(r.Categories, func(i, j int) bool {
		a, b := r.Categories[i], r.Categories[j]
		if a.AllocatedBytes == nil {
			return false
		}
		if b.AllocatedBytes == nil {
			return true
		}
		return *a.AllocatedBytes > *b.AllocatedBytes
	})
	if o.Runtime != nil {
		r.Runtime, err = o.Runtime.observe(ctx)
		if err != nil {
			warn(err.Error())
		}
	}
}

func configureWorkstationOptions(o *workstationOptions, profile string, protected []string, previous, engine, dockerContext string) error {
	var err error
	if profile == "" {
		profile = "workstation"
	}
	o.Policy = storagePolicy{Profile: profile, Protected: protected}
	if previous != "" {
		o.Previous, err = readWorkstationPrevious(previous)
		if err != nil {
			return err
		}
	}
	if engine != "" && engine != "docker" && engine != "podman" {
		return errors.New("engine must be docker or podman")
	}
	if dockerContext != "" && engine != "docker" {
		return errors.New("docker-context requires engine=docker")
	}
	return nil
}

func configureWorkstationRuntime(ctx context.Context, engine, dockerContext string) *workstationRuntimeSource {
	var source *workstationRuntimeSource
	var err error
	if engine != "" {
		source = &workstationRuntimeSource{Engine: engine}
		if engine == "docker" {
			source.Docker, err = resolveDockerStore(ctx, dockerContext)
			// Preserve current filesystem observations when the optional daemon is unavailable.
			if err != nil {
				source.Docker = nil
			}
		}
	}
	return source
}

func workstationWarning(r *workstationReport, reason string) {
	r.Complete = false
	if len(r.Warnings) < 1000 {
		r.Warnings = append(r.Warnings, reason)
	}
}

func validateWorkstationCategoryOutput(out string, categories []workstationCategory) error {
	for _, c := range categories {
		if cacheWithin(c.Path, out) || cacheWithin(out, c.Path) {
			return errors.New("report directory overlaps an inventory category")
		}
	}
	return nil
}

func addWorkstationEnvironmentBase(root, base string, paths map[string]string) {
	candidates := []string{base, filepath.Join(base, "_cacache"), filepath.Join(base, "pkg/mod")}
	for _, layout := range cacheLayouts {
		candidates = append(candidates, filepath.Join(base, filepath.Base(layout.suffix)))
	}
	candidates = append(candidates, filepath.Join(base, "registry/cache"), filepath.Join(base, "caches"), filepath.Join(base, "pnpm/store"), filepath.Join(base, "NuGet/v3-cache"))
	for _, path := range candidates {
		if cacheWithin(root, path) {
			if kind := cacheKind(path, false); kind != "" {
				paths[path] = kind
			}
		}
	}
}
