package services

import (
	"context"
	"reflect"
	"testing"
)

func TestRestrictedShellTestCommandKeepsOriginalCommandInEnvironment(t *testing.T) {
	cmd := restrictedShellTestCommand(context.Background(), "/usr/bin/sudo", "siteuser", "/usr/local/bin/deploycp-rshell", "scp -t folder with spaces")
	want := []string{
		"/usr/bin/sudo", "-n", "-u", "siteuser", "-H", "/usr/bin/env",
		"TERM=xterm-256color", "SSH_ORIGINAL_COMMAND=scp -t folder with spaces",
		"/usr/local/bin/deploycp-rshell",
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("command args = %#v, want %#v", cmd.Args, want)
	}
}
