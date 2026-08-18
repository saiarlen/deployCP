package linux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"deploycp/internal/platform"
)

func TestRestrictedShellScriptSyntax(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "restricted-shell")
	if err := os.WriteFile(scriptPath, []byte(restrictedShellScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("restricted shell syntax is invalid: %v\n%s", err, out)
	}
}

func TestRestrictedShellRoutesTransfersThroughPrivilegedHelper(t *testing.T) {
	for _, want := range []string{
		"sftp-server|internal-sftp",
		"deploycp-transfer restricted-sftp",
		"deploycp-transfer restricted-scp \"$requested_command\"",
		"deploycp-transfer restricted-shell",
	} {
		if !strings.Contains(restrictedShellScript, want) {
			t.Fatalf("restricted shell did not contain %q", want)
		}
	}
	if strings.Contains(restrictedShellScript, ".deploycp_allowed_root") {
		t.Fatal("restricted shell must not trust the user-writable allowed-root marker")
	}
}

func TestManagedSSHDConfigForcesEverySiteUserRequestThroughDispatcher(t *testing.T) {
	config, err := managedSSHDConfig("/usr/local/bin/deploycp-rshell")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Match Group deploycp-site-users",
		"PasswordAuthentication yes",
		"ForceCommand /usr/local/bin/deploycp-rshell",
		"Match all",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("managed sshd config did not contain %q:\n%s", want, config)
		}
	}
	if strings.HasPrefix(config, "PasswordAuthentication") {
		t.Fatal("password authentication must not be enabled globally")
	}
	if strings.Contains(config, "AuthenticationMethods") {
		t.Fatal("restricted SSH relies on OpenSSH's default authentication method; an explicit value is not portable")
	}
}

func TestEnsureManagedSSHDIncludeIsFirstAndIdempotent(t *testing.T) {
	mainConfig := "PasswordAuthentication no\n"
	managed := "/etc/ssh/sshd_config.d/99-deploycp.conf"
	updated := ensureManagedSSHDInclude(mainConfig, managed)
	wantPrefix := "Include " + managed + "\n"
	if !strings.HasPrefix(updated, wantPrefix) {
		t.Fatalf("managed include was not first:\n%s", updated)
	}
	if again := ensureManagedSSHDInclude(updated, managed); again != updated {
		t.Fatal("managed include insertion is not idempotent")
	}
}

func TestManagedSSHDConfigRejectsUnsafeShellPath(t *testing.T) {
	for _, path := range []string{"deploycp-rshell", "/usr/local/bin/deploycp shell"} {
		if _, err := managedSSHDConfig(path); err == nil {
			t.Fatalf("expected unsafe shell path %q to be rejected", path)
		}
	}
}

func TestValidateServiceDefinitionRejectsControlCharacters(t *testing.T) {
	err := validateServiceDefinition(platform.ServiceDefinition{
		Name:          "deploycp-app-test",
		Description:   "DeployCP app: test\nUser=root",
		ExecPath:      "/bin/echo",
		WorkingDir:    "/tmp",
		RestartPolicy: "on-failure",
	})
	if err == nil {
		t.Fatal("expected control character validation error")
	}
}

func TestValidateServiceDefinitionRejectsRelativeSystemdPaths(t *testing.T) {
	err := validateServiceDefinition(platform.ServiceDefinition{
		Name:          "deploycp-app-test",
		Description:   "DeployCP app: test",
		ExecPath:      "python3",
		WorkingDir:    "/tmp",
		RestartPolicy: "on-failure",
	})
	if err == nil || !strings.Contains(err.Error(), "exec path must be absolute") {
		t.Fatalf("expected absolute exec path error, got %v", err)
	}

	err = validateServiceDefinition(platform.ServiceDefinition{
		Name:          "deploycp-app-test",
		Description:   "DeployCP app: test",
		ExecPath:      "/usr/bin/python3",
		WorkingDir:    "/tmp",
		StdoutPath:    "storage/logs/stdout.log",
		RestartPolicy: "on-failure",
	})
	if err == nil || !strings.Contains(err.Error(), "stdout path must be absolute") {
		t.Fatalf("expected absolute stdout path error, got %v", err)
	}
}

func TestRenderUnitQuotesExecArgsAndEnvironment(t *testing.T) {
	unit := renderUnit(platform.ServiceDefinition{
		Name:          "deploycp-app-test",
		Description:   "DeployCP app: test",
		ExecPath:      "/opt/my app/bin/server",
		Args:          []string{"--message", `hello "world"`},
		WorkingDir:    "/opt/my app",
		User:          "deployuser",
		Environment:   map[string]string{"APP_VALUE": `100% "ok"`},
		RestartPolicy: "on-failure",
		MemoryMax:     "512M",
	})

	for _, want := range []string{
		`User=deployuser`,
		`Type=simple`,
		`WorkingDirectory=/opt/my\x20app`,
		`ExecStart="/opt/my app/bin/server" "--message" "hello \"world\""`,
		`Environment="APP_VALUE=100%% \"ok\""`,
		`RestartSec=5`,
		`KillMode=control-group`,
		`LimitNOFILE=50000`,
		`MemoryMax=512M`,
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit did not contain %q:\n%s", want, unit)
		}
	}
}
