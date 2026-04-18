package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	App          AppConfig
	Database     DatabaseConfig
	Security     SecurityConfig
	Paths        PathsConfig
	Integrations IntegrationConfig
	Features     FeatureConfig
	Managed      ManagedConfig
}

type AppConfig struct {
	Name        string
	Env         string
	Host        string
	Port        int
	BaseURL     string
	Version     string
	ReleaseRepo string
}

type DatabaseConfig struct {
	SQLitePath string
}

type SecurityConfig struct {
	SessionSecret        string
	SessionCookieName    string
	SessionSecureCookies string
	CSRFEnabled          bool
	LoginRateLimitPerMin int
	BootstrapAdminUser   string
	BootstrapAdminEmail  string
	BootstrapAdminPass   string
}

type PathsConfig struct {
	StorageRoot         string
	DefaultSiteRoot     string
	LogRoot             string
	RuntimeRoot         string
	HTPasswdRoot        string
	CronDir             string
	NginxBinary         string
	NginxConfigDir      string
	NginxEnabledDir     string
	NginxAvailableDir   string
	SystemctlBinary     string
	LaunchctlBinary     string
	PlistDir            string
	RestrictedShellPath string
	RunuserBinary       string
	CertbotBinary       string
	UFWBinary           string
	FirewallCMDBinary   string
	IPTablesBinary      string
}

type IntegrationConfig struct {
	AdminerURL          string
	PostgresGUIURL      string
	RedisInfoTimeoutSec int
}

type FeatureConfig struct {
	EnableServiceManage bool
	EnableNginxManage   bool
	PlatformMode        string // "auto" (default) | "dryrun" — dryrun simulates all OS operations
}

type ManagedConfig struct {
	MariaDBAdminUser   string
	MariaDBAdminPass   string
	MariaDBAdminHost   string
	MariaDBAdminPort   int
	PostgresAdminUser  string
	PostgresAdminPass  string
	PostgresAdminHost  string
	PostgresAdminPort  int
	PostgresAdminDB    string
	FTPNoLoginShell    string
	ProFTPDConfDir     string
	ProFTPDServiceName string
	RedisServerBinary  string
	VarnishConfigDir   string
	VarnishServiceName string
	VarnishMainVCL     string
	VarnishIncludeVCL  string
	VarnishdBinary     string
}

