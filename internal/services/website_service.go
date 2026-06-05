package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
	"deploycp/internal/system/nginx"
	"deploycp/internal/utils"
	"deploycp/internal/validators"
)

type WebsiteInput struct {
	Name                 string
	RootPath             string
	Type                 string
	AppRuntime           string
	ShellRuntime         string
	ShellRuntimeVersion  string
	PHPVersion           string
	ProxyTarget          string
	Domains              []string
	CustomDirectives     string
	MaintenanceBypassIPs string
	SiteUserID           *uint
	Enabled              bool
}

type PhpSettingsData struct {
	MemoryLimit          string `json:"memory_limit"`
	MaxExecutionTime     string `json:"max_execution_time"`
	MaxInputTime         string `json:"max_input_time"`
	MaxInputVars         string `json:"max_input_vars"`
	PostMaxSize          string `json:"post_max_size"`
	UploadMaxFilesize    string `json:"upload_max_filesize"`
	AdditionalDirectives string `json:"additional_directives"`
}

var phpIniDirectiveNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

type WebsiteService struct {
	cfg        *config.Config
	repo       *repositories.WebsiteRepository
	nginxRepo  *repositories.NginxSiteRepository
	appRepo    *repositories.GoAppRepository
	services   *repositories.ManagedServiceRepository
	siteUsers  *repositories.SiteUserRepository
	ftpUsers   *repositories.FTPUserRepository
	dbRepo     *repositories.DatabaseConnectionRepository
	redisRepo  *repositories.RedisConnectionRepository
	sslRepo    *repositories.SSLCertificateRepository
	varnish    *repositories.VarnishConfigRepository
	ipBlocks   *repositories.IPBlockRepository
	botBlocks  *repositories.BotBlockRepository
	basicAuth  *repositories.BasicAuthRepository
	cloudflare *repositories.CloudflareConfigRepository
	adapter    platform.Adapter
	audit      *AuditService
	ssl        *SSLService
	runtime    *RuntimeService
	cron       *CronService
	database   *DatabaseService
	ftp        *FTPService
	varnishOS  *VarnishService
	packages   *SystemPackageService
}

func NewWebsiteService(
	cfg *config.Config,
	repo *repositories.WebsiteRepository,
	nginxRepo *repositories.NginxSiteRepository,
	appRepo *repositories.GoAppRepository,
	servicesRepo *repositories.ManagedServiceRepository,
	siteUsers *repositories.SiteUserRepository,
	ftpUsers *repositories.FTPUserRepository,
	dbRepo *repositories.DatabaseConnectionRepository,
	redisRepo *repositories.RedisConnectionRepository,
	sslRepo *repositories.SSLCertificateRepository,
	varnish *repositories.VarnishConfigRepository,
	ipBlocks *repositories.IPBlockRepository,
	botBlocks *repositories.BotBlockRepository,
	basicAuth *repositories.BasicAuthRepository,
	cloudflare *repositories.CloudflareConfigRepository,
	adapter platform.Adapter,
	audit *AuditService,
	ssl *SSLService,
	runtime *RuntimeService,
	cron *CronService,
	database *DatabaseService,
	ftp *FTPService,
	varnishOS *VarnishService,
	packages *SystemPackageService,
) *WebsiteService {
	return &WebsiteService{
		cfg:        cfg,
		repo:       repo,
		nginxRepo:  nginxRepo,
		appRepo:    appRepo,
		services:   servicesRepo,
		siteUsers:  siteUsers,
		ftpUsers:   ftpUsers,
		dbRepo:     dbRepo,
		redisRepo:  redisRepo,
		sslRepo:    sslRepo,
		varnish:    varnish,
		ipBlocks:   ipBlocks,
		botBlocks:  botBlocks,
		basicAuth:  basicAuth,
		cloudflare: cloudflare,
		adapter:    adapter,
		audit:      audit,
		ssl:        ssl,
		runtime:    runtime,
		cron:       cron,
		database:   database,
		ftp:        ftp,
		varnishOS:  varnishOS,
		packages:   packages,
	}
}

func (s *WebsiteService) List() ([]models.Website, error) {
	return s.repo.List()
}

func (s *WebsiteService) Find(id uint) (*models.Website, error) {
	return s.repo.Find(id)
}

func (s *WebsiteService) RuntimeInspection(site *models.Website) RuntimeInspection {
	if s.runtime == nil || site == nil {
		return RuntimeInspection{}
	}
	if !strings.EqualFold(strings.TrimSpace(site.Type), "php") {
		return RuntimeInspection{}
	}
	selected := strings.TrimSpace(site.PHPVersion)
	inspection := RuntimeInspection{
		Applicable:      true,
		Runtime:         "php",
		SelectedVersion: selected,
		ServiceVersion:  selected,
	}
	if selected == "" {
		inspection.Issues = append(inspection.Issues, "No PHP-FPM version is selected for this website.")
		return inspection
	}
	root := platformHomeFromWebRoot(site.RootPath)
	inspection.SSHBinary = s.runtime.PlatformShellBinaryPath(root, "php", selected)
	inspection.SSHVersion = s.runtime.PlatformShellVersion(root, "php", selected)
	if inspection.SSHVersion == "" {
		inspection.Issues = append(inspection.Issues, "PHP CLI version could not be resolved for the website shell.")
	} else if !runtimeVersionMatches("php", selected, inspection.SSHVersion) {
		inspection.Issues = append(inspection.Issues, fmt.Sprintf("PHP CLI resolves %s while PHP-FPM is set to %s.", inspection.SSHVersion, selected))
	}
	serviceVersion := phpFPMRuntimeVersion(selected)
	serviceName := "php" + serviceVersion + "-fpm"
	if s.packages != nil {
		serviceName = s.packages.ResolveServiceUnit(context.Background(), serviceName)
	}
	inspection.ServiceBinary = serviceName
	if s.adapter != nil && s.adapter.Services() != nil {
		status, err := s.adapter.Services().Status(context.Background(), serviceName)
		if err != nil || !status.Active {
			inspection.Issues = append(inspection.Issues, fmt.Sprintf("PHP-FPM service %s is not active.", serviceName))
		}
	}
	socketPath := phpFPMSocketPathForPlatform(selected, s.cfg)
	if socketPath == "" {
		inspection.Issues = append(inspection.Issues, "PHP-FPM socket path could not be determined.")
	} else if st, err := os.Stat(socketPath); err != nil || st.IsDir() {
		inspection.Issues = append(inspection.Issues, fmt.Sprintf("PHP-FPM socket is missing: %s", socketPath))
	}
	if s.nginxRepo != nil {
		if nginxCfg, err := s.nginxRepo.FindByWebsite(site.ID); err != nil || nginxCfg == nil || strings.TrimSpace(nginxCfg.ConfigPath) == "" {
			inspection.Issues = append(inspection.Issues, "Generated nginx config for this website could not be found.")
		} else if content, err := os.ReadFile(nginxCfg.ConfigPath); err != nil {
			inspection.Issues = append(inspection.Issues, "Generated nginx config for this website could not be read.")
		} else if !strings.Contains(string(content), "fastcgi_pass unix:"+socketPath+";") {
			inspection.Issues = append(inspection.Issues, fmt.Sprintf("Nginx config is not targeting the expected PHP-FPM socket %s.", socketPath))
		}
	}
	inspection.Healthy = len(inspection.Issues) == 0
	return inspection
}

func (s *WebsiteService) ManagedPHPShellFallbackUsage(version string) ([]string, error) {
	if s.runtime == nil {
		return nil, nil
	}
	items, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, 4)
	for _, site := range items {
		if !strings.EqualFold(strings.TrimSpace(site.Type), "php") {
			continue
		}
		root := platformHomeFromWebRoot(site.RootPath)
		if !s.runtime.UsesManagedPlatformRuntime(root, "php", version) {
			continue
		}
		name := strings.TrimSpace(site.Name)
		if name == "" {
			name = fmt.Sprintf("platform#%d", site.ID)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func phpFPMSocketPathForPlatform(version string, cfg *config.Config) string {
	version = phpFPMRuntimeVersion(version)
	if version == "" {
		return ""
	}
	if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.Features.PlatformMode), "dryrun") {
		return ""
	}
	if goruntime.GOOS == "darwin" {
		return fmt.Sprintf("/opt/homebrew/var/run/php@%s-fpm.sock", version)
	}
	return fmt.Sprintf("/run/php/php%s-fpm.sock", version)
}

func phpFPMRuntimeVersion(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return strings.TrimSpace(version)
}

