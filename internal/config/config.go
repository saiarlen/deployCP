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
}

type AppConfig struct {
	Name    string
	Env     string
	Host    string
	Port    int
	BaseURL string
}

type DatabaseConfig struct {
	SQLitePath string
}

type SecurityConfig struct {
	SessionSecret        string
	SessionCookieName    string
	SessionSecureCookies bool
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
	NginxBinary         string
	NginxConfigDir      string
	NginxEnabledDir     string
	NginxAvailableDir   string
	SystemctlBinary     string
	LaunchctlBinary     string
	PlistDir            string
	RestrictedShellPath string
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

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		App: AppConfig{
			Name:    getEnv("APP_NAME", "DeployCP"),
			Env:     getEnv("APP_ENV", "development"),
			Host:    getEnv("APP_HOST", "0.0.0.0"),
			Port:    getEnvInt("APP_PORT", 8080),
			BaseURL: getEnv("APP_BASE_URL", "http://localhost:8080"),
		},
		Database: DatabaseConfig{
			SQLitePath: getEnv("SQLITE_PATH", "./storage/db/deploycp.sqlite"),
		},
		Security: SecurityConfig{
			SessionSecret:        getEnv("SESSION_SECRET", "change-me-in-production-now"),
			SessionCookieName:    getEnv("SESSION_COOKIE_NAME", "deploycp_session"),
			SessionSecureCookies: getEnvBool("SESSION_SECURE_COOKIES", false),
			CSRFEnabled:          getEnvBool("CSRF_ENABLED", true),
			LoginRateLimitPerMin: getEnvInt("LOGIN_RATE_LIMIT_PER_MIN", 20),
			BootstrapAdminUser:   getEnv("BOOTSTRAP_ADMIN_USERNAME", "admin"),
			BootstrapAdminEmail:  getEnv("BOOTSTRAP_ADMIN_EMAIL", "admin@localhost"),
			BootstrapAdminPass:   getEnv("BOOTSTRAP_ADMIN_PASSWORD", "admin123!ChangeNow"),
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
	}

	cfg.Paths.StorageRoot = getEnv("STORAGE_ROOT", cfg.Paths.StorageRoot)
	cfg.Paths.DefaultSiteRoot = getEnv("DEFAULT_SITE_ROOT", cfg.Paths.DefaultSiteRoot)
	cfg.Paths.LogRoot = getEnv("LOG_ROOT", cfg.Paths.LogRoot)
	cfg.Paths.NginxBinary = getEnv("NGINX_BINARY", cfg.Paths.NginxBinary)
	cfg.Paths.NginxConfigDir = getEnv("NGINX_CONFIG_DIR", cfg.Paths.NginxConfigDir)
	cfg.Paths.NginxEnabledDir = getEnv("NGINX_ENABLED_DIR", cfg.Paths.NginxEnabledDir)
	cfg.Paths.NginxAvailableDir = getEnv("NGINX_AVAILABLE_DIR", cfg.Paths.NginxAvailableDir)
	cfg.Paths.SystemctlBinary = getEnv("SYSTEMCTL_BINARY", cfg.Paths.SystemctlBinary)
	cfg.Paths.LaunchctlBinary = getEnv("LAUNCHCTL_BINARY", cfg.Paths.LaunchctlBinary)
	cfg.Paths.PlistDir = getEnv("LAUNCHD_PLIST_DIR", cfg.Paths.PlistDir)
	cfg.Paths.RestrictedShellPath = getEnv("RESTRICTED_SHELL_PATH", cfg.Paths.RestrictedShellPath)

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
	c.Paths.PlistDir = filepath.Join(root, "dryrun", "launchd")
	c.Paths.RestrictedShellPath = filepath.Join(root, "dryrun", "bin", "deploycp-rshell")
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
	if c.Security.BootstrapAdminUser == "" || c.Security.BootstrapAdminPass == "" {
		errs = append(errs, "bootstrap admin credentials cannot be empty")
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
		filepath.Join(c.Paths.StorageRoot, "generated"),
		filepath.Join(c.Paths.StorageRoot, "ssl"),
	}
	if c.Features.PlatformMode == "dryrun" {
		dirs = append(dirs,
			c.Paths.NginxConfigDir,
			c.Paths.NginxAvailableDir,
			c.Paths.NginxEnabledDir,
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
	return nil
}

func defaultPaths() PathsConfig {
	if runtime.GOOS == "darwin" {
		return PathsConfig{
			StorageRoot:         "./storage",
			DefaultSiteRoot:     "./storage/sites",
			LogRoot:             "./storage/logs",
			NginxBinary:         "/opt/homebrew/bin/nginx",
			NginxConfigDir:      "/opt/homebrew/etc/nginx",
			NginxEnabledDir:     "/opt/homebrew/etc/nginx/servers-enabled",
			NginxAvailableDir:   "/opt/homebrew/etc/nginx/servers-available",
			SystemctlBinary:     "/bin/false",
			LaunchctlBinary:     "/bin/launchctl",
			PlistDir:            "/Library/LaunchDaemons",
			RestrictedShellPath: "/usr/local/bin/deploycp-rshell",
		}
	}

	return PathsConfig{
		StorageRoot:         "./storage",
		DefaultSiteRoot:     "/var/www",
		LogRoot:             "./storage/logs",
		NginxBinary:         "/usr/sbin/nginx",
		NginxConfigDir:      "/etc/nginx",
		NginxEnabledDir:     "/etc/nginx/sites-enabled",
		NginxAvailableDir:   "/etc/nginx/sites-available",
		SystemctlBinary:     "/bin/systemctl",
		LaunchctlBinary:     "/bin/false",
		PlistDir:            "/Library/LaunchDaemons",
		RestrictedShellPath: "/usr/local/bin/deploycp-rshell",
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
