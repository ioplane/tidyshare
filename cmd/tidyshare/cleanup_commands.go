package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func readImagePlan(path string) (imageCleanupPlan, error) {
	var plan imageCleanupPlan
	path, err := filepath.Abs(path)
	if err != nil {
		return plan, err
	}
	if _, err := plainPath(path, false); err != nil {
		return plan, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return plan, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return plan, err
	}
	if !st.Mode().IsRegular() || st.Size() > 8<<20 {
		return plan, errors.New("image plan must be a regular file no larger than 8 MiB")
	}
	d := json.NewDecoder(io.LimitReader(f, (8<<20)+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&plan); err != nil {
		return plan, fmt.Errorf("decode image plan: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return plan, errors.New("image plan contains trailing data")
	}
	return plan, validateImageCleanupPlan(plan)
}

func runCleanup(args []string) int {
	f := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	kind := f.String("kind", "", "duplicates or images (required)")
	plan := f.String("plan", "", "reviewed dups.tsv or image-plan.json (required)")
	out := f.String("out", "", "new report directory (required)")
	format := f.String("formats", "md", "comma-separated json,jsonl,md")
	execute := f.Bool("yes", false, "execute deletion (default: dry run)")
	limit := f.Int("limit", 0, "maximum reviewed targets to process, 0 = all")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	if (*kind != "duplicates" && *kind != "images") || *plan == "" || *out == "" || *limit < 0 || f.NArg() != 0 {
		return fail(errors.New("-kind duplicates|images, -plan and -out are required; limit must be non-negative; no positional arguments"))
	}
	formats, err := parseReportFormats(*format)
	if err != nil {
		return fail(err)
	}
	// Parse the entire input before reserving reports or making any deletion.
	var imagePlan imageCleanupPlan
	if *kind == "images" {
		imagePlan, err = readImagePlan(*plan)
	} else {
		_, _, err = readDuplicatePlan(*plan)
	}
	if err != nil {
		return fail(err)
	}
	finish, closeReports, err := prepareOperationReports(*out, formats)
	if err != nil {
		return fail(err)
	}
	defer closeReports()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var report cleanupReport
	if *kind == "images" {
		report, err = cleanupImages(ctx, imagePlan, *execute, *limit)
	} else {
		report, err = cleanupDuplicates(ctx, *plan, *execute, *limit)
	}
	reportErr := finish(report)
	if err := errors.Join(err, reportErr); err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "cleanup: changed=%d skipped=%d dry_run=%t; reports in %s\n", report.Changed, report.Skipped, report.DryRun, *out)
	return 0
}

func runImagePlan(args []string) int {
	f := flag.NewFlagSet("images-plan", flag.ContinueOnError)
	owner := f.String("owner", "root", "Podman store owner")
	endpoint := f.String("socket", "", "owner-bound local Unix API endpoint")
	out := f.String("out", "", "new report directory (required)")
	format := f.String("formats", "md", "comma-separated json,jsonl,md")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	if *owner == "" || *out == "" || f.NArg() != 0 {
		return fail(errors.New("owner and out are required; no positional arguments"))
	}
	formats, err := parseReportFormats(*format)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	plan, err := planImages(ctx, *owner, *endpoint)
	if err != nil {
		return fail(err)
	}
	files, closeFiles, err := reserveReports(*out, []string{"image-plan.json"})
	if err != nil {
		return fail(err)
	}
	defer closeFiles()
	finish, closeReports, err := prepareOperationReports(*out, formats)
	if err != nil {
		return fail(err)
	}
	defer closeReports()
	enc := json.NewEncoder(files["image-plan.json"])
	enc.SetIndent("", "  ")
	if err := enc.Encode(plan); err != nil {
		return fail(err)
	}
	if err := files["image-plan.json"].Sync(); err != nil {
		return fail(err)
	}
	report, err := cleanupImages(ctx, plan, false, 0)
	if reportErr := finish(report); reportErr != nil {
		err = errors.Join(err, reportErr)
	}
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "image cleanup plan in %s/image-plan.json\n", *out)
	return 0
}
