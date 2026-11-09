#ifndef TIDYSHARE_CACHE_GUARD_DARWIN_H
#define TIDYSHARE_CACHE_GUARD_DARWIN_H

#include <stdint.h>
#include <sys/param.h>

struct tidy_proc_result {
    char path[4 * MAXPATHLEN];
    char root[MAXPATHLEN];
    uint64_t start, start_micro, address, size;
    uint32_t flags;
};

int tidy_proc_query(int kind, int pid, uint64_t arg, struct tidy_proc_result *out);
int tidy_proc_list(int kind, int pid, uint64_t *out, int capacity, int *count);

#endif
