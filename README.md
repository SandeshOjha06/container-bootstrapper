# bootstrapper
 
A minimal container runtime built from scratch using raw Linux primitives: namespaces, `pivot_root`, OverlayFS, cgroups v2, and a hand-rolled virtual network stack.
 
## What it does
 
`bootstrapper` creates an isolated Linux container by directly composing the same low-level mechanisms that Docker and containerd are built on:
 
- **Namespace isolation** — `CLONE_NEWUTS`, `CLONE_NEWPID`, `CLONE_NEWNS`, `CLONE_NEWNET` via `clone()`
- **Filesystem isolation** — OverlayFS (copy-on-write) + `pivot_root`, with a real Alpine Linux rootfs as the read-only base layer
- **Resource limits** — cgroups v2 (`pids.max`, `memory.max`)
- **Networking** — a Linux bridge (`br-boot`), a veth pair connecting host and container, NAT via `iptables` MASQUERADE, and IP forwarding, giving the container real outbound internet access
- **Exec-into-running-container** support, similar to `docker exec`
## Architecture
 
```
┌─────────────────────────── HOST ───────────────────────────┐
│                                                               │
│  parent()                                                    │
│   ├─ downloads/extracts Alpine rootfs → ./basefs              │
│   ├─ clone() with namespace flags → spawns child             │
│   ├─ creates cgroup, applies pids.max / memory.max            │
│   ├─ creates br-boot bridge (10.0.0.1/24)                     │
│   ├─ configures NAT (iptables MASQUERADE) + FORWARD rules     │
│   ├─ creates veth pair, moves veth-child into container netns │
│   ├─ signals child via pipe to unblock                        │
│   └─ on exit: tears down bridge, cgroup, iptables, upper/work  │
│                                                               │
│  ┌──────────────── CONTAINER (new namespaces) ─────────────┐ │
│  │  child()                                                  │ │
│  │   ├─ blocks on pipe until parent finishes host-side setup │ │
│  │   ├─ configures lo + veth-child (10.0.0.2/24) + route      │ │
│  │   ├─ makes mount namespace private (MS_PRIVATE|MS_REC)     │ │
│  │   ├─ mounts OverlayFS: lowerdir=basefs, upperdir=upper     │ │
│  │   ├─ pivot_root into ./merged, unmounts old root            │ │
│  │   ├─ mounts /proc, /sys fresh inside new root                │ │
│  │   ├─ injects /etc/resolv.conf (nameserver 8.8.8.8)           │ │
│  │   └─ exec's /bin/sh as PID 1                                 │ │
│  └───────────────────────────────────────────────────────────┘ │
└───────────────────────────────────────────────────────────────┘
```
 
### Filesystem layering (OverlayFS)
 
| Layer | Path | Role |
|---|---|---|
| lowerdir | `./basefs` | Read-only Alpine rootfs, downloaded once and reused |
| upperdir | `./upper` | Writable layer — all container writes land here (copy-on-write) |
| workdir | `./work` | Kernel-internal scratch space for atomic overlay operations |
| merged | `./merged` | Unified view — becomes the container's `/` after `pivot_root` |
 
## Usage
 
### Run a container
```sh
sudo ./bootstrapper run
```
This downloads the Alpine rootfs (first run only), sets up networking and cgroups, and drops you into a shell inside the new container.
 
### Exec into a running container
```sh
sudo ./bootstrapper exec <pid>
```
Injects a new shell into an already-running container, given its host-side PID (also written to `/tmp/bootstrapper.pid` on start).
 
## Requirements
 
- Linux with cgroups v2 mounted at `/sys/fs/cgroup`
- Root privileges (namespace creation, mounts, cgroups, iptables)
- `iptables` and `tar` available on the host
- Outbound internet access (to download the Alpine base image on first run)
## Current limitations
 
- **No user namespace** — root inside the container is the same root as the host (`CLONE_NEWUSER` not implemented), so container root has full host privileges if it escapes.
- **No disk quota** — cgroups constrain memory and PID count, but not disk usage; a container can write until the host's real disk fills up.
- **Single container at a time** — `upper`, `work`, `merged`, and the cgroup path are hardcoded to fixed paths rather than parameterized per container.
- **No image pulling** — the root filesystem is a single hardcoded Alpine tarball, not an OCI registry image.
- **No seccomp / capability dropping** — the container process retains the full default capability set.