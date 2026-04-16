package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
	"deploycp/internal/utils"
	"deploycp/internal/validators"
	"gorm.io/gorm"
)

type SiteUserInput struct {
	Username      string
	HomeDirectory string
	AllowedRoot   string
	Password      string
	SSHEnabled    bool
	WebsiteID     *uint
}

type SiteUserService struct {
	cfg     *config.Config
	repo    *repositories.SiteUserRepository
	adapter platform.Adapter
	audit   *AuditService
}

func NewSiteUserService(cfg *config.Config, repo *repositories.SiteUserRepository, adapter platform.Adapter, audit *AuditService) *SiteUserService {
	return &SiteUserService{cfg: cfg, repo: repo, adapter: adapter, audit: audit}
}

func (s *SiteUserService) List() ([]models.SiteUser, error) {
	return s.repo.List()
}

func (s *SiteUserService) Find(id uint) (*models.SiteUser, error) {
	return s.repo.Find(id)
}

func (s *SiteUserService) Create(ctx context.Context, in SiteUserInput, actor *uint, ip string) (*models.SiteUser, string, error) {
	if err := validators.ValidateUsername(in.Username); err != nil {
		return nil, "", err
	}
	if _, err := s.repo.FindByUsername(in.Username); err == nil {
		return nil, "", fmt.Errorf("site username already exists")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", err
	}
	if err := validators.ValidatePath(in.HomeDirectory); err != nil {
		return nil, "", err
	}
	if err := validators.ValidatePath(in.AllowedRoot); err != nil {
		return nil, "", err
	}
	if err := utils.ValidatePathWithin(in.HomeDirectory, in.AllowedRoot); err != nil {
		return nil, "", fmt.Errorf("allowed root must be within home: %w", err)
	}
	if err := os.MkdirAll(in.HomeDirectory, 0o750); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(in.AllowedRoot, 0o750); err != nil {
		return nil, "", err
	}
	password := in.Password
	if password == "" {
		password = utils.GeneratePassword()
	}
	if err := s.adapter.Users().EnsureRestrictedShell(ctx, s.cfg.Paths.RestrictedShellPath); err != nil {
		return nil, "", err
	}
	_, _, err := s.adapter.Users().Create(ctx, platform.SiteUserSpec{
		Username:    in.Username,
		Password:    password,
		HomeDir:     in.HomeDirectory,
		AllowedRoot: in.AllowedRoot,
		ShellPath:   s.cfg.Paths.RestrictedShellPath,
	})
	if err != nil {
		return nil, "", err
	}
	model := &models.SiteUser{
		Username:         in.Username,
		HomeDirectory:    filepath.Clean(in.HomeDirectory),
		AllowedRoot:      filepath.Clean(in.AllowedRoot),
		Shell:            s.cfg.Paths.RestrictedShellPath,
		RestrictedPolicy: "restricted-shell",
		IsActive:         true,
		SSHEnabled:       in.SSHEnabled,
		WebsiteID:        in.WebsiteID,
	}
	if err := s.repo.Create(model); err != nil {
		return nil, "", err
	}
	s.audit.Record(actor, "site_user.create", "site_user", fmt.Sprintf("%d", model.ID), ip, map[string]any{"username": in.Username, "allowed_root": in.AllowedRoot})
	return model, password, nil
}

func (s *SiteUserService) ResetPassword(ctx context.Context, id uint, password string, actor *uint, ip string) (string, error) {
	if password == "" {
		password = utils.GeneratePassword()
	}
	user, err := s.repo.Find(id)
	if err != nil {
		return "", err
	}
	if err := s.adapter.Users().SetPassword(ctx, user.Username, password); err != nil {
		return "", err
	}
	now := time.Now()
	user.LastPasswordReset = &now
	if err := s.repo.Update(user); err != nil {
		return "", err
	}
	s.audit.Record(actor, "site_user.password.reset", "site_user", fmt.Sprintf("%d", id), ip, nil)
	return password, nil
}

func (s *SiteUserService) ToggleEnabled(ctx context.Context, id uint, enabled bool, actor *uint, ip string) error {
	user, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	if !enabled {
		if err := s.adapter.Users().Disable(ctx, user.Username); err != nil {
			return err
		}
	}
	user.IsActive = enabled
	if err := s.repo.Update(user); err != nil {
		return err
	}
	s.audit.Record(actor, "site_user.toggle", "site_user", fmt.Sprintf("%d", id), ip, map[string]bool{"enabled": enabled})
	return nil
}

func (s *SiteUserService) Delete(ctx context.Context, id uint, actor *uint, ip string) error {
	user, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	if err := s.adapter.Users().Delete(ctx, user.Username); err != nil {
		return err
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "site_user.delete", "site_user", fmt.Sprintf("%d", id), ip, map[string]string{"username": user.Username})
	return nil
}
