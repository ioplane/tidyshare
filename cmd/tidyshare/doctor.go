package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

type capability struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}
type filesystemCapability struct {
	capability
	FreeBytes *uint64 `json:"free_bytes,omitempty"`
}
type capabilityReport struct {
	Platform     string                `json:"platform"`
	Architecture string                `json:"architecture"`
	Filesystem   filesystemCapability  `json:"filesystem"`
	Dedupe       capability            `json:"dedupe"`
	CacheGuard   capability            `json:"cache_guard"`
	Runtime      capability            `json:"runtime"`
	Managers     map[string]capability `json:"managers"`
}

func hostPlatform() string { return runtime.GOOS }
func inspectCapabilities(ctx context.Context, root, engine, dockerContext string) capabilityReport {
	root, err := filepath.Abs(root)
	if err != nil {
		return capabilityReport{Filesystem: filesystemCapability{capability: capability{"unavailable", err.Error()}}}
	}
	r := capabilityReport{Platform: runtime.GOOS, Architecture: runtime.GOARCH, Managers: map[string]capability{}}
	var fs unix.Statfs_t
	if _, err := plainPath(root, false); err != nil {
		r.Filesystem.capability = capability{"unavailable", "root has unsafe or unavailable components"}
	} else if err := unix.Statfs(root, &fs); err != nil {
		r.Filesystem.capability = capability{"unavailable", err.Error()}
	} else {
		free := uint64(fs.Bavail) * uint64(fs.Bsize)
		r.Filesystem = filesystemCapability{capability{"supported", "free bytes are an observation, not a cleanup estimate"}, &free}
	}
	r.Dedupe = capability{"unsupported", "extent deduplication requires Linux FIDEDUPERANGE"}
	if runtime.GOOS == "linux" {
		r.Dedupe = capability{"unavailable", "Linux operation is available; apply checks filesystem support"}
	}
	if err := cacheMountGuard(root); err != nil {
		r.CacheGuard = capability{"unavailable", err.Error()}
	} else if err := cacheBusy(ctx, root, nil); err != nil {
		r.CacheGuard = capability{"unavailable", err.Error()}
	} else {
		r.CacheGuard = capability{"supported", "no current cache users observed; keep consumers stopped during cleanup"}
	}
	for _, manager := range []string{"go", "pip", "uv", "npm", "pnpm"} {
		if _, err := exec.LookPath(manager); err != nil {
			r.Managers[manager] = capability{"unavailable", "executable not found"}
		} else {
			r.Managers[manager] = capability{"supported", "execution also requires a validated plan and complete guards"}
		}
	}
	switch engine {
	case "docker":
		store, err := resolveDockerStore(ctx, dockerContext)
		if err == nil {
			_, _, err = store.inventory(ctx)
		}
		if err != nil {
			r.Runtime = capability{"unavailable", err.Error()}
		} else {
			r.Runtime = capability{"supported", "local Docker inventory only; cleanup is unsupported"}
		}
	case "podman":
		_, _, err := listLocal("", "root")
		if err != nil {
			r.Runtime = capability{"unavailable", err.Error()}
		} else {
			r.Runtime = capability{"supported", "Podman inventory available"}
		}
	default:
		r.Runtime = capability{"unsupported", "engine must be podman or docker"}
	}
	return r
}

func runDoctor(args []string) int {
	f := flag.NewFlagSet("doctor", flag.ContinueOnError)
	root := f.String("root", ".", "directory whose filesystem and guards are inspected")
	engine := f.String("engine", "podman", "image inventory provider")
	dockerContext := f.String("docker-context", "", "local Docker context")
	machine := f.Bool("json", false, "write JSON to stdout")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	if f.NArg() != 0 {
		return fail(errors.New("doctor accepts no positional arguments"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := inspectCapabilities(ctx, *root, *engine, *dockerContext)
	if *machine {
		if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
			return fail(err)
		}
	} else {
		fmt.Printf("platform: %s/%s\nfilesystem: %s (%s)\ncache guard: %s (%s)\ndedupe: %s (%s)\nruntime: %s (%s)\n", r.Platform, r.Architecture, r.Filesystem.Status, r.Filesystem.Reason, r.CacheGuard.Status, r.CacheGuard.Reason, r.Dedupe.Status, r.Dedupe.Reason, r.Runtime.Status, r.Runtime.Reason)
	}
	return 0
}
