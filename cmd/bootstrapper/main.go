package main

import (
	"io"
	"log"
	"net"
	"net/http"
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

	prepareRootFS()

	r, w, err := os.Pipe()
	if err != nil {
		log.Fatalf("Failed to create pipe: %v", err)
	}

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, os.Args[2:]...)...)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{r}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUTS | unix.CLONE_NEWPID | unix.CLONE_NEWNS | unix.CLONE_NEWNET,
	}

	if err := cmd.Start(); err != nil {
		log.Fatal("Start failed")
	}

	if err := r.Close(); err != nil {
		log.Fatal("r.Close() failed")
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

	// Enable IP forwarding
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644); err != nil {
		log.Fatal("Enable IP forwarding")
	}

	// Configure NAT
	natCmd := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "10.0.0.0/24", "-j", "MASQUERADE")
	if err := natCmd.Run(); err != nil {
		log.Fatalf("Failed to configure NAT masquerade: %v", err)
	}

	// tell the firewall to allow traffic to enter from the bridge
	fwInCmd := exec.Command("iptables", "-A", "FORWARD", "-i", "br-boot", "-j", "ACCEPT")
	if err := fwInCmd.Run(); err != nil {
		log.Fatalf("Failed to allow forward in: %v", err)
	}

	// tell the firewall to allow traffic to exit to the bridge
	fwOutCmd := exec.Command("iptables", "-A", "FORWARD", "-o", "br-boot", "-j", "ACCEPT")
	if err := fwOutCmd.Run(); err != nil {
		log.Fatalf("Failed to allow forward out: %v", err)
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

	if _, err := w.Write([]byte("1")); err != nil {
		log.Fatal("Write byte failed")
	}

	if err := w.Close(); err != nil {
		log.Fatal("w.Close() failed")
	}

	if err := cmd.Wait(); err != nil {
		log.Println("Container exited with errors..")
	} else {
		log.Println("Container exited.")
	}

	log.Println("Starting teardown sequence...")

	// Remove iptables rules
	exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", "10.0.0.0/24", "-j", "MASQUERADE").Run()
	exec.Command("iptables", "-D", "FORWARD", "-i", "br-boot", "-j", "ACCEPT").Run()
	exec.Command("iptables", "-D", "FORWARD", "-o", "br-boot", "-j", "ACCEPT").Run()

	//Destroy the virtual switch
	if err := netlink.LinkDel(bridge); err != nil {
		log.Printf("Warning: Failed to delete bridge: %v", err)
	}

	//Delete the cgroup
	// The process must be dead before the kernel allows cgroup deletion
	if err := os.Remove("/sys/fs/cgroup/bootstrapper"); err != nil {
		log.Printf("Warning: Failed to remove cgroup: %v", err)
	}

	os.RemoveAll("./upper")
	os.RemoveAll("./work")
	os.RemoveAll("./merged")

	log.Println("Teardown complete. Host environment restored.")
}

