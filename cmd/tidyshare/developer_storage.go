package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
)

type discoveryReport struct {
	SchemaVersion int           `json:"schema_version"`
	Operation     string        `json:"operation"`
	Root          string        `json:"root"`
	Caches        []cacheTarget `json:"caches"`
	GitTrees      []gitTree     `json:"git_trees"`
	Warnings      []string      `json:"warnings"`
	Policy        storagePolicy `json:"policy,omitempty"`
}

func discoverStorage(ctx context.Context, root, out string) (discoveryReport, error) {
	return discoverStorageWithPolicy(ctx, root, out, storagePolicy{})
}

func discoverStorageWithPolicy(ctx context.Context, root, out string, policy storagePolicy) (discoveryReport, error) {
	report := discoveryReport{SchemaVersion: 1, Operation: "discover", Root: root, Caches: []cacheTarget{}, GitTrees: []gitTree{}, Warnings: []string{}}
	report.Policy = policy
	trees := map[string]gitTree{}
	warn := func(path string, err error) {
		if len(report.Warnings) < 1000 {
			report.Warnings = append(report.Warnings, path+": "+err.Error())
		} else if len(report.Warnings) == 1000 {
			report.Warnings = append(report.Warnings, "additional discovery warnings omitted")
		}
	}
	inspectGit := func(path string) bool {
		found, err := gitTreesAt(ctx, path, root)
		if err != nil {
			warn(path, err)
			return false
		}
		bare := false
		for _, tree := range found {
			trees[tree.Path] = tree
			bare = bare || (tree.Path == path && tree.Kind == "bare")
		}
		return bare
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if policy.excludes(path) {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			warn(path, walkErr)
			return nil
		}
		if !entry.IsDir() {
			if entry.Type().IsRegular() && (entry.Name() == ".git" || entry.Name() == "HEAD") {
				if inspectGit(filepath.Dir(path)) && entry.Name() == "HEAD" {
					return fs.SkipDir
				}
			}
			return nil
		}
		if path == out || policy.excludes(path) {
			return fs.SkipDir
		}
		if entry.Name() == ".git" && path != root {
			inspectGit(filepath.Dir(path))
			return fs.SkipDir
		}
		cache, err := cacheTargetAt(ctx, path)
		if err != nil {
			warn(path, err)
			return fs.SkipDir
		}
		if cache != nil && policy.overlaps(path) {
			return fs.SkipDir
		}
		if cache != nil {
			report.Caches = append(report.Caches, *cache)
			return fs.SkipDir
		}
		return nil
	})
	for _, tree := range trees {
		report.GitTrees = append(report.GitTrees, tree)
	}
	slices.SortFunc(report.GitTrees, func(a, b gitTree) int { return strings.Compare(a.Path, b.Path) })
	return report, err
}

