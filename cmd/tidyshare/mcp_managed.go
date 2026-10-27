package main

import (
	"context"
	"errors"
	"path/filepath"
)

type mcpManagedPlanInput struct {
	Path           string   `json:"path,omitempty" jsonschema:"user data anchor inside the startup root; omitted means that root"`
	Kinds          []string `json:"kinds,omitempty" jsonschema:"selected categories; omitted means all managed categories"`
	IncludeRuntime bool     `json:"include_runtime,omitempty" jsonschema:"record the fixed Docker source; prune remains blocked"`
}

type mcpManagedCleanInput struct {
	PlanPath   string   `json:"plan_path" jsonschema:"reviewed managed plan inside the startup root or report directory"`
	PlanSHA256 string   `json:"plan_sha256,omitempty" jsonschema:"exact reviewed plan digest; required for execution"`
	Targets    []string `json:"targets,omitempty" jsonschema:"exact target paths from the reviewed plan; required for execution"`
	Execute    bool     `json:"execute,omitempty" jsonschema:"perform changes; also requires startup allow-cleanup"`
	Limit      int      `json:"limit,omitempty" jsonschema:"maximum file entries or native actions; zero means all"`
}

func (o *mcpOperations) planManaged(ctx context.Context, in mcpManagedPlanInput) (mcpOperationResult, error) {
	root, err := o.dataDirectory(in.Path)
	if err != nil {
		return mcpOperationResult{}, err
	}
	if o.options.policy.excludes(root) {
		return mcpOperationResult{}, errors.New("managed root is protected")
	}
	options := managedPlanOptions{Root: root, Kinds: in.Kinds, Policy: o.options.policy}
	if in.IncludeRuntime {
		if o.docker == nil {
			return mcpOperationResult{}, errors.New("managed runtime observation requires fixed Docker startup configuration")
		}
		options.Docker = o.docker
	}
	p, err := createManagedPlan(ctx, options)
	if err != nil {
		return mcpOperationResult{}, err
	}
	if err := o.anchors(); err != nil {
		return mcpOperationResult{}, err
	}
	directory, err := o.newReportDir()
	if err != nil {
		return mcpOperationResult{}, err
	}
	digest, err := writeManagedPlanReports(directory, p)
	return mcpOperationResult{Summary: "managed plan created; blocked categories remain blocked; review the separate contract", ReportDir: directory, Reports: managedReportNames(directory, "managed-plan.json", "managed-plan.md"), PlanSHA256: digest}, err
}

func (o *mcpOperations) cleanManaged(ctx context.Context, in mcpManagedCleanInput) (mcpOperationResult, error) {
	if err := o.anchors(); err != nil {
		return mcpOperationResult{}, err
	}
	if in.Execute && !o.options.allowCleanup {
		return mcpOperationResult{}, errors.New("managed execution requires startup allow-cleanup")
	}
	path := in.PlanPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(o.options.out, path)
	}
	if filepath.Clean(path) != path || (!mcpWithin(o.options.root, path) && !mcpWithin(o.options.out, path)) || o.options.policy.overlaps(path) {
		return mcpOperationResult{}, errors.New("managed plan is outside the permitted scope or protected")
	}
	p, data, err := readManagedPlanData(path)
	if err != nil {
		return mcpOperationResult{}, err
	}
	if _, err := o.dataDirectory(p.Root); err != nil {
		return mcpOperationResult{}, err
	}
	if err := validateManagedPlan(p, o.options.policy); err != nil {
		return mcpOperationResult{}, err
	}
	directory, err := o.newReportDir()
	if err != nil {
		return mcpOperationResult{}, err
	}
	_, err = managedCleanReports(ctx, directory, p, data, managedExecuteOptions{Execute: in.Execute, ExpectedDigest: in.PlanSHA256, Targets: in.Targets, Limit: in.Limit, MandatoryPolicy: o.options.policy})
	return mcpOperationResult{Summary: "managed operation finished; inspect statuses and partial changes", ReportDir: directory, Reports: managedReportNames(directory, "managed-cleanup.json", "managed-cleanup.md", "managed-results.jsonl", "reviewed-plan.json"), PlanSHA256: managedDigest(data), Execute: in.Execute}, err
}
