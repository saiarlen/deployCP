package platform

import "context"

type ServiceDefinition struct {
	Name          string
	Description   string
	ExecPath      string
	Args          []string
	WorkingDir    string
	User          string
	Environment   map[string]string
	RestartPolicy string
	StdoutPath    string
	StderrPath    string
}

type ServiceStatus struct {
	Name      string
	Active    bool
	Enabled   bool
	SubState  string
	RawOutput string
}

type SiteUserSpec struct {
	Username    string
	Password    string
	HomeDir     string
	AllowedRoot string
	ShellPath   string
}

type ServiceManager interface {
	Install(ctx context.Context, def ServiceDefinition) (string, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string) error
	Enable(ctx context.Context, name string) error
	Disable(ctx context.Context, name string) error
	Status(ctx context.Context, name string) (ServiceStatus, error)
	Logs(ctx context.Context, name string, lines int) (string, error)
}

type UserManager interface {
	EnsureRestrictedShell(ctx context.Context, shellPath string) error
	Create(ctx context.Context, spec SiteUserSpec) (uid int, gid int, err error)
	SyncHome(ctx context.Context, username, homeDir, allowedRoot, shellPath string) error
	SetPassword(ctx context.Context, username, password string) error
	Disable(ctx context.Context, username string) error
	Delete(ctx context.Context, username string) error
	ChownRecursive(ctx context.Context, username, path string) error
}

type NginxManager interface {
	Validate(ctx context.Context, nginxBinary string) error
	Reload(ctx context.Context, nginxBinary string) error
}

type Adapter interface {
	Name() string
	Services() ServiceManager
	Users() UserManager
	Nginx() NginxManager
}
