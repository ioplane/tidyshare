package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type managedTool struct {
	Path         string          `json:"path"`
	SHA256       string          `json:"sha256"`
	Identity     managedIdentity `json:"identity"`
	Version      string          `json:"version"`
	NodePath     string          `json:"node_path,omitempty"`
	NodeSHA256   string          `json:"node_sha256,omitempty"`
	NodeIdentity managedIdentity `json:"node_identity,omitempty"`
}

type managedCommandResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

func managedExecutable(path string) (string, string, managedIdentity, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", managedIdentity{}, err
	}
	parent, err := openPlainDirectory(filepath.Dir(resolved), false)
	if err != nil {
		return "", "", managedIdentity{}, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(resolved), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", "", managedIdentity{}, err
	}
	f := os.NewFile(uintptr(fd), resolved)
	defer f.Close()
	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return "", "", managedIdentity{}, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0o022 != 0 {
		return "", "", managedIdentity{}, errors.New("manager must be a regular file without group or world write access")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", managedIdentity{}, err
	}
	if err := unix.Fstat(fd, &after); err != nil || managedStat(before) != managedStat(after) {
		return "", "", managedIdentity{}, errors.New("manager changed during fingerprinting")
	}
	return resolved, hex.EncodeToString(h.Sum(nil)), managedStat(before), nil
}

func managedNativeEnv(scratch, cache string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + scratch, "XDG_CONFIG_HOME=" + scratch, "XDG_CACHE_HOME=" + scratch, "XDG_DATA_HOME=" + scratch, "LANG=C", "GOENV=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOTELEMETRY=off", "GOPATH=" + filepath.Join(scratch, "go"), "GOCACHE=" + cache}
}

func managedCommand(ctx context.Context, path string, args, env []string, dir string) (managedCommandResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second
	stdout, stderr := managedOutput{cancel: cancel}, managedOutput{cancel: cancel}
	stdout.limit = 64 << 10
	stderr.limit = 64 << 10
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	reaped, err := runOwnedCommand(ctx, cmd, managedProcessOps{})
	result := managedCommandResult{ExitCode: -1, Stdout: string(stdout.data), Stderr: string(stderr.data)}
	if reaped && cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if stdout.exceeded || stderr.exceeded {
		return result, errors.Join(err, errors.New("manager output exceeds the qualification bound"))
	}
	return result, err
}

func verifyManagedTool(kind string, tool *managedTool) error {
	if tool == nil {
		return errors.New("native manager is not qualified")
	}
	name := "go"
	if kind == "npm" {
		name = "npm"
	} else if kind != "go_build" {
		return errors.New("unsupported native managed kind")
	}
	current, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	path, digest, identity, err := managedExecutable(current)
	if err != nil {
		return err
	}
	if path != tool.Path || digest != tool.SHA256 || identity != tool.Identity {
		return errors.New("manager identity differs from the reviewed plan")
	}
	if kind == "npm" {
		node, err := exec.LookPath("node")
		if err != nil {
			return err
		}
		path, digest, identity, err := managedExecutable(node)
		if err != nil {
			return err
		}
		if path != tool.NodePath || digest != tool.NodeSHA256 || identity != tool.NodeIdentity {
			return errors.New("the Node interpreter identity differs from the reviewed plan")
		}
	}
	return nil
}

func runManagedNative(ctx context.Context, target managedTarget, tool *managedTool) (managedCommandResult, error) {
	return runManagedNativeWithRunner(ctx, target, tool, managedCommand)
}

type managedRunner func(context.Context, string, []string, []string, string) (managedCommandResult, error)

