package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/ssh"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
	"deploycp/internal/utils"
)

const (
	defaultDiskAlertPct       = 85
	defaultSSLExpiryAlertDays = 14
	maxDeployOutputBytes      = 24000
)

type PlatformOpsService struct {
	cfg      *config.Config
	repos    *repositories.Repositories
	adapter  platform.Adapter
	websites *WebsiteService
	apps     *AppService
	audit    *AuditService
}

type PlatformOpsView struct {
	HealthLatest   *models.PlatformHealthCheck
	HealthHistory  []models.PlatformHealthCheck
	Backups        []models.PlatformBackup
	DeployConfig   *models.PlatformDeployConfig
	OpenAlerts     []models.AlertEvent
	RecentAlerts   []models.AlertEvent
	RuntimeCommand string
	DeployCommand  string
}

type PlatformDeployInput struct {
	RepoURL            string
	Branch             string
	WorkDir            string
	DeployCommand      string
	DeployKeyPrivate   string
	GenerateDeployKey  bool
	RestartAfterDeploy bool
}

type PlatformDeployResult struct {
	PlatformID uint
	Status     string
	Output     string
	StartedAt  time.Time
	FinishedAt time.Time
}

type platformBackupManifest struct {
	Version    int                      `json:"version"`
	PlatformID uint                     `json:"platform_id"`
	CreatedAt  time.Time                `json:"created_at"`
	FilesRoot  string                   `json:"files_root"`
	Databases  []platformBackupDatabase `json:"databases"`
	Redis      []platformBackupRedis    `json:"redis"`
}

type platformBackupDatabase struct {
	ID       uint   `json:"id"`
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	DumpPath string `json:"dump_path"`
}

