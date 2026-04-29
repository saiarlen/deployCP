package services

import (
	"fmt"
	"os"
	"os/exec"
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
	Verified  bool
}

var (
	runtimeGoRe     = regexp.MustCompile(`^go[0-9]+\.[0-9]+(\.[0-9]+)?$`)
	runtimeNodeRe   = regexp.MustCompile(`^node[0-9]+(\.[0-9]+){0,2}$`)
	runtimePythonRe = regexp.MustCompile(`^python[0-9]+\.[0-9]+(\.[0-9]+)?$`)
	runtimePHPRe    = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)
	aptPHPPkgRe     = regexp.MustCompile(`^php([0-9]+\.[0-9]+)-(cli|fpm)$`)
	aptPythonPkgRe  = regexp.MustCompile(`^python([0-9]+\.[0-9]+)$`)
	aptGoPkgRe      = regexp.MustCompile(`^golang-(1\.[0-9]+)-go$`)
	aptNodeMajorRe  = regexp.MustCompile(`([0-9]{2,})\.`)
	rhelPHPModuleRe = regexp.MustCompile(`^php\s+([0-9]+\.[0-9]+)\b`)
	rhelNodeModRe   = regexp.MustCompile(`^nodejs\s+([0-9]+)\b`)
	rhelPythonPkgRe = regexp.MustCompile(`^python([0-9]+\.[0-9]+)$`)
	rhelPHPRemiRe   = regexp.MustCompile(`^php([0-9]{2})-php-(cli|fpm)$`)
	rhelVersionRe   = regexp.MustCompile(`([0-9]+\.[0-9]+)`)
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

