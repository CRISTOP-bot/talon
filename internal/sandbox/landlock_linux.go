//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// Landlock syscall numbers (asm-generic) and ABI constants.
// See Documentation/filesystems/landlock.rst in the kernel tree.
const (
	sysCreateRuleset = 444
	sysAddRule       = 445
	sysRestrictSelf  = 446

	createRulesetVersion = 1

	rulePathBeneath = 1
	ruleNetPort     = 2

	// Filesystem access rights, ABI 1.
	accessExecute    = 1 << 0
	accessWriteFile  = 1 << 1
	accessReadFile   = 1 << 2
	accessReadDir    = 1 << 3
	accessRemoveDir  = 1 << 4
	accessRemoveFile = 1 << 5
	accessMakeChar   = 1 << 6
	accessMakeDir    = 1 << 7
	accessMakeReg    = 1 << 8
	accessMakeSock   = 1 << 9
	accessMakeFIFO   = 1 << 10
	accessMakeBlock  = 1 << 11
	accessMakeSym    = 1 << 12

	accessRefer    = 1 << 13 // ABI 2
	accessTruncate = 1 << 14 // ABI 3
	accessIOCTLDev = 1 << 15 // ABI 5

	// Network access rights, ABI 4 (a separate handled set).
	accessNetConnectTCP = 1 << 0
	accessNetBindTCP    = 1 << 1
)

// rulesetAttr mirrors struct landlock_ruleset_attr.
type rulesetAttr struct {
	HandledAccessFS  uint64
	HandledAccessNet uint64
}

// pathBeneathAttr mirrors struct landlock_path_beneath_attr.
type pathBeneathAttr struct {
	AllowedAccess uint64
	ParentFd      int32
	_pad          uint32
}

// netPortAttr mirrors struct landlock_net_port_attr.
type netPortAttr struct {
	AllowedPort uint64
}

// HelperFlag is the internal flag Talon re-executes itself with.
const HelperFlag = "--talon-sandbox-exec"

// PolicyFlag is the internal flag carrying the policy file path.
const PolicyFlag = "--talon-sandbox-policy"

// PolicyEnv names the environment variable that carries the policy path.
const PolicyEnv = "TALON_SANDBOX_POLICY"

// oPath is O_PATH (010000000 on Linux), which opens a reference to a path
// without reading or executing it. It is not exported by the syscall package,
// so the value is declared here.
const oPath = 0x200000

// probeLandlock returns the supported Landlock ABI version, or 0.
func probeLandlock() int {
	ret, _, errno := syscall.Syscall(sysCreateRuleset, 0, 0, createRulesetVersion)
	if errno != 0 {
		return 0
	}
	return int(ret)
}

// HelperSpec is the JSON contract between Talon and its helper process. It is
// exported so tests can exercise the kernel path with a test binary instead of
// the installed talon binary.
type HelperSpec struct {
	Policy   Policy   `json:"policy"`
	ExecPath string   `json:"exec_path"`
	Args     []string `json:"args"`
}

// EncodeHelperSpec renders a helper specification.
func EncodeHelperSpec(spec HelperSpec) ([]byte, error) { return json.Marshal(spec) }

// DecodeHelperSpec parses a helper specification.
func DecodeHelperSpec(data []byte) (HelperSpec, error) {
	var spec HelperSpec
	err := json.Unmarshal(data, &spec)
	return spec, err
}

// helperCommand builds a command that runs the target under Landlock by
// re-executing Talon as a helper. It reports false when kernel isolation is not
// available, so the caller can fall back and tell the user.
func helperCommand(policy Policy, name string, args []string) (*exec.Cmd, bool, error) {
	if probeLandlock() == 0 {
		return nil, false, nil
	}
	exePath, err := exec.LookPath(name)
	if err != nil {
		return nil, false, fmt.Errorf("cannot resolve %s: %w", name, err)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, false, fmt.Errorf("cannot locate the talon binary: %w", err)
	}
	spec := HelperSpec{Policy: policy, ExecPath: exePath, Args: args}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, false, err
	}
	f, err := os.CreateTemp("", "talon-sandbox-*.json")
	if err != nil {
		return nil, false, fmt.Errorf("cannot write the sandbox policy: %w", err)
	}
	policyPath := f.Name()
	if _, err := f.Write(encoded); err != nil {
		f.Close()
		_ = os.Remove(policyPath)
		return nil, false, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(policyPath)
		return nil, false, err
	}
	if err := os.Chmod(policyPath, 0o600); err != nil {
		_ = os.Remove(policyPath)
		return nil, false, err
	}
	// The executable itself must be inside a root that grants EXECUTE.
	policy.ExecPaths = append(policy.ExecPaths, exePath)

	helperArgs := append([]string{HelperFlag, PolicyFlag, policyPath, "--"}, args...)
	cmd := exec.Command(self, helperArgs...)
	cmd.Env = append(append([]string{}, policy.Env...), PolicyEnv+"="+policyPath)
	cmd.Dir = policy.WorkingDir
	return cmd, true, nil
}

