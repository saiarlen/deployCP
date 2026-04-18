package services

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
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
	services  *repositories.ManagedServiceRepository
	adapter   platform.Adapter
	audit     *AuditService
}

var managedDBIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

func NewDatabaseService(cfg *config.Config, dbRepo *repositories.DatabaseConnectionRepository, redisRepo *repositories.RedisConnectionRepository, services *repositories.ManagedServiceRepository, adapter platform.Adapter, audit *AuditService) *DatabaseService {
	return &DatabaseService{cfg: cfg, dbRepo: dbRepo, redisRepo: redisRepo, services: services, adapter: adapter, audit: audit}
}

func (s *DatabaseService) ListDatabases() ([]models.DatabaseConnection, error) {
	return s.dbRepo.List()
}

func (s *DatabaseService) ListRedis() ([]models.RedisConnection, error) {
	return s.redisRepo.List()
}

func (s *DatabaseService) CreateDatabase(in DBConnectionInput, actor *uint, ip string) error {
	if err := s.ensureDatabaseUsernameUnique(in); err != nil {
		return err
	}
	if err := s.provisionManagedDatabase(in); err != nil {
		return err
	}
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
	if in.DB < 0 {
		return fmt.Errorf("redis database number must be 0 or greater")
	}
	if nextDB, adjusted, err := s.ensureRedisDBUnique(in.Host, in.DB); err != nil {
		return err
	} else if adjusted {
		in.DB = nextDB
	}
	if s.cfg.Features.PlatformMode != "dryrun" && isManagedLocalHost(in.Host) {
		if strings.TrimSpace(in.Password) == "" {
			return fmt.Errorf("local managed redis requires a password")
		}
		if in.Port <= 0 || portUnavailable(in.Host, in.Port) {
			nextPort, err := s.nextAvailableRedisPort(maxInt(in.Port, 6380))
			if err != nil {
				return err
			}
			in.Port = nextPort
		}
	}
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
	if err := s.provisionManagedRedis(item, in.Password, actor, ip); err != nil {
		_ = s.redisRepo.Delete(item.ID)
		return err
	}
	s.audit.Record(actor, "redis.create", "redis_connection", fmt.Sprintf("%d", item.ID), ip, nil)
	return nil
}

func (s *DatabaseService) DeleteDatabase(id uint, actor *uint, ip string) error {
	item, err := s.dbRepo.Find(id)
	if err != nil {
		return err
	}
	if err := s.dropManagedDatabase(item); err != nil {
		return err
	}
	if err := s.dbRepo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *DatabaseService) DeleteDatabaseRecord(item *models.DatabaseConnection, actor *uint, ip string) error {
	if item == nil {
		return nil
	}
	if err := s.dropManagedDatabase(item); err != nil {
		return err
	}
	if err := s.dbRepo.Delete(item.ID); err != nil {
		return err
	}
	s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", item.ID), ip, nil)
	return nil
}

func (s *DatabaseService) DeleteRedis(id uint, actor *uint, ip string) error {
	item, err := s.redisRepo.Find(id)
	if err != nil {
		return err
	}
	if err := s.deprovisionManagedRedis(item, actor, ip); err != nil {
		return err
	}
	if err := s.redisRepo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *DatabaseService) DeleteRedisRecord(item *models.RedisConnection, actor *uint, ip string) error {
	if item == nil {
		return nil
	}
	if err := s.deprovisionManagedRedis(item, actor, ip); err != nil {
		return err
	}
	if err := s.redisRepo.Delete(item.ID); err != nil {
		return err
	}
	s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", item.ID), ip, nil)
	return nil
}

