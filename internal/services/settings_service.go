package services

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/repositories"
)

type SettingsService struct {
	cfg      *config.Config
	repo     *repositories.SettingRepository
	userRepo *repositories.UserPreferenceRepository
	audit    *AuditService
}

type RuntimeVersionState struct {
	Version   string
	Installed bool
}

var (
	runtimeGoRe     = regexp.MustCompile(`^go[0-9]+\.[0-9]+(\.[0-9]+)?$`)
	runtimeNodeRe   = regexp.MustCompile(`^node[0-9]+(\.[0-9]+)?$`)
	runtimePythonRe = regexp.MustCompile(`^python[0-9]+\.[0-9]+(\.[0-9]+)?$`)
	runtimePHPRe    = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)
)

func NewSettingsService(
	cfg *config.Config,
	repo *repositories.SettingRepository,
	userRepo *repositories.UserPreferenceRepository,
	audit *AuditService,
) *SettingsService {
	return &SettingsService{cfg: cfg, repo: repo, userRepo: userRepo, audit: audit}
}

func (s *SettingsService) Defaults() map[string]string {
	return map[string]string{
		"nginx_binary":               s.cfg.Paths.NginxBinary,
		"nginx_config_dir":           s.cfg.Paths.NginxConfigDir,
		"nginx_enabled_dir":          s.cfg.Paths.NginxEnabledDir,
		"nginx_available_dir":        s.cfg.Paths.NginxAvailableDir,
		"service_manager_systemd":    s.cfg.Paths.SystemctlBinary,
		"service_manager_launchd":    s.cfg.Paths.LaunchctlBinary,
		"default_site_root":          s.cfg.Paths.DefaultSiteRoot,
		"adminer_url":                s.cfg.Integrations.AdminerURL,
		"postgres_gui_url":           s.cfg.Integrations.PostgresGUIURL,
		"restricted_shell_path":      s.cfg.Paths.RestrictedShellPath,
		"php_versions":               "8.4,8.3,8.2,8.1,8.0,7.4",
		"go_versions":                "go1.25,go1.24,go1.23,go1.22,go1.21",
		"python_versions":            "python3.13,python3.12,python3.11,python3.10,python3.9",
		"node_versions":              "node24,node22,node20,node18",
		"panel_custom_domain":        "",
		"proftpd_masquerade_address": "",
		"panel_timezone":             "UTC",
		"panel_basic_auth_enabled":   "false",
		"panel_basic_auth_username":  "admin",
	}
}

func (s *SettingsService) Combined() ([]models.Setting, error) {
	stored, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	defaults := s.Defaults()
	index := map[string]models.Setting{}
	for k, v := range defaults {
		index[k] = models.Setting{Key: k, Value: v, Secret: false}
	}
	for _, item := range stored {
		index[item.Key] = item
	}
	keys := make([]string, 0, len(index))
	for k := range index {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]models.Setting, 0, len(keys))
	for _, k := range keys {
		out = append(out, index[k])
	}
	return out, nil
}

func (s *SettingsService) Update(key, value string, actor *uint, ip string) error {
	cleanValue := value
	if key == "panel_timezone" {
		normalizedTZ, err := s.NormalizeTimezone(value)
		if err != nil {
			return err
		}
		cleanValue = normalizedTZ
	}
	secret := key == "session_secret_override" || key == "panel_basic_auth_password_hash"
	if err := s.repo.Set(key, cleanValue, secret); err != nil {
		return err
	}
	if key == "panel_timezone" {
		if err := s.ApplyTimezone(cleanValue); err != nil {
			return err
		}
	}
	s.audit.Record(actor, "settings.update", "setting", key, ip, map[string]any{"secret": secret})
	return nil
}

func (s *SettingsService) Get(key string) (string, error) {
	value, err := s.repo.Get(key)
	if err != nil {
		defaults := s.Defaults()
		if def, ok := defaults[key]; ok {
			return def, nil
		}
		return "", fmt.Errorf("setting not found")
	}
	return value, nil
}

