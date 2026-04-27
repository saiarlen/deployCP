package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
)

var (
	serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,179}$`)
	serviceTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,39}$`)
	phpVersionPattern  = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
	serviceTagPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

type ServiceEntry struct {
	Record        models.ManagedService
	Status        platform.ServiceStatus
	State         string
	CategoryLabel string
	Installed     bool
}

type ServiceInput struct {
	Name         string
	Type         string
	PlatformName string
	UnitPath     string
	Tags         string
	Description  string
	Enabled      bool
	Upsert       bool
}

type ServiceCatalogItem struct {
	Key         string
	Title       string
	Description string
	Name        string
	Type        string
	Priority    int
}

type ServiceCatalogGroup struct {
	Key   string
	Title string
	Items []ServiceCatalogItem
}

type ServiceService struct {
	cfg      *config.Config
	repo     *repositories.ManagedServiceRepository
	settings *repositories.SettingRepository
	adapter  platform.Adapter
	audit    *AuditService
	packages *SystemPackageService
}

func NewServiceService(cfg *config.Config, repo *repositories.ManagedServiceRepository, settings *repositories.SettingRepository, adapter platform.Adapter, audit *AuditService, packages *SystemPackageService) *ServiceService {
	return &ServiceService{cfg: cfg, repo: repo, settings: settings, adapter: adapter, audit: audit, packages: packages}
}

func (s *ServiceService) List(ctx context.Context) ([]ServiceEntry, error) {
	items, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	out := make([]ServiceEntry, 0, len(items))
	for _, item := range items {
		resolvedName := s.resolvedServiceName(ctx, item.Name)
		status, _ := s.adapter.Services().Status(ctx, resolvedName)
		installed := s.installedState(ctx, item.Name)
		state := serviceEntryState(status, installed)
		out = append(out, ServiceEntry{
			Record:        item,
			Status:        status,
			State:         state,
			CategoryLabel: s.categoryTitle(item.Type),
			Installed:     installed,
		})
	}
	return out, nil
}

func (s *ServiceService) ListSystem(ctx context.Context) ([]ServiceEntry, error) {
	tracked, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	trackedByName := make(map[string]models.ManagedService, len(tracked))
	for _, item := range tracked {
		trackedByName[strings.ToLower(strings.TrimSpace(item.Name))] = item
	}

	core := s.coreSystemServices()
	seen := make(map[string]struct{}, len(core))
	out := make([]ServiceEntry, 0, len(core)+len(tracked))

	for _, def := range core {
		key := strings.ToLower(strings.TrimSpace(def.Name))
		seen[key] = struct{}{}
		record := def
		if trackedItem, ok := trackedByName[key]; ok {
			record = trackedItem
		}
		resolvedName := s.resolvedServiceName(ctx, record.Name)
		status, _ := s.adapter.Services().Status(ctx, resolvedName)
		installed := s.installedState(ctx, record.Name)
		state := serviceEntryState(status, installed)
		out = append(out, ServiceEntry{
			Record:        record,
			Status:        status,
			State:         state,
			CategoryLabel: s.categoryTitle(record.Type),
			Installed:     installed,
		})
	}

	for _, item := range tracked {
		key := strings.ToLower(strings.TrimSpace(item.Name))
		if _, ok := seen[key]; ok {
			continue
		}
		if !isSystemManagedService(item) {
			continue
		}
		resolvedName := s.resolvedServiceName(ctx, item.Name)
		status, _ := s.adapter.Services().Status(ctx, resolvedName)
		installed := s.installedState(ctx, item.Name)
		state := serviceEntryState(status, installed)
		out = append(out, ServiceEntry{
			Record:        item,
			Status:        status,
			State:         state,
			CategoryLabel: s.categoryTitle(item.Type),
			Installed:     installed,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Record.Name) < strings.ToLower(out[j].Record.Name)
	})
	return out, nil
}

