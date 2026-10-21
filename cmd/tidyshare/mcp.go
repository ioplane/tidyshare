package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpOutputLimit = 8 << 20

type mcpOptions struct {
	root, out               string
	allowCleanup            bool
	imageOwner, imageSocket string
}
type mcpOperations struct {
	options           mcpOptions
	rootInfo, outInfo os.FileInfo
	executable        string
}
type mcpScanInput struct {
	Path    string `json:"path,omitempty" jsonschema:"directory within the startup root; omitted means the root"`
	Formats string `json:"formats,omitempty" jsonschema:"comma-separated json,jsonl,md; omitted means all three"`
	MinSize int64  `json:"min_size,omitempty" jsonschema:"minimum duplicate file size in bytes; zero selects 1 MiB"`
}
type mcpPlanInput struct {
	Formats string `json:"formats,omitempty" jsonschema:"comma-separated json,jsonl,md; omitted means all three"`
}
type mcpCleanupInput struct {
	PlanPath string `json:"plan_path" jsonschema:"reviewed plan within the startup root or report directory"`
	Execute  bool   `json:"execute,omitempty" jsonschema:"perform cleanup; requires startup -allow-cleanup; false means dry run"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum actions; zero means all"`
	Formats  string `json:"formats,omitempty" jsonschema:"comma-separated json,jsonl,md; omitted means all three"`
}
type mcpOperationResult struct {
	Summary   string   `json:"summary"`
	ReportDir string   `json:"report_dir"`
	Reports   []string `json:"reports"`
	Execute   bool     `json:"execute"`
}

func runMCP(args []string) error {
	var o mcpOptions
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.StringVar(&o.root, "root", "", "allowed scan and duplicate-cleanup root (required)")
	flags.StringVar(&o.out, "out", "", "private report parent directory (required)")
	flags.BoolVar(&o.allowCleanup, "allow-cleanup", false, "allow tools to request cleanup explicitly")
	flags.StringVar(&o.imageOwner, "image-owner", "root", "fixed owner of the Podman image store")
	flags.StringVar(&o.imageSocket, "image-socket", "", "fixed local Unix Podman API socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("mcp accepts no positional arguments")
	}
	operations, err := newMCPOperations(o)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return operations.server().Run(ctx, &mcp.StdioTransport{MaxLineLength: 1 << 20})
}

func newMCPOperations(o mcpOptions) (*mcpOperations, error) {
	if o.root == "" || o.out == "" {
		return nil, errors.New("mcp requires -root and -out")
	}
	var err error
	o.root, err = filepath.Abs(o.root)
	if err != nil {
		return nil, err
	}
	o.out, err = filepath.Abs(o.out)
	if err != nil {
		return nil, err
	}
	if mcpWithin(o.root, o.out) || mcpWithin(o.out, o.root) {
		return nil, errors.New("mcp data root and report directory must not overlap")
	}
	rootInfo, err := plainPath(o.root, false)
	if err != nil || !rootInfo.IsDir() {
		return nil, errors.New("mcp root must be an existing directory without symlink components")
	}
	if _, err := plainPath(o.out, true); err != nil {
		return nil, errors.New("mcp report path has unsafe components")
	}
	if err := os.MkdirAll(o.out, 0o700); err != nil {
		return nil, fmt.Errorf("create mcp report directory: %w", err)
	}
	outInfo, err := plainPath(o.out, false)
	if err != nil || !outInfo.IsDir() {
		return nil, errors.New("mcp report path must be a directory")
	}
	if o.imageOwner == "" {
		o.imageOwner = "root"
	}
	if o.imageSocket == "" {
		owner, err := user.Lookup(o.imageOwner)
		if err != nil {
			return nil, errors.New("mcp image owner is unknown")
		}
		o.imageSocket = "unix:///run/podman/podman.sock"
		if owner.Uid != "0" {
			o.imageSocket = "unix://" + filepath.Join("/run/user", owner.Uid, "podman/podman.sock")
		}
	}
	if _, err := socketPath(o.imageSocket); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &mcpOperations{options: o, rootInfo: rootInfo, outInfo: outInfo, executable: executable}, nil
}

