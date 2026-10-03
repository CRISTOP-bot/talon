// Package sandbox constrains what Talon's child processes can touch.
//
// Two layers, and Talon never pretends the second one exists:
//
//  1. In-process capability checks. Talon resolves paths itself, filters the
//     environment and decides whether a command may run. These work everywhere.
//  2. Kernel-enforced isolation for child processes. On Linux with Landlock
//     (5.13+) Talon re-executes itself as a helper, applies a Landlock ruleset
//     and only then execs the requested command. Where Landlock is unavailable
//     but bubblewrap or firejail is installed, the external helper is used.
//     Where neither is available, Status() reports that honestly and only
//     layer 1 applies.
//
// Landlock cannot be undone once applied, which is why the helper is a separate
// process: Talon's own session keeps full access while every command it spawns
// is confined.
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// NetworkMode controls a sandboxed child's network access.
type NetworkMode string

// Network modes for child processes.
const (
	// NetworkNone blocks all TCP connections (Landlock ABI 4+ only).
	NetworkNone NetworkMode = "none"
	// NetworkPorts allows only the listed TCP ports.
	NetworkPorts NetworkMode = "ports"
	// NetworkOff does not restrict networking (no kernel support available).
	NetworkOff NetworkMode = "off"
)

// Policy describes the confinement applied to a child process.
type Policy struct {
	// ReadRoots are the directories (and files) the child may read.
	ReadRoots []string
	// WriteRoots are the directories the child may create, modify or delete.
	WriteRoots []string
	// ExecPaths are the specific executables the child may exec. When empty,
	// ExecRoots (usually the system directories) are used instead.
	ExecPaths []string
	// Env is the exact environment the child receives. It is built by the
	// caller with credentials already removed.
	Env []string
	// WorkingDir is the child's working directory.
	WorkingDir string
	// Network selects the child's network access.
	Network NetworkMode
	// AllowedPorts is used when Network is NetworkPorts.
	AllowedPorts []int
	// ShareNetwork makes the child inherit Talon's network namespace, which is
	// the only way to keep network isolation portable. It is off by default.
	ShareNetwork bool
}

// Capability is one thing a sandbox can enforce. They are reported separately
// because they are enforced by different layers.
type Capability string

// Capabilities.
const (
	CapFSRead    Capability = "filesystem.read"
	CapFSWrite   Capability = "filesystem.write"
	CapExec      Capability = "process.exec"
	CapNetwork   Capability = "network.connect"
	CapEnv       Capability = "environment"
	CapDevices   Capability = "device_access"
	CapProcesses Capability = "process.spawn"
)

// Status describes what isolation is actually available on this machine.
type Status struct {
	// Kernel is the mechanism that would be used: "landlock", "landlock+netns",
	// "bubblewrap", "firejail" or "none".
	Kernel string `json:"kernel"`
	// Available reports whether kernel-level isolation can be applied.
	Available bool `json:"available"`
	// LandlockABI is the supported Landlock ABI version, or 0.
	LandlockABI int `json:"landlock_abi,omitempty"`
	// Enforced lists capabilities the kernel can enforce for child processes.
	Enforced []Capability `json:"enforced"`
	// InProcess lists capabilities Talon enforces itself regardless of the kernel.
	InProcess []Capability `json:"in_process"`
	// Notes explains limitations in plain language.
	Notes []string `json:"notes"`
}

// String renders the status for the terminal.
func (s Status) String() string {
	var b strings.Builder
	if s.Available {
		fmt.Fprintf(&b, "kernel isolation: %s (Landlock ABI %d)", s.Kernel, s.LandlockABI)
	} else {
		b.WriteString("kernel isolation: NOT AVAILABLE")
	}
	if len(s.Enforced) > 0 {
		fmt.Fprintf(&b, "\n  enforced by the kernel: %s", joinCaps(s.Enforced))
	}
	if len(s.InProcess) > 0 {
		fmt.Fprintf(&b, "\n  enforced by Talon:     %s", joinCaps(s.InProcess))
	}
	for _, n := range s.Notes {
		b.WriteString("\n  note: ")
		b.WriteString(n)
	}
	return b.String()
}

func joinCaps(caps []Capability) string {
	parts := make([]string, 0, len(caps))
	for _, c := range caps {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ", ")
}

// InProcessCapabilities are always enforced by Talon itself.
func InProcessCapabilities() []Capability {
	caps := []Capability{CapFSRead, CapFSWrite, CapExec, CapEnv, CapProcesses}
	sort.Slice(caps, func(i, j int) bool { return caps[i] < caps[j] })
	return caps
}

var (
	cachedStatus *Status
	cachedProbe  bool
)