func (s *ServiceService) Catalog() []ServiceCatalogItem {
	items := []ServiceCatalogItem{
		{Key: "nginx", Title: "NGINX", Description: "Reverse proxy and web server", Name: "nginx", Type: "web", Priority: 10},
		{Key: "mariadb", Title: "MariaDB", Description: "MySQL-compatible relational database", Name: "mariadb", Type: "database", Priority: 20},
		{Key: "postgresql", Title: "PostgreSQL", Description: "Advanced SQL database service", Name: "postgresql", Type: "database", Priority: 21},
		{Key: "redis", Title: "Redis", Description: "In-memory data structure store", Name: "redis-server", Type: "cache", Priority: 30},
		{Key: "varnish", Title: "Varnish Cache", Description: "HTTP accelerator and caching layer", Name: "varnish", Type: "cache", Priority: 31},
		{Key: "rabbitmq", Title: "RabbitMQ", Description: "Message broker for queues and events", Name: "rabbitmq-server", Type: "queue", Priority: 40},
		{Key: "docker", Title: "Docker", Description: "Container runtime daemon", Name: "docker", Type: "system", Priority: 50},
	}

	for _, version := range s.phpVersions() {
		keyVer := strings.ReplaceAll(version, ".", "-")
		items = append(items, ServiceCatalogItem{
			Key:         "php-fpm-" + keyVer,
			Title:       "PHP-FPM " + version,
			Description: "PHP FastCGI process manager pool",
			Name:        "php" + version + "-fpm",
			Type:        "php-fpm",
			Priority:    60,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].Title < items[j].Title
	})

	return items
}

func (s *ServiceService) CatalogGroups() []ServiceCatalogGroup {
	groupMap := map[string][]ServiceCatalogItem{}
	for _, item := range s.Catalog() {
		key := strings.ToLower(strings.TrimSpace(item.Type))
		groupMap[key] = append(groupMap[key], item)
	}
	order := []string{"web", "database", "cache", "php-fpm", "queue", "system", "application", "custom"}
	groups := make([]ServiceCatalogGroup, 0, len(order))
	for _, key := range order {
		items := groupMap[key]
		if len(items) == 0 {
			continue
		}
		groups = append(groups, ServiceCatalogGroup{
			Key:   key,
			Title: s.categoryTitle(key),
			Items: items,
		})
	}
	return groups
}

func (s *ServiceService) Types() []string {
	return []string{"web", "database", "cache", "php-fpm", "queue", "system", "application", "custom"}
}

func (s *ServiceService) PlatformName() string {
	return s.adapter.Name()
}

func (s *ServiceService) Create(input ServiceInput, actor *uint, ip string) (bool, error) {
	sanitized, err := s.sanitizeInput(input)
	if err != nil {
		return false, err
	}
	if err := s.validateTrackableService(context.Background(), sanitized); err != nil {
		return false, err
	}
	if existing, err := s.repo.FindByName(sanitized.Name); err == nil {
		if !input.Upsert {
			return false, fmt.Errorf("service identifier already exists")
		}
		existing.Type = sanitized.Type
		existing.PlatformName = sanitized.PlatformName
		existing.UnitPath = sanitized.UnitPath
		existing.Enabled = sanitized.Enabled
		if saveErr := s.repo.Update(existing); saveErr != nil {
			return false, saveErr
		}
		s.audit.Record(actor, "service.ensure", "service", existing.Name, ip, map[string]any{
			"type":          existing.Type,
			"platform_name": existing.PlatformName,
			"enabled":       existing.Enabled,
		})
		return false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}

	item := models.ManagedService{
		Name:         sanitized.Name,
		Type:         sanitized.Type,
		PlatformName: sanitized.PlatformName,
		UnitPath:     sanitized.UnitPath,
		Tags:         sanitized.Tags,
		Description:  sanitized.Description,
		Enabled:      sanitized.Enabled,
	}
	if err := s.repo.Create(&item); err != nil {
		return false, err
	}
	s.audit.Record(actor, "service.create", "service", item.Name, ip, map[string]any{
		"type":          item.Type,
		"platform_name": item.PlatformName,
		"tags":          item.Tags,
		"enabled":       item.Enabled,
	})
	return true, nil
}

