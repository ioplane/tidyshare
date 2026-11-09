package main

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func fileAtime(info os.FileInfo) int64 { return info.Sys().(*syscall.Stat_t).Atim.Sec }
func fileTimes(info os.FileInfo) (unix.Timespec, unix.Timespec) {
	st := info.Sys().(*syscall.Stat_t)
	return unix.Timespec{Sec: st.Mtim.Sec, Nsec: st.Mtim.Nsec}, unix.Timespec{Sec: st.Ctim.Sec, Nsec: st.Ctim.Nsec}
}

func renameNoReplace(fromfd int, from string, tofd int, to string) error {
	return unix.Renameat2(fromfd, from, tofd, to, unix.RENAME_NOREPLACE)
}

// dedupe makes dup share its extents with keeper through FIDEDUPERANGE. The
// kernel compares the bytes itself and refuses (status DIFFERS) if they differ,
// so no content can be lost; mtime, owner, ACLs and the path of dup are kept.
func dedupe(keeper, dup string, size int64) error {
	src, err := os.OpenFile(keeper, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dup, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer dst.Close()
	srcInfo, err := src.Stat()
	if err != nil {
		return err
	}
	dstInfo, err := dst.Stat()
	if err != nil {
		return err
	}
	if size <= 0 || !srcInfo.Mode().IsRegular() || !dstInfo.Mode().IsRegular() || srcInfo.Size() != size || dstInfo.Size() != size || srcInfo.Sys().(*syscall.Stat_t).Dev != dstInfo.Sys().(*syscall.Stat_t).Dev {
		return errors.New("dedupe requires regular files of the planned size on one filesystem")
	}
	const chunk = 16 << 20
	for off := int64(0); off < size; {
		r := unix.FileDedupeRange{
			Src_offset: uint64(off), Src_length: uint64(min(chunk, size-off)),
			Info: []unix.FileDedupeRangeInfo{{Dest_fd: int64(dst.Fd()), Dest_offset: uint64(off)}},
		}
		if err := unix.IoctlFileDedupeRange(int(src.Fd()), &r); err != nil {
			return err
		}
		switch i := r.Info[0]; {
		case i.Status == unix.FILE_DEDUPE_RANGE_DIFFERS:
			return errors.New("content differs")
		case i.Status != unix.FILE_DEDUPE_RANGE_SAME:
			return syscall.Errno(-i.Status)
		case i.Bytes_deduped == 0:
			return errors.New("no progress")
		default:
			off += int64(i.Bytes_deduped)
		}
	}
	return nil
}

func platformDuplicateGuard(*os.File) error { return nil }
