// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The tests in this file's family run a CLI the way a user does: the
// provider is BUILT, and the CLI finds it through a dev_overrides entry for
// its own registry's address, with no init and no in-process attachment.
// That matters where the CLIs differ in how they resolve provider addresses
// (a state written by the other CLI), which an in-process provider hides.

// Registry hosts of the two CLIs.
const (
	terraformHost = "registry.terraform.io"
	tofuHost      = "registry.opentofu.org"
)

// envProviderDir names a directory holding a provider binary built from this
// checkout; CI builds it before the tests start. Unset, the test builds one.
const envProviderDir = "ATAILA_PROVIDER_DIR"

// buildProvider returns a directory holding the provider binary: the one in
// ATAILA_PROVIDER_DIR, or one it builds.
func buildProvider(t *testing.T) string {
	t.Helper()
	name := "terraform-provider-ataila"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if dir := os.Getenv(envProviderDir); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s=%s holds no %s: %v", envProviderDir, dir, name, err)
		}
		return dir
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command is needed to build the provider: %v", err)
	}
	dir := t.TempDir()
	cmd := exec.Command(goBin, "build", "-o", filepath.Join(dir, name), ".")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the provider: %v\n%s", err, out)
	}
	return dir
}

// cli is one CLI working in a directory it may share with another CLI.
type cli struct {
	t    *testing.T
	bin  string
	name string // "terraform" or "tofu", for messages
	dir  string
	rc   string
}

// newCLI prepares bin to run in dir with the built provider at providerDir,
// registered under host only.
func newCLI(t *testing.T, bin, host, dir, providerDir string) *cli {
	t.Helper()
	name := "terraform"
	if host == tofuHost {
		name = "tofu"
	}
	rc := filepath.Join(t.TempDir(), name+".rc")
	cfg := fmt.Sprintf("provider_installation {\n  dev_overrides {\n    %q = %q\n  }\n}\n",
		host+"/ataila/ataila", filepath.ToSlash(providerDir))
	if err := os.WriteFile(rc, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return &cli{t: t, bin: bin, name: name, dir: dir, rc: rc}
}

// run executes one command and returns its combined output and exit code.
func (c *cli) run(args ...string) (string, int) {
	c.t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(),
		"TF_CLI_CONFIG_FILE="+c.rc,
		"TOFU_CLI_CONFIG_FILE="+c.rc,
		"TF_IN_AUTOMATION=1",
		"CHECKPOINT_DISABLE=1",
		"TF_REATTACH_PROVIDERS=",
		// Each CLI keeps its own working data; the state file is shared.
		"TF_DATA_DIR="+filepath.Join(c.dir, ".data-"+c.name),
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		c.t.Fatalf("%s %s: %v", c.name, strings.Join(args, " "), err)
	}
	c.t.Logf("%s %s: exit %d", c.name, strings.Join(args, " "), code)
	return out.String(), code
}

// must runs a command that has to succeed.
func (c *cli) must(args ...string) string {
	c.t.Helper()
	out, code := c.run(args...)
	if code != 0 {
		c.t.Fatalf("%s %s failed (exit %d):\n%s", c.name, strings.Join(args, " "), code, out)
	}
	return out
}

func (c *cli) apply(extra ...string) string {
	c.t.Helper()
	return c.must(append([]string{"apply", "-input=false", "-no-color", "-auto-approve"}, extra...)...)
}

// noDiff requires plan -detailed-exitcode to find nothing to do.
func (c *cli) noDiff(why string) {
	c.t.Helper()
	out, code := c.run("plan", "-input=false", "-no-color", "-detailed-exitcode")
	if code != 0 {
		c.t.Fatalf("%s: %s plan found something to do or failed (exit %d):\n%s", why, c.name, code, out)
	}
}

// stateProviders lists the provider addresses a state file names.
func stateProviders(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"provider": `) && !seen[line] {
			seen[line] = true
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(line, `"provider": `), ","))
		}
	}
	return out
}