type platformBackupRedis struct {
	ID       uint   `json:"id"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DB       int    `json:"db"`
	Label    string `json:"label"`
	DumpPath string `json:"dump_path"`
}

type redisKeyDump struct {
	Key       string `json:"key"`
	TTLMillis int64  `json:"ttl_millis"`
	Payload   string `json:"payload"`
}

func NewPlatformOpsService(cfg *config.Config, repos *repositories.Repositories, adapter platform.Adapter, websites *WebsiteService, apps *AppService, audit *AuditService) *PlatformOpsService {
	return &PlatformOpsService{cfg: cfg, repos: repos, adapter: adapter, websites: websites, apps: apps, audit: audit}
}

func (s *PlatformOpsService) View(ctx context.Context, websiteID uint) PlatformOpsView {
	view := PlatformOpsView{}
	if s == nil || s.repos == nil {
		return view
	}
	view.HealthLatest, _ = s.repos.HealthChecks.LatestByWebsite(websiteID)
	view.HealthHistory, _ = s.repos.HealthChecks.HistoryByWebsite(websiteID, 10)
	view.Backups, _ = s.repos.Backups.ListByWebsite(websiteID)
	view.DeployConfig, _ = s.repos.DeployConfigs.FindByWebsite(websiteID)
	view.OpenAlerts, _ = s.repos.Alerts.ListOpenByWebsite(websiteID)
	view.RecentAlerts, _ = s.repos.Alerts.ListRecentByWebsite(websiteID, 10)
	view.RuntimeCommand = fmt.Sprintf("deploycp runtime restart --platform %d", websiteID)
	view.DeployCommand = fmt.Sprintf("deploycp deploy --platform %d", websiteID)
	if view.DeployConfig != nil && strings.TrimSpace(view.DeployConfig.RepoURL) != "" {
		view.DeployCommand = fmt.Sprintf("deploycp deploy --platform %d --branch %s", websiteID, shellDisplayArg(view.DeployConfig.Branch))
	}
	_ = ctx
	return view
}

func (s *PlatformOpsService) SaveDeployConfig(ctx context.Context, websiteID uint, in PlatformDeployInput, actor *uint, ip string) error {
	site, err := s.website(websiteID)
	if err != nil {
		return err
	}
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	in.Branch = strings.TrimSpace(in.Branch)
	if in.Branch == "" {
		in.Branch = "main"
	}
	if err := validateGitRepoURL(in.RepoURL); err != nil {
		return err
	}
	if err := validateGitRefName(in.Branch); err != nil {
		return err
	}
	workDir, err := s.resolvePlatformWorkDir(site, in.WorkDir)
	if err != nil {
		return err
	}
	cfg := &models.PlatformDeployConfig{
		WebsiteID:          site.ID,
		RepoURL:            in.RepoURL,
		Branch:             in.Branch,
		WorkDir:            workDir,
		DeployCommand:      normalizeDeployCommand(in.DeployCommand),
		RestartAfterDeploy: in.RestartAfterDeploy,
	}
	if existing, err := s.repos.DeployConfigs.FindByWebsite(site.ID); err == nil && existing != nil {
		cfg.ID = existing.ID
		cfg.CreatedAt = existing.CreatedAt
		cfg.DeployKeyPrivateEnc = existing.DeployKeyPrivateEnc
		cfg.DeployKeyPublic = existing.DeployKeyPublic
		cfg.DeployKeyPath = existing.DeployKeyPath
	}
	if in.GenerateDeployKey {
		privateKey, publicKey, err := generateDeploySSHKey(site.Name)
		if err != nil {
			return err
		}
		keyPath, pub, enc, err := s.writeDeployKey(site, privateKey, publicKey)
		if err != nil {
			return err
		}
		cfg.DeployKeyPath = keyPath
		cfg.DeployKeyPublic = pub
		cfg.DeployKeyPrivateEnc = enc
	} else if strings.TrimSpace(in.DeployKeyPrivate) != "" {
		keyPath, pub, enc, err := s.writeDeployKey(site, in.DeployKeyPrivate, "")
		if err != nil {
			return err
		}
		cfg.DeployKeyPath = keyPath
		cfg.DeployKeyPublic = pub
		cfg.DeployKeyPrivateEnc = enc
	}
	if err := s.repos.DeployConfigs.Upsert(cfg); err != nil {
		return err
	}
	s.audit.Record(actor, "platform.deploy_config.update", "website", strconv.FormatUint(uint64(site.ID), 10), ip, map[string]any{
		"repo":        in.RepoURL,
		"branch":      in.Branch,
		"work_dir":    workDir,
		"has_key":     cfg.DeployKeyPrivateEnc != "",
		"generated":   in.GenerateDeployKey,
		"restart":     in.RestartAfterDeploy,
		"has_command": strings.TrimSpace(in.DeployCommand) != "",
	})
	_ = ctx
	return nil
}

func (s *PlatformOpsService) CheckHealth(ctx context.Context, websiteID uint, actor *uint, ip string) (*models.PlatformHealthCheck, error) {
	site, err := s.website(websiteID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	status := "ok"
	messages := []string{}
	serviceStatus := "not_applicable"
	httpStatus := 0
	sslStatus := "not_configured"
	probedHTTP := false

	if app := runtimeFromWebsite(site); app != nil && strings.TrimSpace(app.ServiceName) != "" && s.adapter != nil {
		svcStatus, err := s.adapter.Services().Status(ctx, app.ServiceName)
		if err != nil {
			serviceStatus = "unknown"
			status = worseStatus(status, "warning")
			messages = append(messages, "service status unavailable: "+err.Error())
		} else if svcStatus.Active {
			serviceStatus = "running"
		} else {
			serviceStatus = "stopped"
			status = worseStatus(status, "critical")
			messages = append(messages, "runtime service is stopped")
		}
		if strings.TrimSpace(app.HealthPath) != "" && strings.TrimSpace(app.Host) != "" && app.Port > 0 {
			probedHTTP = true
			code, err := probeHTTP(ctx, fmt.Sprintf("http://%s:%d%s", app.Host, app.Port, normalizedHealthPath(app.HealthPath)))
			httpStatus = code
			if err != nil {
				status = worseStatus(status, "critical")
				messages = append(messages, "runtime health check failed: "+err.Error())
			} else if code < 200 || code >= 400 {
				status = worseStatus(status, "critical")
				messages = append(messages, fmt.Sprintf("runtime health check returned HTTP %d", code))
			}
		}
	}
	if !probedHTTP {
		if domain := primaryDomainForHealth(site); domain != "" {
			probedHTTP = true
			scheme := "http"
			if site.SSLReady {
				scheme = "https"
			}
			code, err := probeHTTP(ctx, scheme+"://"+domain+"/")
			httpStatus = code
			if err != nil {
				status = worseStatus(status, "warning")
				messages = append(messages, "domain health check failed: "+err.Error())
			} else if code < 200 || code >= 500 {
				status = worseStatus(status, "warning")
				messages = append(messages, fmt.Sprintf("domain health check returned HTTP %d", code))
			}
		}
	}

	if cert, days := s.sslExpiry(site); cert != nil {
		switch {
		case cert.NotAfter == nil:
			sslStatus = "unknown"
			status = worseStatus(status, "warning")
			messages = append(messages, "certificate expiry is unknown")
		case days < 0:
			sslStatus = "expired"
			status = worseStatus(status, "critical")
			messages = append(messages, "SSL certificate has expired")
		case days <= defaultSSLExpiryAlertDays:
			sslStatus = "expiring"
			status = worseStatus(status, "warning")
			messages = append(messages, fmt.Sprintf("SSL certificate expires in %d day(s)", days))
		default:
			sslStatus = "valid"
		}
	}

	diskPct, err := diskUsedPct(platformHomeFromWebRoot(site.RootPath))
	if err != nil {
		status = worseStatus(status, "warning")
		messages = append(messages, "disk usage unavailable: "+err.Error())
	} else if diskPct >= defaultDiskAlertPct {
		status = worseStatus(status, "critical")
		messages = append(messages, fmt.Sprintf("disk usage is %.1f%%", diskPct))
	}
	if len(messages) == 0 {
		messages = append(messages, "all checks passed")
	}
	check := &models.PlatformHealthCheck{
		WebsiteID:      site.ID,
		Status:         status,
		ServiceStatus:  serviceStatus,
		HTTPStatusCode: httpStatus,
		SSLStatus:      sslStatus,
		DiskUsedPct:    diskPct,
		Message:        strings.Join(messages, "; "),
		CheckedAt:      now,
	}
	if err := s.repos.HealthChecks.Create(check); err != nil {
		return nil, err
	}
	s.syncAlerts(site.ID, serviceStatus, sslStatus, diskPct, check.Message, now)
	s.audit.Record(actor, "platform.health.check", "website", strconv.FormatUint(uint64(site.ID), 10), ip, map[string]any{"status": status})
	return check, nil
}

func (s *PlatformOpsService) CreateBackup(ctx context.Context, websiteID uint, kind string, actor *uint, ip string) (*models.PlatformBackup, error) {
	site, err := s.website(websiteID)
	if err != nil {
		return nil, err
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "manual"
	}
	backupDir := filepath.Join(s.cfg.Paths.BackupRoot, sanitizeName(site.Name))
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return nil, err
	}
	filePath := filepath.Join(backupDir, fmt.Sprintf("%s-%s.tar.gz", sanitizeName(site.Name), time.Now().UTC().Format("20060102T150405Z")))
	item := &models.PlatformBackup{WebsiteID: site.ID, FilePath: filePath, Status: "running", Kind: kind}
	if err := s.repos.Backups.Create(item); err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(s.cfg.Paths.BackupRoot, ".deploycp-platform-backup-*")
	if err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		_ = s.repos.Backups.Update(item)
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	extras, manifest, err := s.preparePlatformBackupExtras(ctx, site, tmpDir)
	if err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		_ = s.repos.Backups.Update(item)
		return nil, err
	}
	if err := writeTarGz(platformHomeFromWebRoot(site.RootPath), filePath, extras); err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		_ = s.repos.Backups.Update(item)
		return nil, err
	}
	st, err := os.Stat(filePath)
	if err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		_ = s.repos.Backups.Update(item)
		return nil, err
	}
	now := time.Now()
	item.Status = "completed"
	item.SizeBytes = st.Size()
	item.Message = backupManifestSummary(manifest)
	item.CompletedAt = &now
	if err := s.repos.Backups.Update(item); err != nil {
		return nil, err
	}
	s.audit.Record(actor, "platform.backup.create", "website", strconv.FormatUint(uint64(site.ID), 10), ip, map[string]any{"file": filePath, "size": st.Size(), "kind": kind})
	_ = ctx
	return item, nil
}

func (s *PlatformOpsService) RestoreBackup(ctx context.Context, websiteID, backupID uint, actor *uint, ip string) error {
	site, err := s.website(websiteID)
	if err != nil {
		return err
	}
	backup, err := s.repos.Backups.Find(backupID)
	if err != nil {
		return err
	}
	if backup.WebsiteID != site.ID {
		return fmt.Errorf("backup does not belong to this platform")
	}
	if backup.Status != "completed" {
		return fmt.Errorf("backup is not completed")
	}
	if _, err := os.Stat(backup.FilePath); err != nil {
		return fmt.Errorf("backup archive is unavailable: %w", err)
	}
	if _, err := s.CreateBackup(ctx, websiteID, "pre-restore", actor, ip); err != nil {
		return fmt.Errorf("pre-restore backup failed: %w", err)
	}
	tmpDir, err := os.MkdirTemp(s.cfg.Paths.BackupRoot, ".deploycp-platform-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if err := extractBackupMetadata(backup.FilePath, tmpDir); err != nil {
		return err
	}
	if err := extractTarGzSafe(backup.FilePath, platformHomeFromWebRoot(site.RootPath)); err != nil {
		return err
	}
	if err := s.restorePlatformBackupData(ctx, site, tmpDir); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(platformHomeFromWebRoot(site.RootPath), ".deploycp-backup"))
	if err := s.RepairPlatform(ctx, site.ID, actor, ip); err != nil {
		return fmt.Errorf("restore completed but repair failed: %w", err)
	}
	s.audit.Record(actor, "platform.backup.restore", "website", strconv.FormatUint(uint64(site.ID), 10), ip, map[string]any{"backup_id": backup.ID})
	return nil
}

func (s *PlatformOpsService) preparePlatformBackupExtras(ctx context.Context, site *models.Website, tmpDir string) (map[string]string, platformBackupManifest, error) {
	metadataRoot := filepath.Join(tmpDir, ".deploycp-backup")
	dbRoot := filepath.Join(metadataRoot, "databases")
	redisRoot := filepath.Join(metadataRoot, "redis")
	if err := os.MkdirAll(dbRoot, 0o700); err != nil {
		return nil, platformBackupManifest{}, err
	}
	if err := os.MkdirAll(redisRoot, 0o700); err != nil {
		return nil, platformBackupManifest{}, err
	}
	manifest := platformBackupManifest{
		Version:    1,
		PlatformID: site.ID,
		CreatedAt:  time.Now().UTC(),
		FilesRoot:  platformHomeFromWebRoot(site.RootPath),
	}
	extras := map[string]string{}
	addExtra := func(archiveName, diskPath string) {
		extras[filepath.ToSlash(archiveName)] = diskPath
	}

	databases, err := s.scopedDatabases(site.ID)
	if err != nil {
		return nil, platformBackupManifest{}, err
	}
	for _, db := range databases {
		name := fmt.Sprintf("%s-%d-%s.sql", sanitizeName(db.Engine), db.ID, sanitizeName(db.Database))
		archiveName := filepath.Join(".deploycp-backup", "databases", name)
		diskPath := filepath.Join(dbRoot, name)
		if err := s.dumpDatabase(ctx, &db, diskPath); err != nil {
			return nil, platformBackupManifest{}, fmt.Errorf("database backup failed for %s: %w", db.Label, err)
		}
		manifest.Databases = append(manifest.Databases, platformBackupDatabase{
			ID:       db.ID,
			Engine:   strings.ToLower(strings.TrimSpace(db.Engine)),
			Host:     db.Host,
			Port:     db.Port,
			Database: db.Database,
			Username: db.Username,
			DumpPath: filepath.ToSlash(archiveName),
		})
		addExtra(archiveName, diskPath)
	}

	redisItems, err := s.scopedRedis(site.ID)
	if err != nil {
		return nil, platformBackupManifest{}, err
	}
	for _, item := range redisItems {
		name := fmt.Sprintf("redis-%d-db%d.json", item.ID, item.DB)
		archiveName := filepath.Join(".deploycp-backup", "redis", name)
		diskPath := filepath.Join(redisRoot, name)
		if err := s.dumpRedis(ctx, &item, diskPath); err != nil {
			return nil, platformBackupManifest{}, fmt.Errorf("redis backup failed for %s: %w", item.Label, err)
		}
		manifest.Redis = append(manifest.Redis, platformBackupRedis{
			ID:       item.ID,
			Host:     item.Host,
			Port:     item.Port,
			DB:       item.DB,
			Label:    item.Label,
			DumpPath: filepath.ToSlash(archiveName),
		})
		addExtra(archiveName, diskPath)
	}

	manifestPath := filepath.Join(metadataRoot, "manifest.json")
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, platformBackupManifest{}, err
	}
	if err := os.WriteFile(manifestPath, append(payload, '\n'), 0o600); err != nil {
		return nil, platformBackupManifest{}, err
	}
	addExtra(filepath.Join(".deploycp-backup", "manifest.json"), manifestPath)
	return extras, manifest, nil
}

func (s *PlatformOpsService) restorePlatformBackupData(ctx context.Context, site *models.Website, tmpDir string) error {
	manifestPath := filepath.Join(tmpDir, ".deploycp-backup", "manifest.json")
	payload, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest platformBackupManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return fmt.Errorf("invalid backup manifest: %w", err)
	}
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported backup manifest version: %d", manifest.Version)
	}
	if manifest.PlatformID != 0 && manifest.PlatformID != site.ID {
		return fmt.Errorf("backup belongs to platform %d, not %d", manifest.PlatformID, site.ID)
	}
	for _, entry := range manifest.Databases {
		dumpPath := filepath.Join(tmpDir, filepath.Clean(entry.DumpPath))
		if !pathWithin(dumpPath, tmpDir) {
			return fmt.Errorf("database dump path escapes restore root: %s", entry.DumpPath)
		}
		item, err := s.repos.Databases.Find(entry.ID)
		if err != nil {
			return fmt.Errorf("database connection %d is missing; restore panel metadata before restoring data", entry.ID)
		}
		if !databaseBelongsToPlatform(item, site.ID) {
			return fmt.Errorf("database connection %d is not attached to this platform", entry.ID)
		}
		if err := s.restoreDatabase(ctx, item, dumpPath); err != nil {
			return fmt.Errorf("database restore failed for %s: %w", item.Label, err)
		}
	}
	for _, entry := range manifest.Redis {
		dumpPath := filepath.Join(tmpDir, filepath.Clean(entry.DumpPath))
		if !pathWithin(dumpPath, tmpDir) {
			return fmt.Errorf("redis dump path escapes restore root: %s", entry.DumpPath)
		}
		item, err := s.repos.Redis.Find(entry.ID)
		if err != nil {
			return fmt.Errorf("redis connection %d is missing; restore panel metadata before restoring data", entry.ID)
		}
		if !redisBelongsToPlatform(item, site.ID) {
			return fmt.Errorf("redis connection %d is not attached to this platform", entry.ID)
		}
		if err := s.restoreRedis(ctx, item, dumpPath); err != nil {
			return fmt.Errorf("redis restore failed for %s: %w", item.Label, err)
		}
	}
	return nil
}

func (s *PlatformOpsService) scopedDatabases(platformID uint) ([]models.DatabaseConnection, error) {
	if s == nil || s.repos == nil || s.repos.Databases == nil {
		return nil, fmt.Errorf("database repository unavailable")
	}
	items, err := s.repos.Databases.List()
	if err != nil {
		return nil, err
	}
	out := make([]models.DatabaseConnection, 0, len(items))
	for _, item := range items {
		if databaseBelongsToPlatform(&item, platformID) {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *PlatformOpsService) scopedRedis(platformID uint) ([]models.RedisConnection, error) {
	if s == nil || s.repos == nil || s.repos.Redis == nil {
		return nil, fmt.Errorf("redis repository unavailable")
	}
	items, err := s.repos.Redis.List()
	if err != nil {
		return nil, err
	}
	out := make([]models.RedisConnection, 0, len(items))
	for _, item := range items {
		if redisBelongsToPlatform(&item, platformID) {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *PlatformOpsService) dumpDatabase(ctx context.Context, item *models.DatabaseConnection, destPath string) error {
	if item == nil {
		return fmt.Errorf("database connection is nil")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return os.WriteFile(destPath, []byte("-- deploycp dry-run database dump\n"), 0o600)
	}
	password, err := utils.DecryptString(s.cfg.Security.SessionSecret, item.PasswordEnc)
	if err != nil {
		return err
	}
	engine := strings.ToLower(strings.TrimSpace(item.Engine))
	switch engine {
	case "mariadb", "mysql":
		bin, err := findExecutable("mariadb-dump", "mysqldump")
		if err != nil {
			return err
		}
		port := item.Port
		if port <= 0 {
			port = 3306
		}
		args := []string{
			"--host", item.Host,
			"--port", strconv.Itoa(port),
			"--user", item.Username,
			"--single-transaction",
			"--routines",
			"--triggers",
			"--default-character-set=utf8mb4",
			item.Database,
		}
		return runCommandToFile(ctx, "", []string{"MYSQL_PWD=" + password}, destPath, bin, args...)
	case "postgres", "postgresql":
		bin, err := findExecutable("pg_dump")
		if err != nil {
			return err
		}
		port := item.Port
		if port <= 0 {
			port = 5432
		}
		args := []string{
			"--host", item.Host,
			"--port", strconv.Itoa(port),
			"--username", item.Username,
			"--format", "plain",
			"--clean",
			"--if-exists",
			"--no-owner",
			"--no-privileges",
			"--dbname", item.Database,
		}
		return runCommandToFile(ctx, "", []string{"PGPASSWORD=" + password}, destPath, bin, args...)
	default:
		return fmt.Errorf("unsupported database engine: %s", item.Engine)
	}
}

func (s *PlatformOpsService) restoreDatabase(ctx context.Context, item *models.DatabaseConnection, dumpPath string) error {
	if item == nil {
		return fmt.Errorf("database connection is nil")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	password, err := utils.DecryptString(s.cfg.Security.SessionSecret, item.PasswordEnc)
	if err != nil {
		return err
	}
	engine := strings.ToLower(strings.TrimSpace(item.Engine))
	switch engine {
	case "mariadb", "mysql":
		bin, err := findExecutable("mariadb", "mysql")
		if err != nil {
			return err
		}
		port := item.Port
		if port <= 0 {
			port = 3306
		}
		args := []string{
			"--host", item.Host,
			"--port", strconv.Itoa(port),
			"--user", item.Username,
			"--binary-mode",
			item.Database,
		}
		return runCommandFromFile(ctx, "", []string{"MYSQL_PWD=" + password}, dumpPath, bin, args...)
	case "postgres", "postgresql":
		bin, err := findExecutable("psql")
		if err != nil {
			return err
		}
		port := item.Port
		if port <= 0 {
			port = 5432
		}
		args := []string{
			"--host", item.Host,
			"--port", strconv.Itoa(port),
			"--username", item.Username,
			"--dbname", item.Database,
			"--set", "ON_ERROR_STOP=1",
			"--file", dumpPath,
		}
		return runCommand(ctx, "", []string{"PGPASSWORD=" + password}, bin, args...)
	default:
		return fmt.Errorf("unsupported database engine: %s", item.Engine)
	}
}

func (s *PlatformOpsService) dumpRedis(ctx context.Context, item *models.RedisConnection, destPath string) error {
	if item == nil {
		return fmt.Errorf("redis connection is nil")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return os.WriteFile(destPath, []byte("[]\n"), 0o600)
	}
	password := ""
	if strings.TrimSpace(item.PasswordEnc) != "" {
		var err error
		password, err = utils.DecryptString(s.cfg.Security.SessionSecret, item.PasswordEnc)
		if err != nil {
			return err
		}
	}
	port := item.Port
	if port <= 0 {
		port = 6379
	}
	client := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", item.Host, port), Password: password, DB: item.DB})
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var cursor uint64
	dumps := []redisKeyDump{}
	for {
		keys, next, err := client.Scan(ctx, cursor, "*", 100).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			payload, err := client.Dump(ctx, key).Bytes()
			if err != nil {
				return err
			}
			ttl, err := client.PTTL(ctx, key).Result()
			if err != nil {
				return err
			}
			ttlMillis := ttl.Milliseconds()
			if ttlMillis < 0 {
				ttlMillis = 0
			}
			dumps = append(dumps, redisKeyDump{
				Key:       key,
				TTLMillis: ttlMillis,
				Payload:   base64.StdEncoding.EncodeToString(payload),
			})
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	payload, err := json.MarshalIndent(dumps, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, append(payload, '\n'), 0o600)
}

func (s *PlatformOpsService) restoreRedis(ctx context.Context, item *models.RedisConnection, dumpPath string) error {
	if item == nil {
		return fmt.Errorf("redis connection is nil")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	payload, err := os.ReadFile(dumpPath)
	if err != nil {
		return err
	}
	var dumps []redisKeyDump
	if err := json.Unmarshal(payload, &dumps); err != nil {
		return err
	}
	password := ""
	if strings.TrimSpace(item.PasswordEnc) != "" {
		password, err = utils.DecryptString(s.cfg.Security.SessionSecret, item.PasswordEnc)
		if err != nil {
			return err
		}
	}
	port := item.Port
	if port <= 0 {
		port = 6379
	}
	client := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", item.Host, port), Password: password, DB: item.DB})
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := client.FlushDB(ctx).Err(); err != nil {
		return err
	}
	for _, entry := range dumps {
		raw, err := base64.StdEncoding.DecodeString(entry.Payload)
		if err != nil {
			return fmt.Errorf("invalid redis dump payload for %q: %w", entry.Key, err)
		}
		if err := client.RestoreReplace(ctx, entry.Key, time.Duration(entry.TTLMillis)*time.Millisecond, string(raw)).Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *PlatformOpsService) Deploy(ctx context.Context, websiteID uint, branchOverride string, actor *uint, ip string) (*PlatformDeployResult, error) {
	site, err := s.website(websiteID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.repos.DeployConfigs.FindByWebsite(site.ID)
	if err != nil {
		return nil, fmt.Errorf("deploy config is not set for this platform")
	}
	branch := strings.TrimSpace(branchOverride)
	if branch == "" {
		branch = strings.TrimSpace(cfg.Branch)
	}
	if branch == "" {
		branch = "main"
	}
	if err := validateGitRefName(branch); err != nil {
		return nil, err
	}
	if err := validateGitRepoURL(cfg.RepoURL); err != nil {
		return nil, err
	}
	workDir, err := s.resolvePlatformWorkDir(site, cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	output := &strings.Builder{}
	if err := s.gitSync(ctx, cfg, workDir, branch, output); err != nil {
		finished := time.Now()
		msg := trimDeployOutput(output.String() + "\n" + err.Error())
		_ = s.repos.DeployConfigs.UpdateDeployResult(site.ID, "failed", msg, finished)
		return &PlatformDeployResult{PlatformID: site.ID, Status: "failed", Output: msg, StartedAt: started, FinishedAt: finished}, err
	}
	if strings.TrimSpace(cfg.DeployCommand) != "" {
		if err := s.runDeployCommand(ctx, cfg.DeployCommand, workDir, output); err != nil {
			finished := time.Now()
			msg := trimDeployOutput(output.String() + "\n" + err.Error())
			_ = s.repos.DeployConfigs.UpdateDeployResult(site.ID, "failed", msg, finished)
			return &PlatformDeployResult{PlatformID: site.ID, Status: "failed", Output: msg, StartedAt: started, FinishedAt: finished}, err
		}
	}
	if cfg.RestartAfterDeploy {
		if err := s.RestartRuntime(ctx, site.ID, actor, ip); err != nil {
			finished := time.Now()
			msg := trimDeployOutput(output.String() + "\nrestart failed: " + err.Error())
			_ = s.repos.DeployConfigs.UpdateDeployResult(site.ID, "failed", msg, finished)
			return &PlatformDeployResult{PlatformID: site.ID, Status: "failed", Output: msg, StartedAt: started, FinishedAt: finished}, err
		}
		output.WriteString("\nruntime restarted\n")
	}
	finished := time.Now()
	msg := trimDeployOutput(output.String())
	if err := s.repos.DeployConfigs.UpdateDeployResult(site.ID, "success", msg, finished); err != nil {
		return nil, err
	}
	s.audit.Record(actor, "platform.deploy", "website", strconv.FormatUint(uint64(site.ID), 10), ip, map[string]any{"branch": branch, "work_dir": workDir})
	return &PlatformDeployResult{PlatformID: site.ID, Status: "success", Output: msg, StartedAt: started, FinishedAt: finished}, nil
}

func (s *PlatformOpsService) RestartRuntime(ctx context.Context, websiteID uint, actor *uint, ip string) error {
	site, err := s.website(websiteID)
	if err != nil {
		return err
	}
	if app := runtimeFromWebsite(site); app != nil && strings.TrimSpace(app.ServiceName) != "" {
		if err := s.adapter.Services().Restart(ctx, app.ServiceName); err != nil {
			return err
		}
		s.audit.Record(actor, "platform.runtime.restart", "website", strconv.FormatUint(uint64(site.ID), 10), ip, map[string]string{"service": app.ServiceName})
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(site.Type), "php") {
		version := phpFPMRuntimeVersion(site.PHPVersion)
		if version == "" {
			return fmt.Errorf("PHP-FPM version is not configured")
		}
		serviceName := "php" + version + "-fpm"
		if err := s.adapter.Services().Restart(ctx, serviceName); err != nil {
			return err
		}
		s.audit.Record(actor, "platform.runtime.restart", "website", strconv.FormatUint(uint64(site.ID), 10), ip, map[string]string{"service": serviceName})
		return nil
	}
	return fmt.Errorf("platform does not have a managed runtime service")
}

func (s *PlatformOpsService) RepairPlatform(ctx context.Context, websiteID uint, actor *uint, ip string) error {
	site, err := s.website(websiteID)
	if err != nil {
		return err
	}
	if s.websites != nil {
		if err := s.websites.RefreshConfig(ctx, site.ID); err != nil {
			return err
		}
	}
	if app := runtimeFromWebsite(site); app != nil && s.apps != nil {
		if err := s.apps.Reconcile(ctx, app.ID, actor, ip); err != nil {
			return err
		}
	}
	s.audit.Record(actor, "platform.repair", "website", strconv.FormatUint(uint64(site.ID), 10), ip, nil)
	return nil
}

func (s *PlatformOpsService) ResolvePlatformID(ref, cwd string) (uint, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" {
		if n, err := strconv.ParseUint(ref, 10, 64); err == nil && n > 0 {
			return uint(n), nil
		}
		items, err := s.repos.Websites.List()
		if err != nil {
			return 0, err
		}
		for _, item := range items {
			if strings.EqualFold(strings.TrimSpace(item.Name), ref) {
				return item.ID, nil
			}
			for _, domain := range item.Domains {
				if strings.EqualFold(strings.TrimSpace(domain.Domain), ref) {
					return item.ID, nil
				}
			}
		}
		return 0, fmt.Errorf("platform %q not found", ref)
	}
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	cwdAbs, _ := filepath.Abs(cwd)
	items, err := s.repos.Websites.List()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		root := platformHomeFromWebRoot(item.RootPath)
		rootAbs, _ := filepath.Abs(root)
		if cwdAbs == rootAbs || strings.HasPrefix(cwdAbs, rootAbs+string(filepath.Separator)) {
			return item.ID, nil
		}
	}
	return 0, fmt.Errorf("platform not resolved; pass --platform <id|domain>")
}

func (s *PlatformOpsService) website(id uint) (*models.Website, error) {
	if s == nil || s.repos == nil || s.repos.Websites == nil {
		return nil, fmt.Errorf("platform repository unavailable")
	}
	return s.repos.Websites.Find(id)
}

func (s *PlatformOpsService) resolvePlatformWorkDir(site *models.Website, raw string) (string, error) {
	home := platformHomeFromWebRoot(site.RootPath)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = site.RootPath
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(home, raw)
	}
	clean := filepath.Clean(raw)
	if !pathWithin(clean, home) {
		return "", fmt.Errorf("deploy workdir must be inside platform home")
	}
	return clean, nil
}

func (s *PlatformOpsService) writeDeployKey(site *models.Website, privateKey, publicKey string) (string, string, string, error) {
	privateKey = strings.TrimSpace(privateKey)
	if privateKey == "" {
		return "", "", "", fmt.Errorf("private key is empty")
	}
	keyHash := sha256.Sum256([]byte(privateKey))
	keyDir := filepath.Join(platformHomeFromWebRoot(site.RootPath), ".deploycp", "deploy")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return "", "", "", err
	}
	keyPath := filepath.Join(keyDir, "id_deploy")
	if err := utils.WriteFileAtomic(keyPath, []byte(privateKey+"\n"), 0o600); err != nil {
		return "", "", "", err
	}
	pub := strings.TrimSpace(publicKey)
	if pub == "" {
		pub = strings.TrimSpace(s.sshKeygenPublic(keyPath))
	}
	enc, err := utils.EncryptString(s.cfg.Security.SessionSecret, privateKey)
	if err != nil {
		return "", "", "", err
	}
	if pub == "" {
		pub = "sha256:" + hex.EncodeToString(keyHash[:])[:24]
	}
	return keyPath, pub, enc, nil
}

func generateDeploySSHKey(label string) (string, string, error) {
	pubKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	comment := "deploycp"
	label = strings.TrimSpace(label)
	if label != "" {
		comment += "-" + label
	}
	privateBlock, err := ssh.MarshalPrivateKey(privateKey, comment)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return "", "", err
	}
	privatePEM := strings.TrimSpace(string(pem.EncodeToMemory(privateBlock)))
	publicAuthorizedKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	return privatePEM, publicAuthorizedKey, nil
}

func (s *PlatformOpsService) sshKeygenPublic(keyPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-y", "-f", keyPath)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func (s *PlatformOpsService) gitSync(ctx context.Context, cfg *models.PlatformDeployConfig, workDir, branch string, output *strings.Builder) error {
	if err := os.MkdirAll(workDir, 0o775); err != nil {
		return err
	}
	gitDir := filepath.Join(workDir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		entries, readErr := os.ReadDir(workDir)
		if readErr != nil {
			return readErr
		}
		nonHidden := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			nonHidden++
		}
		if nonHidden > 0 {
			return fmt.Errorf("deploy workdir is not a git repository and is not empty: %s", workDir)
		}
		if err := s.runGit(ctx, cfg, workDir, output, "clone", "--branch", branch, "--single-branch", cfg.RepoURL, "."); err != nil {
			return err
		}
		return nil
	}
	if err := s.runGit(ctx, cfg, workDir, output, "remote", "set-url", "origin", cfg.RepoURL); err != nil {
		return err
	}
	if err := s.runGit(ctx, cfg, workDir, output, "fetch", "--prune", "origin", branch); err != nil {
		return err
	}
	if err := s.runGit(ctx, cfg, workDir, output, "checkout", "-B", branch, "origin/"+branch); err != nil {
		return err
	}
	return s.runGit(ctx, cfg, workDir, output, "reset", "--hard", "origin/"+branch)
}

func (s *PlatformOpsService) runGit(ctx context.Context, cfg *models.PlatformDeployConfig, workDir string, output *strings.Builder, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", gitCommandArgs(workDir, args...)...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	if strings.TrimSpace(cfg.DeployKeyPath) != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -i "+cfg.DeployKeyPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new")
	}
	out, err := cmd.CombinedOutput()
	output.WriteString("$ git " + strings.Join(args, " ") + "\n")
	output.Write(out)
	if err != nil {
		return fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return nil
}

func gitCommandArgs(workDir string, args ...string) []string {
	workDir = filepath.Clean(strings.TrimSpace(workDir))
	if workDir == "" || workDir == "." {
		return args
	}
	out := []string{"-c", "safe.directory=" + workDir}
	return append(out, args...)
}

func (s *PlatformOpsService) runDeployCommand(ctx context.Context, command, workDir string, output *strings.Builder) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("deploy command execution is not supported on windows")
	}
	command = normalizeDeployCommand(command)
	if command == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", command)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	output.WriteString("$ " + command + "\n")
	output.Write(out)
	if err != nil {
		return fmt.Errorf("deploy command failed: %w", err)
	}
	return nil
}

func normalizeDeployCommand(command string) string {
	command = strings.ReplaceAll(command, "\r\n", "\n")
	command = strings.ReplaceAll(command, "\r", "\n")
	return strings.TrimSpace(command)
}

func (s *PlatformOpsService) sslExpiry(site *models.Website) (*models.SSLCertificate, int) {
	if s.repos.SSL == nil || site == nil {
		return nil, 0
	}
	items, err := s.repos.SSL.List()
	if err != nil {
		return nil, 0
	}
	domains := map[string]struct{}{}
	for _, d := range site.Domains {
		domains[strings.ToLower(strings.TrimSpace(d.Domain))] = struct{}{}
	}
	for i := range items {
		if _, ok := domains[strings.ToLower(strings.TrimSpace(items[i].Domain))]; !ok {
			continue
		}
		if items[i].NotAfter == nil {
			return &items[i], 0
		}
		return &items[i], int(time.Until(*items[i].NotAfter).Hours() / 24)
	}
	return nil, 0
}

func (s *PlatformOpsService) syncAlerts(websiteID uint, serviceStatus, sslStatus string, diskPct float64, summary string, at time.Time) {
	check := func(active bool, typ, severity, message string) {
		if active {
			item, isNew, err := s.repos.Alerts.OpenOrUpdate(websiteID, typ, severity, message, at)
			if err == nil && isNew {
				s.sendAlertWebhook(item)
			}
			return
		}
		_ = s.repos.Alerts.Resolve(websiteID, typ, at)
	}
	check(serviceStatus == "stopped", "service_down", "critical", "Runtime service is stopped. "+summary)
	check(sslStatus == "expired" || sslStatus == "expiring", "ssl_expiring", "warning", "SSL certificate needs attention. "+summary)
	check(diskPct >= defaultDiskAlertPct, "disk_high", "critical", fmt.Sprintf("Platform disk usage is %.1f%%.", diskPct))
}

func (s *PlatformOpsService) sendAlertWebhook(item *models.AlertEvent) {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.Integrations.AlertWebhookURL) == "" || item == nil {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"website_id": item.WebsiteID,
		"type":       item.Type,
		"severity":   item.Severity,
		"message":    item.Message,
		"opened_at":  item.OpenedAt,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Integrations.AlertWebhookURL, strings.NewReader(string(body)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

func validateGitRepoURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "git@") {
		if strings.ContainsAny(raw, "\x00\r\n") || !strings.Contains(raw, ":") {
			return fmt.Errorf("invalid SSH git URL")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid git repository URL")
	}
	switch u.Scheme {
	case "https", "ssh":
		return nil
	default:
		return fmt.Errorf("git repository URL must use https or ssh")
	}
}

func validateGitRefName(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("branch is required")
	}
	if strings.HasPrefix(ref, "-") || strings.Contains(ref, "..") || strings.ContainsAny(ref, " ~^:?*[\\;&|$`<>(){}\x00\r\n") {
		return fmt.Errorf("invalid branch name")
	}
	return nil
}