func (s *SettingsService) PHPFPMVersions() []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	if entries, err := os.ReadDir("/etc/php"); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			version := strings.TrimSpace(entry.Name())
			if version == "" || !isValidRuntimeVersion("php", version) {
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
	for _, version := range s.detectRPMPHPFPMVersions() {
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		out = append(out, version)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

func (s *SettingsService) PHPFPMVersionChoices() []string {
	installed := s.PHPFPMVersions()
	seen := map[string]struct{}{}
	out := make([]string, 0, len(installed)+6)
	for _, version := range installed {
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		out = append(out, version)
	}
	var available []string
	switch s.detectPackageManager() {
	case "apt":
		available = s.aptAvailableRuntimeVersions("php")
	case "dnf", "yum":
		available = s.rhelAvailableRuntimeVersions("php")
	}
	for _, version := range available {
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		out = append(out, version)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

func (s *SettingsService) RuntimeVersionStates(runtime string) []RuntimeVersionState {
	installed := s.installedRuntimeVersions(runtime)
	out := make([]RuntimeVersionState, 0, len(installed))
	for _, v := range installed {
		out = append(out, RuntimeVersionState{Version: v, Installed: true, Verified: s.verifyInstalledRuntimeVersion(runtime, v)})
	}
	return out
}

func (s *SettingsService) AvailableRuntimeVersions(runtime string) []string {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	if runtime == "" {
		return []string{}
	}
	installedSet := map[string]struct{}{}
	for _, item := range s.installedRuntimeVersions(runtime) {
		installedSet[item] = struct{}{}
	}
	if strings.EqualFold(strings.TrimSpace(s.cfg.Features.PlatformMode), "dryrun") {
		return filterOutInstalledVersions(s.configuredRuntimeVersions(runtime), installedSet)
	}
	if managed := s.managedAvailableRuntimeVersions(runtime); len(managed) > 0 {
		return filterOutInstalledVersions(managed, installedSet)
	}
	switch s.detectPackageManager() {
	case "apt":
		return filterOutInstalledVersions(s.aptAvailableRuntimeVersions(runtime), installedSet)
	case "dnf", "yum":
		return filterOutInstalledVersions(s.rhelAvailableRuntimeVersions(runtime), installedSet)
	default:
		return []string{}
	}
}

func (s *SettingsService) managedAvailableRuntimeVersions(runtime string) []string {
	script := filepath.Join(".", "scripts", "linux", "runtime-manager.sh")
	if st, err := os.Stat(script); err != nil || st.IsDir() {
		return []string{}
	}
	out, err := exec.Command(
		"/bin/bash",
		script,
		"list-remote",
		runtime,
		"-",
		s.cfg.Paths.RuntimeRoot,
	).Output()
	if err != nil {
		return []string{}
	}
	seen := map[string]struct{}{}
	versions := make([]string, 0, 8)
	for _, line := range strings.Split(string(out), "\n") {
		version := strings.TrimSpace(line)
		if version == "" || !isValidRuntimeVersion(runtime, version) {
			continue
		}
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	return versions
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
	if available := s.AvailableRuntimeVersions(runtime); len(available) > 0 && !containsRuntimeVersion(available, v) {
		return fmt.Errorf("%s is not available from the managed runtime catalog", v)
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

func (s *SettingsService) detectPackageManager() string {
	if _, err := exec.LookPath("apt-cache"); err == nil {
		if _, err := exec.LookPath("apt-get"); err == nil {
			return "apt"
		}
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		return "dnf"
	}
	if _, err := exec.LookPath("yum"); err == nil {
		return "yum"
	}
	return ""
}

func (s *SettingsService) aptAvailableRuntimeVersions(runtime string) []string {
	switch runtime {
	case "php":
		return s.aptSearchMappedVersions(`^php[0-9]+\.[0-9]+-(cli|fpm)$`, func(pkg string) string {
			matches := aptPHPPkgRe.FindStringSubmatch(pkg)
			if len(matches) != 3 {
				return ""
			}
			return matches[1]
		})
	case "python":
		return s.aptSearchMappedVersions(`^python[0-9]+\.[0-9]+$`, func(pkg string) string {
			matches := aptPythonPkgRe.FindStringSubmatch(pkg)
			if len(matches) != 2 {
				return ""
			}
			return "python" + matches[1]
		})
	case "go":
		return s.aptSearchMappedVersions(`^golang-1\.[0-9]+-go$`, func(pkg string) string {
			matches := aptGoPkgRe.FindStringSubmatch(pkg)
			if len(matches) != 2 {
				return ""
			}
			return "go" + matches[1]
		})
	case "node":
		return s.aptNodeAvailableVersions()
	default:
		return []string{}
	}
}

func (s *SettingsService) aptSearchMappedVersions(pattern string, mapper func(string) string) []string {
	if strings.TrimSpace(pattern) == "" || mapper == nil {
		return []string{}
	}
	out, err := exec.Command("apt-cache", "search", "--names-only", pattern).Output()
	if err != nil {
		return []string{}
	}
	seen := map[string]struct{}{}
	versions := make([]string, 0, 8)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		version := strings.TrimSpace(mapper(fields[0]))
		if version == "" || !isValidRuntimeVersionGuess(version) {
			continue
		}
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i] > versions[j] })
	return versions
}

func (s *SettingsService) aptNodeAvailableVersions() []string {
	out, err := exec.Command("apt-cache", "policy", "nodejs").Output()
	if err != nil {
		return []string{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Candidate:") {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "Candidate:"))
		if candidate == "" || candidate == "(none)" {
			return []string{}
		}
		matches := aptNodeMajorRe.FindStringSubmatch(candidate)
		if len(matches) != 2 {
			return []string{}
		}
		version := "node" + matches[1]
		if !isValidRuntimeVersion("node", version) {
			return []string{}
		}
		return []string{version}
	}
	return []string{}
}

func (s *SettingsService) rhelAvailableRuntimeVersions(runtime string) []string {
	manager := s.detectPackageManager()
	switch runtime {
	case "php":
		versions := s.rhelModuleVersions(manager, "php", rhelPHPModuleRe, func(v string) string { return v })
		versions = appendUniqueStrings(versions, s.rhelSearchMappedVersions(manager, "php*-php-cli", func(pkg string) string {
			matches := rhelPHPRemiRe.FindStringSubmatch(pkg)
			if len(matches) != 3 {
				return ""
			}
			if len(matches[1]) != 2 {
				return ""
			}
			return matches[1][:1] + "." + matches[1][1:]
		})...)
		return sortRuntimeVersionsDesc(versions)
	case "node":
		versions := s.rhelModuleVersions(manager, "nodejs", rhelNodeModRe, func(v string) string { return "node" + v })
		if len(versions) == 0 {
			if version := s.rhelInfoVersion(manager, "nodejs"); version != "" {
				versions = append(versions, "node"+strings.Split(version, ".")[0])
			}
		}
		return sortRuntimeVersionsDesc(versions)
	case "python":
		versions := s.rhelSearchMappedVersions(manager, "python3*", func(pkg string) string {
			matches := rhelPythonPkgRe.FindStringSubmatch(stripArchSuffix(pkg))
			if len(matches) != 2 {
				return ""
			}
			return "python" + matches[1]
		})
		return sortRuntimeVersionsDesc(versions)
	case "go":
		if version := s.rhelInfoVersion(manager, "golang"); version != "" {
			return []string{"go" + version}
		}
		return []string{}
	default:
		return []string{}
	}
}

func (s *SettingsService) rhelModuleVersions(manager, module string, re *regexp.Regexp, mapper func(string) string) []string {
	if strings.TrimSpace(manager) == "" || strings.TrimSpace(module) == "" || re == nil || mapper == nil {
		return []string{}
	}
	out, err := exec.Command(manager, "module", "list", module, "--all").Output()
	if err != nil {
		return []string{}
	}
	seen := map[string]struct{}{}
	versions := make([]string, 0, 6)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Name") || strings.HasPrefix(line, "Hint") {
			continue
		}
		matches := re.FindStringSubmatch(line)
		if len(matches) < 2 {
			continue
		}
		version := strings.TrimSpace(mapper(matches[1]))
		if version == "" || !isValidRuntimeVersionGuess(version) {
			continue
		}
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	return versions
}

func (s *SettingsService) rhelSearchMappedVersions(manager, query string, mapper func(string) string) []string {
	if strings.TrimSpace(manager) == "" || strings.TrimSpace(query) == "" || mapper == nil {
		return []string{}
	}
	out, err := exec.Command(manager, "list", "available", query).Output()
	if err != nil {
		return []string{}
	}
	seen := map[string]struct{}{}
	versions := make([]string, 0, 8)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		pkg := stripArchSuffix(fields[0])
		version := strings.TrimSpace(mapper(pkg))
		if version == "" || !isValidRuntimeVersionGuess(version) {
			continue
		}
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	return versions
}

func (s *SettingsService) rhelInfoVersion(manager, pkg string) string {
	if strings.TrimSpace(manager) == "" || strings.TrimSpace(pkg) == "" {
		return ""
	}
	out, err := exec.Command(manager, "info", pkg).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "version") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		match := rhelVersionRe.FindString(strings.TrimSpace(parts[1]))
		if match == "" {
			continue
		}
		return match
	}
	return ""
}

func (s *SettingsService) detectRPMPHPFPMVersions() []string {
	manager := s.detectPackageManager()
	if manager != "dnf" && manager != "yum" {
		return []string{}
	}
	out, err := exec.Command("rpm", "-qa").Output()
	if err != nil {
		return []string{}
	}
	seen := map[string]struct{}{}
	versions := make([]string, 0, 6)
	for _, line := range strings.Split(string(out), "\n") {
		pkg := stripArchSuffix(strings.TrimSpace(line))
		if pkg == "" || !strings.Contains(pkg, "php-fpm") {
			continue
		}
		if strings.HasPrefix(pkg, "php-fpm-") || pkg == "php-fpm" {
			if version := s.rhelInfoVersion(manager, "php-fpm"); version != "" {
				if _, ok := seen[version]; !ok {
					seen[version] = struct{}{}
					versions = append(versions, version)
				}
			}
			continue
		}
		matches := regexp.MustCompile(`^php([0-9]{2})-php-fpm`).FindStringSubmatch(pkg)
		if len(matches) != 2 {
			continue
		}
		version := matches[1][:1] + "." + matches[1][1:]
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i] > versions[j] })
	return versions
}