func (s *ServiceService) Update(id uint, input ServiceInput, actor *uint, ip string) error {
	existing, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	sanitized, err := s.sanitizeInput(input)
	if err != nil {
		return err
	}
	if err := s.validateTrackableService(context.Background(), sanitized); err != nil {
		return err
	}

	if existing.Name != sanitized.Name {
		if item, findErr := s.repo.FindByName(sanitized.Name); findErr == nil && item.ID != existing.ID {
			return fmt.Errorf("service identifier already exists")
		} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
	}

	existing.Name = sanitized.Name
	existing.Type = sanitized.Type
	existing.PlatformName = sanitized.PlatformName
	existing.UnitPath = sanitized.UnitPath
	existing.Tags = sanitized.Tags
	existing.Description = sanitized.Description
	existing.Enabled = sanitized.Enabled

	if err := s.repo.Update(existing); err != nil {
		return err
	}
	s.audit.Record(actor, "service.update", "service", existing.Name, ip, map[string]any{
		"type":          existing.Type,
		"platform_name": existing.PlatformName,
		"tags":          existing.Tags,
		"enabled":       existing.Enabled,
	})
	return nil
}

func (s *ServiceService) Delete(id uint, actor *uint, ip string) error {
	existing, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "service.delete", "service", existing.Name, ip, nil)
	return nil
}

func (s *ServiceService) ActionByRef(ctx context.Context, ref, action string, actor *uint, ip string) error {
	item, err := s.findByRef(ref)
	if err != nil {
		return err
	}
	return s.Action(ctx, item, action, actor, ip)
}

func (s *ServiceService) Action(ctx context.Context, item *models.ManagedService, action string, actor *uint, ip string) error {
	if !s.cfg.Features.EnableServiceManage {
		return fmt.Errorf("service actions are disabled by configuration")
	}
	action = strings.ToLower(strings.TrimSpace(action))
	validActions := map[string]struct{}{
		"start":   {},
		"stop":    {},
		"restart": {},
		"reload":  {},
		"enable":  {},
		"disable": {},
	}
	if _, ok := validActions[action]; !ok {
		return fmt.Errorf("invalid action")
	}
	svc := s.adapter.Services()
	var err error
	targetName := s.resolvedServiceName(ctx, item.Name)
	if targetName == "" {
		targetName = item.Name
	}

	switch action {
	case "start":
		if err = s.ensureInstalledIfRequested(ctx, item.Name, action, actor, ip); err != nil {
			return err
		}
		err = svc.Start(ctx, targetName)
	case "stop":
		err = svc.Stop(ctx, targetName)
	case "restart":
		if err = s.ensureInstalledIfRequested(ctx, item.Name, action, actor, ip); err != nil {
			return err
		}
		err = svc.Restart(ctx, targetName)
	case "reload":
		// Not every service manager has a direct reload API; restart is the safest fallback.
		if err = s.ensureInstalledIfRequested(ctx, item.Name, action, actor, ip); err != nil {
			return err
		}
		err = svc.Restart(ctx, targetName)
	case "enable":
		if err = s.ensureInstalledIfRequested(ctx, item.Name, action, actor, ip); err != nil {
			return err
		}
		err = svc.Enable(ctx, targetName)
	case "disable":
		err = svc.Disable(ctx, targetName)
	}
	if err != nil {
		return err
	}

	if action == "enable" || action == "disable" {
		item.Enabled = action == "enable"
		_ = s.repo.Update(item)
	}

	s.audit.Record(actor, "service.action."+action, "service", item.Name, ip, map[string]any{"service_id": item.ID, "resolved_name": targetName})
	return nil
}

func (s *ServiceService) categoryTitle(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "web":
		return "Web and Proxy"
	case "database":
		return "Databases"
	case "cache":
		return "Caching"
	case "php-fpm":
		return "PHP-FPM"
	case "queue":
		return "Queues"
	case "system":
		return "System Services"
	case "application":
		return "Application Services"
	default:
		return "Custom"
	}
}