func (s *WebsiteService) Create(ctx context.Context, in WebsiteInput, actor *uint, ip string) (*models.Website, error) {
	if err := s.ensurePHPFPMVersion(ctx, strings.TrimSpace(in.Type), strings.TrimSpace(in.PHPVersion), actor, ip); err != nil {
		return nil, err
	}
	if err := s.ensureShellRuntimeVersion(strings.TrimSpace(in.ShellRuntime), strings.TrimSpace(in.ShellRuntimeVersion)); err != nil {
		return nil, err
	}
	normalizedBypass, err := normalizeMaintenanceBypassIPs(in.MaintenanceBypassIPs)
	if err != nil {
		return nil, err
	}
	in.MaintenanceBypassIPs = normalizedBypass
	if err := s.validate(in); err != nil {
		return nil, err
	}
	if err := s.ensureDomainsAvailable(in.Domains, 0); err != nil {
		return nil, err
	}
	platformHome := platformHomeFromWebRoot(in.RootPath)
	site := &models.Website{
		Name:                 in.Name,
		RootPath:             in.RootPath,
		Type:                 in.Type,
		AppRuntime:           in.AppRuntime,
		ShellRuntime:         strings.ToLower(strings.TrimSpace(in.ShellRuntime)),
		ShellRuntimeVersion:  strings.TrimSpace(in.ShellRuntimeVersion),
		PHPVersion:           in.PHPVersion,
		ProxyTarget:          in.ProxyTarget,
		CustomDirectives:     in.CustomDirectives,
		MaintenanceBypassIPs: in.MaintenanceBypassIPs,
		SiteUserID:           in.SiteUserID,
		Enabled:              in.Enabled,
		AccessLogPath:        filepath.Join(platformHome, "logs", "access.log"),
		ErrorLogPath:         filepath.Join(platformHome, "logs", "error.log"),
	}
	if err := s.repo.Create(site, in.Domains); err != nil {
		return nil, err
	}
	rollbackCreate := func(cause error) (*models.Website, error) {
		_ = s.repo.Delete(site.ID)
		if strings.TrimSpace(platformHome) != "" {
			_ = removeTreeSafe(platformHome, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot)
		}
		return nil, cause
	}
	// Re-fetch so Domains association is loaded for nginx config generation.
	if created, err := s.repo.Find(site.ID); err == nil {
		site = created
	}
	if err := s.ensureWebsiteFilesystem(ctx, site); err != nil {
		return rollbackCreate(err)
	}
	if err := s.applyPlatformRuntime(site, actor, ip); err != nil {
		return rollbackCreate(err)
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return rollbackCreate(err)
	}
	s.audit.Record(actor, "website.create", "website", fmt.Sprintf("%d", site.ID), ip, in)
	return site, nil
}

func (s *WebsiteService) Update(ctx context.Context, id uint, in WebsiteInput, actor *uint, ip string) error {
	if err := s.ensurePHPFPMVersion(ctx, strings.TrimSpace(in.Type), strings.TrimSpace(in.PHPVersion), actor, ip); err != nil {
		return err
	}
	if err := s.ensureShellRuntimeVersion(strings.TrimSpace(in.ShellRuntime), strings.TrimSpace(in.ShellRuntimeVersion)); err != nil {
		return err
	}
	normalizedBypass, err := normalizeMaintenanceBypassIPs(in.MaintenanceBypassIPs)
	if err != nil {
		return err
	}
	in.MaintenanceBypassIPs = normalizedBypass
	if err := s.validate(in); err != nil {
		return err
	}
	if err := s.ensureDomainsAvailable(in.Domains, id); err != nil {
		return err
	}
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	site.Name = in.Name
	site.RootPath = in.RootPath
	site.Type = in.Type
	site.ShellRuntime = strings.ToLower(strings.TrimSpace(in.ShellRuntime))
	site.ShellRuntimeVersion = strings.TrimSpace(in.ShellRuntimeVersion)
	site.PHPVersion = in.PHPVersion
	site.ProxyTarget = in.ProxyTarget
	site.CustomDirectives = in.CustomDirectives
	site.MaintenanceBypassIPs = in.MaintenanceBypassIPs
	site.SiteUserID = in.SiteUserID
	site.Enabled = in.Enabled
	if err := s.repo.Update(site, in.Domains); err != nil {
		return err
	}
	if refreshed, err := s.repo.Find(id); err == nil {
		site = refreshed
	}
	if err := s.ensureWebsiteFilesystem(ctx, site); err != nil {
		return err
	}
	if err := s.applyPlatformRuntime(site, actor, ip); err != nil {
		return err
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return err
	}
	s.audit.Record(actor, "website.update", "website", fmt.Sprintf("%d", id), ip, in)
	return nil
}

func (s *WebsiteService) Delete(ctx context.Context, id uint, actor *uint, ip string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	if app := runtimeFromWebsite(site); app != nil {
		if err := s.deleteLinkedAppRuntime(ctx, app, actor, ip); err != nil {
			return err
		}
	}
	if err := s.deleteWebsiteScopedUsers(ctx, site, actor, ip); err != nil {
		return err
	}
	if err := s.deleteWebsiteLegacyData(site, actor, ip); err != nil {
		return err
	}
	if s.adapter != nil {
		if err := s.adapter.Users().DeleteSharedAccess(ctx, websiteSharedGroup(site.ID)); err != nil {
			return err
		}
	}
	if s.cron != nil {
		if err := s.cron.DeleteWebsiteJobs(ctx, site.ID, actor, ip); err != nil {
			return err
		}
	}
	if err := s.disableConfig(site.Name); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Join(s.cfg.Paths.NginxAvailableDir, site.Name+".conf")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if s.cfg.Features.EnableNginxManage {
		if err := s.EnsureNginxUnknownHostReject(); err != nil {
			return err
		}
		if err := s.cleanupDanglingNginxConfigEntries(); err != nil {
			return err
		}
		if err := s.adapter.Nginx().Validate(ctx, s.cfg.Paths.NginxBinary); err != nil {
			return err
		}
		if err := s.adapter.Nginx().Reload(ctx, s.cfg.Paths.NginxBinary); err != nil {
			return err
		}
	}
	if strings.TrimSpace(site.RootPath) != "" {
		platformHome := platformHomeFromWebRoot(site.RootPath)
		if err := removeTreeSafe(platformHome, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot); err != nil {
			return err
		}
	}
	if strings.TrimSpace(s.cfg.Paths.BackupRoot) != "" {
		backupDir := filepath.Join(s.cfg.Paths.BackupRoot, sanitizeName(site.Name))
		if err := removeTreeSafe(backupDir, s.cfg.Paths.BackupRoot, s.cfg.Paths.StorageRoot); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Also remove the legacy separate log directory if it exists outside the platform home.
	logDir := filepath.Dir(site.AccessLogPath)
	if strings.TrimSpace(logDir) == "" || logDir == "." {
		logDir = filepath.Join(s.cfg.Paths.LogRoot, "sites", site.Name)
	}
	_ = removeTreeSafe(logDir, s.cfg.Paths.LogRoot, s.cfg.Paths.StorageRoot)
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "website.delete", "website", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *WebsiteService) RemoveRuntime(ctx context.Context, id uint, actor *uint, ip string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	app := runtimeFromWebsite(site)
	if app == nil {
		return nil
	}
	if err := s.deleteLinkedAppRuntime(ctx, app, actor, ip); err != nil {
		return err
	}
	if err := s.appRepo.ClearRuntime(id); err != nil {
		return err
	}
	if err := s.RefreshConfig(ctx, id); err != nil {
		return err
	}
	s.audit.Record(actor, "platform.runtime.delete", "website", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *WebsiteService) ToggleEnabled(ctx context.Context, id uint, enabled bool, actor *uint, ip string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	site.Enabled = enabled
	if err := s.repo.Update(site, domainsFromModel(site.Domains)); err != nil {
		return err
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return err
	}
	s.audit.Record(actor, "website.toggle", "website", fmt.Sprintf("%d", id), ip, map[string]bool{"enabled": enabled})
	return nil
}

// ApplyAppProxy sets a linked website to type proxy targeting the app bind address and reloads nginx.
func (s *WebsiteService) ApplyAppProxy(ctx context.Context, websiteID *uint, host string, port int, actor *uint, ip string) error {
	if websiteID == nil || *websiteID == 0 {
		return nil
	}
	if err := s.cleanupDanglingNginxConfigEntries(); err != nil {
		return err
	}
	site, err := s.repo.Find(*websiteID)
	if err != nil {
		return err
	}
	site.Type = "proxy"
	site.ProxyTarget = fmt.Sprintf("http://%s:%d", host, port)
	if err := s.repo.Update(site, domainsFromModel(site.Domains)); err != nil {
		return err
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return err
	}
	if actor != nil {
		s.audit.Record(actor, "website.proxy_from_app", "website", fmt.Sprintf("%d", site.ID), ip, map[string]string{"proxy_target": site.ProxyTarget})
	}
	return nil
}

func (s *WebsiteService) RecentLogs(id uint, lines int) (string, string, error) {
	site, err := s.repo.Find(id)
	if err != nil {
		return "", "", err
	}
	if lines <= 0 {
		lines = 120
	}
	access, _ := tailFile(site.AccessLogPath, lines)
	errors, _ := tailFile(site.ErrorLogPath, lines)
	return access, errors, nil
}

func ParsePhpSettings(raw string) PhpSettingsData {
	var data PhpSettingsData
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &data)
	}
	data, _ = normalizePhpSettings(data)
	return data
}

func normalizePhpSettings(data PhpSettingsData) (PhpSettingsData, error) {
	if data.MemoryLimit == "" {
		data.MemoryLimit = "256M"
	}
	if data.MaxExecutionTime == "" {
		data.MaxExecutionTime = "60"
	}
	if data.MaxInputTime == "" {
		data.MaxInputTime = "60"
	}
	if data.MaxInputVars == "" {
		data.MaxInputVars = "1000"
	}
	if data.PostMaxSize == "" {
		data.PostMaxSize = "64M"
	}
	if data.UploadMaxFilesize == "" {
		data.UploadMaxFilesize = "64M"
	}
	var err error
	if data.MemoryLimit, err = normalizePHPSizeSetting("memory_limit", data.MemoryLimit); err != nil {
		return data, err
	}
	if data.PostMaxSize, err = normalizePHPSizeSetting("post_max_size", data.PostMaxSize); err != nil {
		return data, err
	}
	if data.UploadMaxFilesize, err = normalizePHPSizeSetting("upload_max_filesize", data.UploadMaxFilesize); err != nil {
		return data, err
	}
	if data.MaxExecutionTime, err = normalizePHPIntSetting("max_execution_time", data.MaxExecutionTime, 1, 3600); err != nil {
		return data, err
	}
	if data.MaxInputTime, err = normalizePHPIntSetting("max_input_time", data.MaxInputTime, 1, 3600); err != nil {
		return data, err
	}
	if data.MaxInputVars, err = normalizePHPIntSetting("max_input_vars", data.MaxInputVars, 1, 100000); err != nil {
		return data, err
	}
	if data.AdditionalDirectives, err = normalizePHPAdditionalDirectives(data.AdditionalDirectives); err != nil {
		return data, err
	}
	return data, nil
}