func (s *DatabaseService) ReconcileManagedRedis(ctx context.Context, actor *uint, ip string) error {
	items, err := s.redisRepo.List()
	if err != nil {
		return err
	}
	for i := range items {
		if !isManagedLocalHost(items[i].Host) {
			continue
		}
		password := ""
		if strings.TrimSpace(items[i].PasswordEnc) != "" {
			password, err = utils.DecryptString(s.cfg.Security.SessionSecret, items[i].PasswordEnc)
			if err != nil {
				return err
			}
		}
		if err := s.provisionManagedRedis(&items[i], password, actor, ip); err != nil {
			return err
		}
	}
	return nil
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

func (s *DatabaseService) provisionManagedDatabase(in DBConnectionInput) error {
	if s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	if !isManagedLocalHost(in.Host) {
		return nil
	}
	if !managedDBIdentRe.MatchString(strings.TrimSpace(in.Database)) || !managedDBIdentRe.MatchString(strings.TrimSpace(in.Username)) {
		return fmt.Errorf("managed database names and usernames must match [A-Za-z_][A-Za-z0-9_]*")
	}
	switch strings.ToLower(strings.TrimSpace(in.Engine)) {
	case "mariadb":
		return s.provisionMariaDB(in)
	case "postgres":
		return s.provisionPostgres(in)
	default:
		return fmt.Errorf("unsupported engine")
	}
}

func (s *DatabaseService) ensureDatabaseUsernameUnique(in DBConnectionInput) error {
	items, err := s.dbRepo.List()
	if err != nil {
		return err
	}
	engine := strings.ToLower(strings.TrimSpace(in.Engine))
	username := strings.TrimSpace(in.Username)
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(item.Engine), engine) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Username), username) {
			return fmt.Errorf("database username already exists")
		}
	}
	return nil
}

func (s *DatabaseService) ensureRedisDBUnique(host string, requested int) (int, bool, error) {
	items, err := s.redisRepo.List()
	if err != nil {
		return requested, false, err
	}
	host = normalizeRedisHost(host)
	used := map[int]struct{}{}
	for _, item := range items {
		if normalizeRedisHost(item.Host) != host {
			continue
		}
		used[item.DB] = struct{}{}
	}
	if _, ok := used[requested]; !ok {
		return requested, false, nil
	}
	for next := 0; next < 4096; next++ {
		if _, ok := used[next]; ok {
			continue
		}
		return next, true, nil
	}
	return requested, false, fmt.Errorf("no available redis database number found")
}

func normalizeRedisHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" || host == "::1" {
		return "127.0.0.1"
	}
	return host
}

func (s *DatabaseService) dropManagedDatabase(item *models.DatabaseConnection) error {
	if s.cfg.Features.PlatformMode == "dryrun" || item == nil || !isManagedLocalHost(item.Host) {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(item.Engine)) {
	case "mariadb":
		return s.dropMariaDB(item)
	case "postgres":
		return s.dropPostgres(item)
	default:
		return nil
	}
}

