package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"time"
)

type managedStopError struct {
	Cause  error
	Roots  []string
	PID    int
	reaped <-chan struct{}
}

func (e *managedStopError) Error() string {
	return fmt.Sprintf("process ownership or stop is uncertain (leader PID %d); retained roots: %s: %v", e.PID, strings.Join(e.Roots, ", "), e.Cause)
}
func (e *managedStopError) Unwrap() error { return e.Cause }

func managedUncertain(err error) bool { var stop *managedStopError; return errors.As(err, &stop) }

func retainManagedRoot(err error, root string) error {
	var stop *managedStopError
	if !errors.As(err, &stop) {
		return err
	}
	roots := append([]string{}, stop.Roots...)
	roots = append(roots, root)
	return &managedStopError{Cause: err, Roots: roots, PID: stop.PID, reaped: stop.reaped}
}

type managedProcessOps struct {
	observe    func(int) (bool, error)
	signal     func(int, syscall.Signal) error
	killLeader func() error
}

func signalManagedGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return errors.New("invalid owned process group")
	}
	return syscall.Kill(-pgid, signal)
}

// Cmd receives file descriptors so observer failure can close and join every copy.
type managedPipe struct {
	reader, writer *os.File
	done           chan error
}

func managedPipes(cmd *exec.Cmd) ([]managedPipe, error) {
	var pipes []managedPipe
	shared := cmd.Stdout != nil && reflect.TypeOf(cmd.Stdout).Comparable() && cmd.Stdout == cmd.Stderr
	for index, target := range []*io.Writer{&cmd.Stdout, &cmd.Stderr} {
		if index == 1 && shared {
			cmd.Stderr = cmd.Stdout
			continue
		}
		if *target == nil {
			continue
		}
		if _, ok := (*target).(*os.File); ok {
			continue
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			for _, p := range pipes {
				_ = p.writer.Close()
				_ = p.reader.Close()
				err = errors.Join(err, <-p.done)
			}
			return nil, err
		}
		destination := *target
		pipe := managedPipe{reader, writer, make(chan error, 1)}
		pipes = append(pipes, pipe)
		*target = writer
		go func() { _, err := io.Copy(destination, reader); pipe.done <- err }()
	}
	return pipes, nil
}

func finishManagedPipes(pipes []managedPipe, force bool) error {
	if force {
		for _, p := range pipes {
			_ = p.reader.Close()
		}
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var result error
	for _, p := range pipes {
		select {
		case err := <-p.done:
			if !force {
				result = errors.Join(result, err)
			}
		case <-timer.C:
			for _, p := range pipes {
				_ = p.reader.Close()
			}
			result = errors.Join(result, exec.ErrWaitDelay, <-p.done)
			force = true
		}
		_ = p.reader.Close()
	}
	return result
}

// The exclusive owner holds the leader unreaped through its last group signal.
func runOwnedCommand(ctx context.Context, cmd *exec.Cmd, ops managedProcessOps) (bool, error) {
	if err := managedProcessCapability(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if ops.observe == nil {
		ops.observe = waitManagedLeader
	}
	if ops.signal == nil {
		ops.signal = signalManagedGroup
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	pipes, err := managedPipes(cmd)
	if err != nil {
		return false, err
	}
	if err = cmd.Start(); err != nil {
		for _, p := range pipes {
			_ = p.writer.Close()
		}
		_ = finishManagedPipes(pipes, true)
		return false, err
	}
	for _, p := range pipes {
		_ = p.writer.Close()
	}
	pid := cmd.Process.Pid
	if ops.killLeader == nil {
		ops.killLeader = cmd.Process.Kill
	}
	leaderExited := false
	for {
		exited, observeErr := ops.observe(pid)
		if errors.Is(observeErr, syscall.EINTR) {
			continue
		}
		if observeErr != nil {
			_ = finishManagedPipes(pipes, true)
			if errors.Is(observeErr, syscall.ECHILD) {
				// Another reaper released the PID; no numeric wait or signal is safe.
				return false, &managedStopError{Cause: errors.Join(observeErr, cmd.Process.Release()), PID: pid}
			}
			// Ownership cannot be proved. Only a passive reaper survives this return.
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			return false, &managedStopError{Cause: observeErr, PID: pid, reaped: done}
		}
		if exited || ctx.Err() != nil {
			leaderExited = exited
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	stopErr := ops.signal(pid, syscall.SIGKILL)
	if errors.Is(stopErr, syscall.ESRCH) {
		stopErr = nil
	}
	if stopErr != nil && !leaderExited {
		// Successful waitid still reserves this direct child's PID for its sole owner.
		if directErr := ops.killLeader(); directErr != nil {
			_ = finishManagedPipes(pipes, true)
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			return false, &managedStopError{Cause: errors.Join(stopErr, directErr, ctx.Err()), PID: pid, reaped: done}
		}
	}
	waitErr := cmd.Wait()
	copyErr := finishManagedPipes(pipes, false)
	until := time.Now().Add(2 * time.Second)
	for {
		observation := ops.signal(pid, 0)
		if errors.Is(observation, syscall.ESRCH) {
			// Darwin can reject a signal to a zombie-only group; disappearance proves stop.
			if errors.Is(copyErr, exec.ErrWaitDelay) {
				return true, &managedStopError{Cause: errors.Join(waitErr, copyErr, ctx.Err()), PID: pid}
			}
			return true, errors.Join(waitErr, copyErr, ctx.Err())
		}
		if time.Now().After(until) {
			return true, &managedStopError{Cause: errors.Join(errors.New("process group quiescence was not proved"), stopErr, observation, waitErr, copyErr, ctx.Err()), PID: pid}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type managedOutput struct {
	mcpBoundedBuffer
	cancel context.CancelFunc
}

func (b *managedOutput) Write(data []byte) (int, error) {
	n, err := b.mcpBoundedBuffer.Write(data)
	if b.exceeded {
		b.cancel()
	}
	return n, err
}