func (s *SettingsService) UserTheme(userID uint) string {
	if userID != 0 {
		if v, err := s.userRepo.Get(userID, "ui_theme"); err == nil && (v == "dark" || v == "light") {
			return v
		}
	}
	if v, err := s.repo.Get("theme"); err == nil && (v == "dark" || v == "light") {
		return v
	}
	return "light"
}

func (s *SettingsService) SetUserTheme(userID uint, theme string, actor *uint, ip string) error {
	if theme != "dark" && theme != "light" {
		return fmt.Errorf("invalid theme")
	}
	if err := s.userRepo.Set(userID, "ui_theme", theme); err != nil {
		return err
	}
	s.audit.Record(actor, "settings.theme", "user_preference", fmt.Sprintf("%d", userID), ip, map[string]any{"theme": theme})
	return nil
}

func (s *SettingsService) RuntimeVersions(runtime string) []string {
	installed := s.installedRuntimeVersions(runtime)
	if len(installed) > 0 {
		return installed
	}
	if strings.EqualFold(strings.TrimSpace(s.cfg.Features.PlatformMode), "dryrun") {
		return s.configuredRuntimeVersions(runtime)
	}
	return []string{}
}

func (s *SettingsService) RuntimeVersionStates(runtime string) []RuntimeVersionState {
	installed := s.installedRuntimeVersions(runtime)
	out := make([]RuntimeVersionState, 0, len(installed))
	for _, v := range installed {
		out = append(out, RuntimeVersionState{Version: v, Installed: true})
	}
	return out
}

func (s *SettingsService) SyncInstalledRuntimeCatalogs() error {
	for _, runtime := range []string{"go", "node", "python", "php"} {
		if err := s.syncInstalledRuntimeCatalog(runtime); err != nil {
			return err
		}
	}
	return nil
}