func probeHTTP(ctx context.Context, target string) (int, error) {
	client := &http.Client{Timeout: 4 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return resp.StatusCode, nil
}

func normalizedHealthPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func primaryDomainForHealth(site *models.Website) string {
	if site == nil {
		return ""
	}
	for _, domain := range site.Domains {
		name := strings.TrimSpace(domain.Domain)
		if domain.Primary && name != "" && !strings.Contains(name, "*") {
			return name
		}
	}
	for _, domain := range site.Domains {
		name := strings.TrimSpace(domain.Domain)
		if name != "" && !strings.Contains(name, "*") {
			return name
		}
	}
	return ""
}

func diskUsedPct(path string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	total := float64(stat.Blocks) * float64(stat.Bsize)
	available := float64(stat.Bavail) * float64(stat.Bsize)
	if total <= 0 {
		return 0, nil
	}
	return (total - available) / total * 100, nil
}

func databaseBelongsToPlatform(item *models.DatabaseConnection, platformID uint) bool {
	if item == nil {
		return false
	}
	return (item.WebsiteID != nil && *item.WebsiteID == platformID) || (item.GoAppID != nil && *item.GoAppID == platformID)
}

func redisBelongsToPlatform(item *models.RedisConnection, platformID uint) bool {
	if item == nil {
		return false
	}
	return (item.WebsiteID != nil && *item.WebsiteID == platformID) || (item.GoAppID != nil && *item.GoAppID == platformID)
}

func backupManifestSummary(manifest platformBackupManifest) string {
	parts := []string{"files"}
	if len(manifest.Databases) == 1 {
		parts = append(parts, "1 database dump")
	} else if len(manifest.Databases) > 1 {
		parts = append(parts, fmt.Sprintf("%d database dumps", len(manifest.Databases)))
	}
	if len(manifest.Redis) == 1 {
		parts = append(parts, "1 redis dump")
	} else if len(manifest.Redis) > 1 {
		parts = append(parts, fmt.Sprintf("%d redis dumps", len(manifest.Redis)))
	}
	return strings.Join(parts, " + ")
}

func findExecutable(names ...string) (string, error) {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("required executable not found: %s", strings.Join(names, " or "))
}

func runCommandToFile(ctx context.Context, dir string, extraEnv []string, destPath, bin string, args ...string) error {
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = out
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", filepath.Base(bin), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runCommandFromFile(ctx context.Context, dir string, extraEnv []string, srcPath, bin string, args ...string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = in
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", filepath.Base(bin), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runCommand(ctx context.Context, dir string, extraEnv []string, bin string, args ...string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", filepath.Base(bin), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func writeTarGz(srcRoot, destFile string, extraFiles ...map[string]string) error {
	srcRoot = filepath.Clean(srcRoot)
	tmp := destFile + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(srcRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil || rel == "." {
			return err
		}
		if strings.HasPrefix(rel, ".deploycp/deploy/id_deploy") {
			return nil
		}
		if rel == ".deploycp-backup" || strings.HasPrefix(rel, ".deploycp-backup"+string(filepath.Separator)) {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			header.Linkname = target
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
	if err == nil && len(extraFiles) > 0 {
		for archiveName, diskPath := range extraFiles[0] {
			if archiveName == "" || filepath.IsAbs(archiveName) || strings.HasPrefix(filepath.Clean(archiveName), "..") {
				err = fmt.Errorf("unsafe backup metadata path: %s", archiveName)
				break
			}
			if err = addTarFile(tw, archiveName, diskPath); err != nil {
				break
			}
		}
	}
	if closeErr := tw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, destFile)
}

func addTarFile(tw *tar.Writer, archiveName, diskPath string) error {
	info, err := os.Stat(diskPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup metadata is not a regular file: %s", diskPath)
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(filepath.Clean(archiveName))
	header.Mode = 0o600
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	in, err := os.Open(diskPath)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(tw, in)
	return err
}

func extractBackupMetadata(archivePath, destRoot string) error {
	destRoot = filepath.Clean(destRoot)
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("backup contains unsafe metadata path: %s", header.Name)
		}
		if name != ".deploycp-backup" && !strings.HasPrefix(name, ".deploycp-backup"+string(filepath.Separator)) {
			continue
		}
		target := filepath.Join(destRoot, name)
		if !pathWithin(target, destRoot) {
			return fmt.Errorf("backup metadata escapes restore root: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			continue
		}
	}
}

func extractTarGzSafe(archivePath, destRoot string) error {
	destRoot = filepath.Clean(destRoot)
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("backup contains unsafe path: %s", header.Name)
		}
		target := filepath.Join(destRoot, name)
		if !pathWithin(target, destRoot) {
			return fmt.Errorf("backup path escapes platform root: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			parent := filepath.Dir(target)
			if err := os.MkdirAll(parent, 0o755); err != nil {
				return err
			}
			if resolved, err := filepath.EvalSymlinks(parent); err == nil && !pathWithin(resolved, destRoot) {
				return fmt.Errorf("backup parent escapes platform root through symlink: %s", header.Name)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode)&0o666)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(header.Linkname) {
				return fmt.Errorf("backup contains absolute symlink: %s", header.Name)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(target), header.Linkname))
			if !pathWithin(resolved, destRoot) {
				return fmt.Errorf("backup symlink escapes platform root: %s", header.Name)
			}
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			continue
		}
	}
}

func pathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if abs, err := filepath.Abs(path); err == nil {
		path = filepath.Clean(abs)
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = filepath.Clean(abs)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot := filepath.Clean(resolved)
		if rel, relErr := filepath.Rel(root, path); relErr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			path = filepath.Join(resolvedRoot, rel)
		}
		root = resolvedRoot
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = filepath.Clean(resolved)
	}
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func sanitizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-", "\x00", "")
	name = replacer.Replace(name)
	if name == "" {
		return "platform"
	}
	return name
}

func shellDisplayArg(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "main"
	}
	if strings.ContainsAny(value, " \t'\"") {
		return strconv.Quote(value)
	}
	return value
}

func trimDeployOutput(out string) string {
	out = strings.TrimSpace(out)
	if len(out) <= maxDeployOutputBytes {
		return out
	}
	return out[len(out)-maxDeployOutputBytes:]
}

func worseStatus(current, next string) string {
	rank := map[string]int{"ok": 0, "warning": 1, "critical": 2}
	if rank[next] > rank[current] {
		return next
	}
	return current
}