func Load() (*Config, error) {
	loadEnvFiles()

	cfg := &Config{
		App: AppConfig{
			Name:        getEnv("APP_NAME", "DeployCP"),
			Env:         getEnv("APP_ENV", "development"),
			Host:        getEnv("APP_HOST", "0.0.0.0"),
			Port:        getEnvInt("APP_PORT", 2024),
			BaseURL:     getEnv("APP_BASE_URL", "http://localhost:2024"),
			Version:     getEnv("APP_VERSION", ""),
			ReleaseRepo: getEnv("DEPLOYCP_REPO", "saiarlen/deployCP"),
		},
		Database: DatabaseConfig{
			SQLitePath: getEnv("SQLITE_PATH", "./storage/db/deploycp.sqlite"),
		},
		Security: SecurityConfig{
			SessionSecret:        getEnv("SESSION_SECRET", "change-me-in-production-now"),
			SessionCookieName:    getEnv("SESSION_COOKIE_NAME", "deploycp_session"),
			SessionSecureCookies: normalizeSecureCookieMode(getEnv("SESSION_SECURE_COOKIES", "auto")),
			CSRFEnabled:          getEnvBool("CSRF_ENABLED", true),
			LoginRateLimitPerMin: getEnvInt("LOGIN_RATE_LIMIT_PER_MIN", 20),
			BootstrapAdminUser:   getEnv("BOOTSTRAP_ADMIN_USERNAME", ""),
			BootstrapAdminEmail:  getEnv("BOOTSTRAP_ADMIN_EMAIL", ""),
			BootstrapAdminPass:   getEnv("BOOTSTRAP_ADMIN_PASSWORD", ""),
		},
		Paths: defaultPaths(),
		Integrations: IntegrationConfig{
			AdminerURL:          getEnv("ADMINER_URL", "http://127.0.0.1:8081"),
			PostgresGUIURL:      getEnv("POSTGRES_GUI_URL", "http://127.0.0.1:8082"),
			RedisInfoTimeoutSec: getEnvInt("REDIS_INFO_TIMEOUT_SEC", 3),
		},
		Features: FeatureConfig{
			EnableServiceManage: getEnvBool("FEATURE_SERVICE_MANAGE", true),
			EnableNginxManage:   getEnvBool("FEATURE_NGINX_MANAGE", true),
			PlatformMode:        strings.ToLower(getEnv("PLATFORM_MODE", "auto")),
		},
		Managed: ManagedConfig{
			MariaDBAdminUser:   getEnv("MARIADB_ADMIN_USER", ""),
			MariaDBAdminPass:   getEnv("MARIADB_ADMIN_PASSWORD", ""),
			MariaDBAdminHost:   getEnv("MARIADB_ADMIN_HOST", "127.0.0.1"),
			MariaDBAdminPort:   getEnvInt("MARIADB_ADMIN_PORT", 3306),
			PostgresAdminUser:  getEnv("POSTGRES_ADMIN_USER", ""),
			PostgresAdminPass:  getEnv("POSTGRES_ADMIN_PASSWORD", ""),
			PostgresAdminHost:  getEnv("POSTGRES_ADMIN_HOST", "127.0.0.1"),
			PostgresAdminPort:  getEnvInt("POSTGRES_ADMIN_PORT", 5432),
			PostgresAdminDB:    getEnv("POSTGRES_ADMIN_DB", "postgres"),
			FTPNoLoginShell:    getEnv("FTP_NOLOGIN_SHELL", "/usr/sbin/nologin"),
			ProFTPDConfDir:     getEnv("PROFTPD_CONF_DIR", "/etc/proftpd/conf.d"),
			ProFTPDServiceName: getEnv("PROFTPD_SERVICE_NAME", "proftpd"),
			RedisServerBinary:  getEnv("REDIS_SERVER_BINARY", "/usr/bin/redis-server"),
			VarnishConfigDir:   getEnv("VARNISH_CONFIG_DIR", "/etc/varnish/deploycp.d"),
			VarnishServiceName: getEnv("VARNISH_SERVICE_NAME", "varnish"),
			VarnishMainVCL:     getEnv("VARNISH_MAIN_VCL", "/etc/varnish/default.vcl"),
			VarnishIncludeVCL:  getEnv("VARNISH_INCLUDE_VCL", "/etc/varnish/deploycp.d/deploycp.vcl"),
			VarnishdBinary:     getEnv("VARNISHD_BINARY", "/usr/sbin/varnishd"),
		},
	}

	cfg.Paths.StorageRoot = getEnv("STORAGE_ROOT", cfg.Paths.StorageRoot)
	cfg.Paths.DefaultSiteRoot = getEnv("DEFAULT_SITE_ROOT", cfg.Paths.DefaultSiteRoot)
	cfg.Paths.LogRoot = getEnv("LOG_ROOT", cfg.Paths.LogRoot)
	cfg.Paths.RuntimeRoot = getEnv("RUNTIME_ROOT", cfg.Paths.RuntimeRoot)
	cfg.Paths.HTPasswdRoot = getEnv("HTPASSWD_ROOT", cfg.Paths.HTPasswdRoot)
	cfg.Paths.CronDir = getEnv("CRON_DIR", cfg.Paths.CronDir)
	cfg.Paths.NginxBinary = getEnv("NGINX_BINARY", cfg.Paths.NginxBinary)
	cfg.Paths.NginxConfigDir = getEnv("NGINX_CONFIG_DIR", cfg.Paths.NginxConfigDir)
	cfg.Paths.NginxEnabledDir = getEnv("NGINX_ENABLED_DIR", cfg.Paths.NginxEnabledDir)
	cfg.Paths.NginxAvailableDir = getEnv("NGINX_AVAILABLE_DIR", cfg.Paths.NginxAvailableDir)
	cfg.Paths.SystemctlBinary = getEnv("SYSTEMCTL_BINARY", cfg.Paths.SystemctlBinary)
	cfg.Paths.LaunchctlBinary = getEnv("LAUNCHCTL_BINARY", cfg.Paths.LaunchctlBinary)
	cfg.Paths.PlistDir = getEnv("LAUNCHD_PLIST_DIR", cfg.Paths.PlistDir)
	cfg.Paths.RestrictedShellPath = getEnv("RESTRICTED_SHELL_PATH", cfg.Paths.RestrictedShellPath)
	cfg.Paths.RunuserBinary = getEnv("RUNUSER_BINARY", cfg.Paths.RunuserBinary)
	cfg.Paths.CertbotBinary = getEnv("CERTBOT_BINARY", cfg.Paths.CertbotBinary)
	cfg.Paths.UFWBinary = getEnv("UFW_BINARY", cfg.Paths.UFWBinary)
	cfg.Paths.FirewallCMDBinary = getEnv("FIREWALLCMD_BINARY", cfg.Paths.FirewallCMDBinary)
	cfg.Paths.IPTablesBinary = getEnv("IPTABLES_BINARY", cfg.Paths.IPTablesBinary)

	if cfg.Features.PlatformMode == "dryrun" {
		cfg.applyDryrunPaths()
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.prepareDirectories(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadEnvFiles() {
	candidates := make([]string, 0, 4)
	if explicit := strings.TrimSpace(os.Getenv("DEPLOYCP_ENV_FILE")); explicit != "" {
		candidates = append(candidates, explicit)
	}
	candidates = append(candidates, ".env")
	if exePath, err := os.Executable(); err == nil && strings.TrimSpace(exePath) != "" {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, ".env"),
			filepath.Join(filepath.Dir(exeDir), ".env"),
		)
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if _, err := os.Stat(candidate); err == nil {
			_ = godotenv.Overload(candidate)
		}
	}
}

// applyDryrunPaths redirects system paths to local storage so dryrun mode
// never touches /etc, /usr/local, /Library, or any other privileged location.
func (c *Config) applyDryrunPaths() {
	root := c.Paths.StorageRoot
	if root == "" {
		root = "./storage"
	}
	c.Paths.NginxConfigDir = filepath.Join(root, "dryrun", "nginx")
	c.Paths.NginxAvailableDir = filepath.Join(root, "dryrun", "nginx", "sites-available")
	c.Paths.NginxEnabledDir = filepath.Join(root, "dryrun", "nginx", "sites-enabled")
	c.Paths.NginxBinary = "/bin/echo"
	c.Paths.SystemctlBinary = "/bin/echo"
	c.Paths.LaunchctlBinary = "/bin/echo"
	c.Paths.CronDir = filepath.Join(root, "dryrun", "cron.d")
	c.Paths.RuntimeRoot = filepath.Join(root, "dryrun", "runtimes")
	c.Paths.HTPasswdRoot = filepath.Join(root, "dryrun", "htpasswd")
	c.Paths.CertbotBinary = "/bin/echo"
	c.Paths.RunuserBinary = "/bin/echo"
	c.Paths.UFWBinary = "/bin/echo"
	c.Paths.FirewallCMDBinary = "/bin/echo"
	c.Paths.IPTablesBinary = "/bin/echo"
	c.Paths.PlistDir = filepath.Join(root, "dryrun", "launchd")
	c.Paths.RestrictedShellPath = filepath.Join(root, "dryrun", "bin", "deploycp-rshell")
	c.Managed.ProFTPDConfDir = filepath.Join(root, "dryrun", "proftpd", "conf.d")
	c.Managed.VarnishConfigDir = filepath.Join(root, "dryrun", "varnish")
	c.Managed.VarnishMainVCL = filepath.Join(root, "dryrun", "varnish", "default.vcl")
	c.Managed.VarnishIncludeVCL = filepath.Join(root, "dryrun", "varnish", "deploycp.vcl")
	c.Managed.VarnishdBinary = "/bin/echo"
	c.Managed.RedisServerBinary = "/bin/echo"
}

func normalizeSecureCookieMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on":
		return "true"
	case "false", "0", "no", "off":
		return "false"
	default:
		return "auto"
	}
}

