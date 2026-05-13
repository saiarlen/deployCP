package linux

import (
	"strings"
	"testing"

	"deploycp/internal/platform"
)

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
		Environment:   map[string]string{"APP_VALUE": `100% "ok"`},
		RestartPolicy: "on-failure",
	})

	for _, want := range []string{
		`WorkingDirectory=/opt/my\x20app`,
		`ExecStart="/opt/my app/bin/server" "--message" "hello \"world\""`,
		`Environment="APP_VALUE=100%% \"ok\""`,
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit did not contain %q:\n%s", want, unit)
		}
	}
}
