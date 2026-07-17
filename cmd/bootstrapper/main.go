package main

import (
	"syscall"
	"os"
	"os/exec"
	"log"
	"golang.org/x/sys/unix"
)

func main() {
cmd := exec.Command("/bin/sh")


cmd.SysProcAttr = &syscall.SysProcAttr{
	Cloneflags: unix.CLONE_NEWPID | unix.CLONE_NEWUTS,
}

cmd.Stdin = os.Stdin
cmd.Stdout = os.Stdout
cmd.Stderr = os.Stderr

if err := cmd.Run(); err != nil{
	log.Fatalf("Failed to run isolated process: %v", err)
}

}