func normalizePHPSizeSetting(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	suffix := ""
	last := value[len(value)-1]
	if last == 'k' || last == 'K' || last == 'm' || last == 'M' || last == 'g' || last == 'G' {
		suffix = strings.ToUpper(string(last))
		value = strings.TrimSpace(value[:len(value)-1])
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("%s must be a positive size such as 128M", name)
	}
	return fmt.Sprintf("%d%s", n, suffix), nil
}

func normalizePHPIntSetting(name, value string, minValue, maxValue int) (string, error) {
	value = strings.TrimSpace(value)
	n, err := strconv.Atoi(value)
	if err != nil || n < minValue || n > maxValue {
		return "", fmt.Errorf("%s must be between %d and %d", name, minValue, maxValue)
	}
	return strconv.Itoa(n), nil
}

func normalizePHPAdditionalDirectives(raw string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.ContainsRune(line, '\x00') {
			return "", fmt.Errorf("additional PHP directive line %d contains an invalid character", i+1)
		}
		if strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "[") {
			return "", fmt.Errorf("additional PHP directive line %d must be a key = value directive, not a section", i+1)
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("additional PHP directive line %d must use key = value format", i+1)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if !phpIniDirectiveNamePattern.MatchString(key) {
			return "", fmt.Errorf("additional PHP directive line %d has an invalid directive name", i+1)
		}
		out = append(out, key+" = "+value)
	}
	return strings.Join(out, "\n"), nil
}

func (s *WebsiteService) UpdateAppRuntime(id uint, runtime string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	site.AppRuntime = runtime
	domains := make([]string, 0, len(site.Domains))
	for _, d := range site.Domains {
		domains = append(domains, d.Domain)
	}
	return s.repo.Update(site, domains)
}

func (s *WebsiteService) SyncShellRuntime(id uint, runtimeName, version string) error {
	if id == 0 {
		return nil
	}
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	runtimeName = strings.ToLower(strings.TrimSpace(runtimeName))
	version = strings.TrimSpace(version)
	if strings.TrimSpace(site.ShellRuntime) == runtimeName && strings.TrimSpace(site.ShellRuntimeVersion) == version {
		return nil
	}
	site.ShellRuntime = runtimeName
	site.ShellRuntimeVersion = version
	return s.repo.Update(site, domainsFromModel(site.Domains))
}

func (s *WebsiteService) UpdateShellRuntime(ctx context.Context, id uint, runtimeName, version string, actor *uint, ip string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	runtimeName = strings.ToLower(strings.TrimSpace(runtimeName))
	version = strings.TrimSpace(version)
	if err := s.ensureShellRuntimeVersion(runtimeName, version); err != nil {
		return err
	}
	site.ShellRuntime = runtimeName
	site.ShellRuntimeVersion = version
	if err := s.repo.Update(site, domainsFromModel(site.Domains)); err != nil {
		return err
	}
	if err := s.applyPlatformRuntime(site, actor, ip); err != nil {
		return err
	}
	s.audit.Record(actor, "website.runtime.update", "website", fmt.Sprintf("%d", id), ip, map[string]any{
		"runtime": runtimeName,
		"version": version,
	})
	return nil
}

func (s *WebsiteService) UpdatePhpSettings(ctx context.Context, id uint, phpVersion string, data PhpSettingsData, actor *uint, ip string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	data, err = normalizePhpSettings(data)
	if err != nil {
		return err
	}
	if phpVersion != "" {
		if err := s.ensurePHPFPMVersion(ctx, "php", strings.TrimSpace(phpVersion), actor, ip); err != nil {
			return err
		}
		site.PHPVersion = phpVersion
	}
	b, _ := json.Marshal(data)
	site.PhpSettings = string(b)
	domains := make([]string, 0, len(site.Domains))
	for _, d := range site.Domains {
		domains = append(domains, d.Domain)
	}
	if err := s.repo.Update(site, domains); err != nil {
		return err
	}
	if err := s.ensureWebsiteFilesystem(ctx, site); err != nil {
		return err
	}
	if err := s.applyPlatformRuntime(site, actor, ip); err != nil {
		return err
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return err
	}
	if err := s.restartPHPFPMVersion(ctx, strings.TrimSpace(site.PHPVersion)); err != nil {
		return err
	}
	s.audit.Record(actor, "website.php_settings.update", "website", fmt.Sprintf("%d", id), ip, map[string]any{
		"php_version": strings.TrimSpace(site.PHPVersion),
	})
	return nil
}

func (s *WebsiteService) ensurePHPFPMVersion(ctx context.Context, siteType, version string, actor *uint, ip string) error {
	if !strings.EqualFold(strings.TrimSpace(siteType), "php") {
		return nil
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return fmt.Errorf("php-fpm version is required")
	}
	if s.packages == nil || s.cfg == nil || s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	serviceVersion := phpFPMRuntimeVersion(version)
	serviceName := "php" + serviceVersion + "-fpm"
	if !s.packages.IsInstalled(ctx, serviceName) {
		return fmt.Errorf("PHP-FPM %s is not installed. Install PHP %s from Settings before creating or updating this PHP platform", serviceVersion, serviceVersion)
	}
	unitName := s.packages.ResolveServiceUnit(ctx, serviceName)
	if unitName == "" {
		unitName = serviceName
	}
	_ = s.adapter.Services().Enable(ctx, unitName)
	_ = s.adapter.Services().Start(ctx, unitName)
	return nil
}

func (s *WebsiteService) restartPHPFPMVersion(ctx context.Context, version string) error {
	if s == nil || s.adapter == nil || s.cfg == nil || s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	serviceVersion := phpFPMRuntimeVersion(version)
	if serviceVersion == "" {
		return nil
	}
	serviceName := "php" + serviceVersion + "-fpm"
	if s.packages != nil {
		if unitName := s.packages.ResolveServiceUnit(ctx, serviceName); strings.TrimSpace(unitName) != "" {
			serviceName = unitName
		}
	}
	return s.adapter.Services().Restart(ctx, serviceName)
}

func (s *WebsiteService) RefreshConfig(ctx context.Context, id uint) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	if err := s.ensureWebsiteFilesystem(ctx, site); err != nil {
		return err
	}
	if err := s.applyPlatformRuntime(site, nil, ""); err != nil {
		return err
	}
	return s.writeNginxConfig(ctx, site)
}

func (s *WebsiteService) ensureWebsiteFilesystem(ctx context.Context, site *models.Website) error {
	if site == nil {
		return nil
	}
	platformHome := platformHomeFromWebRoot(site.RootPath)
	requiredDirs := []string{
		platformHome,
		site.RootPath,
		filepath.Join(platformHome, "logs"),
	}
	for _, dir := range requiredDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create platform directory %s: %w", dir, err)
		}
		ensurePathTraversable(dir)
	}
	for _, logPath := range []string{site.AccessLogPath, site.ErrorLogPath} {
		if strings.TrimSpace(logPath) == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return fmt.Errorf("prepare log directory: %w", err)
		}
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			if err := os.WriteFile(logPath, []byte(""), 0o664); err != nil {
				return fmt.Errorf("create log file: %w", err)
			}
		}
	}
	if err := s.ensurePublicWebRoot(site.RootPath); err != nil {
		return err
	}
	if err := s.syncPHPUserINI(site); err != nil {
		return err
	}
	memberNames := map[string]struct{}{}
	siteUser := site.SiteUser
	if siteUser == nil && site.SiteUserID != nil && *site.SiteUserID > 0 && s.siteUsers != nil {
		if item, err := s.siteUsers.Find(*site.SiteUserID); err == nil {
			siteUser = item
			site.SiteUser = item
		}
	}
	if siteUser != nil && strings.TrimSpace(siteUser.Username) != "" {
		expectedHome := platformHome
		expectedAllowedRoot := platformHome
		needsSync := filepath.Clean(strings.TrimSpace(siteUser.HomeDirectory)) != filepath.Clean(expectedHome) ||
			filepath.Clean(strings.TrimSpace(siteUser.AllowedRoot)) != filepath.Clean(expectedAllowedRoot) ||
			strings.TrimSpace(siteUser.Shell) != strings.TrimSpace(s.cfg.Paths.RestrictedShellPath)
		if needsSync {
			if err := s.adapter.Users().SyncHome(ctx, siteUser.Username, expectedHome, expectedAllowedRoot, s.cfg.Paths.RestrictedShellPath); err != nil {
				return err
			}
			siteUser.HomeDirectory = filepath.Clean(expectedHome)
			siteUser.AllowedRoot = filepath.Clean(expectedAllowedRoot)
			siteUser.Shell = s.cfg.Paths.RestrictedShellPath
			if s.siteUsers != nil {
				if err := s.siteUsers.Update(siteUser); err != nil {
					return err
				}
			}
		}
		memberNames[strings.TrimSpace(siteUser.Username)] = struct{}{}
	}
	if s.siteUsers != nil {
		if additional, err := s.siteUsers.ListByWebsite(site.ID); err == nil {
			for _, item := range additional {
				if strings.TrimSpace(item.Username) == "" {
					continue
				}
				memberNames[strings.TrimSpace(item.Username)] = struct{}{}
			}
		}
	}
	if s.ftpUsers != nil {
		if items, err := s.ftpUsers.ListByWebsite(site.ID); err == nil {
			for _, item := range items {
				if strings.TrimSpace(item.Username) == "" {
					continue
				}
				memberNames[strings.TrimSpace(item.Username)] = struct{}{}
			}
		}
	}
	if siteUser != nil && strings.TrimSpace(siteUser.Username) != "" {
		members := make([]string, 0, len(memberNames))
		for username := range memberNames {
			members = append(members, username)
		}
		sort.Strings(members)
		if err := s.adapter.Users().SyncSharedAccess(ctx, platformHome, siteUser.Username, websiteSharedGroup(site.ID), members); err != nil {
			return err
		}
	}
	return nil
}

