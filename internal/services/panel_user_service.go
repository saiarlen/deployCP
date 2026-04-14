package services

import (
	"fmt"
	"net/mail"
	"strings"

	"gorm.io/gorm"

	"deploycp/internal/models"
	"deploycp/internal/repositories"
	"deploycp/internal/utils"
	"deploycp/internal/validators"
)

type PanelUserInput struct {
	Username    string
	Email       string
	Name        string
	Password    string
	Status      string
	Role        string
	PlatformIDs []uint
}

type PanelUserService struct {
	repo   *repositories.UserRepository
	access *repositories.UserPlatformAccessRepository
	audit  *AuditService
}

func NewPanelUserService(repo *repositories.UserRepository, access *repositories.UserPlatformAccessRepository, audit *AuditService) *PanelUserService {
	return &PanelUserService{repo: repo, access: access, audit: audit}
}

func (s *PanelUserService) List() ([]models.User, error) {
	return s.repo.List()
}

func (s *PanelUserService) Find(id uint) (*models.User, error) {
	return s.repo.FindByID(id)
}

func (s *PanelUserService) Create(in PanelUserInput, actor *uint, ip string) (*models.User, error) {
	user, platformIDs, err := s.normalizeInput(in, 0)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.FindByUsername(user.Username); err == nil {
		return nil, fmt.Errorf("username already exists")
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	if _, err := s.repo.FindByEmail(user.Email); err == nil {
		return nil, fmt.Errorf("email already exists")
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	if err := s.repo.Create(user); err != nil {
		return nil, err
	}
	if s.access != nil {
		if err := s.access.ReplaceForUser(user.ID, platformIDs); err != nil {
			_ = s.repo.Delete(user.ID)
			return nil, err
		}
	}
	s.audit.Record(actor, "panel_user.create", "user", fmt.Sprintf("%d", user.ID), ip, map[string]any{
		"username":  user.Username,
		"email":     user.Email,
		"name":      user.Name,
		"role":      user.Role,
		"active":    user.IsActive,
		"platforms": platformIDs,
	})
	return user, nil
}

func (s *PanelUserService) Update(id uint, in PanelUserInput, actor *uint, ip string) error {
	existing, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	updated, platformIDs, err := s.normalizeInput(in, id)
	if err != nil {
		return err
	}
	if dup, err := s.repo.FindByUsername(updated.Username); err == nil && dup.ID != id {
		return fmt.Errorf("username already exists")
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if dup, err := s.repo.FindByEmail(updated.Email); err == nil && dup.ID != id {
		return fmt.Errorf("email already exists")
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}

	existing.Username = updated.Username
	existing.Email = updated.Email
	existing.Name = updated.Name
	existing.Role = updated.Role
	existing.IsAdmin = updated.IsAdmin
	existing.IsActive = updated.IsActive
	existing.PasswordHash = updated.PasswordHash
	if err := s.repo.Update(existing); err != nil {
		return err
	}
	if s.access != nil {
		if err := s.access.ReplaceForUser(existing.ID, platformIDs); err != nil {
			return err
		}
	}
	s.audit.Record(actor, "panel_user.update", "user", fmt.Sprintf("%d", id), ip, map[string]any{
		"username":  existing.Username,
		"email":     existing.Email,
		"name":      existing.Name,
		"role":      existing.Role,
		"active":    existing.IsActive,
		"platforms": platformIDs,
	})
	return nil
}

func (s *PanelUserService) Delete(id uint, actor *uint, ip string) error {
	existing, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	if s.access != nil {
		_ = s.access.DeleteByUser(id)
	}
	s.audit.Record(actor, "panel_user.delete", "user", fmt.Sprintf("%d", id), ip, map[string]any{
		"username": existing.Username,
		"email":    existing.Email,
	})
	return nil
}

func (s *PanelUserService) normalizeInput(in PanelUserInput, existingID uint) (*models.User, []uint, error) {
	username := strings.ToLower(strings.TrimSpace(in.Username))
	email := strings.ToLower(strings.TrimSpace(in.Email))
	name := strings.TrimSpace(in.Name)
	password := strings.TrimSpace(in.Password)
	role := strings.ToLower(strings.TrimSpace(in.Role))
	status := strings.ToLower(strings.TrimSpace(in.Status))
	platformIDs := sanitizePlatformIDs(in.PlatformIDs)

	if err := validators.ValidateUsername(username); err != nil {
		return nil, nil, err
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, nil, fmt.Errorf("valid email is required")
	}
	if name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	if len(password) < 10 {
		return nil, nil, fmt.Errorf("password must be at least 10 characters")
	}
	switch role {
	case "admin", "site_manager", "user":
	default:
		return nil, nil, fmt.Errorf("role must be admin, site manager, or user")
	}
	if role == "user" && len(platformIDs) == 0 {
		return nil, nil, fmt.Errorf("select at least one platform for role user")
	}
	if role != "user" {
		platformIDs = nil
	}

	isActive := true
	switch status {
	case "active":
		isActive = true
	case "notactive", "inactive":
		isActive = false
	default:
		return nil, nil, fmt.Errorf("status must be active or notactive")
	}

	hash, err := utils.HashPassword(password)
	if err != nil {
		return nil, nil, err
	}
	return &models.User{
		ID:           existingID,
		Username:     username,
		Email:        email,
		Name:         name,
		PasswordHash: hash,
		Role:         role,
		IsAdmin:      role == "admin",
		IsActive:     isActive,
	}, platformIDs, nil
}

func sanitizePlatformIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
