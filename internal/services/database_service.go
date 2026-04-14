package services

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/repositories"
	"deploycp/internal/utils"
)

type DBConnectionInput struct {
	Label       string
	Engine      string
	Host        string
	Port        int
	Database    string
	Username    string
	Password    string
	Environment string
	WebsiteID   *uint
	GoAppID     *uint
}

type RedisInput struct {
	Label       string
	Host        string
	Port        int
	Password    string
	DB          int
	Environment string
	WebsiteID   *uint
	GoAppID     *uint
}

type RedisDiagnostics struct {
	Ping   string
	Info   string
	DBSize int64
}

type DatabaseService struct {
	cfg       *config.Config
	dbRepo    *repositories.DatabaseConnectionRepository
	redisRepo *repositories.RedisConnectionRepository
	audit     *AuditService
}

func NewDatabaseService(cfg *config.Config, dbRepo *repositories.DatabaseConnectionRepository, redisRepo *repositories.RedisConnectionRepository, audit *AuditService) *DatabaseService {
	return &DatabaseService{cfg: cfg, dbRepo: dbRepo, redisRepo: redisRepo, audit: audit}
}

func (s *DatabaseService) ListDatabases() ([]models.DatabaseConnection, error) {
	return s.dbRepo.List()
}

func (s *DatabaseService) ListRedis() ([]models.RedisConnection, error) {
	return s.redisRepo.List()
}

func (s *DatabaseService) CreateDatabase(in DBConnectionInput, actor *uint, ip string) error {
	encrypted, err := utils.EncryptString(s.cfg.Security.SessionSecret, in.Password)
	if err != nil {
		return err
	}
	item := &models.DatabaseConnection{
		Label:       in.Label,
		Engine:      in.Engine,
		Host:        in.Host,
		Port:        in.Port,
		Database:    in.Database,
		Username:    in.Username,
		PasswordEnc: encrypted,
		Environment: in.Environment,
		WebsiteID:   in.WebsiteID,
		GoAppID:     in.GoAppID,
	}
	if err := s.dbRepo.Create(item); err != nil {
		return err
	}
	s.audit.Record(actor, "database.create", "database_connection", fmt.Sprintf("%d", item.ID), ip, map[string]any{"engine": in.Engine, "host": in.Host})
	return nil
}

func (s *DatabaseService) CreateRedis(in RedisInput, actor *uint, ip string) error {
	encrypted := ""
	if in.Password != "" {
		enc, err := utils.EncryptString(s.cfg.Security.SessionSecret, in.Password)
		if err != nil {
			return err
		}
		encrypted = enc
	}
	item := &models.RedisConnection{
		Label:       in.Label,
		Host:        in.Host,
		Port:        in.Port,
		PasswordEnc: encrypted,
		DB:          in.DB,
		Environment: in.Environment,
		WebsiteID:   in.WebsiteID,
		GoAppID:     in.GoAppID,
	}
	if err := s.redisRepo.Create(item); err != nil {
		return err
	}
	s.audit.Record(actor, "redis.create", "redis_connection", fmt.Sprintf("%d", item.ID), ip, nil)
	return nil
}

func (s *DatabaseService) DeleteDatabase(id uint, actor *uint, ip string) error {
	if err := s.dbRepo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *DatabaseService) DeleteRedis(id uint, actor *uint, ip string) error {
	if err := s.redisRepo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *DatabaseService) TestDatabase(id uint) error {
	item, err := s.dbRepo.Find(id)
	if err != nil {
		return err
	}
	password, err := utils.DecryptString(s.cfg.Security.SessionSecret, item.PasswordEnc)
	if err != nil {
		return err
	}
	var dsn string
	switch item.Engine {
	case "mariadb":
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=3s&readTimeout=3s&writeTimeout=3s", item.Username, password, item.Host, item.Port, item.Database)
		_, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		return err
	case "postgres":
		dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable connect_timeout=3", item.Host, item.Port, item.Username, password, item.Database)
		_, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		return err
	default:
		return fmt.Errorf("unsupported engine")
	}
}

func (s *DatabaseService) TestRedis(id uint) error {
	item, err := s.redisRepo.Find(id)
	if err != nil {
		return err
	}
	password := ""
	if item.PasswordEnc != "" {
		password, err = utils.DecryptString(s.cfg.Security.SessionSecret, item.PasswordEnc)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.Integrations.RedisInfoTimeoutSec)*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", item.Host, item.Port), Password: password, DB: item.DB})
	defer client.Close()
	return client.Ping(ctx).Err()
}

func (s *DatabaseService) RedisInfo(id uint) (*RedisDiagnostics, error) {
	item, err := s.redisRepo.Find(id)
	if err != nil {
		return nil, err
	}
	password := ""
	if item.PasswordEnc != "" {
		password, err = utils.DecryptString(s.cfg.Security.SessionSecret, item.PasswordEnc)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.Integrations.RedisInfoTimeoutSec)*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", item.Host, item.Port), Password: password, DB: item.DB})
	defer client.Close()
	pong, _ := client.Ping(ctx).Result()
	info, _ := client.Info(ctx).Result()
	size, _ := client.DBSize(ctx).Result()
	if len(info) > 2000 {
		info = info[:2000]
	}
	return &RedisDiagnostics{Ping: pong, Info: info, DBSize: size}, nil
}

func (s *DatabaseService) AdminerURL() string {
	return s.cfg.Integrations.AdminerURL
}

func (s *DatabaseService) PostgresGUIURL(id uint) (string, error) {
	item, err := s.dbRepo.Find(id)
	if err != nil {
		return "", err
	}
	if item.Engine != "postgres" {
		return "", fmt.Errorf("connection is not postgres")
	}
	password, err := utils.DecryptString(s.cfg.Security.SessionSecret, item.PasswordEnc)
	if err != nil {
		return "", err
	}
	pgURL := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", url.QueryEscape(item.Username), url.QueryEscape(password), item.Host, item.Port, item.Database)
	base, err := url.Parse(s.cfg.Integrations.PostgresGUIURL)
	if err != nil {
		return "", err
	}
	q := base.Query()
	q.Set("url", pgURL)
	base.RawQuery = q.Encode()
	return base.String(), nil
}
