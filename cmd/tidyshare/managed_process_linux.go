package main

import "golang.org/x/sys/unix"

func managedProcessCapability() error { return nil }
func waitManagedLeader(pid int) (bool, error) {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
	return info.Signo != 0, err
}