func (s *ServiceService) LogsByRef(ctx context.Context, ref string, lines int) (string, string, error) {
	item, err := s.findByRef(ref)
	if err != nil {
		return "", "", err
	}
	targetName := s.resolvedServiceName(ctx, item.Name)
	logs, err := s.adapter.Services().Logs(ctx, targetName, lines)
	return item.Name, logs, err
}

func (s *ServiceService) sanitizeInput(input ServiceInput) (ServiceInput, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ServiceInput{}, fmt.Errorf("service identifier is required")
	}
	if !serviceNamePattern.MatchString(name) {
		return ServiceInput{}, fmt.Errorf("service identifier allows only letters, numbers, and . _ : @ -")
	}

	typ := strings.ToLower(strings.TrimSpace(input.Type))
	if typ == "" {
		typ = "system"
	}
	if !serviceTypePattern.MatchString(typ) {
		return ServiceInput{}, fmt.Errorf("invalid service category")
	}

	platformName := strings.TrimSpace(input.PlatformName)
	if platformName == "" {
		platformName = s.adapter.Name()
	}
	description := strings.TrimSpace(input.Description)
	if len(description) > 500 {
		return ServiceInput{}, fmt.Errorf("description must be 500 characters or less")
	}
	tags, err := normalizeServiceTags(input.Tags)
	if err != nil {
		return ServiceInput{}, err
	}

	return ServiceInput{
		Name:         name,
		Type:         typ,
		PlatformName: platformName,
		UnitPath:     strings.TrimSpace(input.UnitPath),
		Tags:         tags,
		Description:  description,
		Enabled:      input.Enabled,
	}, nil
}

func (s *ServiceService) findByRef(ref string) (*models.ManagedService, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("service reference is required")
	}
	if id, err := strconv.ParseUint(ref, 10, 64); err == nil {
		return s.repo.Find(uint(id))
	}
	item, err := s.repo.FindByName(ref)
	if err == nil {
		return item, nil
	}
	for _, candidate := range s.coreSystemServices() {
		if strings.EqualFold(strings.TrimSpace(candidate.Name), ref) {
			copy := candidate
			return &copy, nil
		}
	}
	return nil, err
}

func (s *ServiceService) phpVersions() []string {
	seen := map[string]struct{}{}
	found := make([]string, 0, 6)
	if entries, err := os.ReadDir("/etc/php"); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			v := strings.TrimSpace(entry.Name())
			if v == "" || !phpVersionPattern.MatchString(v) {
				continue
			}
			if _, err := os.Stat(filepath.Join("/etc/php", v, "fpm")); err != nil {
				continue
			}
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			found = append(found, v)
		}
	}
	for _, v := range s.rpmPHPFPMVersions() {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		found = append(found, v)
	}
	if len(found) > 0 {
		sort.SliceStable(found, func(i, j int) bool { return found[i] > found[j] })
		return found
	}
	return []string{}
}

func (s *ServiceService) rpmPHPFPMVersions() []string {
	if _, err := exec.LookPath("rpm"); err != nil {
		return []string{}
	}
	out, err := exec.Command("rpm", "-qa").Output()
	if err != nil {
		return []string{}
	}
	seen := map[string]struct{}{}
	versions := make([]string, 0, 4)
	for _, line := range strings.Split(string(out), "\n") {
		pkg := strings.TrimSpace(line)
		if pkg == "" {
			continue
		}
		switch {
		case strings.Contains(pkg, "php-fpm"):
		default:
			continue
		}
		if strings.HasPrefix(pkg, "php-fpm-") || pkg == "php-fpm" {
			if version := s.detectDefaultPHPVersion(); version != "" {
				if _, ok := seen[version]; !ok {
					seen[version] = struct{}{}
					versions = append(versions, version)
				}
			}
			continue
		}
		matches := regexp.MustCompile(`^php([0-9]{2})-php-fpm`).FindStringSubmatch(pkg)
		if len(matches) == 2 {
			version := matches[1][:1] + "." + matches[1][1:]
			if _, ok := seen[version]; ok {
				continue
			}
			seen[version] = struct{}{}
			versions = append(versions, version)
		}
	}
	return versions
}