func (o *mcpOperations) server() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "tidyshare", Version: version}, nil)
	closed, nonDestructive, destructive := false, false, true
	// Planning tools create private reports; annotations describe their additive effect.
	planning := &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}
	cleanup := &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed}
	mcp.AddTool(server, &mcp.Tool{Name: "scan", Description: "Scan an allowed directory read-only and write private reports and a duplicate plan", Annotations: planning}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpScanInput) (*mcp.CallToolResult, mcpOperationResult, error) {
		result, err := o.scan(ctx, in)
		return mcpReply(result, err)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "plan_images", Description: "Plan unnamed unused images in the fixed owner store; no images removed", Annotations: planning}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpPlanInput) (*mcp.CallToolResult, mcpOperationResult, error) {
		result, err := o.planImages(ctx, in)
		return mcpReply(result, err)
	})
	for _, kind := range []string{"duplicates", "images"} {
		mcp.AddTool(server, &mcp.Tool{Name: "cleanup_" + kind, Description: "Validate a reviewed " + kind + " plan; dry run unless execute=true and startup permits cleanup", Annotations: cleanup}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpCleanupInput) (*mcp.CallToolResult, mcpOperationResult, error) {
			result, err := o.cleanup(ctx, kind, in)
			return mcpReply(result, err)
		})
	}
	return server
}

func mcpReply(result mcpOperationResult, err error) (*mcp.CallToolResult, mcpOperationResult, error) {
	if result.Reports == nil {
		result.Reports = []string{}
	}
	if err != nil {
		result.Summary = err.Error()
		return &mcp.CallToolResult{IsError: true}, result, nil
	}
	return nil, result, nil
}

func mcpWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (o *mcpOperations) anchors() error {
	for _, anchor := range []struct {
		path string
		info os.FileInfo
	}{{o.options.root, o.rootInfo}, {o.options.out, o.outInfo}} {
		now, err := plainPath(anchor.path, false)
		if err != nil || !os.SameFile(anchor.info, now) {
			return errors.New("mcp configured directory changed; restart after review")
		}
	}
	return nil
}

func (o *mcpOperations) scan(ctx context.Context, in mcpScanInput) (mcpOperationResult, error) {
	if err := o.anchors(); err != nil {
		return mcpOperationResult{}, err
	}
	path := in.Path
	if path == "" {
		path = o.options.root
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(o.options.root, path)
	}
	if filepath.Clean(path) != path || !mcpWithin(o.options.root, path) {
		return mcpOperationResult{}, errors.New("scan path is outside the configured root")
	}
	info, err := plainPath(path, false)
	if err != nil || !info.IsDir() {
		return mcpOperationResult{}, errors.New("scan path must be a directory without symlink components")
	}
	if in.MinSize < 0 {
		return mcpOperationResult{}, errors.New("min_size must be non-negative")
	}
	minSize := in.MinSize
	if minSize == 0 {
		minSize = 1 << 20
	}
	return o.run(ctx, "scan", false, in.Formats, "-root", path, "-skip", o.options.out, "-images=false", "-min-size", strconv.FormatInt(minSize, 10))
}

func (o *mcpOperations) planImages(ctx context.Context, in mcpPlanInput) (mcpOperationResult, error) {
	return o.run(ctx, "images-plan", false, in.Formats, "-owner", o.options.imageOwner, "-socket", o.options.imageSocket)
}

