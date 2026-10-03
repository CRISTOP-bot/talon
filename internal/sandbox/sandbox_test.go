package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectIsHonest(t *testing.T) {
	ResetCache()
	s := Detect()
	if !s.Available {
		// Honesty requirement: an unavailable sandbox must say so and must
		// never claim kernel capabilities.
		if s.Kernel != "none" {
			t.Errorf("kernel = %q while unavailable", s.Kernel)
		}
		if len(s.Enforced) != 0 {
			t.Errorf("unavailable sandbox claims enforcement: %v", s.Enforced)
		}
		if !strings.Contains(s.String(), "NOT AVAILABLE") {
			t.Errorf("status does not say it is unavailable:\n%s", s.String())
		}
		if !strings.Contains(strings.Join(s.Notes, " "), "policy layer") {
			t.Errorf("no explanation of what still protects the user: %v", s.Notes)
		}
	}
	if len(s.InProcess) == 0 {
		t.Error("in-process capabilities must always be listed")
	}
	for _, c := range InProcessCapabilities() {
		if !containsCap(s.InProcess, c) {
			t.Errorf("%s missing from in-process list", c)
		}
	}
}

func containsCap(list []Capability, want Capability) bool {
	for _, c := range list {
		if c == want {
			return true
		}
	}
	return false
}

func TestValidateRejectsBadPolicies(t *testing.T) {
	cases := []Policy{
		{},
		{WorkingDir: "relative", ReadRoots: []string{"/tmp"}},
		{WorkingDir: "/tmp", ReadRoots: []string{"relative/root"}},
		{WorkingDir: "/tmp"},
	}
	for i, p := range cases {
		if err := p.Validate(); err == nil {
			t.Errorf("case %d should be invalid: %+v", i, p)
		}
	}
	good := Policy{WorkingDir: "/tmp", ReadRoots: []string{"/usr"}, WriteRoots: []string{"/tmp"}}
	if err := good.Validate(); err != nil {
		t.Errorf("valid policy rejected: %v", err)
	}
}

func TestDefaultPolicyConfinesToWorkspace(t *testing.T) {
	dir := t.TempDir()
	p := DefaultPolicy(dir, []string{"PATH=/usr/bin"})
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range p.ReadRoots {
		if r == dir {
			found = true
		}
	}
	if !found {
		t.Errorf("workspace missing from read roots: %v", p.ReadRoots)
	}
	if len(p.WriteRoots) != 1 || p.WriteRoots[0] != dir {
		t.Errorf("write roots = %v, want only the workspace", p.WriteRoots)
	}
	if p.Network != NetworkOff {
		t.Errorf("network = %s", p.Network)
	}
}

func TestDescribeIncludesRoots(t *testing.T) {
	p := Policy{ReadRoots: []string{"/usr"}, WriteRoots: []string{"/work"},
		WorkingDir: "/work", Network: NetworkPorts, AllowedPorts: []int{443}, Env: []string{"A=1"}}
	d := p.Describe()
	for _, want := range []string{"/usr", "/work", "network:", "443", "env vars:"} {
		if !strings.Contains(d, want) {
			t.Errorf("describe missing %q:\n%s", want, d)
		}
	}
}