func (c *Config) Validate() error {
	var errs []string
	if c.Security.SessionSecret == "" || len(c.Security.SessionSecret) < 16 {
		errs = append(errs, "SESSION_SECRET must be at least 16 characters")
	}
	if c.App.Port < 1 || c.App.Port > 65535 {
		errs = append(errs, "APP_PORT must be a valid TCP port")
	}
	if !strings.Contains(c.Database.SQLitePath, ".sqlite") && !strings.Contains(c.Database.SQLitePath, ".db") {
		errs = append(errs, "SQLITE_PATH should be a sqlite file path")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) prepareDirectories() error {
	dirs := []string{
		filepath.Dir(c.Database.SQLitePath),
		c.Paths.StorageRoot,
		c.Paths.DefaultSiteRoot,
		c.Paths.LogRoot,
		c.Paths.RuntimeRoot,
		c.Paths.HTPasswdRoot,
		filepath.Join(c.Paths.StorageRoot, "generated"),
		filepath.Join(c.Paths.StorageRoot, "ssl"),
	}
	if c.Features.PlatformMode == "dryrun" {
		dirs = append(dirs,
			c.Paths.NginxConfigDir,
			c.Paths.NginxAvailableDir,
			c.Paths.NginxEnabledDir,
			c.Paths.CronDir,
			c.Managed.ProFTPDConfDir,
			c.Managed.VarnishConfigDir,
			filepath.Dir(c.Managed.VarnishMainVCL),
			filepath.Dir(c.Managed.VarnishIncludeVCL),
			c.Paths.PlistDir,
			filepath.Dir(c.Paths.RestrictedShellPath),
		)
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}
	if c.Features.PlatformMode == "dryrun" {
		if err := ensureDryrunFile(c.Managed.VarnishIncludeVCL, "sub deploycp_recv {\n}\n\nsub deploycp_backend_response {\n}\n"); err != nil {
			return err
		}
		if err := ensureDryrunFile(c.Managed.VarnishMainVCL, fmt.Sprintf("vcl 4.1;\ninclude %q;\n\nsub vcl_recv {\n    call deploycp_recv;\n}\n\nsub vcl_backend_response {\n    call deploycp_backend_response;\n}\n", c.Managed.VarnishIncludeVCL)); err != nil {
			return err
		}
	}
	return nil
}

func ensureDryrunFile(path string, content string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write dryrun file %s: %w", path, err)
	}
	return nil
}

