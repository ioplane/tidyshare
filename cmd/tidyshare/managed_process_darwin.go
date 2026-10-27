//go:build darwin && cgo

package main

/*
#include <sys/wait.h>
#include <errno.h>
static int tidyshare_observe_child(int pid, int *exited) {
 siginfo_t info = {0};
 if (waitid(P_PID, pid, &info, WEXITED|WNOWAIT|WNOHANG) == -1) return errno;
 *exited = info.si_pid != 0 && (info.si_code == CLD_EXITED || info.si_code == CLD_KILLED || info.si_code == CLD_DUMPED);
 return 0;
}
*/
import "C"
import "syscall"

func managedProcessCapability() error { return nil }
func waitManagedLeader(pid int) (bool, error) {
	var exited C.int
	code := C.tidyshare_observe_child(C.int(pid), &exited)
	if code != 0 {
		return false, syscall.Errno(code)
	}
	return exited != 0, nil
}