func TestCommandFallsBackWithoutKernelSupport(t *testing.T) {
	ResetCache()
	dir := t.TempDir()
	policy := DefaultPolicy(dir, []string{"PATH=/usr/bin:/bin"})
	cmd, isolated, err := Command(policy, "true", nil)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd == nil {
		t.Fatal("Command returned no command")
	}
	if isolated != Available() {
		t.Errorf("isolated = %v but Available = %v", isolated, Available())
	}
	// A sandboxed command goes through the helper; an unsandboxed one runs
	// directly. Both are checked, because only one can be executed here.
	if isolated {
		if !containsArg(cmd.Args, HelperFlag) {
			t.Errorf("isolated command is not going through the helper: %v", cmd.Args)
		}
		return
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fallback command failed: %v (%s)", err, out)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// runSandboxed runs a command through the kernel helper using this test binary
// as the helper entry point, which is how the kernel layer is verified without
// requiring the installed talon binary.
func runSandboxed(t *testing.T, policy Policy, exe string, args []string) (string, error) {
	t.Helper()
	spec := HelperSpec{Policy: policy, ExecPath: exe, Args: args}
	data, err := EncodeHelperSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(t.TempDir(), "policy-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()

	helperArgs := append([]string{"-test.run=TestSandboxHelperEntry", "--", PolicyFlag, f.Name(), "--"}, exe)
	helperArgs = append(helperArgs, args...)
	cmd := exec.Command(os.Args[0], helperArgs...)
	cmd.Env = append([]string{"GO_TALON_SANDBOX_HELPER=1"}, policy.Env...)
	cmd.Dir = policy.WorkingDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestSandboxHelperEntry is the helper process used by runSandboxed.
func TestSandboxHelperEntry(t *testing.T) {
	if os.Getenv("GO_TALON_SANDBOX_HELPER") != "1" {
		t.Skip("not the helper process")
	}
	idx := -1
	for i, a := range os.Args {
		if a == "--" {
			idx = i
			break
		}
	}
	if idx < 0 {
		os.Exit(2)
	}
	code := RunHelper(os.Args[idx+1:])
	os.Exit(code)
}

// TestKernelIsolationConfinesWrites is the real test of the kernel layer: it
// runs a command that tries to write outside its write roots.
func TestKernelIsolationConfinesWrites(t *testing.T) {
	if !Available() {
		t.Skip("no kernel sandbox available on this machine")
	}
	workspace := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "escaped.txt")

	policy := DefaultPolicy(workspace, []string{"PATH=/usr/bin:/bin", "HOME=" + workspace})
	out, _ := runSandboxed(t, policy, "/bin/sh", []string{"-c", "echo escaped > " + target})
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatalf("sandboxed command wrote outside its roots: %s (%s)", target, out)
	}

	// Writing inside the workspace must still work.
	inside := filepath.Join(workspace, "ok.txt")
	out, err := runSandboxed(t, policy, "/bin/sh", []string{"-c", "echo fine > " + inside})
	if err != nil {
		t.Errorf("write inside the workspace was blocked: %v (%s)", err, out)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("expected %s to exist: %v", inside, err)
	}
}

func TestKernelIsolationBlocksReadingOutsideRoots(t *testing.T) {
	if !Available() {
		t.Skip("no kernel sandbox available on this machine")
	}
	workspace := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy(workspace, []string{"PATH=/usr/bin:/bin", "HOME=" + workspace})
	out, err := runSandboxed(t, policy, "/bin/cat", []string{secret})
	if strings.Contains(out, "classified") {
		t.Fatalf("sandboxed command read a file outside its roots: %s", out)
	}
	if err == nil {
		t.Error("the read should have failed")
	}
}

func TestHelperRemovesThePolicyFile(t *testing.T) {
	if !Available() {
		t.Skip("no kernel sandbox available")
	}
	workspace := t.TempDir()
	policy := DefaultPolicy(workspace, []string{"PATH=/usr/bin:/bin", "HOME=" + workspace})
	spec := HelperSpec{Policy: policy, ExecPath: "/bin/true"}
	data, err := EncodeHelperSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runSandboxedWithPolicyFile(t, policyPath, policy)
	if err != nil {
		t.Fatalf("helper failed: %v (%s)", err, out)
	}
	if _, err := os.Stat(policyPath); !os.IsNotExist(err) {
		t.Error("the policy file must be deleted after use so it cannot be replayed")
	}
}

func runSandboxedWithPolicyFile(t *testing.T, policyPath string, policy Policy) (string, error) {
	t.Helper()
	helperArgs := []string{"-test.run=TestSandboxHelperEntry", "--", PolicyFlag, policyPath}
	cmd := exec.Command(os.Args[0], helperArgs...)
	cmd.Env = append([]string{"GO_TALON_SANDBOX_HELPER=1"}, policy.Env...)
	cmd.Dir = policy.WorkingDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestHelperRefusesMissingPolicy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	helperArgs := []string{"-test.run=TestSandboxHelperEntry", "--"}
	cmd := exec.Command(os.Args[0], helperArgs...)
	cmd.Env = append(os.Environ(), "GO_TALON_SANDBOX_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("the helper must fail without a policy: %s", out)
	}
	if !strings.Contains(string(out), "no policy") {
		t.Errorf("helper error should explain itself: %s", out)
	}
}