func (s *ServiceService) detectDefaultPHPVersion() string {
	phpBin, err := exec.LookPath("php")
	if err != nil {
		return ""
	}
	out, err := exec.Command(phpBin, "-v").CombinedOutput()
	if err != nil {
		return ""
	}
	matches := regexp.MustCompile(`PHP\s+([0-9]+\.[0-9]+)`).FindStringSubmatch(string(out))
	if len(matches) != 2 {
		return ""
	}
	return matches[1]
}

func (s *ServiceService) coreSystemServices() []models.ManagedService {
	platformName := s.adapter.Name()
	services := []models.ManagedService{
		{Name: "nginx", Type: "web", PlatformName: platformName, Enabled: true, Tags: "web,proxy", Description: "NGINX reverse proxy and web server."},
		{Name: "mariadb", Type: "database", PlatformName: platformName, Enabled: true, Tags: "db,mysql", Description: "MariaDB SQL database server."},
		{Name: "postgresql", Type: "database", PlatformName: platformName, Enabled: true, Tags: "db,postgres", Description: "PostgreSQL SQL database server."},
		{Name: "redis-server", Type: "cache", PlatformName: platformName, Enabled: true, Tags: "cache,redis", Description: "Redis in-memory cache and data store."},
		{Name: "varnish", Type: "cache", PlatformName: platformName, Enabled: false, Tags: "cache,varnish", Description: "Varnish HTTP accelerator cache."},
	}
	for _, version := range s.phpVersions() {
		name := "php" + version + "-fpm"
		services = append(services, models.ManagedService{
			Name:         name,
			Type:         "php-fpm",
			PlatformName: platformName,
			Enabled:      true,
			Tags:         "php,fpm",
			Description:  "PHP-FPM runtime pool " + version + ".",
		})
	}
	return services
}

func normalizeServiceTags(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		tag := strings.ToLower(strings.TrimSpace(part))
		if tag == "" {
			continue
		}
		if !serviceTagPattern.MatchString(tag) {
			return "", fmt.Errorf("invalid tag %q: use letters, numbers, dash, underscore", tag)
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return "", nil
	}
	sort.Strings(out)
	return strings.Join(out, ","), nil
}

func isSystemManagedService(item models.ManagedService) bool {
	name := strings.ToLower(strings.TrimSpace(item.Name))
	if strings.HasPrefix(name, "deploycp-app-") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(item.Type)) {
	case "go-app", "app", "runtime", "platform":
		return false
	default:
		return true
	}
}

func serviceEntryState(status platform.ServiceStatus, installed bool) string {
	if !installed {
		return "not-installed"
	}
	if status.Active {
		return "running"
	}
	if sub := strings.ToLower(strings.TrimSpace(status.SubState)); sub != "" {
		return sub
	}
	return "stopped"
}

func (s *ServiceService) installedState(ctx context.Context, serviceName string) bool {
	if s.packages == nil {
		return true
	}
	if !s.packages.KnownService(serviceName) {
		return s.packages.UnitExists(ctx, serviceName, "")
	}
	return s.packages.IsInstalled(ctx, serviceName)
}

func (s *ServiceService) resolvedServiceName(ctx context.Context, serviceName string) string {
	if s.packages == nil {
		return serviceName
	}
	return s.packages.ResolveServiceUnit(ctx, serviceName)
}

func (s *ServiceService) ensureInstalledIfRequested(ctx context.Context, serviceName, action string, actor *uint, ip string) error {
	switch action {
	case "start", "restart", "reload", "enable":
	default:
		return nil
	}
	if s.packages == nil || !s.packages.KnownService(serviceName) {
		return nil
	}
	return s.packages.EnsureInstalled(ctx, serviceName, actor, ip)
}

func (s *ServiceService) validateTrackableService(ctx context.Context, input ServiceInput) error {
	if s.packages == nil || s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	if s.packages.KnownService(input.Name) {
		return nil
	}
	if !s.packages.UnitExists(ctx, input.Name, input.UnitPath) {
		return fmt.Errorf("service unit %s was not found on this host", strings.TrimSpace(input.Name))
	}
	return nil
}
