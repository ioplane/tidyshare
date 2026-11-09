/* Owned, finite fixture: test quiescence uncertainty, not escaped-process containment. */
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static void record(const char *path, const char *line) {
    FILE *f = fopen(path, "a");
    if (!f || fputs(line, f) == EOF || fclose(f) != 0) _exit(70);
}

int main(int argc, char **argv) {
    char directory[PATH_MAX], modepath[PATH_MAX], logpath[PATH_MAX], pidpath[PATH_MAX];
    char mode[64] = "", selected[PATH_MAX] = "", seed[PATH_MAX], line[PATH_MAX + 64];
    if (strlen(argv[0]) >= sizeof(directory)) return 70;
    strcpy(directory, argv[0]);
    char *slash = strrchr(directory, '/');
    if (!slash) return 70;
    *slash = 0;
    if (snprintf(modepath, sizeof(modepath), "%s/mode", directory) >= (int)sizeof(modepath) ||
        snprintf(logpath, sizeof(logpath), "%s/calls", directory) >= (int)sizeof(logpath) ||
        snprintf(pidpath, sizeof(pidpath), "%s/pids", directory) >= (int)sizeof(pidpath)) return 70;
    if (!strcmp(slash + 1, "node")) { record(logpath, "node-start\n"); return 1; }
    if (argc == 2 && !strcmp(argv[1], "version")) { puts("go fixture-1"); return 0; }
    const char *cache = getenv("GOCACHE"), *home = getenv("HOME");
    if (!cache || !home || argc != 3 || strcmp(argv[1], "clean") || strcmp(argv[2], "-cache")) return 70;
    FILE *control = fopen(modepath, "r");
    if (!control || !fgets(mode, sizeof(mode), control) || !fgets(selected, sizeof(selected), control) || fclose(control)) return 70;
    mode[strcspn(mode, "\n")] = 0; selected[strcspn(selected, "\n")] = 0;
    snprintf(line, sizeof(line), "%s\n", cache); record(logpath, line);
    if (snprintf(seed, sizeof(seed), "%s/00/fixture-a", cache) >= (int)sizeof(seed)) return 70;
    if (unlink(seed) != 0 && errno != ENOENT) return 70;
    if (strcmp(mode, "pipe") && strcmp(mode, "qualification") && (strcmp(mode, "execution") || strcmp(cache, selected))) return 0;
    if (getpgrp() != getpid() && setpgid(0, 0)) return 70;

    int handshake[2];
    if (pipe(handshake)) return 70;
    pid_t original = getpgrp(), parent = fork();
    if (parent == -1) return 70;
    if (parent == 0) {
        close(handshake[0]);
        if (setpgid(0, 0)) _exit(70);
        if (strcmp(mode, "pipe")) {
            int nullfd = open("/dev/null", O_RDWR);
            if (nullfd == -1 || dup2(nullfd, 0) == -1 || dup2(nullfd, 1) == -1 || dup2(nullfd, 2) == -1) _exit(70);
            if (nullfd > 2) close(nullfd);
        }
        int ready[2]; if (pipe(ready)) _exit(70);
        pid_t child = fork(); if (child == -1) _exit(70);
        if (child == 0) {
            close(ready[0]);
            if (strcmp(mode, "pipe") && setpgid(0, original)) _exit(70);
            if (write(ready[1], "R", 1) != 1) _exit(70);
            close(ready[1]); close(handshake[1]);
            for (;;) pause();
        }
        close(ready[1]); char ack;
        if (read(ready[0], &ack, 1) != 1) _exit(70);
        close(ready[0]);
        snprintf(line, sizeof(line), "%d %d %d\n", (int)getpid(), (int)child, (int)original);
        record(pidpath, line);
        char retained[PATH_MAX];
        if (snprintf(retained, sizeof(retained), "%s/fixture-pids", home) >= (int)sizeof(retained)) _exit(70);
        record(retained, line);
        if (write(handshake[1], "R", 1) != 1) _exit(70);
        close(handshake[1]);
        struct timespec delay = {strcmp(mode, "pipe") ? 4 : 8, 0};
        while (nanosleep(&delay, &delay) && errno == EINTR) {}
        /* Only this fixture parent reaps its child; it has a fixed lifetime. */
        kill(child, SIGKILL);
        while (waitpid(child, NULL, 0) == -1 && errno == EINTR) {}
        _exit(0);
    }
    close(handshake[1]); char ack;
    if (read(handshake[0], &ack, 1) != 1) return 70;
    close(handshake[0]);
    return 0;
}