// RunHelper is the helper's entry point. It applies Landlock and then execs the
// requested command. It must run in its own process: Landlock cannot be undone.
func RunHelper(argv []string) int {
	policyPath := ""
	for i := 0; i < len(argv); i++ {
		if argv[i] == PolicyFlag && i+1 < len(argv) {
			policyPath = argv[i+1]
			i++
		}
	}
	if policyPath == "" {
		policyPath = os.Getenv(PolicyEnv)
	}
	if policyPath == "" {
		fmt.Fprintln(os.Stderr, "talon sandbox helper: no policy provided")
		return 2
	}
	data, err := os.ReadFile(policyPath)
	// The policy is single-use: remove it so a stray process cannot replay it.
	_ = os.Remove(policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "talon sandbox helper: cannot read policy: %v\n", err)
		return 2
	}
	var spec HelperSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		fmt.Fprintf(os.Stderr, "talon sandbox helper: invalid policy: %v\n", err)
		return 2
	}
	if err := spec.Policy.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "talon sandbox helper: %v\n", err)
		return 2
	}
	if spec.ExecPath == "" {
		fmt.Fprintln(os.Stderr, "talon sandbox helper: no command given")
		return 2
	}
	if err := applyLandlock(spec.Policy); err != nil {
		fmt.Fprintf(os.Stderr, "talon sandbox helper: %v\n", err)
		return 2
	}
	argv2 := append([]string{spec.ExecPath}, spec.Args...)
	if err := syscall.Exec(spec.ExecPath, argv2, spec.Policy.Env); err != nil {
		fmt.Fprintf(os.Stderr, "talon sandbox helper: exec %s: %v\n", spec.ExecPath, err)
		return 126
	}
	return 0
}

// prSetNoNewPrivs is prctl(PR_SET_NO_NEW_PRIVS). Landlock refuses to enforce a
// ruleset without it unless the process has CAP_SYS_ADMIN, and setting it also
// removes setuid escalation from the sandboxed command.
const prSetNoNewPrivs = 38

// applyLandlock installs and enforces a ruleset on the current process.
func applyLandlock(p Policy) error {
	abi := probeLandlock()
	if abi == 0 {
		return fmt.Errorf("landlock is not available on this kernel")
	}
	// Mandatory before restrict_self for an unprivileged process.
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", errno)
	}
	handled := uint64(accessExecute | accessWriteFile | accessReadFile | accessReadDir |
		accessRemoveDir | accessRemoveFile | accessMakeChar | accessMakeDir |
		accessMakeReg | accessMakeSock | accessMakeFIFO | accessMakeBlock | accessMakeSym)
	if abi >= 2 {
		handled |= accessRefer
	}
	if abi >= 3 {
		handled |= accessTruncate
	}
	if abi >= 5 {
		// Not handling IOCTL_DEV keeps /dev/null and friends usable; device
		// creation is still denied because MAKE_CHAR/BLOCK are handled.
		handled &^= accessIOCTLDev
	}

	var netHandled, netPorts uint64
	if p.Network == NetworkPorts && abi >= 4 {
		netHandled = accessNetConnectTCP
		for _, port := range p.AllowedPorts {
			if port > 0 && port <= 65535 {
				netPorts |= 1 << uint(port)
			}
		}
	}

	attr := rulesetAttr{HandledAccessFS: handled, HandledAccessNet: netHandled}
	fd, _, errno := syscall.Syscall(sysCreateRuleset,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	rulesetFd := int(fd)
	defer syscall.Close(rulesetFd)

	readOnly := uint64(accessExecute | accessReadFile | accessReadDir)
	readWrite := readOnly | accessWriteFile | accessRemoveDir | accessRemoveFile |
		accessMakeChar | accessMakeDir | accessMakeReg | accessMakeSock |
		accessMakeFIFO | accessMakeBlock | accessMakeSym
	if abi >= 2 {
		readWrite |= accessRefer
	}
	if abi >= 3 {
		readWrite |= accessTruncate
	}

	for _, root := range p.ReadRoots {
		if err := addPathRule(rulesetFd, root, readOnly); err != nil {
			return fmt.Errorf("read root %s: %w", root, err)
		}
	}
	for _, root := range p.WriteRoots {
		if err := addPathRule(rulesetFd, root, readWrite); err != nil {
			return fmt.Errorf("write root %s: %w", root, err)
		}
	}
	if p.WorkingDir != "" {
		_ = addPathRule(rulesetFd, p.WorkingDir, readOnly)
	}
	if netHandled != 0 {
		npa := netPortAttr{AllowedPort: netPorts}
		if _, _, errno := syscall.Syscall6(sysAddRule, uintptr(rulesetFd), ruleNetPort,
			uintptr(unsafe.Pointer(&npa)), 0, 0, 0); errno != 0 {
			return fmt.Errorf("network rule: %w", errno)
		}
	}

	if _, _, errno := syscall.Syscall(sysRestrictSelf, uintptr(rulesetFd), 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

func addPathRule(rulesetFd int, path string, allowed uint64) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	fd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer syscall.Close(fd)
	pba := pathBeneathAttr{AllowedAccess: allowed, ParentFd: int32(fd)}
	if _, _, errno := syscall.Syscall6(sysAddRule, uintptr(rulesetFd), rulePathBeneath,
		uintptr(unsafe.Pointer(&pba)), 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