func (s *SettingsService) configuredRuntimeVersions(runtime string) []string {
	key := runtimeVersionKey(runtime)
	if key == "" {
		return []string{}
	}
	raw, err := s.Get(key)
	if err != nil {
		return []string{}
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';'
	})
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		v := strings.TrimSpace(part)
		if v == "" || !isValidRuntimeVersion(runtime, v) {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func (s *SettingsService) installedRuntimeVersions(runtime string) []string {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	if runtime == "" {
		return []string{}
	}
	out := make([]string, 0, 8)
	seen := map[string]struct{}{}

	root := filepath.Join(s.cfg.Paths.RuntimeRoot, runtime)
	if entries, err := os.ReadDir(root); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			version := strings.TrimSpace(entry.Name())
			if version == "" || !isValidRuntimeVersion(runtime, version) {
				continue
			}
			if _, ok := seen[version]; ok {
				continue
			}
			seen[version] = struct{}{}
			out = append(out, version)
		}
	}

	if runtime == "php" {
		if entries, err := os.ReadDir("/etc/php"); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				version := strings.TrimSpace(entry.Name())
				if version == "" || !isValidRuntimeVersion(runtime, version) {
					continue
				}
				if _, err := os.Stat(filepath.Join("/etc/php", version, "fpm")); err != nil {
					continue
				}
				if _, ok := seen[version]; ok {
					continue
				}
				seen[version] = struct{}{}
				out = append(out, version)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

func (s *SettingsService) syncInstalledRuntimeCatalog(runtime string) error {
	key := runtimeVersionKey(runtime)
	if key == "" {
		return nil
	}
	installed := s.installedRuntimeVersions(runtime)
	if len(installed) == 0 {
		return nil
	}
	configured := s.configuredRuntimeVersions(runtime)
	merged := make([]string, 0, len(installed)+len(configured))
	seen := map[string]struct{}{}
	for _, version := range installed {
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		merged = append(merged, version)
	}
	for _, version := range configured {
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		merged = append(merged, version)
	}
	current := strings.Join(configured, ",")
	next := strings.Join(merged, ",")
	if current == next {
		return nil
	}
	return s.repo.Set(key, next, false)
}

func (s *SettingsService) AddRuntimeVersion(runtime, version string, actor *uint, ip string) error {
	v := strings.TrimSpace(version)
	if !isValidRuntimeVersion(runtime, v) {
		return fmt.Errorf("invalid %s version", strings.TrimSpace(runtime))
	}
	versions := s.RuntimeVersions(runtime)
	for _, item := range versions {
		if item == v {
			return nil
		}
	}
	versions = append([]string{v}, versions...)
	return s.saveRuntimeVersions(runtime, versions, actor, ip)
}

func (s *SettingsService) RemoveRuntimeVersion(runtime, version string, actor *uint, ip string) error {
	v := strings.TrimSpace(version)
	if v == "" {
		return fmt.Errorf("version is required")
	}
	versions := s.RuntimeVersions(runtime)
	filtered := make([]string, 0, len(versions))
	for _, item := range versions {
		if item == v {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return fmt.Errorf("at least one %s version is required", strings.TrimSpace(runtime))
	}
	return s.saveRuntimeVersions(runtime, filtered, actor, ip)
}

func (s *SettingsService) saveRuntimeVersions(runtime string, versions []string, actor *uint, ip string) error {
	key := runtimeVersionKey(runtime)
	if key == "" {
		return fmt.Errorf("unsupported runtime")
	}
	clean := make([]string, 0, len(versions))
	seen := map[string]struct{}{}
	for _, item := range versions {
		v := strings.TrimSpace(item)
		if v == "" || !isValidRuntimeVersion(runtime, v) {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		clean = append(clean, v)
	}
	if len(clean) == 0 {
		return fmt.Errorf("at least one %s version is required", strings.TrimSpace(runtime))
	}
	return s.Update(key, strings.Join(clean, ","), actor, ip)
}

func runtimeVersionKey(runtime string) string {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "go":
		return "go_versions"
	case "python":
		return "python_versions"
	case "node":
		return "node_versions"
	case "php":
		return "php_versions"
	default:
		return ""
	}
}

func isValidRuntimeVersion(runtime, version string) bool {
	v := strings.TrimSpace(version)
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "go":
		return runtimeGoRe.MatchString(v)
	case "python":
		return runtimePythonRe.MatchString(v)
	case "node":
		return runtimeNodeRe.MatchString(v)
	case "php":
		return runtimePHPRe.MatchString(v)
	default:
		return false
	}
}

func (s *SettingsService) SupportedTimezones() []string {
	return []string{
		"UTC",
		"Asia/Kolkata",
		"Asia/Dubai",
		"Asia/Singapore",
		"Asia/Tokyo",
		"Australia/Sydney",
		"Europe/London",
		"Europe/Berlin",
		"Europe/Paris",
		"Europe/Moscow",
		"Africa/Johannesburg",
		"America/New_York",
		"America/Chicago",
		"America/Denver",
		"America/Los_Angeles",
		"America/Phoenix",
		"America/Toronto",
		"America/Vancouver",
		"America/Sao_Paulo",
		"Pacific/Auckland",
	}
}

func (s *SettingsService) NormalizeTimezone(value string) (string, error) {
	tz := strings.TrimSpace(value)
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", fmt.Errorf("invalid timezone")
	}
	return tz, nil
}

func (s *SettingsService) ApplyTimezone(value string) error {
	tz, err := s.NormalizeTimezone(value)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return fmt.Errorf("failed to load timezone: %w", err)
	}
	_ = os.Setenv("TZ", tz)
	time.Local = loc
	return nil
}

func (s *SettingsService) ApplyConfiguredTimezone() error {
	tz, err := s.Get("panel_timezone")
	if err != nil {
		tz = "UTC"
	}
	return s.ApplyTimezone(tz)
}
