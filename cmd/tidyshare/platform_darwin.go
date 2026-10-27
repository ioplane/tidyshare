package main

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func fileAtime(info os.FileInfo) int64 { return info.Sys().(*syscall.Stat_t).Atimespec.Sec }
func fileTimes(info os.FileInfo) (unix.Timespec, unix.Timespec) {
	st := info.Sys().(*syscall.Stat_t)
	return unix.Timespec{Sec: st.Mtimespec.Sec, Nsec: st.Mtimespec.Nsec}, unix.Timespec{Sec: st.Ctimespec.Sec, Nsec: st.Ctimespec.Nsec}
}

func renameNoReplace(fromfd int, from string, tofd int, to string) error {
	return unix.RenameatxNp(fromfd, from, tofd, to, unix.RENAME_EXCL)
}
func dedupe(keeper, dup string, size int64) error { return syscall.ENOTSUP }

func platformDuplicateGuard(file *os.File) error {
	size, err := unix.Fgetxattr(int(file.Fd()), "com.apple.ResourceFork", nil)
	if errors.Is(err, unix.ENOATTR) {
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect Darwin resource fork")
	}
	if size > 0 {
		return errors.New("the Darwin resource fork is not covered by the duplicate plan")
	}
	return nil
}