func (o *mcpOperations) cleanup(ctx context.Context, kind string, in mcpCleanupInput) (mcpOperationResult, error) {
	if in.Execute && !o.options.allowCleanup {
		return mcpOperationResult{}, errors.New("cleanup execution is disabled; restart with -allow-cleanup after approval")
	}
	if in.Limit < 0 {
		return mcpOperationResult{}, errors.New("limit must be non-negative")
	}
	if err := o.anchors(); err != nil {
		return mcpOperationResult{}, err
	}
	path := in.PlanPath
	if filepath.Clean(path) != path || !filepath.IsAbs(path) || (!mcpWithin(o.options.root, path) && !mcpWithin(o.options.out, path)) {
		return mcpOperationResult{}, errors.New("plan must be an absolute path within the configured root or report directory")
	}
	info, err := plainPath(path, false)
	if err != nil || !info.Mode().IsRegular() || info.Size() > mcpOutputLimit {
		return mcpOperationResult{}, errors.New("plan must be a bounded regular file without symlink components")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return mcpOperationResult{}, errors.New("cannot open plan")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return mcpOperationResult{}, errors.New("plan changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, mcpOutputLimit+1))
	if err != nil || len(body) > mcpOutputLimit {
		return mcpOperationResult{}, errors.New("cannot read bounded plan")
	}
	// Snapshot the bounded input before parsing; validation and the child see identical bytes.
	dir, err := o.newReportDir()
	if err != nil {
		return mcpOperationResult{}, err
	}
	planPath := filepath.Join(dir, "reviewed-plan")
	reserved, closePlan, err := reserveReports(dir, []string{"reviewed-plan"})
	if err != nil {
		return mcpOperationResult{}, errors.New("cannot reserve reviewed plan")
	}
	defer closePlan()
	if _, err := reserved["reviewed-plan"].Write(body); err != nil {
		return mcpOperationResult{}, errors.New("cannot save reviewed plan")
	}
	if err := reserved["reviewed-plan"].Sync(); err != nil {
		return mcpOperationResult{}, errors.New("cannot sync reviewed plan")
	}
	if kind == "duplicates" {
		groups, _, err := readPlan(bytes.NewReader(body))
		if err != nil {
			return mcpOperationResult{}, errors.New("invalid duplicate plan")
		}
		for _, rows := range groups {
			for _, row := range rows {
				if !mcpWithin(o.options.root, row.path) || mcpWithin(o.options.out, row.path) {
					return mcpOperationResult{}, errors.New("duplicate plan contains a source outside the configured data root")
				}
				if _, err := plainPath(row.path, row.act == "dup"); err != nil {
					return mcpOperationResult{}, errors.New("duplicate plan contains an unsafe or missing source")
				}
			}
		}
	} else {
		var plan imageCleanupPlan
		if err := json.Unmarshal(body, &plan); err != nil || plan.Owner != o.options.imageOwner || plan.Endpoint != o.options.imageSocket {
			return mcpOperationResult{}, errors.New("image plan does not match the configured owner and endpoint")
		}
		if err := validateImageCleanupPlan(plan); err != nil {
			return mcpOperationResult{}, errors.New("invalid image cleanup plan")
		}
	}
	args := []string{"-kind", kind, "-plan", planPath, "-limit", strconv.Itoa(in.Limit)}
	if in.Execute {
		args = append(args, "-yes")
	}
	return o.runIn(ctx, dir, "cleanup", in.Execute, in.Formats, args...)
}

func (o *mcpOperations) newReportDir() (string, error) {
	if err := o.anchors(); err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp(o.options.out, "mcp-")
	if err != nil {
		return "", errors.New("cannot create private report directory")
	}
	return directory, nil
}

func (o *mcpOperations) run(ctx context.Context, command string, execute bool, formats string, args ...string) (mcpOperationResult, error) {
	directory, err := o.newReportDir()
	if err != nil {
		return mcpOperationResult{}, err
	}
	return o.runIn(ctx, directory, command, execute, formats, args...)
}

func (o *mcpOperations) runIn(ctx context.Context, directory, command string, execute bool, formats string, args ...string) (mcpOperationResult, error) {
	if formats == "" {
		formats = "json,jsonl,md"
	}
	for _, format := range strings.Split(formats, ",") {
		if format != "json" && format != "jsonl" && format != "md" {
			return mcpOperationResult{}, errors.New("formats must contain only json,jsonl,md")
		}
	}
	if err := o.anchors(); err != nil {
		return mcpOperationResult{}, err
	}
	args = append([]string{command}, args...)
	args = append(args, "-out", directory, "-formats", formats)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	child := exec.CommandContext(ctx, o.executable, args...)
	child.Cancel = func() error { return child.Process.Signal(syscall.SIGTERM) }
	child.WaitDelay = 5 * time.Second
	var stdout, stderr mcpBoundedBuffer
	stdout.limit = mcpOutputLimit
	stderr.limit = 64 << 10
	child.Stdout = &stdout
	child.Stderr = &stderr
	runErr := child.Run()
	result := mcpOperationResult{Summary: command + " completed", ReportDir: directory, Reports: []string{}, Execute: execute}
	if command == "cleanup" && !execute {
		result.Summary = "cleanup dry run completed; no data removed"
	}
	entries, listErr := os.ReadDir(directory)
	if listErr == nil {
		for _, entry := range entries {
			if entry.Type().IsRegular() && entry.Name() != "reviewed-plan" {
				result.Reports = append(result.Reports, filepath.Join(directory, entry.Name()))
			}
		}
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return result, errors.New("operation cancelled or timed out; review private reports before retrying cleanup")
		}
		return result, errors.New("operation failed; inspect private reports locally")
	}
	if stdout.exceeded || stderr.exceeded {
		return result, errors.New("operation output exceeded safety limit; inspect private reports locally")
	}
	if listErr != nil {
		return result, errors.New("cannot list private reports")
	}
	return result, nil
}

type mcpBoundedBuffer struct {
	data     []byte
	limit    int
	exceeded bool
}

func (b *mcpBoundedBuffer) Write(data []byte) (int, error) {
	count := len(data)
	available := b.limit - len(b.data)
	if len(data) > available {
		b.exceeded = true
		data = data[:available]
	}
	b.data = append(b.data, data...)
	return count, nil
}
