package main

import (
	"log"
	"os"
	"os/exec"	
	"syscall"

	"golang.org/x/sys/unix"
)

func main() {

	if (len(os.Args) < 2) {
		panic("Expected 'run' or 'child'")
	}

	switch os.Args[1] {
		case "run":
			parent()

		case "child":
			child()

		default:
			panic("an error occured...")

	}
}


func parent() {
	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, os.Args[2:]...)...)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUTS | unix.CLONE_NEWPID | unix.CLONE_NEWNS,
	}

	if err := cmd.Run(); err != nil {
		log.Fatal("Parent failed")
	}

}

func child(){

	// declare that the mount is private 
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		log.Fatalf("Private mount failed: %v", err)
	}

	// set hostname
	if err := unix.Sethostname([]byte("myContainer")); err != nil {
		log.Fatalf("Setting hostname failed: %v", err)
	}
		
	if err := unix.Chroot("./rootfs"); err != nil {
		log.Fatalf("Chroot failed: %v", err)
	}


	if err := os.Chdir("/"); err != nil {
		log.Fatalf("Chdir failed: %v", err)
	}
		// isolate mountspace
		// src, target, filesystemtype, mountflag, data
	if err := unix.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		log.Fatalf("Proc mount falied: %v", err)

	}

	// raw exec
	// replaces the current process with /bin/sh

	if err := unix.Exec("/bin/sh", []string{"/bin/sh"}, os.Environ()); err != nil {
		log.Fatalf("Exec failed: %v", err)
	}



}
