package services

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/repositories"
	"deploycp/internal/utils"
)

type AuthService struct {
	cfg      *config.Config
	users    *repositories.UserRepository
	sessions *repositories.SessionRepository
	audit    *AuditService
}

func NewAuthService(cfg *config.Config, users *repositories.UserRepository, sessions *repositories.SessionRepository, audit *AuditService) *AuthService {
	return &AuthService{cfg: cfg, users: users, sessions: sessions, audit: audit}
}

func (s *AuthService) EnsureBootstrapAdmin() error {
	_, err := s.users.FirstAdmin()
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	// If no credentials in .env, skip — the setup page will handle it.
	if strings.TrimSpace(s.cfg.Security.BootstrapAdminUser) == "" || strings.TrimSpace(s.cfg.Security.BootstrapAdminPass) == "" {
		return nil
	}
	hash, err := utils.HashPassword(s.cfg.Security.BootstrapAdminPass)
	if err != nil {
		return err
	}
	email := strings.TrimSpace(s.cfg.Security.BootstrapAdminEmail)
	if email == "" {
		email = "admin@localhost"
	}
	admin := &models.User{
		Username:     s.cfg.Security.BootstrapAdminUser,
		Email:        email,
		PasswordHash: hash,
		Role:         "admin",
		IsAdmin:      true,
		IsActive:     true,
	}
	return s.users.Create(admin)
}

func (s *AuthService) NeedsSetup() bool {
	_, err := s.users.FirstAdmin()
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func (s *AuthService) CreateInitialAdmin(name, email, username, password string) error {
	if !s.NeedsSetup() {
		return fmt.Errorf("admin already exists")
	}
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	username = strings.TrimSpace(username)
	if name == "" || email == "" || username == "" || password == "" {
		return fmt.Errorf("all fields are required")
	}
	if len(username) < 3 {
		return fmt.Errorf("username must be at least 3 characters")
	}
	if len(password) < 10 {
		return fmt.Errorf("password must be at least 10 characters")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return fmt.Errorf("valid email is required")
	}
	hash, err := utils.HashPassword(password)
	if err != nil {
		return err
	}
	admin := &models.User{
		Username:     username,
		Name:         name,
		Email:        email,
		PasswordHash: hash,
		Role:         "admin",
		IsAdmin:      true,
		IsActive:     true,
	}
	return s.users.Create(admin)
}

func (s *AuthService) Authenticate(username, password string) (*models.User, error) {
	u, err := s.users.FindByUsername(username)
	if err != nil {
		return nil, errors.New("invalid username or password")
	}
	if !u.IsActive || !utils.VerifyPassword(u.PasswordHash, password) {
		return nil, errors.New("invalid username or password")
	}
	_ = s.users.UpdateLastLogin(u.ID)
	return u, nil
}

func (s *AuthService) StartUserSession(user *models.User, sid string, ip, ua string) error {
	expiresAt := time.Now().Add(24 * time.Hour)
	if sid == "" {
		sid = uuid.NewString()
	}
	return s.sessions.Create(&models.AuthSession{SessionID: sid, UserID: user.ID, IP: ip, UserAgent: ua, ExpiresAt: expiresAt})
}

func (s *AuthService) EndUserSession(sid string) error {
	if sid == "" {
		return nil
	}
	return s.sessions.DeleteBySessionID(sid)
}

func (s *AuthService) ChangePassword(userID uint, current, next string) error {
	u, err := s.users.FindByID(userID)
	if err != nil {
		return err
	}
	if !utils.VerifyPassword(u.PasswordHash, current) {
		return errors.New("current password is incorrect")
	}
	hash, err := utils.HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.users.UpdatePassword(userID, hash); err != nil {
		return err
	}
	s.audit.Record(&userID, "auth.password.update", "user", "self", "", nil)
	return nil
}

func (s *AuthService) UpdateProfile(userID uint, name, email string) error {
	u, err := s.users.FindByID(userID)
	if err != nil {
		return err
	}
	nextName := strings.TrimSpace(name)
	nextEmail := strings.ToLower(strings.TrimSpace(email))
	if nextName == "" {
		return fmt.Errorf("name is required")
	}
	if _, err := mail.ParseAddress(nextEmail); err != nil {
		return fmt.Errorf("valid email is required")
	}
	if existing, err := s.users.FindByEmail(nextEmail); err == nil && existing.ID != userID {
		return fmt.Errorf("email already exists")
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	u.Name = nextName
	u.Email = nextEmail
	if err := s.users.Update(u); err != nil {
		return err
	}
	s.audit.Record(&userID, "auth.profile.update", "user", "self", "", map[string]any{
		"name":  nextName,
		"email": nextEmail,
	})
	return nil
}
