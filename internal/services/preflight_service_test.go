package services

import (
	"context"
	"reflect"
	"testing"

	"deploycp/internal/models"
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

func TestActiveSSHUsersFiltersDisabledAndUnnamedAccounts(t *testing.T) {
	users := activeSSHUsers([]models.SiteUser{
		{Username: "primary", IsActive: true, SSHEnabled: true},
		{Username: "disabled", IsActive: false, SSHEnabled: true},
		{Username: "sftp-disabled", IsActive: true, SSHEnabled: false},
		{Username: "   ", IsActive: true, SSHEnabled: true},
		{Username: "additional", IsActive: true, SSHEnabled: true},
	})
	if got, want := []string{users[0].Username, users[1].Username}, []string{"primary", "additional"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active SSH users = %#v, want %#v", got, want)
	}
}
