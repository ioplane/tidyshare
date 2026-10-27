package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Returning after only the leader exits leaves its private scratch consumer alive.
func TestManagedCommandStopsDescendants(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		timeout      time.Duration
		wantError    bool
	}{
		{"normal-devnull", `sleep 8 >/dev/null 2>&1 & echo $! > "$1"; echo ready`, time.Second, false},
		{"inherited-pipe", `sleep 8 & echo $! > "$1"; echo ready`, time.Second, false},
		{"deadline", `sleep 8 >/dev/null 2>&1 & echo $! > "$1"; wait`, 100 * time.Millisecond, true},
		{"output-bound", `sleep 8 >/dev/null 2>&1 & echo $! > "$1"; head -c 70000 /dev/zero`, time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "child")
			t.Cleanup(func() {
				if b, e := os.ReadFile(pidfile); e == nil {
					pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
					if pid > 0 {
						until := time.Now().Add(10 * time.Second)
						for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
							if time.Now().After(until) {
								t.Errorf("finite owned fixture remains: %d", pid)
								break
							}
							time.Sleep(10 * time.Millisecond)
						}
					}
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			result, err := managedCommand(ctx, "/bin/sh", []string{"-c", tc.script, "fixture", pidfile}, os.Environ(), "/")
			if (err != nil) != tc.wantError {
				t.Errorf("result %+v error %v", result, err)
			}
			b, e := os.ReadFile(pidfile)
			if e != nil {
				t.Fatal(e)
			}
			pid, e := strconv.Atoi(strings.TrimSpace(string(b)))
			if e != nil {
				t.Fatal(e)
			}
			if e := syscall.Kill(pid, 0); !errors.Is(e, syscall.ESRCH) {
				t.Fatalf("descendant %d remains after return: %v", pid, e)
			}
		})
	}
}

func TestManagedCommandCancellationStopsGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	_, err := managedCommand(ctx, "/bin/sh", []string{"-c", "sleep 60"}, os.Environ(), "/")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}

func TestManagedOwnedRealGo(t *testing.T) {
	path, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "version")
	reaped, err := runOwnedCommand(context.Background(), cmd, managedProcessOps{
		observe: func(pid int) (bool, error) {
			exited, err := waitManagedLeader(pid)
			if err != nil {
				return exited, fmt.Errorf("waitid: %w", err)
			}
			return exited, nil
		},
		signal: func(pid int, sig syscall.Signal) error {
			err := signalManagedGroup(pid, sig)
			if err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("group signal %d: %w", sig, err)
			}
			return err
		},
	})
	if err != nil {
		t.Fatalf("go %s reaped=%t: %v", path, reaped, err)
	}
}

// Darwin waitid can report a stop even when only WEXITED was requested.
func TestManagedStoppedLeaderIsNotExited(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGCONT)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		exited, err := waitManagedLeader(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if exited {
			t.Fatal("stopped leader reported as exited")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagedObserverFailureDoesNotSignalOrRaceOutput(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cause := errors.New("fixture observer unavailable")
	signaled := false
	reaped, err := runOwnedCommand(context.Background(), cmd, managedProcessOps{
		observe: func(int) (bool, error) { return false, cause },
		signal:  func(int, syscall.Signal) error { signaled = true; return nil },
	})
	var stop *managedStopError
	if reaped || signaled || !errors.As(err, &stop) || !errors.Is(err, cause) || stop.PID <= 0 {
		t.Fatalf("uncertain process mishandled: %v", err)
	}
	// Only the live fixture owner stops this unreaped direct child; passive Wait joins.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stop.reaped:
	case <-time.After(time.Second):
		t.Fatal("passive reaper did not finish")
	}
	if output.Len() != 0 {
		t.Fatal("unexpected output")
	}
}

func TestManagedECHILDDoesNotSignal(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	signals := 0
	_, err := runOwnedCommand(context.Background(), cmd, managedProcessOps{
		observe: func(int) (bool, error) {
			_, err := cmd.Process.Wait()
			if err != nil {
				t.Fatal(err)
			}
			return false, syscall.ECHILD
		},
		signal: func(int, syscall.Signal) error { signals++; return nil },
	})
	if !managedUncertain(err) || !errors.Is(err, syscall.ECHILD) || signals != 0 {
		t.Fatalf("lost ownership signaled: %v signals=%d", err, signals)
	}
}

func TestManagedGroupRejectsInvalidID(t *testing.T) {
	for _, pid := range []int{-1, 0} {
		if err := signalManagedGroup(pid, 0); err == nil {
			t.Fatal("nonpositive group accepted")
		}
	}
}

func TestManagedFailedDirectStopReturnsWithoutBlockingWait(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	reaped, err := runOwnedCommand(ctx, cmd, managedProcessOps{
		signal:     func(int, syscall.Signal) error { return syscall.EPERM },
		killLeader: func() error { return syscall.EPERM },
	})
	var stop *managedStopError
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if stop != nil && stop.reaped != nil {
			select {
			case <-stop.reaped:
			case <-time.After(time.Second):
				t.Error("passive Wait did not finish")
			}
		}
	})
	if reaped || !errors.As(err, &stop) || time.Since(started) > time.Second || !errors.Is(err, syscall.EPERM) {
		t.Fatalf("stop failure blocked or lost uncertainty: reaped=%t err=%v", reaped, err)
	}
}

func TestManagedWaitDelayIsStopUncertainty(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	bin, mode, await := managedFailureTool(t)
	defer await()
	mode("pipe", "")
	_, err := managedCommand(context.Background(), filepath.Join(bin, "go"), []string{"clean", "-cache"}, managedNativeEnv(root, cache), root)
	if !managedUncertain(err) || !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("I/O stop uncertainty lost: %v", err)
	}
}
