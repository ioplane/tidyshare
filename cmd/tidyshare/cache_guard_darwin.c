//go:build darwin && cgo

#include "cache_guard_darwin.h"
#include <errno.h>
#include <libproc.h>
#include <stdlib.h>
#include <string.h>

static int exact_result(int got, size_t expected) {
    if (got == (int)expected) return 0;
    return errno ? errno : EIO;
}

static int copy_path(char *out, size_t capacity, const char *path, size_t size) {
    size_t n = strnlen(path, size);
    if (n == 0 || n >= size - 1 || n >= capacity || path[0] != '/') return EIO;
    memcpy(out, path, n + 1);
    return 0;
}

int tidy_proc_query(int kind, int pid, uint64_t arg, struct tidy_proc_result *out) {
    int err;
    memset(out, 0, sizeof(*out));
    errno = 0;
    switch (kind) {
    case 3: {
        struct proc_bsdinfo info;
        err = exact_result(proc_pidinfo(pid, PROC_PIDTBSDINFO, 1, &info, sizeof(info)), sizeof(info));
        if (err) return err;
        if (info.pbi_pid != (uint32_t)pid) return ESRCH;
        out->start = info.pbi_start_tvsec;
        out->start_micro = info.pbi_start_tvusec;
        out->flags = info.pbi_flags;
        return 0;
    }
    case 4: {
        struct proc_vnodepathinfo info;
        err = exact_result(proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &info, sizeof(info)), sizeof(info));
        if (err) return err;
        err = copy_path(out->path, sizeof(out->path), info.pvi_cdir.vip_path, sizeof(info.pvi_cdir.vip_path));
        if (err) return err;
        if (!info.pvi_rdir.vip_vi.vi_stat.vst_mode && !info.pvi_rdir.vip_path[0]) {
            strcpy(out->root, "/");
            return 0;
        }
        return copy_path(out->root, sizeof(out->root), info.pvi_rdir.vip_path, sizeof(info.pvi_rdir.vip_path));
    }
    case 5: {
        char path[PROC_PIDPATHINFO_MAXSIZE] = {0};
        int n = proc_pidpath(pid, path, sizeof(path));
        if (n <= 0) return errno ? errno : EIO;
        if (n >= (int)sizeof(path) - 1) return EOVERFLOW;
        return copy_path(out->path, sizeof(out->path), path, sizeof(path));
    }
    case 6:
    case 7: {
        struct vnode_fdinfowithpath info;
        int n = kind == 6 ? proc_pidfdinfo(pid, (int)arg, PROC_PIDFDVNODEPATHINFO, &info, sizeof(info))
                          : proc_pidfileportinfo(pid, (uint32_t)arg, PROC_PIDFILEPORTVNODEPATHINFO, &info, sizeof(info));
        err = exact_result(n, sizeof(info));
        if (err) return err;
        return copy_path(out->path, sizeof(out->path), info.pvip.vip_path, sizeof(info.pvip.vip_path));
    }
    case 8: {
        struct proc_regionwithpathinfo info;
        err = exact_result(proc_pidinfo(pid, PROC_PIDREGIONPATHINFO, arg, &info, sizeof(info)), sizeof(info));
        if (err) return err;
        if (info.prp_prinfo.pri_flags & PROC_REGION_SUBMAP) return ENOTSUP;
        out->address = info.prp_prinfo.pri_address;
        out->size = info.prp_prinfo.pri_size;
        if (!info.prp_vip.vip_path[0] && !info.prp_vip.vip_vi.vi_stat.vst_mode) {
            /* libproc does not distinguish anonymous memory from an unavailable vnode. */
            out->flags = info.prp_prinfo.pri_share_mode == SM_EMPTY ? 0 : 1;
            return 0;
        }
        return copy_path(out->path, sizeof(out->path), info.prp_vip.vip_path, sizeof(info.prp_vip.vip_path));
    }
    default:
        return EINVAL;
    }
}

int tidy_proc_list(int kind, int pid, uint64_t *out, int capacity, int *count) {
    size_t width = kind == 0 ? sizeof(pid_t) : sizeof(struct proc_fdinfo);
    if (capacity <= 0 || capacity > 1048576 || kind < 0 || kind > 2) return EINVAL;
    void *buffer = calloc((size_t)capacity, width);
    if (!buffer) return ENOMEM;
    errno = 0;
    int size = (int)(capacity * width);
    int n = kind == 0 ? proc_listpids(PROC_ALL_PIDS, 0, buffer, size)
                     : proc_pidinfo(pid, kind == 1 ? PROC_PIDLISTFDS : PROC_PIDLISTFILEPORTS, 0, buffer, size);
    int err = errno;
    if (n < 0 || (n == 0 && err) || n > size || n % width != 0) {
        free(buffer);
        return err ? err : EIO;
    }
    *count = n / (int)width;
    for (int i = 0; i < *count; i++) {
        if (kind == 0) out[i] = (uint32_t)((pid_t *)buffer)[i];
        else if (kind == 1) {
            struct proc_fdinfo *fds = buffer;
            out[i] = (uint64_t)fds[i].proc_fdtype << 32 | (uint32_t)fds[i].proc_fd;
        } else {
            struct proc_fileportinfo *ports = buffer;
            out[i] = (uint64_t)ports[i].proc_fdtype << 32 | ports[i].proc_fileport;
        }
    }
    free(buffer);
    return 0;
}