func (s *WebsiteService) writeNginxConfig(ctx context.Context, site *models.Website) error {
	if !s.cfg.Features.EnableNginxManage {
		return nil
	}
	var cert *models.SSLCertificate
	if s.sslRepo != nil {
		if items, err := s.sslRepo.List(); err == nil {
			cert = firstWebsiteCert(site, items)
		}
	}
	var basicAuth *models.BasicAuth
	if s.basicAuth != nil {
		basicAuth, _ = s.basicAuth.FindByWebsite(site.ID)
	}
	ipBlocks := []models.IPBlock{}
	if s.ipBlocks != nil {
		ipBlocks, _ = s.ipBlocks.ListByWebsite(site.ID)
	}
	botBlocks := []models.BotBlock{}
	if s.botBlocks != nil {
		botBlocks, _ = s.botBlocks.ListByWebsite(site.ID)
	}
	basicAuthPath := ""
	if basicAuth != nil && basicAuth.Enabled && strings.TrimSpace(basicAuth.Username) != "" && strings.TrimSpace(basicAuth.PasswordEnc) != "" {
		path, err := s.ensureBasicAuthFile(site, basicAuth)
		if err != nil {
			return err
		}
		basicAuthPath = path
	}
	cloudflareEnabled := false
	if s.cloudflare != nil {
		if cfCfg, err := s.cloudflare.FindByWebsite(site.ID); err == nil && cfCfg != nil {
			cloudflareEnabled = cfCfg.Enabled
		}
	}
	cfg := nginx.BuildWebsiteConfig(s.cfg, site, nginx.WebsiteConfigOptions{
		Certificate:       cert,
		BasicAuth:         basicAuth,
		BasicAuthPath:     basicAuthPath,
		IPBlocks:          ipBlocks,
		BotBlocks:         botBlocks,
		CloudflareEnabled: cloudflareEnabled,
	})
	if err := s.cleanupDanglingNginxConfigEntries(); err != nil {
		return err
	}
	if err := s.removeStaleNginxDomainConfigs(site, cfg); err != nil {
		return err
	}
	if err := utils.WriteFileAtomic(cfg.ConfigPath, []byte(cfg.Content), 0o644); err != nil {
		return err
	}
	if err := s.enableConfig(cfg.ConfigPath, cfg.EnabledPath); err != nil {
		return err
	}
	if err := s.EnsureNginxUnknownHostReject(); err != nil {
		return err
	}
	now := time.Now()
	_ = s.nginxRepo.Upsert(&models.NginxSiteConfig{WebsiteID: site.ID, ConfigPath: cfg.ConfigPath, EnabledPath: cfg.EnabledPath, Checksum: cfg.Checksum, Enabled: true, LastValidatedAt: &now})
	if err := s.adapter.Nginx().Validate(ctx, s.cfg.Paths.NginxBinary); err != nil {
		return err
	}
	return s.adapter.Nginx().Reload(ctx, s.cfg.Paths.NginxBinary)
}

func (s *WebsiteService) EnsureNginxUnknownHostReject() error {
	if !s.cfg.Features.EnableNginxManage {
		return nil
	}
	if err := s.cleanupDanglingNginxConfigEntries(); err != nil {
		return err
	}
	if err := s.disableStockNginxDefaultSite(); err != nil {
		return err
	}
	name := "00-deploycp-catchall.conf"
	available := filepath.Join(s.cfg.Paths.NginxAvailableDir, name)
	enabled := filepath.Join(s.cfg.Paths.NginxEnabledDir, name)
	content := strings.TrimSpace(`# Managed by DeployCP. Do not edit.
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    return 444;
}`) + "\n"
	if err := utils.WriteFileAtomic(available, []byte(content), 0o644); err != nil {
		return err
	}
	return s.enableConfig(available, enabled)
}

func (s *WebsiteService) disableStockNginxDefaultSite() error {
	path := filepath.Join(s.cfg.Paths.NginxEnabledDir, "default")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	text := string(content)
	isStockDefault := strings.Contains(text, "/var/www/html") ||
		strings.Contains(text, "Default server configuration") ||
		strings.Contains(text, "Welcome to nginx")
	if !isStockDefault {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *WebsiteService) removeStaleNginxDomainConfigs(site *models.Website, current nginx.GeneratedConfig) error {
	if site == nil {
		return nil
	}
	domains := domainsFromModel(site.Domains)
	if len(domains) == 0 {
		return nil
	}
	excluded := map[string]struct{}{}
	for _, path := range []string{current.ConfigPath, current.EnabledPath} {
		if cleaned := filepath.Clean(strings.TrimSpace(path)); cleaned != "." && cleaned != "" {
			excluded[cleaned] = struct{}{}
		}
	}
	for _, dir := range []string{s.cfg.Paths.NginxAvailableDir, s.cfg.Paths.NginxEnabledDir} {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".conf" {
				return nil
			}
			cleaned := filepath.Clean(path)
			if _, ok := excluded[cleaned]; ok {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if !looksLikeDeployCPNginxConfig(s.cfg, string(content)) {
				return nil
			}
			if !nginxConfigHasAnyServerName(string(content), domains) {
				return nil
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *WebsiteService) cleanupDanglingNginxConfigEntries() error {
	if s == nil || s.cfg == nil {
		return nil
	}
	for _, dir := range []string{s.cfg.Paths.NginxEnabledDir, s.cfg.Paths.NginxAvailableDir} {
		if err := cleanupDanglingNginxDir(strings.TrimSpace(dir)); err != nil {
			return err
		}
	}
	return nil
}

func cleanupDanglingNginxDir(dir string) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		if _, err := os.Stat(target); err != nil {
			if os.IsNotExist(err) {
				if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
					return removeErr
				}
				continue
			}
			return err
		}
	}
	return nil
}

func looksLikeDeployCPNginxConfig(cfg *config.Config, content string) bool {
	if strings.Contains(content, "# Managed by DeployCP") || strings.Contains(content, "_deploycp_") {
		return true
	}
	if cfg != nil && strings.TrimSpace(cfg.Paths.DefaultSiteRoot) != "" && strings.Contains(content, strings.TrimSpace(cfg.Paths.DefaultSiteRoot)) {
		return true
	}
	return strings.Contains(content, "/deploycp/platforms/sites/")
}

func nginxConfigHasAnyServerName(content string, domains []string) bool {
	wanted := map[string]struct{}{}
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain != "" {
			wanted[domain] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "server_name ") {
			continue
		}
		line = strings.TrimSuffix(strings.TrimPrefix(line, "server_name "), ";")
		for _, name := range strings.Fields(line) {
			if _, ok := wanted[strings.ToLower(strings.TrimSpace(name))]; ok {
				return true
			}
		}
	}
	return false
}

func (s *WebsiteService) applyPlatformRuntime(site *models.Website, actor *uint, ip string) error {
	if s.runtime == nil || site == nil {
		return nil
	}
	switch site.Type {
	case "php":
		return s.runtime.ApplyPHPWebsiteRuntime(platformHomeFromWebRoot(site.RootPath), site.PHPVersion, actor, ip)
	case "proxy":
		runtimeName, version := s.linkedAppRuntime(site)
		if runtimeName != "" && version != "" {
			return s.runtime.ApplyPlatformRuntime(platformHomeFromWebRoot(site.RootPath), runtimeName, version, actor, ip)
		}
	}
	if runtimeName := strings.ToLower(strings.TrimSpace(site.ShellRuntime)); runtimeName != "" && strings.TrimSpace(site.ShellRuntimeVersion) != "" {
		return s.runtime.ApplyPlatformRuntime(platformHomeFromWebRoot(site.RootPath), runtimeName, strings.TrimSpace(site.ShellRuntimeVersion), actor, ip)
	}
	return s.runtime.ApplyPlatformRuntime(platformHomeFromWebRoot(site.RootPath), "", "", actor, ip)
}

func (s *WebsiteService) ensureShellRuntimeVersion(runtimeName, version string) error {
	runtimeName = strings.ToLower(strings.TrimSpace(runtimeName))
	version = strings.TrimSpace(version)
	if runtimeName == "" && version == "" {
		return nil
	}
	if runtimeName == "binary" {
		return nil
	}
	if runtimeName == "" || version == "" {
		return fmt.Errorf("shell runtime and version must both be selected")
	}
	if s.runtime != nil && !s.runtime.VerifyInstalledVersion(runtimeName, version) {
		return fmt.Errorf("selected %s runtime %s is not installed or is not verifiable on this server", runtimeName, version)
	}
	return nil
}

func (s *WebsiteService) linkedAppRuntime(site *models.Website) (string, string) {
	if site == nil {
		return "", ""
	}
	runtimeName := strings.ToLower(strings.TrimSpace(site.AppRuntime))
	if runtimeName == "" || s.appRepo == nil {
		return "", ""
	}
	app, err := s.appRepo.FindByWebsiteID(site.ID)
	if err != nil || app == nil {
		return runtimeName, ""
	}
	version := appEnvValue(app.EnvVars, "RUNTIME_VERSION")
	return runtimeName, strings.TrimSpace(version)
}