func runDiscover(args []string) int {
	flags := flag.NewFlagSet("discover", flag.ContinueOnError)
	profile, protected := policyFlags(flags)
	root := flags.String("root", "", "directory to inspect for language caches and Git trees (required)")
	out := flags.String("out", "", "new report directory (required)")
	formatsValue := flags.String("formats", "json,jsonl,md", "comma-separated json,jsonl,md; cache-plan.json is always retained")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	if *root == "" || *out == "" || flags.NArg() != 0 {
		return fail(errors.New("discover requires -root and -out, without positional arguments"))
	}
	formats, err := parseReportFormats(*formatsValue)
	if err != nil {
		return fail(err)
	}
	*root, err = filepath.Abs(*root)
	if err != nil {
		return fail(err)
	}
	*out, err = filepath.Abs(*out)
	if err != nil {
		return fail(err)
	}
	info, err := plainPath(*root, false)
	if err != nil || !info.IsDir() {
		return fail(errors.New("discovery root must be a directory without symlink components"))
	}
	if *root == *out {
		return fail(errors.New("discovery output must differ from the input root"))
	}
	if _, err := plainPath(*out, true); err != nil {
		return fail(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	policy, err := newStoragePolicy(*profile, *protected)
	if err != nil {
		return fail(err)
	}
	report, walkErr := discoverStorageWithPolicy(ctx, *root, *out, policy)
	if err := writeDiscoveryReports(*out, formats, report); err != nil {
		return fail(errors.Join(walkErr, err))
	}
	if walkErr != nil {
		return fail(walkErr)
	}
	if len(report.Warnings) != 0 {
		return fail(errors.New("discovery incomplete; inspect report warnings before using the cache plan"))
	}
	fmt.Fprintf(os.Stderr, "discovery: caches=%d git_trees=%d; reports in %s\n", len(report.Caches), len(report.GitTrees), *out)
	return 0
}

func writeDiscoveryReports(out string, formats []string, report discoveryReport) error {
	for _, cache := range report.Caches {
		if cacheWithin(cache.Path, out) {
			return errors.New("reports must remain outside discovered caches")
		}
	}
	plan := cachePlan{SchemaVersion: 2, Platform: runtime.GOOS, Policy: report.Policy, Root: report.Root, Targets: []cacheTarget{}}
	for _, cache := range report.Caches {
		if cache.Cleanup {
			plan.Targets = append(plan.Targets, cache)
		}
	}
	if err := validateCachePlan(plan); err != nil {
		return err
	}
	if err := cachePlanLocations(plan, filepath.Join(out, "cache-plan.json"), out); err != nil {
		return err
	}
	names := []string{"cache-plan.json"}
	for _, format := range formats {
		names = append(names, "discovery."+format)
	}
	files, closeFiles, err := reserveReports(out, names)
	if err != nil {
		return err
	}
	defer closeFiles()
	if err := json.NewEncoder(files["cache-plan.json"]).Encode(plan); err != nil {
		return err
	}
	for _, format := range formats {
		if err := encodeDiscoveryReport(files["discovery."+format], format, report); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := file.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func encodeDiscoveryReport(w io.Writer, format string, report discoveryReport) error {
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(report)
	case "jsonl":
		encode := func(kind string, value any) error {
			return json.NewEncoder(w).Encode(struct {
				SchemaVersion int    `json:"schema_version"`
				Type          string `json:"type"`
				Data          any    `json:"data"`
			}{1, kind, value})
		}
		if err := encode("summary", struct {
			Operation string `json:"operation"`
			Root      string `json:"root"`
			Caches    int    `json:"caches"`
			GitTrees  int    `json:"git_trees"`
			Warnings  int    `json:"warnings"`
		}{"discover", report.Root, len(report.Caches), len(report.GitTrees), len(report.Warnings)}); err != nil {
			return err
		}
		for _, cache := range report.Caches {
			if err := encode("cache", cache); err != nil {
				return err
			}
		}
		for _, tree := range report.GitTrees {
			if err := encode("git_tree", tree); err != nil {
				return err
			}
		}
		for _, warning := range report.Warnings {
			if err := encode("warning", warning); err != nil {
				return err
			}
		}
		return nil
	case "md":
		text := bufio.NewWriter(w)
		fmt.Fprintf(text, "# Developer storage\n\nRoot: %s\n\n## Language caches\n\n| kind | path | logical bytes | files | cleanup | reason |\n| --- | --- | ---: | ---: | --- | --- |\n", markdownCell(report.Root))
		for _, cache := range report.Caches {
			fmt.Fprintf(text, "| %s | %s | %d | %d | %t | %s |\n", markdownCell(cache.Kind), markdownCell(cache.Path), cache.Bytes, cache.Files, cache.Cleanup, markdownCell(cache.Reason))
		}
		fmt.Fprint(text, "\n## Git trees\n\n| path | kind | branch | HEAD | locked | prunable | missing | reason |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, tree := range report.GitTrees {
			fmt.Fprintf(text, "| %s | %s | %s | %s | %t | %t | %t | %s |\n", markdownCell(tree.Path), markdownCell(tree.Kind), markdownCell(tree.Branch), markdownCell(tree.Head), tree.Locked, tree.Prunable, tree.Missing, markdownCell(tree.Reason))
		}
		if len(report.Warnings) != 0 {
			fmt.Fprint(text, "\n## Warnings\n\n")
			for _, warning := range report.Warnings {
				fmt.Fprintf(text, "- %s\n", markdownCell(warning))
			}
		}
		return text.Flush()
	default:
		return errors.New("unsupported discovery report format")
	}
}