func runManagedNativeWithRunner(ctx context.Context, target managedTarget, tool *managedTool, run managedRunner) (outcome managedCommandResult, resultErr error) {
	if err := verifyManagedTool(target.Kind, tool); err != nil {
		return managedCommandResult{ExitCode: -1}, err
	}
	scratch, err := privateTemp("tidyshare-managed-command-")
	if err != nil {
		return managedCommandResult{ExitCode: -1}, err
	}
	defer func() {
		if managedUncertain(resultErr) {
			resultErr = retainManagedRoot(resultErr, scratch)
		} else {
			_ = os.RemoveAll(scratch)
		}
	}()
	args := []string{"clean", "-cache"}
	program := tool.Path
	if target.Kind == "npm" {
		program = tool.NodePath
		userConfig, globalConfig := filepath.Join(scratch, "user.npmrc"), filepath.Join(scratch, "global.npmrc")
		for _, path := range []string{userConfig, globalConfig} {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				return managedCommandResult{ExitCode: -1}, err
			}
		}
		args = []string{tool.Path, "cache", "clean", "--force", "--global", "--cache", filepath.Dir(target.Path), "--ignore-scripts", "--audit=false", "--fund=false", "--userconfig", userConfig, "--globalconfig", globalConfig, "--logs-dir", scratch}
	}
	return run(ctx, program, args, managedNativeEnv(scratch, target.Path), scratch)
}

func qualifyManagedTool(ctx context.Context, kind string) (*managedTool, error) {
	return qualifyManagedToolWithRunner(ctx, kind, managedCommand)
}

func qualifyManagedToolWithRunner(ctx context.Context, kind string, run managedRunner) (qualified *managedTool, resultErr error) {
	name := "go"
	if kind == "npm" {
		name = "npm"
	} else if kind != "go_build" {
		return nil, errors.New("unsupported native qualification")
	}
	command, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	tool := &managedTool{}
	tool.Path, tool.SHA256, tool.Identity, err = managedExecutable(command)
	if err != nil {
		return nil, err
	}
	scratch, err := privateTemp("tidyshare-managed-qualification-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if managedUncertain(resultErr) {
			resultErr = retainManagedRoot(resultErr, scratch)
		} else {
			_ = os.RemoveAll(scratch)
		}
	}()
	versionProgram, versionArgs := tool.Path, []string{"version"}
	if kind == "npm" {
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, err
		}
		tool.NodePath, tool.NodeSHA256, tool.NodeIdentity, err = managedExecutable(node)
		if err != nil {
			return nil, err
		}
		versionProgram, versionArgs = tool.NodePath, []string{tool.Path, "--version"}
	}
	version, err := run(ctx, versionProgram, versionArgs, managedNativeEnv(scratch, filepath.Join(scratch, "cache")), scratch)
	if err != nil {
		return nil, fmt.Errorf("native version unavailable: %w", err)
	}
	tool.Version = strings.TrimSpace(version.Stdout)
	if tool.Version == "" {
		return nil, errors.New("native version is empty")
	}
	cache := filepath.Join(scratch, "cache")
	seed := filepath.Join(cache, "00/fixture-a")
	if kind == "npm" {
		cache = filepath.Join(scratch, "npm/_cacache")
		seed = filepath.Join(cache, "content-v2/sha512/00/fixture")
	}
	if err := os.MkdirAll(filepath.Dir(seed), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(seed, []byte("owned-qualification-fixture"), 0o600); err != nil {
		return nil, err
	}
	outside := filepath.Join(scratch, "outside")
	if err := os.WriteFile(outside, []byte("preserve"), 0o600); err != nil {
		return nil, err
	}
	result, err := runManagedNativeWithRunner(ctx, managedTarget{Kind: kind, Path: cache}, tool, run)
	if err != nil {
		return nil, fmt.Errorf("private native cleanup fixture failed: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("private native cleanup fixture exited %d", result.ExitCode)
	}
	if _, err := os.Lstat(seed); !os.IsNotExist(err) {
		return nil, errors.New("native cleanup did not remove the owned fixture")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "preserve" {
		return nil, errors.New("native cleanup changed the outside fixture")
	}
	return tool, nil
}
