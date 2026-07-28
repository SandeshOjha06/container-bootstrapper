#define _GNU_SOURCE
#include <fcntl.h>
#include <sched.h>
#include <unistd.h>
#include <stdlib.h>
#include <stdio.h>
#include <sys/wait.h>

// This constructor runs BEFORE Go's runtime initializes.
__attribute__((constructor)) void init_exec(void) {
	char *pid_str = getenv("BOOTSTRAPPER_EXEC_PID");
	if (pid_str == NULL) {
		return; // Normal boot, ignore and proceed to Go's main()
	}

	char path[256];
	// 'mnt' must be joined LAST so we can still read the host's /proc
	char *namespaces[] = { "uts", "net", "pid", "mnt" }; 
	
	for (int i = 0; i < 4; i++) {
		snprintf(path, sizeof(path), "/proc/%s/ns/%s", pid_str, namespaces[i]);
		int fd = open(path, O_RDONLY);
		if (fd == -1) {
			perror("open namespace");
			exit(1);
		}
		if (setns(fd, 0) == -1) {
			perror("setns");
			exit(1);
		}
		close(fd);
	}

	//setns() for PID namespaces only applies to the children 
	// of the calling process. We must fork() to actually enter the PID namespace.
	pid_t child = fork();
	if (child == 0) {
		execl("/bin/sh", "/bin/sh", NULL);
		perror("execl");
		exit(1);
	}
	waitpid(child, NULL, 0);
	exit(0); // Exit completely, never allowing Go to start.
}