func stripArchSuffix(pkg string) string {
	pkg = strings.TrimSpace(pkg)
	for _, arch := range []string{".x86_64", ".aarch64", ".noarch", ".src", ".i686", ".ppc64le", ".s390x"} {
		if strings.HasSuffix(pkg, arch) {
			return strings.TrimSuffix(pkg, arch)
		}
	}
	return pkg
}

func appendUniqueStrings(base []string, extras ...string) []string {
	seen := make(map[string]struct{}, len(base))
	out := append([]string{}, base...)
	for _, item := range out {
		seen[item] = struct{}{}
	}
	for _, item := range extras {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func sortRuntimeVersionsDesc(items []string) []string {
	out := append([]string{}, items...)
	sort.SliceStable(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

func isValidRuntimeVersionGuess(version string) bool {
	switch {
	case runtimeGoRe.MatchString(version):
		return true
	case runtimeNodeRe.MatchString(version):
		return true
	case runtimePythonRe.MatchString(version):
		return true
	case runtimePHPRe.MatchString(version):
		return true
	default:
		return false
	}
}

func (s *SettingsService) verifyInstalledRuntimeVersion(runtime, version string) bool {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if runtime == "" || version == "" {
		return false
	}
	binary := ""
	args := []string{}
	expect := ""
	switch runtime {
	case "go":
		binary = filepath.Join(s.cfg.Paths.RuntimeRoot, runtime, version, "bin", "go")
		args = []string{"version"}
		expect = version
	case "node":
		binary = filepath.Join(s.cfg.Paths.RuntimeRoot, runtime, version, "bin", "node")
		args = []string{"--version"}
		expect = strings.TrimPrefix(version, "node")
	case "python":
		binary = filepath.Join(s.cfg.Paths.RuntimeRoot, runtime, version, "bin", "python3")
		args = []string{"--version"}
		expect = strings.TrimPrefix(version, "python")
	case "php":
		binary = filepath.Join(s.cfg.Paths.RuntimeRoot, runtime, version, "bin", "php")
		args = []string{"-v"}
		expect = version
	default:
		return false
	}
	if st, err := os.Stat(binary); err != nil || st.IsDir() {
		return false
	}
	out, err := exec.Command(binary, args...).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), expect)
}

func containsRuntimeVersion(items []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, item := range items {
		if strings.TrimSpace(item) == target {
			return true
		}
	}
	return false
}

func filterOutInstalledVersions(items []string, installed map[string]struct{}) []string {
	if len(items) == 0 || len(installed) == 0 {
		return append([]string{}, items...)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if _, ok := installed[strings.TrimSpace(item)]; ok {
			continue
		}
		out = append(out, item)
	}
	return out
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