func (s *WebsiteService) ensurePublicWebRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create site root: %w", err)
	}
	// Site root (htdocs) must be world-readable and traversable for nginx (www-data).
	if err := os.Chmod(root, 0o755); err != nil {
		return fmt.Errorf("set site root permissions: %w", err)
	}
	// Secure .deploycp in both htdocs and platform home.
	for _, base := range []string{root, platformHomeFromWebRoot(root)} {
		hiddenDir := filepath.Join(base, ".deploycp")
		if stat, err := os.Stat(hiddenDir); err == nil && stat.IsDir() {
			if filepath.Clean(base) == filepath.Clean(platformHomeFromWebRoot(root)) {
				_ = os.Chmod(hiddenDir, 0o750)
			} else {
				_ = os.Chmod(hiddenDir, 0o700)
			}
		}
	}
	indexPath := filepath.Join(root, "index.html")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		page := `<!doctype html>
<html><head><meta charset="utf-8"><title>Welcome</title>
<style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#0c0c0d;color:#e4e4e7}div{text-align:center}h1{font-size:1.5rem;font-weight:600;margin:0 0 .5rem}p{color:#71717a;font-size:.875rem}</style>
</head><body><div><h1>Site Ready</h1><p>Replace this file with your content.</p></div></body></html>
`
		_ = os.WriteFile(indexPath, []byte(page), 0o664)
	}
	notFoundPath := filepath.Join(root, "_deploycp_404.html")
	if _, err := os.Stat(notFoundPath); os.IsNotExist(err) {
		page := `<!doctype html>
<html><head><meta charset="utf-8"><title>Page Not Found</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root{color-scheme:dark light}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px;background:#0b1220;color:#e5e7eb;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
.card{max-width:620px;width:100%;border:1px solid rgba(148,163,184,.18);background:rgba(15,23,42,.88);backdrop-filter:blur(12px);border-radius:24px;padding:32px;box-shadow:0 20px 50px rgba(2,6,23,.35)}
.eyebrow{display:inline-flex;padding:6px 10px;border-radius:999px;background:rgba(59,130,246,.14);color:#93c5fd;font-size:.75rem;font-weight:700;letter-spacing:.08em;text-transform:uppercase}
h1{margin:18px 0 10px;font-size:2rem;line-height:1.1}
p{margin:0;color:#94a3b8;line-height:1.7}
.code{margin-top:18px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.82rem;color:#cbd5e1;background:rgba(15,23,42,.72);border:1px solid rgba(148,163,184,.14);padding:10px 12px;border-radius:14px}
</style></head>
<body><div class="card"><span class="eyebrow">DeployCP</span><h1>Page not found</h1><p>The requested file or route does not exist for this platform. If this is a static site, add the file under <strong>htdocs</strong>. If this should be handled by an app router, configure the platform accordingly.</p><div class="code">HTTP 404 · Generated by DeployCP</div></div></body></html>
`
		_ = os.WriteFile(notFoundPath, []byte(page), 0o664)
	}
	maintenancePath := filepath.Join(root, "_deploycp_maintenance.html")
	if _, err := os.Stat(maintenancePath); os.IsNotExist(err) {
		page := `<!doctype html>
<html><head><meta charset="utf-8"><title>Scheduled Maintenance</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root{color-scheme:dark light}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px;background:#0f172a;color:#e2e8f0;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
.card{max-width:620px;width:100%;border:1px solid rgba(148,163,184,.18);background:rgba(15,23,42,.9);backdrop-filter:blur(10px);border-radius:24px;padding:32px;box-shadow:0 20px 50px rgba(2,6,23,.35)}
.eyebrow{display:inline-flex;padding:6px 10px;border-radius:999px;background:rgba(59,130,246,.14);color:#93c5fd;font-size:.75rem;font-weight:700;letter-spacing:.08em;text-transform:uppercase}
h1{margin:18px 0 10px;font-size:2rem;line-height:1.1}
p{margin:0;color:#94a3b8;line-height:1.7}
.code{margin-top:18px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.82rem;color:#cbd5e1;background:rgba(15,23,42,.72);border:1px solid rgba(148,163,184,.14);padding:10px 12px;border-radius:14px}
</style></head>
<body><div class="card"><span class="eyebrow">DeployCP</span><h1>Scheduled maintenance</h1><p>This platform is temporarily unavailable while maintenance is in progress. Please try again in a few minutes.</p><div class="code">HTTP 503 · Generated by DeployCP</div></div></body></html>
`
		_ = os.WriteFile(maintenancePath, []byte(page), 0o664)
	}
	strayAllowed := filepath.Join(root, ".deploycp_allowed_root")
	if _, err := os.Stat(strayAllowed); err == nil {
		_ = os.Remove(strayAllowed)
	}
	// Ensure all existing files in the root are world-readable so nginx can serve them.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fp := filepath.Join(root, e.Name())
		if info, err := os.Stat(fp); err == nil {
			perm := info.Mode().Perm()
			desired := perm | 0o024
			if desired != perm {
				_ = os.Chmod(fp, desired)
			}
		}
	}
	return nil
}

func (s *WebsiteService) syncPHPUserINI(site *models.Website) error {
	if site == nil || strings.TrimSpace(site.RootPath) == "" {
		return nil
	}
	path := filepath.Join(site.RootPath, ".user.ini")
	if !strings.EqualFold(strings.TrimSpace(site.Type), "php") {
		return removeManagedPHPUserINI(path)
	}
	data := ParsePhpSettings(site.PhpSettings)
	content := renderPHPUserINI(data)
	if err := utils.WriteFileAtomic(path, []byte(content), 0o664); err != nil {
		return fmt.Errorf("write PHP .user.ini: %w", err)
	}
	return nil
}