// Detect probes the machine for kernel isolation support. The result is cached
// because the probe is a syscall, not a policy decision.
func Detect() Status {
	if cachedProbe && cachedStatus != nil {
		return *cachedStatus
	}
	cachedProbe = true
	s := Status{
		Kernel:    "none",
		Available: false,
		InProcess: InProcessCapabilities(),
		Notes: []string{
			"Talon enforces path containment, environment filtering and command policy in-process; " +
				"that layer is outside the model's reach and cannot be bypassed by document content.",
		},
	}

	if abi := probeLandlock(); abi > 0 {
		s.Kernel = "landlock"
		s.Available = true
		s.LandlockABI = abi
		s.Enforced = []Capability{CapFSRead, CapFSWrite, CapExec, CapProcesses}
		s.Notes = append(s.Notes,
			fmt.Sprintf("Landlock ABI %d is available, so every command runs in a helper process confined by the kernel.", abi))
		if abi >= 4 {
			s.Enforced = append(s.Enforced, CapNetwork)
			s.Notes = append(s.Notes,
				"TCP connections can be limited to an explicit port list; DNS and UDP are not mediated by Landlock.")
		} else {
			s.Notes = append(s.Notes,
				"this Landlock ABI cannot restrict networking; kernel 6.7 or newer is needed for port control.")
		}
		if abi < 5 {
			s.Notes = append(s.Notes,
				"device ioctls are not mediated on this Landlock ABI; device creation stays denied and Talon's path policy still applies.")
		}
		s.Notes = append(s.Notes,
			"sandboxed commands run with no_new_privs set, so setuid escalation is unavailable to them.")
		s.Notes = append(s.Notes,
			"Talon does not currently build bwrap or firejail command lines; they are reported but not used, "+
				"so the only kernel isolation in use is Landlock.")
		sort.Slice(s.Enforced, func(i, j int) bool { return s.Enforced[i] < s.Enforced[j] })
		cachedStatus = &s
		return s
	}

	if runtime.GOOS != "linux" {
		s.Notes = append(s.Notes,
			fmt.Sprintf("kernel isolation is not implemented for %s: child processes run with your user's full privileges, "+
				"guarded only by Talon's policy layer.", runtime.GOOS))
	}
	for _, helper := range []struct{ bin, name string }{
		{"bwrap", "bubblewrap"},
		{"firejail", "firejail"},
	} {
		if path, err := exec.LookPath(helper.bin); err == nil {
			s.Notes = append(s.Notes,
				helper.name+" is installed at "+path+" but Talon does not build its command line; "+
					"Landlock is the only kernel isolation currently used.")
			break
		}
	}
	cachedStatus = &s
	return s
}

// Available reports whether kernel isolation can be enforced.
func Available() bool { return Detect().Available }

// ResetCache clears the probe result (tests).
func ResetCache() { cachedStatus, cachedProbe = nil, false }

// DefaultPolicy builds a policy that confines a command to the workspace while
// still letting it read the toolchain it needs.
func DefaultPolicy(workspace string, env []string) Policy {
	roots := []string{workspace}
	for _, d := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/alternatives", "/etc/ssl", "/opt/homebrew", "/usr/local"} {
		if isDir(d) {
			roots = append(roots, d)
		}
	}
	return Policy{
		ReadRoots:    roots,
		WriteRoots:   []string{workspace},
		WorkingDir:   workspace,
		Env:          env,
		Network:      NetworkOff,
		ShareNetwork: false,
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Validate reports configuration mistakes before a sandbox is attempted.
func (p Policy) Validate() error {
	if p.WorkingDir == "" {
		return fmt.Errorf("sandbox policy needs a working directory")
	}
	if !filepath.IsAbs(p.WorkingDir) {
		return fmt.Errorf("sandbox working directory %q must be absolute", p.WorkingDir)
	}
	if len(p.WriteRoots) == 0 && len(p.ReadRoots) == 0 {
		return fmt.Errorf("sandbox policy has no roots: the child could not read anything")
	}
	for _, root := range append(append([]string{}, p.ReadRoots...), p.WriteRoots...) {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("sandbox root %q must be an absolute path", root)
		}
	}
	return nil
}

// Describe renders a policy for the `talon sandbox show` command.
func (p Policy) Describe() string {
	var b strings.Builder
	b.WriteString("read roots:\n")
	for _, r := range p.ReadRoots {
		b.WriteString("  " + r + "\n")
	}
	b.WriteString("write roots:\n")
	for _, r := range p.WriteRoots {
		b.WriteString("  " + r + "\n")
	}
	fmt.Fprintf(&b, "working dir: %s\n", p.WorkingDir)
	fmt.Fprintf(&b, "network:    %s", p.Network)
	if p.Network == NetworkPorts && len(p.AllowedPorts) > 0 {
		ports := make([]string, 0, len(p.AllowedPorts))
		for _, p := range p.AllowedPorts {
			ports = append(ports, fmt.Sprint(p))
		}
		b.WriteString(" (ports " + strings.Join(ports, ", ") + ")")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "env vars:   %d\n", len(p.Env))
	return b.String()
}

// Command builds an *exec.Cmd for name, applying kernel isolation when it is
// available. The boolean reports whether the kernel layer was actually used;
// callers surface that to the user instead of assuming it.
//
// When isolation is unavailable the command still runs, because refusing every
// command would be worse; but the caller must be able to say so.
func Command(policy Policy, name string, args []string) (*exec.Cmd, bool, error) {
	if err := policy.Validate(); err != nil {
		return nil, false, err
	}
	if cmd, wrapped, err := helperCommand(policy, name, args); wrapped || err != nil {
		return cmd, wrapped, err
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = policy.WorkingDir
	cmd.Env = policy.Env
	return cmd, false, nil
}