func defaultPaths() PathsConfig {
	if runtime.GOOS == "darwin" {
		return PathsConfig{
			StorageRoot:         "./storage",
			DefaultSiteRoot:     "./storage/sites",
			LogRoot:             "./storage/logs",
			RuntimeRoot:         "./storage/runtimes",
			HTPasswdRoot:        "./storage/generated/htpasswd",
			CronDir:             "/etc/cron.d",
			NginxBinary:         "/opt/homebrew/bin/nginx",
			NginxConfigDir:      "/opt/homebrew/etc/nginx",
			NginxEnabledDir:     "/opt/homebrew/etc/nginx/servers-enabled",
			NginxAvailableDir:   "/opt/homebrew/etc/nginx/servers-available",
			SystemctlBinary:     "/bin/false",
			LaunchctlBinary:     "/bin/launchctl",
			PlistDir:            "/Library/LaunchDaemons",
			RestrictedShellPath: "/usr/local/bin/deploycp-rshell",
			RunuserBinary:       "/usr/bin/su",
			CertbotBinary:       "/opt/homebrew/bin/certbot",
			UFWBinary:           "/opt/homebrew/bin/ufw",
			FirewallCMDBinary:   "/opt/homebrew/bin/firewall-cmd",
			IPTablesBinary:      "/opt/homebrew/bin/iptables",
		}
	}

	return PathsConfig{
		StorageRoot:         "./storage",
		DefaultSiteRoot:     "/home/deploycp/platforms/sites",
		LogRoot:             "./storage/logs",
		RuntimeRoot:         "./storage/runtimes",
		HTPasswdRoot:        "./storage/generated/htpasswd",
		CronDir:             "/etc/cron.d",
		NginxBinary:         "/usr/sbin/nginx",
		NginxConfigDir:      "/etc/nginx",
		NginxEnabledDir:     "/etc/nginx/sites-enabled",
		NginxAvailableDir:   "/etc/nginx/sites-available",
		SystemctlBinary:     "/bin/systemctl",
		LaunchctlBinary:     "/bin/false",
		PlistDir:            "/Library/LaunchDaemons",
		RestrictedShellPath: "/usr/local/bin/deploycp-rshell",
		RunuserBinary:       "/usr/sbin/runuser",
		CertbotBinary:       "/usr/bin/certbot",
		UFWBinary:           "/usr/sbin/ufw",
		FirewallCMDBinary:   "/usr/bin/firewall-cmd",
		IPTablesBinary:      "/usr/sbin/iptables",
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := getEnv(key, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.ToLower(getEnv(key, ""))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
