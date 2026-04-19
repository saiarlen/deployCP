package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/repositories"
	"deploycp/internal/system"
	"deploycp/internal/utils"
	"deploycp/internal/validators"
	"gorm.io/gorm"
)

type FTPService struct {
	cfg       *config.Config
	repo      *repositories.FTPUserRepository
	siteUsers *repositories.SiteUserRepository
	settings  *repositories.SettingRepository
	runner    *system.Runner
	audit     *AuditService
}

func NewFTPService(cfg *config.Config, repo *repositories.FTPUserRepository, siteUsers *repositories.SiteUserRepository, settings *repositories.SettingRepository, runner *system.Runner, audit *AuditService) *FTPService {
	return &FTPService{cfg: cfg, repo: repo, siteUsers: siteUsers, settings: settings, runner: runner, audit: audit}
}

func (s *FTPService) Create(ctx context.Context, item *models.FTPUser, actor *uint, ip string) error {
	if item == nil {
		return fmt.Errorf("ftp user is required")
	}
	if err := validators.ValidateUsername(item.Username); err != nil {
		return err
	}
	if _, err := s.repo.FindByUsername(item.Username); err == nil {
		return fmt.Errorf("ftp username already exists")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if s.siteUsers != nil {
		if _, err := s.siteUsers.FindByUsername(item.Username); err == nil {
			return fmt.Errorf("username already exists as a site user")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if err := validators.ValidatePath(item.HomeDir); err != nil {
		return err
	}
	if strings.TrimSpace(item.Password) == "" {
		item.Password = utils.GeneratePassword()
	}
	encrypted, err := utils.EncryptString(s.cfg.Security.SessionSecret, item.Password)
	if err != nil {
		return err
	}
	item.PasswordEnc = encrypted
	if err := os.MkdirAll(item.HomeDir, 0o750); err != nil {
		return err
	}
	if err := s.ensureServerConfig(ctx, actor, ip); err != nil {
		return err
	}
	if s.cfg.Features.PlatformMode != "dryrun" {
		if err := s.ensureSystemUser(ctx, item.Username, item.Password, item.HomeDir, actor, ip); err != nil {
			return err
		}
	}
	if err := s.repo.Create(item); err != nil {
		return err
	}
	s.audit.Record(actor, "ftp_user.create", "ftp_user", fmt.Sprintf("%d", item.ID), ip, map[string]any{"username": item.Username, "website_id": item.WebsiteID})
	return nil
}

func (s *FTPService) DeleteByID(ctx context.Context, id uint, actor *uint, ip string) error {
	item, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	return s.DeleteModel(ctx, item, actor, ip)
}

func (s *FTPService) DeleteModel(ctx context.Context, item *models.FTPUser, actor *uint, ip string) error {
	if item == nil {
		return nil
	}
	if s.cfg.Features.PlatformMode != "dryrun" {
		_, _ = s.runner.Run(ctx, system.CommandRequest{
			Binary:      "/usr/sbin/userdel",
			Args:        []string{item.Username},
			Timeout:     20 * time.Second,
			AuditAction: "ftp.user.delete",
			ActorUserID: actor,
			IP:          ip,
		})
	}
	if err := s.repo.Delete(item.ID); err != nil {
		return err
	}
	s.audit.Record(actor, "ftp_user.delete", "ftp_user", fmt.Sprintf("%d", item.ID), ip, map[string]string{"username": item.Username})
	return nil
}

func (s *FTPService) DeleteByWebsite(ctx context.Context, websiteID uint, actor *uint, ip string) error {
	items, err := s.repo.ListByWebsite(websiteID)
	if err != nil {
		return err
	}
	for i := range items {
		if err := s.DeleteModel(ctx, &items[i], actor, ip); err != nil {
			return err
		}
	}
	return nil
}

func (s *FTPService) ReconcileConfig(ctx context.Context, actor *uint, ip string) error {
	return s.ensureServerConfig(ctx, actor, ip)
}

func (s *FTPService) ReconcileUsers(ctx context.Context, actor *uint, ip string) error {
	items, err := s.repo.List()
	if err != nil {
		return err
	}
	for i := range items {
		if s.cfg.Features.PlatformMode == "dryrun" {
			continue
		}
		if err := os.MkdirAll(items[i].HomeDir, 0o750); err != nil {
			return err
		}
		password, decErr := utils.DecryptString(s.cfg.Security.SessionSecret, items[i].PasswordEnc)
		if decErr != nil {
			return decErr
		}
		if err := s.ensureSystemUser(ctx, items[i].Username, password, items[i].HomeDir, actor, ip); err != nil {
			return err
		}
	}
	return s.ensureServerConfig(ctx, actor, ip)
}

func (s *FTPService) ensureSystemUser(ctx context.Context, username, password, homeDir string, actor *uint, ip string) error {
	_, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/id",
		Args:        []string{"-u", username},
		Timeout:     5 * time.Second,
		AuditAction: "ftp.user.lookup",
		ActorUserID: actor,
		IP:          ip,
	})
	if err != nil {
		if _, createErr := s.runner.Run(ctx, system.CommandRequest{
			Binary:      "/usr/sbin/useradd",
			Args:        []string{"-M", "-d", homeDir, "-s", s.cfg.Managed.FTPNoLoginShell, username},
			Timeout:     20 * time.Second,
			AuditAction: "ftp.user.create",
			ActorUserID: actor,
			IP:          ip,
		}); createErr != nil {
			return createErr
		}
	}
	if _, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/chpasswd",
		Stdin:       fmt.Sprintf("%s:%s\n", username, password),
		Timeout:     10 * time.Second,
		AuditAction: "ftp.user.password",
		ActorUserID: actor,
		IP:          ip,
	}); err != nil {
		return err
	}
	return nil
}

func (s *FTPService) ensureServerConfig(ctx context.Context, actor *uint, ip string) error {
	if s.cfg.Managed.ProFTPDConfDir == "" {
		return nil
	}
	masquerade := ""
	if s.settings != nil {
		masquerade, _ = s.settings.Get("proftpd_masquerade_address")
	}
	lines := []string{
		"# DeployCP managed ProFTPD settings",
		"RequireValidShell off",
		"DefaultRoot ~",
		"Umask 002 002",
	}
	if strings.TrimSpace(masquerade) != "" {
		lines = append(lines, "MasqueradeAddress "+strings.TrimSpace(masquerade))
	}
	path := filepath.Join(s.cfg.Managed.ProFTPDConfDir, "deploycp.conf")
	if err := utils.WriteFileAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	if strings.TrimSpace(s.cfg.Managed.ProFTPDServiceName) != "" {
		_, _ = s.runner.Run(ctx, system.CommandRequest{
			Binary:      s.cfg.Paths.SystemctlBinary,
			Args:        []string{"reload", s.cfg.Managed.ProFTPDServiceName},
			Timeout:     20 * time.Second,
			AuditAction: "ftp.service.reload",
			ActorUserID: actor,
			IP:          ip,
		})
	}
	return nil
}