func removeManagedPHPUserINI(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !strings.HasPrefix(string(content), "; Managed by DeployCP") {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func renderPHPUserINI(data PhpSettingsData) string {
	data, _ = normalizePhpSettings(data)
	var b strings.Builder
	b.WriteString("; Managed by DeployCP. Changes may be overwritten from the panel.\n")
	b.WriteString("memory_limit = " + data.MemoryLimit + "\n")
	b.WriteString("max_execution_time = " + data.MaxExecutionTime + "\n")
	b.WriteString("max_input_time = " + data.MaxInputTime + "\n")
	b.WriteString("max_input_vars = " + data.MaxInputVars + "\n")
	b.WriteString("post_max_size = " + data.PostMaxSize + "\n")
	b.WriteString("upload_max_filesize = " + data.UploadMaxFilesize + "\n")
	if strings.TrimSpace(data.AdditionalDirectives) != "" {
		b.WriteString("\n; Additional directives\n")
		b.WriteString(strings.TrimSpace(data.AdditionalDirectives))
		b.WriteString("\n")
	}
	return b.String()
}

func (s *WebsiteService) ensureBasicAuthFile(site *models.Website, auth *models.BasicAuth) (string, error) {
	path := filepath.Join(s.cfg.Paths.HTPasswdRoot, site.Name+".htpasswd")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	ensurePathTraversable(path)
	password, err := utils.DecryptString(s.cfg.Security.SessionSecret, auth.PasswordEnc)
	if err != nil {
		return "", err
	}
	hash, err := utils.HashHTPasswdPassword(password)
	if err != nil {
		return "", err
	}
	content := fmt.Sprintf("%s:%s\n", auth.Username, hash)
	if err := utils.WriteFileAtomic(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (s *WebsiteService) enableConfig(source, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	_ = os.Remove(link)
	return os.Symlink(absSource, link)
}

func (s *WebsiteService) disableConfig(name string) error {
	return os.Remove(filepath.Join(s.cfg.Paths.NginxEnabledDir, name+".conf"))
}

func (s *WebsiteService) deleteLinkedAppRuntime(ctx context.Context, app *models.GoApp, actor *uint, ip string) error {
	if app == nil {
		return nil
	}
	serviceName := strings.TrimSpace(app.ServiceName)
	unitPath := ""
	if serviceName != "" {
		if managed, err := s.services.FindByName(serviceName); err == nil && managed != nil {
			unitPath = managed.UnitPath
		}
		_ = s.adapter.Services().Stop(ctx, serviceName)
		_ = s.adapter.Services().Disable(ctx, serviceName)
		if app.WebsiteID != nil {
			if site, err := s.repo.Find(*app.WebsiteID); err == nil {
				for _, user := range runtimeSudoerUsersForSite(site, s.siteUsers) {
					if err := removeSiteUserRuntimeSudoers(s.cfg, user.Username, serviceName); err != nil {
						return err
					}
				}
			}
		}
		if err := s.services.DeleteByName(serviceName); err != nil {
			return err
		}
		if err := removeServiceUnitFile(s.cfg, s.adapter.Name(), serviceName, unitPath); err != nil {
			return err
		}
	}
	if strings.EqualFold(strings.TrimSpace(app.Runtime), "python") {
		if venvPath := pythonRuntimeVenvPathForApp(app); venvPath != "" {
			if err := removeTreeSafe(venvPath, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot); err != nil {
				return err
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(app.Runtime), "node") {
		if toolsPath := nodeRuntimeToolsPathForApp(app); toolsPath != "" {
			if err := removeTreeSafe(toolsPath, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot); err != nil {
				return err
			}
		}
	}
	for _, logPath := range []string{app.StdoutLogPath, app.StderrLogPath} {
		if strings.TrimSpace(logPath) == "" {
			continue
		}
		logDir := filepath.Dir(logPath)
		if err := removeTreeSafe(logDir, s.cfg.Paths.LogRoot, s.cfg.Paths.StorageRoot); err != nil {
			return err
		}
	}
	if err := s.deleteLegacyAppDatabases(app, actor, ip); err != nil {
		return err
	}
	return nil
}

func (s *WebsiteService) deleteWebsiteScopedUsers(ctx context.Context, site *models.Website, actor *uint, ip string) error {
	if site == nil {
		return nil
	}
	userIDs := map[uint]struct{}{}
	if site.SiteUserID != nil && *site.SiteUserID > 0 {
		userIDs[*site.SiteUserID] = struct{}{}
	}
	additional, err := s.siteUsers.ListByWebsite(site.ID)
	if err != nil {
		return err
	}
	for _, u := range additional {
		userIDs[u.ID] = struct{}{}
	}
	for uid := range userIDs {
		user, err := s.siteUsers.Find(uid)
		if err != nil {
			if errorsIsGormNotFound(err) {
				continue
			}
			return err
		}
		shouldDelete := true
		if site.SiteUserID != nil && *site.SiteUserID == uid {
			refs, err := s.repo.CountBySiteUserIDExcept(uid, site.ID)
			if err != nil {
				return err
			}
			if refs > 0 {
				shouldDelete = false
			}
		}
		if !shouldDelete {
			continue
		}
		_ = s.adapter.Users().Delete(ctx, user.Username)
		if err := s.siteUsers.Delete(uid); err != nil {
			return err
		}
		s.audit.Record(actor, "site_user.delete", "site_user", fmt.Sprintf("%d", uid), ip, map[string]string{"username": user.Username})
	}
	return nil
}

func (s *WebsiteService) deleteWebsiteLegacyData(site *models.Website, actor *uint, ip string) error {
	if site == nil {
		return nil
	}
	if s.ftp != nil {
		if err := s.ftp.DeleteByWebsite(context.Background(), site.ID, actor, ip); err != nil {
			return err
		}
	} else if err := s.ftpUsers.DeleteByWebsite(site.ID); err != nil {
		return err
	}
	domainSet := map[string]struct{}{}
	for _, d := range site.Domains {
		domain := strings.ToLower(strings.TrimSpace(d.Domain))
		if domain != "" {
			domainSet[domain] = struct{}{}
		}
	}

	dbItems, err := s.dbRepo.List()
	if err != nil {
		return err
	}
	siteToken := strings.ToLower(strings.TrimSpace(site.Name))
	for _, db := range dbItems {
		if db.WebsiteID != nil && *db.WebsiteID == site.ID {
			if s.database != nil {
				if err := s.database.DeleteDatabaseRecord(&db, actor, ip); err != nil {
					return err
				}
			} else if err := s.dbRepo.Delete(db.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", db.ID), ip, map[string]any{"source": "website-delete"})
			continue
		}
		if db.WebsiteID == nil && db.GoAppID == nil && siteToken != "" && strings.Contains(strings.ToLower(db.Label), siteToken) {
			if s.database != nil {
				if err := s.database.DeleteDatabaseRecord(&db, actor, ip); err != nil {
					return err
				}
			} else if err := s.dbRepo.Delete(db.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", db.ID), ip, map[string]any{"source": "website-delete-legacy-label"})
		}
	}

	redisItems, err := s.redisRepo.List()
	if err != nil {
		return err
	}
	for _, redis := range redisItems {
		if redis.WebsiteID != nil && *redis.WebsiteID == site.ID {
			if s.database != nil {
				if err := s.database.DeleteRedisRecord(&redis, actor, ip); err != nil {
					return err
				}
			} else if err := s.redisRepo.Delete(redis.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", redis.ID), ip, map[string]any{"source": "website-delete"})
			continue
		}
		if redis.WebsiteID == nil && redis.GoAppID == nil && siteToken != "" && strings.Contains(strings.ToLower(redis.Label), siteToken) {
			if s.database != nil {
				if err := s.database.DeleteRedisRecord(&redis, actor, ip); err != nil {
					return err
				}
			} else if err := s.redisRepo.Delete(redis.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", redis.ID), ip, map[string]any{"source": "website-delete-legacy-label"})
		}
	}

	sslItems, err := s.sslRepo.List()
	if err != nil {
		return err
	}
	for _, cert := range sslItems {
		if _, ok := domainSet[strings.ToLower(strings.TrimSpace(cert.Domain))]; !ok {
			continue
		}
		if s.ssl != nil {
			if err := s.ssl.Delete(cert.ID, actor, ip); err != nil {
				return err
			}
		} else {
			if err := s.sslRepo.Delete(cert.ID); err != nil {
				return err
			}
		}
		s.audit.Record(actor, "ssl.delete", "ssl_certificate", fmt.Sprintf("%d", cert.ID), ip, map[string]any{"source": "website-delete", "domain": cert.Domain})
	}
	if s.varnishOS != nil {
		if err := s.varnishOS.DeleteWebsiteConfig(context.Background(), site, actor, ip); err != nil {
			return err
		}
	}
	return nil
}

func (s *WebsiteService) deleteLegacyAppDatabases(app *models.GoApp, actor *uint, ip string) error {
	if app == nil {
		return nil
	}
	dbItems, err := s.dbRepo.List()
	if err != nil {
		return err
	}
	appToken := strings.ToLower(strings.TrimSpace(app.Name))
	for _, db := range dbItems {
		if db.GoAppID != nil && *db.GoAppID == app.ID {
			if s.database != nil {
				if err := s.database.DeleteDatabaseRecord(&db, actor, ip); err != nil {
					return err
				}
			} else if err := s.dbRepo.Delete(db.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", db.ID), ip, map[string]any{"source": "app-delete"})
			continue
		}
		if db.WebsiteID == nil && db.GoAppID == nil && appToken != "" && strings.Contains(strings.ToLower(db.Label), appToken) {
			if s.database != nil {
				if err := s.database.DeleteDatabaseRecord(&db, actor, ip); err != nil {
					return err
				}
			} else if err := s.dbRepo.Delete(db.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", db.ID), ip, map[string]any{"source": "app-delete-legacy-label"})
		}
	}

	redisItems, err := s.redisRepo.List()
	if err != nil {
		return err
	}
	for _, redis := range redisItems {
		if redis.GoAppID != nil && *redis.GoAppID == app.ID {
			if s.database != nil {
				if err := s.database.DeleteRedisRecord(&redis, actor, ip); err != nil {
					return err
				}
			} else if err := s.redisRepo.Delete(redis.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", redis.ID), ip, map[string]any{"source": "app-delete"})
			continue
		}
		if redis.WebsiteID == nil && redis.GoAppID == nil && appToken != "" && strings.Contains(strings.ToLower(redis.Label), appToken) {
			if s.database != nil {
				if err := s.database.DeleteRedisRecord(&redis, actor, ip); err != nil {
					return err
				}
			} else if err := s.redisRepo.Delete(redis.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", redis.ID), ip, map[string]any{"source": "app-delete-legacy-label"})
		}
	}
	return nil
}

func (s *WebsiteService) validate(in WebsiteInput) error {
	if err := validators.Require(in.Name, "name"); err != nil {
		return err
	}
	if err := validators.ValidatePath(in.RootPath); err != nil {
		return err
	}
	if err := validators.ValidateDomains(in.Domains); err != nil {
		return err
	}
	switch in.Type {
	case "static", "proxy", "php":
	default:
		return fmt.Errorf("type must be static, php, or proxy")
	}
	if in.Type == "php" {
		if err := validators.ValidatePHPVersion(in.PHPVersion); err != nil {
			return err
		}
	}
	if in.Type == "proxy" && strings.TrimSpace(in.ProxyTarget) == "" {
		return fmt.Errorf("proxy target is required for proxy websites")
	}
	normalizedBypass, err := normalizeMaintenanceBypassIPs(in.MaintenanceBypassIPs)
	if err != nil {
		return err
	}
	in.MaintenanceBypassIPs = normalizedBypass
	return nil
}

func (s *WebsiteService) ensureDomainsAvailable(domains []string, excludeWebsiteID uint) error {
	seen := make(map[string]struct{}, len(domains))
	for _, raw := range domains {
		domain := normalizedDomain(raw)
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			return fmt.Errorf("domain %s is listed more than once", domain)
		}
		seen[domain] = struct{}{}
		owner, err := s.repo.FindDomainOwner(domain, excludeWebsiteID)
		if err != nil {
			return err
		}
		if owner != nil {
			return fmt.Errorf("domain %s is already used by another platform", domain)
		}
	}
	return nil
}

func normalizedDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

func normalizeMaintenanceBypassIPs(raw string) (string, error) {
	values := []string{}
	seen := map[string]struct{}{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == '\t' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if ip := net.ParseIP(part); ip != nil {
			part = ip.String()
		} else if _, block, err := net.ParseCIDR(part); err == nil {
			part = block.String()
		} else {
			return "", fmt.Errorf("invalid maintenance bypass IP or CIDR: %s", part)
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		values = append(values, part)
	}
	return strings.Join(values, "\n"), nil
}

func domainsFromModel(items []models.WebsiteDomain) []string {
	out := make([]string, 0, len(items))
	for _, d := range items {
		out = append(out, d.Domain)
	}
	return out
}

func runtimeFromWebsite(site *models.Website) *models.GoApp {
	if site == nil {
		return nil
	}
	if strings.TrimSpace(site.AppRuntime) == "" && strings.TrimSpace(site.ServiceName) == "" {
		return nil
	}
	wid := site.ID
	workingDirectory := strings.TrimSpace(site.AppWorkingDirectory)
	if workingDirectory == "" {
		workingDirectory = site.RootPath
	}
	return &models.GoApp{
		ID:                  site.ID,
		Name:                site.Name,
		Runtime:             site.AppRuntime,
		ExecutionMode:       site.ExecutionMode,
		ProcessManager:      site.ProcessManager,
		BinaryPath:          site.BinaryPath,
		EntryPoint:          site.EntryPoint,
		WorkingDirectory:    workingDirectory,
		AppWorkingDirectory: site.AppWorkingDirectory,
		Host:                site.Host,
		Port:                site.Port,
		StartArgs:           site.StartArgs,
		HealthPath:          site.HealthPath,
		RestartPolicy:       site.RestartPolicy,
		Workers:             site.Workers,
		WorkerClass:         site.WorkerClass,
		MaxMemory:           site.MaxMemory,
		Timeout:             site.Timeout,
		ExecMode:            site.ExecMode,
		StdoutLogPath:       site.StdoutLogPath,
		StderrLogPath:       site.StderrLogPath,
		ServiceName:         site.ServiceName,
		WebsiteID:           &wid,
		Enabled:             site.Enabled,
	}
}

type LogFileInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"-"`
}

func (s *WebsiteService) LogDir(id uint) (string, error) {
	site, err := s.repo.Find(id)
	if err != nil {
		return "", err
	}
	// Prefer the platform-local logs directory (inside the platform home).
	platformHome := platformHomeFromWebRoot(site.RootPath)
	platformLogDir := filepath.Join(platformHome, "logs")
	if st, err := os.Stat(platformLogDir); err == nil && st.IsDir() {
		return platformLogDir, nil
	}
	// Fallback to the directory containing the configured access log path.
	dir := filepath.Dir(site.AccessLogPath)
	if dir == "." || dir == "" {
		dir = filepath.Join(s.cfg.Paths.LogRoot, "sites", site.Name)
	}
	return dir, nil
}

func (s *WebsiteService) ListLogFiles(id uint) ([]LogFileInfo, error) {
	site, err := s.repo.Find(id)
	if err != nil {
		return nil, err
	}
	platformHome := platformHomeFromWebRoot(site.RootPath)
	platformLogDir := filepath.Join(platformHome, "logs")
	_ = os.MkdirAll(platformLogDir, 0o755)
	seen := map[string]struct{}{}
	files := make([]LogFileInfo, 0, 14)
	addNamed := func(name string, logType string, path string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		files = append(files, LogFileInfo{Name: name, Type: logType, Path: path})
	}
	addFile := func(path string, logType string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		name := filepath.Base(path)
		if name == "." || name == "" {
			return
		}
		addNamed(name, logType, path)
	}
	addFile(site.AccessLogPath, "access")
	addFile(site.ErrorLogPath, "error")
	if entries, err := os.ReadDir(platformLogDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			logType := "other"
			if strings.Contains(name, "access") {
				logType = "access"
			} else if strings.Contains(name, "error") {
				logType = "error"
			}
			addFile(filepath.Join(platformLogDir, name), logType)
		}
	}
	if app := s.logRuntimeApp(site); app != nil {
		addNamed("runtime-stdout.log", "runtime", app.StdoutLogPath)
		addNamed("runtime-stderr.log", "runtime", app.StderrLogPath)
		if strings.TrimSpace(app.ServiceName) != "" {
			addNamed("runtime-journal.log", "journal", app.ServiceName)
			addNamed("runtime-service-status.txt", "status", app.ServiceName)
			addNamed("runtime-systemd-unit.conf", "unit", app.ServiceName)
			addNamed("deploycp-panel-filtered.log", "panel", app.ServiceName)
		}
		if strings.EqualFold(strings.TrimSpace(app.Runtime), "python") {
			addNamed("python-venv-info.txt", "python", pythonRuntimeVenvPathForApp(app))
		}
		if strings.EqualFold(strings.TrimSpace(app.Runtime), "node") {
			addNamed("node-runtime-info.txt", "node", nodeRuntimeToolsPathForApp(app))
		}
	}
	if s.nginxRepo != nil {
		if nginxCfg, err := s.nginxRepo.FindByWebsite(site.ID); err == nil && nginxCfg != nil && strings.TrimSpace(nginxCfg.ConfigPath) != "" {
			addNamed("nginx-vhost.conf", "config", nginxCfg.ConfigPath)
		}
	}
	addNamed("platform-debug.txt", "debug", "")
	sort.Slice(files, func(i, j int) bool {
		pi := logTypePriority(files[i].Type)
		pj := logTypePriority(files[j].Type)
		if pi != pj {
			return pi < pj
		}
		return files[i].Name < files[j].Name
	})
	return files, nil
}

func (s *WebsiteService) ReadLogFile(id uint, filename string, lines int) (string, error) {
	site, err := s.repo.Find(id)
	if err != nil {
		return "", err
	}
	safe := filepath.Base(filename)
	if content, ok, err := s.readPlatformVirtualLog(site, safe, lines); ok || err != nil {
		return content, err
	}
	var fp string
	switch safe {
	case filepath.Base(site.AccessLogPath):
		fp = site.AccessLogPath
	case filepath.Base(site.ErrorLogPath):
		fp = site.ErrorLogPath
	default:
		dir, dirErr := s.LogDir(id)
		if dirErr != nil {
			return "", dirErr
		}
		fp = filepath.Join(dir, safe)
	}
	abs, _ := filepath.Abs(fp)
	platformHome := platformHomeFromWebRoot(site.RootPath)
	allowedRoots := []string{
		filepath.Join(platformHome, "logs"),
		filepath.Dir(strings.TrimSpace(site.AccessLogPath)),
		filepath.Dir(strings.TrimSpace(site.ErrorLogPath)),
	}
	allowed := false
	for _, root := range allowedRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, _ := filepath.Abs(root)
		if abs == absRoot || strings.HasPrefix(abs, absRoot+string(os.PathSeparator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("invalid filename")
	}
	if lines <= 0 {
		lines = 100
	}
	content, err := tailFile(fp, lines)
	if err != nil {
		return "", err
	}
	return content, nil
}

func logTypePriority(logType string) int {
	switch strings.ToLower(strings.TrimSpace(logType)) {
	case "access":
		return 10
	case "error":
		return 20
	case "runtime":
		return 30
	case "journal", "status", "unit":
		return 40
	case "debug", "config":
		return 50
	case "python":
		return 60
	case "panel":
		return 70
	default:
		return 90
	}
}

func (s *WebsiteService) logRuntimeApp(site *models.Website) *models.GoApp {
	if site == nil {
		return nil
	}
	if s.appRepo != nil {
		if app, err := s.appRepo.FindByWebsiteID(site.ID); err == nil && app != nil {
			return app
		}
	}
	return runtimeFromWebsite(site)
}

func (s *WebsiteService) readPlatformVirtualLog(site *models.Website, name string, lines int) (string, bool, error) {
	app := s.logRuntimeApp(site)
	switch name {
	case "runtime-stdout.log":
		if app == nil {
			return "", true, fmt.Errorf("runtime is not configured")
		}
		content, err := tailOptionalLogFile(app.StdoutLogPath, normalizedLogLines(lines))
		return content, true, err
	case "runtime-stderr.log":
		if app == nil {
			return "", true, fmt.Errorf("runtime is not configured")
		}
		content, err := tailOptionalLogFile(app.StderrLogPath, normalizedLogLines(lines))
		return content, true, err
	case "runtime-journal.log":
		if app == nil || strings.TrimSpace(app.ServiceName) == "" || s.adapter == nil {
			return "", true, fmt.Errorf("runtime service is not configured")
		}
		content, err := s.adapter.Services().Logs(context.Background(), app.ServiceName, normalizedLogLines(lines))
		return content, true, err
	case "runtime-service-status.txt":
		if app == nil || strings.TrimSpace(app.ServiceName) == "" || s.adapter == nil {
			return "", true, fmt.Errorf("runtime service is not configured")
		}
		status, err := s.adapter.Services().Status(context.Background(), app.ServiceName)
		if err != nil {
			return "", true, err
		}
		return formatRuntimeServiceStatus(app, status), true, nil
	case "runtime-systemd-unit.conf":
		if app == nil || strings.TrimSpace(app.ServiceName) == "" {
			return "", true, fmt.Errorf("runtime service is not configured")
		}
		content, err := s.readRuntimeUnitFile(app.ServiceName)
		return content, true, err
	case "deploycp-panel-filtered.log":
		content, err := s.filteredPanelLogs(site, app, normalizedLogLines(lines))
		return content, true, err
	case "nginx-vhost.conf":
		content, err := s.readWebsiteVhostConfig(site)
		return content, true, err
	case "platform-debug.txt":
		return s.platformDebugSnapshot(site, app), true, nil
	case "python-venv-info.txt":
		if app == nil || !strings.EqualFold(strings.TrimSpace(app.Runtime), "python") {
			return "", true, fmt.Errorf("python runtime is not configured")
		}
		return s.pythonVenvDebugInfo(app), true, nil
	case "node-runtime-info.txt":
		if app == nil || !strings.EqualFold(strings.TrimSpace(app.Runtime), "node") {
			return "", true, fmt.Errorf("node runtime is not configured")
		}
		return s.nodeRuntimeDebugInfo(app), true, nil
	default:
		return "", false, nil
	}
}

func normalizedLogLines(lines int) int {
	if lines <= 0 {
		return 100
	}
	if lines > 5000 {
		return 5000
	}
	return lines
}

func tailOptionalLogFile(path string, lines int) (string, error) {
	content, err := tailFile(path, lines)
	if err == nil {
		return content, nil
	}
	if os.IsNotExist(err) {
		return "Log file has not been created yet.\n", nil
	}
	return "", err
}

func formatRuntimeServiceStatus(app *models.GoApp, status platform.ServiceStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "service=%s\n", strings.TrimSpace(app.ServiceName))
	fmt.Fprintf(&b, "runtime=%s\n", strings.TrimSpace(app.Runtime))
	fmt.Fprintf(&b, "process_manager=%s\n", normalizeProcessManager(app.ProcessManager))
	fmt.Fprintf(&b, "active=%t\n", status.Active)
	fmt.Fprintf(&b, "enabled=%t\n", status.Enabled)
	fmt.Fprintf(&b, "sub_state=%s\n", strings.TrimSpace(status.SubState))
	fmt.Fprintf(&b, "bind=%s:%d\n", strings.TrimSpace(app.Host), app.Port)
	fmt.Fprintf(&b, "working_directory=%s\n", strings.TrimSpace(app.WorkingDirectory))
	fmt.Fprintf(&b, "binary=%s\n", strings.TrimSpace(app.BinaryPath))
	fmt.Fprintf(&b, "entry_point=%s\n", strings.TrimSpace(app.EntryPoint))
	if raw := strings.TrimSpace(status.RawOutput); raw != "" {
		fmt.Fprintf(&b, "\nraw_status=%s\n", raw)
	}
	return b.String()
}

func (s *WebsiteService) readRuntimeUnitFile(serviceName string) (string, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return "", fmt.Errorf("service name is required")
	}
	if s.services != nil {
		if managed, err := s.services.FindByName(serviceName); err == nil && managed != nil && strings.TrimSpace(managed.UnitPath) != "" {
			content, readErr := os.ReadFile(managed.UnitPath)
			if readErr == nil {
				return string(content), nil
			}
		}
	}
	unitPath := filepath.Join("/etc/systemd/system", serviceName+".service")
	content, err := os.ReadFile(unitPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *WebsiteService) readWebsiteVhostConfig(site *models.Website) (string, error) {
	if site == nil || s.nginxRepo == nil {
		return "", fmt.Errorf("nginx config is not available")
	}
	nginxCfg, err := s.nginxRepo.FindByWebsite(site.ID)
	if err != nil {
		return "", err
	}
	if nginxCfg == nil || strings.TrimSpace(nginxCfg.ConfigPath) == "" {
		return "", fmt.Errorf("nginx config is not available")
	}
	content, err := os.ReadFile(nginxCfg.ConfigPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *WebsiteService) filteredPanelLogs(site *models.Website, app *models.GoApp, lines int) (string, error) {
	if s.adapter == nil {
		return "", fmt.Errorf("platform adapter is not available")
	}
	content, err := s.adapter.Services().Logs(context.Background(), "deploycp", lines)
	if err != nil {
		return content, err
	}
	var tokens []string
	if site != nil {
		tokens = append(tokens, strings.TrimSpace(site.Name))
		for _, domain := range domainsFromModel(site.Domains) {
			tokens = append(tokens, strings.TrimSpace(domain))
		}
	}
	if app != nil {
		tokens = append(tokens, strings.TrimSpace(app.ServiceName), strings.TrimSpace(app.Name))
	}
	var out []string
	for _, line := range strings.Split(content, "\n") {
		for _, token := range tokens {
			if token != "" && strings.Contains(line, token) {
				out = append(out, line)
				break
			}
		}
	}
	if len(out) == 0 {
		return "No recent DeployCP panel log lines matched this platform.\n", nil
	}
	return strings.Join(out, "\n") + "\n", nil
}

func (s *WebsiteService) platformDebugSnapshot(site *models.Website, app *models.GoApp) string {
	var b strings.Builder
	fmt.Fprintf(&b, "platform_id=%d\n", site.ID)
	fmt.Fprintf(&b, "name=%s\n", strings.TrimSpace(site.Name))
	fmt.Fprintf(&b, "type=%s\n", strings.TrimSpace(site.Type))
	fmt.Fprintf(&b, "enabled=%t\n", site.Enabled)
	fmt.Fprintf(&b, "domains=%s\n", strings.Join(domainsFromModel(site.Domains), ", "))
	fmt.Fprintf(&b, "platform_home=%s\n", platformHomeFromWebRoot(site.RootPath))
	fmt.Fprintf(&b, "web_root=%s\n", strings.TrimSpace(site.RootPath))
	fmt.Fprintf(&b, "access_log=%s\n", strings.TrimSpace(site.AccessLogPath))
	fmt.Fprintf(&b, "error_log=%s\n", strings.TrimSpace(site.ErrorLogPath))
	fmt.Fprintf(&b, "proxy_target=%s\n", strings.TrimSpace(site.ProxyTarget))
	if app != nil {
		fmt.Fprintf(&b, "\n[runtime]\n")
		fmt.Fprintf(&b, "service=%s\n", strings.TrimSpace(app.ServiceName))
		fmt.Fprintf(&b, "runtime=%s\n", strings.TrimSpace(app.Runtime))
		fmt.Fprintf(&b, "process_manager=%s\n", normalizeProcessManager(app.ProcessManager))
		fmt.Fprintf(&b, "binary=%s\n", strings.TrimSpace(app.BinaryPath))
		fmt.Fprintf(&b, "entry_point=%s\n", strings.TrimSpace(app.EntryPoint))
		fmt.Fprintf(&b, "working_directory=%s\n", strings.TrimSpace(app.WorkingDirectory))
		fmt.Fprintf(&b, "bind=%s:%d\n", strings.TrimSpace(app.Host), app.Port)
		fmt.Fprintf(&b, "stdout_log=%s\n", strings.TrimSpace(app.StdoutLogPath))
		fmt.Fprintf(&b, "stderr_log=%s\n", strings.TrimSpace(app.StderrLogPath))
		fmt.Fprintf(&b, "runtime_version=%s\n", appEnvValue(app.EnvVars, "RUNTIME_VERSION"))
	}
	return b.String()
}

func (s *WebsiteService) pythonVenvDebugInfo(app *models.GoApp) string {
	venvPath := pythonRuntimeVenvPathForApp(app)
	var b strings.Builder
	fmt.Fprintf(&b, "venv=%s\n", venvPath)
	fmt.Fprintf(&b, "runtime_version=%s\n", appEnvValue(app.EnvVars, "RUNTIME_VERSION"))
	if marker, err := os.ReadFile(pythonVenvVersionMarkerPath(venvPath)); err == nil {
		fmt.Fprintf(&b, "venv_marker=%s\n", strings.TrimSpace(string(marker)))
	}
	b.WriteString("\n[python]\n")
	b.WriteString(runDebugCommand(filepath.Join(venvPath, "bin", "python"), "--version"))
	b.WriteString("\n[pip freeze]\n")
	b.WriteString(runDebugCommand(filepath.Join(venvPath, "bin", "pip"), "freeze"))
	requirementsPath := filepath.Join(appServiceWorkingDir(app), "requirements.txt")
	if content, err := os.ReadFile(requirementsPath); err == nil {
		b.WriteString("\n[requirements.txt]\n")
		b.Write(content)
		if !strings.HasSuffix(string(content), "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (s *WebsiteService) nodeRuntimeDebugInfo(app *models.GoApp) string {
	toolsPath := nodeRuntimeToolsPathForApp(app)
	workingDir := appServiceWorkingDir(app)
	var b strings.Builder
	fmt.Fprintf(&b, "tools=%s\n", toolsPath)
	fmt.Fprintf(&b, "working_directory=%s\n", workingDir)
	fmt.Fprintf(&b, "runtime_version=%s\n", appEnvValue(app.EnvVars, "RUNTIME_VERSION"))
	if marker, err := os.ReadFile(nodeToolsVersionMarkerPath(toolsPath)); err == nil {
		fmt.Fprintf(&b, "tools_marker=%s\n", strings.TrimSpace(string(marker)))
	}
	nodeBin := strings.TrimSpace(app.BinaryPath)
	if s.runtime != nil {
		if resolved, err := s.runtime.ResolveBinary("node", appEnvValue(app.EnvVars, "RUNTIME_VERSION"), "node"); err == nil && strings.TrimSpace(resolved) != "" {
			nodeBin = resolved
		}
	}
	b.WriteString("\n[node]\n")
	b.WriteString(runDebugCommand(nodeBin, "--version"))
	if normalizeProcessManager(app.ProcessManager) == "pm2" {
		b.WriteString("\n[pm2]\n")
		b.WriteString(runDebugCommand(filepath.Join(toolsPath, "node_modules", ".bin", "pm2-runtime"), "--version"))
	}
	packagePath := filepath.Join(workingDir, "package.json")
	if content, err := os.ReadFile(packagePath); err == nil {
		b.WriteString("\n[package.json]\n")
		b.Write(content)
		if !strings.HasSuffix(string(content), "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func runDebugCommand(binary string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("%s timed out\n", strings.Join(append([]string{binary}, args...), " "))
	}
	if err != nil {
		return fmt.Sprintf("%s failed: %v\n%s\n", strings.Join(append([]string{binary}, args...), " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}

func firstWebsiteCert(site *models.Website, items []models.SSLCertificate) *models.SSLCertificate {
	if site == nil {
		return nil
	}
	domainSet := make(map[string]struct{}, len(site.Domains))
	for _, item := range site.Domains {
		domainSet[strings.ToLower(strings.TrimSpace(item.Domain))] = struct{}{}
	}
	for i := range items {
		if _, ok := domainSet[strings.ToLower(strings.TrimSpace(items[i].Domain))]; !ok {
			continue
		}
		if strings.TrimSpace(items[i].CertPath) == "" || strings.TrimSpace(items[i].KeyPath) == "" {
			continue
		}
		return &items[i]
	}
	return nil
}

func tailFile(path string, lines int) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errorsIsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	rows := strings.Split(string(content), "\n")
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return strings.Join(rows, "\n"), nil
}

func errorsIsNotFound(err error) bool {
	return err != nil && (os.IsNotExist(err) || errorsIsGormNotFound(err))
}

func errorsIsGormNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}

// ensurePathTraversable sets the world-execute bit (o+x) on every parent
// directory of target so that nginx (www-data) and site users can traverse
// the path. Only adds execute — does not grant read or write.
// platformHomeFromWebRoot returns the platform home directory from a web root path.
// If the root ends with /htdocs, the platform home is the parent directory.
func platformHomeFromWebRoot(webRoot string) string {
	return platformHomeFromPath(webRoot)
}

func websiteSharedGroup(websiteID uint) string {
	return fmt.Sprintf("deploycp-w%d", websiteID)
}

func ensurePathTraversable(target string) {
	target = filepath.Clean(target)
	var dirs []string
	for d := filepath.Dir(target); d != "/" && d != "." && d != target; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		target = d
	}
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil {
			continue
		}
		perm := info.Mode().Perm()
		if perm&0o001 == 0 {
			_ = os.Chmod(d, perm|0o001)
		}
	}
}