func (s *DatabaseService) provisionMariaDB(in DBConnectionInput) error {
	if strings.TrimSpace(s.cfg.Managed.MariaDBAdminUser) == "" {
		return fmt.Errorf("managed mariadb provisioning requires MARIADB_ADMIN_USER")
	}
	dsn := s.mariaDBDSN()
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close()
	}
	statements := []string{
		fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", in.Database),
		fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY ?", in.Username),
		fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'127.0.0.1' IDENTIFIED BY ?", in.Username),
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'", in.Database, in.Username),
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'127.0.0.1'", in.Database, in.Username),
		"FLUSH PRIVILEGES",
	}
	for _, stmt := range statements {
		if strings.Contains(stmt, "IDENTIFIED BY ?") {
			if err := db.Exec(stmt, in.Password).Error; err != nil {
				return err
			}
			continue
		}
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// mariaDBDSN builds a Go MySQL DSN. When the admin password is empty and the
// host is local, it connects via unix socket (works with MariaDB socket auth).
func (s *DatabaseService) mariaDBDSN() string {
	user := s.cfg.Managed.MariaDBAdminUser
	pass := s.cfg.Managed.MariaDBAdminPass
	host := s.cfg.Managed.MariaDBAdminHost
	port := s.cfg.Managed.MariaDBAdminPort
	if strings.TrimSpace(pass) == "" && isManagedLocalHost(host) {
		for _, sock := range []string{
			"/var/run/mysqld/mysqld.sock",
			"/var/lib/mysql/mysql.sock",
			"/tmp/mysql.sock",
		} {
			if _, err := os.Stat(sock); err == nil {
				return fmt.Sprintf("%s@unix(%s)/mysql?parseTime=true&multiStatements=true", user, sock)
			}
		}
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/mysql?parseTime=true&multiStatements=true", user, pass, host, port)
}

func (s *DatabaseService) provisionPostgres(in DBConnectionInput) error {
	if strings.TrimSpace(s.cfg.Managed.PostgresAdminUser) == "" {
		return fmt.Errorf("managed postgres provisioning requires POSTGRES_ADMIN_USER")
	}
	dsn := s.postgresDSN()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close()
	}
	var roleExists int64
	if err := db.Raw("SELECT COUNT(*) FROM pg_roles WHERE rolname = ?", in.Username).Scan(&roleExists).Error; err != nil {
		return err
	}
	if roleExists == 0 {
		if err := db.Exec(fmt.Sprintf("CREATE ROLE \"%s\" LOGIN PASSWORD ?", in.Username), in.Password).Error; err != nil {
			return err
		}
	}
	var dbExists int64
	if err := db.Raw("SELECT COUNT(*) FROM pg_database WHERE datname = ?", in.Database).Scan(&dbExists).Error; err != nil {
		return err
	}
	if dbExists == 0 {
		if err := db.Exec(fmt.Sprintf("CREATE DATABASE \"%s\" OWNER \"%s\"", in.Database, in.Username)).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *DatabaseService) dropMariaDB(item *models.DatabaseConnection) error {
	if strings.TrimSpace(s.cfg.Managed.MariaDBAdminUser) == "" {
		return nil
	}
	dsn := s.mariaDBDSN()
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return err
	}
	if err := db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", item.Database)).Error; err != nil {
		return err
	}
	if s.canDropDatabaseUser(item) {
		for _, host := range []string{"localhost", "127.0.0.1"} {
			_ = db.Exec(fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s'", item.Username, host)).Error
		}
		_ = db.Exec("FLUSH PRIVILEGES").Error
	}
	return nil
}

func (s *DatabaseService) dropPostgres(item *models.DatabaseConnection) error {
	if strings.TrimSpace(s.cfg.Managed.PostgresAdminUser) == "" {
		return nil
	}
	dsn := s.postgresDSN()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return err
	}
	_ = db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = ? AND pid <> pg_backend_pid()`, item.Database).Error
	if err := db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS \"%s\"", item.Database)).Error; err != nil {
		return err
	}
	if s.canDropDatabaseUser(item) {
		_ = db.Exec(fmt.Sprintf("DROP ROLE IF EXISTS \"%s\"", item.Username)).Error
	}
	return nil
}

// postgresDSN builds a Go PostgreSQL DSN. When the password is empty, it omits
// the password field so the driver can fall back to trust/peer auth.
func (s *DatabaseService) postgresDSN() string {
	user := s.cfg.Managed.PostgresAdminUser
	pass := s.cfg.Managed.PostgresAdminPass
	host := s.cfg.Managed.PostgresAdminHost
	port := s.cfg.Managed.PostgresAdminPort
	dbname := s.cfg.Managed.PostgresAdminDB
	if strings.TrimSpace(pass) == "" {
		return fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable", host, port, user, dbname)
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable", host, port, user, pass, dbname)
}

func (s *DatabaseService) canDropDatabaseUser(item *models.DatabaseConnection) bool {
	items, err := s.dbRepo.List()
	if err != nil {
		return false
	}
	for _, other := range items {
		if other.ID == item.ID {
			continue
		}
		if strings.EqualFold(other.Engine, item.Engine) && strings.EqualFold(other.Username, item.Username) {
			return false
		}
	}
	return true
}

func isManagedLocalHost(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	return h == "" || h == "127.0.0.1" || h == "localhost" || h == "::1"
}

func (s *DatabaseService) provisionManagedRedis(item *models.RedisConnection, password string, actor *uint, ip string) error {
	if s.cfg.Features.PlatformMode == "dryrun" || item == nil || !isManagedLocalHost(item.Host) {
		return nil
	}
	if strings.TrimSpace(s.cfg.Managed.RedisServerBinary) == "" {
		return fmt.Errorf("REDIS_SERVER_BINARY is required for managed redis")
	}
	root := filepath.Join(s.cfg.Paths.StorageRoot, "generated", "redis", fmt.Sprintf("%d", item.ID))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	logDir := filepath.Join(s.cfg.Paths.LogRoot, "redis")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	confPath := filepath.Join(root, "redis.conf")
	logPath := filepath.Join(logDir, fmt.Sprintf("%d.log", item.ID))
	content := strings.Join([]string{
		"bind 127.0.0.1",
		"protected-mode yes",
		fmt.Sprintf("port %d", item.Port),
		"daemonize no",
		"supervised no",
		fmt.Sprintf("dir %s", root),
		"dbfilename dump.rdb",
		fmt.Sprintf("logfile %s", logPath),
		fmt.Sprintf("databases %d", maxInt(item.DB+1, 16)),
		fmt.Sprintf("requirepass %s", password),
	}, "\n") + "\n"
	if err := utils.WriteFileAtomic(confPath, []byte(content), 0o640); err != nil {
		return err
	}
	serviceName := fmt.Sprintf("deploycp-redis-%d", item.ID)
	unitPath, err := s.adapter.Services().Install(context.Background(), platform.ServiceDefinition{
		Name:          serviceName,
		Description:   "DeployCP Redis instance",
		ExecPath:      s.cfg.Managed.RedisServerBinary,
		Args:          []string{confPath},
		WorkingDir:    root,
		RestartPolicy: "always",
		StdoutPath:    logPath,
		StderrPath:    logPath,
	})
	if err != nil {
		return err
	}
	_ = s.services.Upsert(&models.ManagedService{Name: serviceName, Type: "cache", PlatformName: s.adapter.Name(), UnitPath: unitPath, Enabled: true, Tags: "redis,managed", Description: "DeployCP managed Redis instance"})
	if err := s.adapter.Services().Enable(context.Background(), serviceName); err != nil {
		return err
	}
	if err := s.adapter.Services().Start(context.Background(), serviceName); err != nil {
		return err
	}
	s.audit.Record(actor, "redis.provision", "redis_connection", fmt.Sprintf("%d", item.ID), ip, map[string]any{"service": serviceName, "port": item.Port})
	return nil
}

func (s *DatabaseService) deprovisionManagedRedis(item *models.RedisConnection, actor *uint, ip string) error {
	if s.cfg.Features.PlatformMode == "dryrun" || item == nil || !isManagedLocalHost(item.Host) {
		return nil
	}
	serviceName := fmt.Sprintf("deploycp-redis-%d", item.ID)
	unitPath := ""
	if svc, err := s.services.FindByName(serviceName); err == nil && svc != nil {
		unitPath = svc.UnitPath
	}
	_ = s.adapter.Services().Stop(context.Background(), serviceName)
	_ = s.adapter.Services().Disable(context.Background(), serviceName)
	_ = s.services.DeleteByName(serviceName)
	if err := removeServiceUnitFile(s.cfg, s.adapter.Name(), serviceName, unitPath); err != nil {
		return err
	}
	root := filepath.Join(s.cfg.Paths.StorageRoot, "generated", "redis", fmt.Sprintf("%d", item.ID))
	if err := removeTreeSafe(root, s.cfg.Paths.StorageRoot); err != nil {
		return err
	}
	logPath := filepath.Join(s.cfg.Paths.LogRoot, "redis", fmt.Sprintf("%d.log", item.ID))
	if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.audit.Record(actor, "redis.deprovision", "redis_connection", fmt.Sprintf("%d", item.ID), ip, map[string]any{"service": serviceName})
	return nil
}

func (s *DatabaseService) nextAvailableRedisPort(start int) (int, error) {
	if start < 1024 {
		start = 6380
	}
	for port := start; port < 65535; port++ {
		if !portUnavailable("127.0.0.1", port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available redis port found")
}

func portUnavailable(host string, port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
