package main

import (
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func main() {

	if len(os.Args) < 2 {
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
		Cloneflags: unix.CLONE_NEWUTS | unix.CLONE_NEWPID | unix.CLONE_NEWNS | unix.CLONE_NEWNET,
	}

	if err := cmd.Start(); err != nil {
		log.Fatal("Start failed")
	}

	containerPID := cmd.Process.Pid
	log.Printf("Container started, Child PID on host: %d", containerPID)

	cgroupPath := "/sys/fs/cgroup/bootstrapper"

	if err := os.Mkdir(cgroupPath, 0755); err != nil && !os.IsExist(err) {
		log.Fatalf("Error creating the folder: %v", err)
	}

	// apply limits(PIDs max, memory max and assign the process)
	// limit - 20 concurrent process
	if err := os.WriteFile(cgroupPath+"/pids.max", []byte("20"), 0644); err != nil {
		log.Fatalf("Failed to set pids.max: %v", err)
	}

	// set memory to 30mb
	if err := os.WriteFile(cgroupPath+"/memory.max", []byte("31457280"), 0644); err != nil {
		log.Fatalf("Failed to set memory.max: %v", err)
	}

	pidStr := strconv.Itoa(containerPID)
	if err := os.WriteFile(cgroupPath+"/cgroup.procs", []byte(pidStr), 0644); err != nil {
		log.Fatalf("Failed to join cgroup: %v", err)
	}

	// define the bridge
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br-boot"}}

	// create the link
	if err := netlink.LinkAdd(bridge); err != nil {
		// If the error is not "file exists", Crash.
		if !strings.Contains(err.Error(), "file exists") {
			log.Fatalf("Error creating link: %v", err)
		}
		// If "file exists", ignore
	}

	// parse the ip address
	addr, err := netlink.ParseAddr("10.0.0.1/24")
	if err != nil {
		log.Fatalf("Error parsing the ip address: %v", err)
	}

	// attach the ip to the bridge
	if err := netlink.AddrAdd(bridge, addr); err != nil {
		if !strings.Contains(err.Error(), "file exists") {
			log.Fatalf("Error attaching the ip address: %v", err)
		}
	}

	if err := netlink.LinkSetUp(bridge); err != nil {
		log.Fatalf("Error setting up the link: %v", err)
	}

	// define the veth pair
	vethAttrs := netlink.NewLinkAttrs()
	vethAttrs.Name = "veth-host"

	veth := &netlink.Veth{
		LinkAttrs: vethAttrs,
		PeerName:  "veth-child",
	}

	// create veth pair on the host
	if err := netlink.LinkAdd(veth); err != nil {
		if !strings.Contains(err.Error(), "file exists") {
			log.Fatalf("Error creating veth pair: %v", err)
		}
	}

	//  Move veth-child into the container's network namespace
	childLink, err := netlink.LinkByName("veth-child")
	if err != nil {
		log.Fatalf("Failed to find veth-child: %v", err)
	}

	if err := netlink.LinkSetNsPid(childLink, containerPID); err != nil {
		log.Fatalf("Failed to move veth-child into namespace: %v", err)
	}

	// Attach veth-host to the br-boot bridge switch
	hostLink, err := netlink.LinkByName("veth-host")
	if err != nil {
		log.Fatalf("Failed to find veth-host: %v", err)
	}

	if err := netlink.LinkSetMaster(hostLink, bridge); err != nil {
		log.Fatalf("Failed to attach veth-host to bridge: %v", err)
	}

	// Bring veth-host up on the host
	if err := netlink.LinkSetUp(hostLink); err != nil {
		log.Fatalf("Failed to bring veth-host up: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		log.Fatal("Wait failed")
	}
}

func child() {

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

	//isolate sys mount
	if err := unix.Mount("sysfs", "/sys", "sysfs", 0, ""); err != nil {
		log.Fatalf("Sys mount failed: %v", err)
	}

	// raw exec
	// replaces the current process with /in/sh

	if err := unix.Exec("/bin/sh", []string{"/bin/sh"}, os.Environ()); err != nil {
		log.Fatalf("Exec failed: %v", err)
	}

}