func child() {

	// block operations
	freezePipe := os.NewFile(uintptr(3), "pipe")
	if freezePipe == nil {
		log.Fatal("Freeze Pipe fd is invalid or missing")
	}

	buf := make([]byte, 1)
	// blocking system call to freeze the child
	if _, err := freezePipe.Read(buf); err != nil {
		log.Fatal("Failed to read from pipe")
	}

	freezePipe.Close()

	lo, err := netlink.LinkByName("lo")
	if err != nil {
		log.Fatal("Failed to find lo")
	}

	vethChild, err := netlink.LinkByName("veth-child")

	if err := netlink.LinkSetUp(lo); err != nil {
		log.Fatal("Failed to bring lo UP")
	}

	addr, err := netlink.ParseAddr("10.0.0.2/24")
	if err != nil {
		log.Fatalf("Error parsing the ip address: %v", err)
	}

	if err := netlink.AddrAdd(vethChild, addr); err != nil {
		log.Fatal("Failed to assign IP to veth-child")
	}

	if err := netlink.LinkSetUp(vethChild); err != nil {
		log.Fatal("Failed to bring up addr")
	}

	route := &netlink.Route{
		Scope:     netlink.SCOPE_UNIVERSE,
		LinkIndex: vethChild.Attrs().Index,
		Gw:        net.ParseIP("10.0.0.1"),
	}

	if err := netlink.RouteAdd(route); err != nil {
		log.Fatalf("Failed to add to default route")
	}

	// declare that the mount is private
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		log.Fatalf("Private mount failed: %v", err)
	}

	// set hostname
	if err := unix.Sethostname([]byte("myContainer")); err != nil {
		log.Fatalf("Setting hostname failed: %v", err)
	}

	//  Create the necessary OverlayFS directories
	for _, dir := range []string{"upper", "work", "merged"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("Failed to create %s dir: %v", dir, err)
		}
	}

	//  Instruct the kernel to merge them
	overlayOpts := "lowerdir=./basefs,upperdir=./upper,workdir=./work"
	if err := unix.Mount("overlay", "./merged", "overlay", 0, overlayOpts); err != nil {
		log.Fatalf("OverlayFS mount failed: %v", err)
	}


	// Bind mounting the merged directory to itself satisfies this strict kernel rule.
	if err := unix.Mount("./merged", "./merged", "", unix.MS_BIND, ""); err != nil {
		log.Fatalf("Bind mount failed: %v", err)
	}

	//  Create a temporary directory to hold the old host root
	putOld := "./merged/put_old"
	if err := os.MkdirAll(putOld, 0700); err != nil {
		log.Fatalf("Failed to create put_old directory: %v", err)
	}

	//Pivot the root, this swaps the mount points at the kernel level.
	if err := unix.PivotRoot("./merged", putOld); err != nil {
		log.Fatalf("PivotRoot failed: %v", err)
	}

	//  Update the current working directory to the new root
	if err := os.Chdir("/"); err != nil {
		log.Fatalf("Chdir failed: %v", err)
	}

	//  Unmount the old host root so the container has zero access to it
	putOldInside := "/put_old"
	if err := unix.Unmount(putOldInside, unix.MNT_DETACH); err != nil {
		log.Fatalf("Unmount put_old failed: %v", err)
	}

	// Delete the temporary folder
	if err := os.Remove(putOldInside); err != nil {
		log.Fatalf("Failed to remove put_old: %v", err)
	}

	// isolate mountspace
	// src, target, filesystemtype, mountflag, data
	if err := unix.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		log.Fatalf("Proc mount falied: %v", err)

	}

	// Inject DNS Configuration
	if err := os.MkdirAll("/etc", 0755); err != nil {
		log.Fatalf("Failed to create /etc directory: %v", err)
	}

	if err := os.WriteFile("/etc/resolv.conf", []byte("nameserver 8.8.8.8\n"), 0644); err != nil {
		log.Fatalf("Failed to inject resolv.conf: %v", err)
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

func prepareRootFS() {
	if _, err := os.Stat("./basefs"); err == nil {
		return
	}

	log.Println("Downloading Alpine Linux basefs...")

	tarURL := "https://dl-cdn.alpinelinux.org/alpine/v3.18/releases/x86_64/alpine-minirootfs-3.18.4-x86_64.tar.gz"

	tarPath := "alpine-rootfs.tar.gz"

	// download the tarball

	resp, err := http.Get(tarURL)
	if err != nil {
		log.Fatalf("Failed to download Alpine: %v", err)
	}
	defer resp.Body.Close()

	outFile, err := os.Create(tarPath)
	if err != nil {
		log.Fatalf("Failed to create tar file: %v", err)
	}
	defer outFile.Close()

	// copy to drive
	if _, err := io.Copy(outFile, resp.Body); err != nil {
		log.Fatalf("Failed to save the file: %v", err)
	}

	// Extract the FileSystem
	log.Println("Extracting basefs...")
	if err := os.MkdirAll("basefs", 0755); err != nil {
		log.Fatalf("Failed to extraxt the file system: %v", err)
	}

	tarCmd := exec.Command("tar", "-xzf", tarPath, "-C", "basefs")
	if err := tarCmd.Run(); err != nil {
		log.Fatalf("Failed to extract tarball: %v", err)
	}

	log.Println("Rootfs provisioned successfully.")
}
