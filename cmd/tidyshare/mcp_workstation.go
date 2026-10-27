package main

import (
	"context"
	"errors"
	"path/filepath"
)

type mcpWorkstationInput struct {
	ProjectRoots   []string `json:"project_roots,omitempty" jsonschema:"explicit project directories inside the startup root"`
	Previous       string   `json:"previous,omitempty" jsonschema:"previous workstation.json inside the startup root or report directory"`
	MinFreeBytes   uint64   `json:"min_free_bytes,omitempty" jsonschema:"optional filesystem free-space budget in bytes"`
	IncludeRuntime bool     `json:"include_runtime,omitempty" jsonschema:"inspect the runtime fixed at startup"`
}

func (o *mcpOperations) inventoryWorkstation(ctx context.Context, in mcpWorkstationInput) (mcpOperationResult, error) {
	if err := o.anchors(); err != nil {
		return mcpOperationResult{}, err
	}
	options := workstationOptions{Root: o.options.root, Policy: o.options.policy, MinFreeBytes: in.MinFreeBytes}
	for _, path := range in.ProjectRoots {
		path, err := o.dataDirectory(path)
		if err != nil {
			return mcpOperationResult{}, err
		}
		if o.options.policy.excludes(path) {
			return mcpOperationResult{}, errors.New("project root is protected")
		}
		options.ProjectRoots = append(options.ProjectRoots, path)
	}
	if in.Previous != "" {
		path := in.Previous
		if !filepath.IsAbs(path) {
			path = filepath.Join(o.options.out, path)
		}
		if filepath.Clean(path) != path || (!mcpWithin(o.options.root, path) && !mcpWithin(o.options.out, path)) || o.options.policy.overlaps(path) {
			return mcpOperationResult{}, errors.New("previous report is outside the permitted scope or protected")
		}
		var err error
		options.Previous, err = readWorkstationPrevious(path)
		if err != nil {
			return mcpOperationResult{}, err
		}
	}
	if in.IncludeRuntime {
		options.Runtime = &workstationRuntimeSource{Engine: o.options.engine, Docker: o.docker}
	}
	report, scanErr := collectWorkstation(ctx, options)
	if err := o.anchors(); err != nil {
		return mcpOperationResult{}, err
	}
	directory, err := o.newReportDir()
	if err != nil {
		return mcpOperationResult{}, err
	}
	result := mcpOperationResult{Summary: "workstation inventory completed; consumer inactivity is not established", ReportDir: directory, Reports: []string{filepath.Join(directory, "workstation.json"), filepath.Join(directory, "workstation.md")}}
	if err := writeWorkstationReports(directory, report); err != nil {
		return result, errors.Join(scanErr, err)
	}
	return result, scanErr
}
