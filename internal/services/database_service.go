package services

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	if in.Port < 1 || in.Port > 65535 {
		in.Port = 6379
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
	pong, err := client.Ping(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis connect failed: %w", err)
	}
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

func (s *DatabaseService) PostgresGUIBaseURL() string {
	return s.cfg.Integrations.PostgresGUIURL
}

func (s *DatabaseService) EnsureAdminerReady() error {
	return s.ensureLoopbackUIReady(
		s.cfg.Integrations.AdminerURL,
		func(port int) error { return s.startAdminerHelper(port) },
	)
}

func (s *DatabaseService) EnsurePostgresGUIReady() error {
	return s.ensureLoopbackUIReady(
		s.cfg.Integrations.PostgresGUIURL,
		func(port int) error { return s.startPgwebHelper(port) },
	)
}

// AdminerDBURL returns an Adminer URL with server/username/db pre-filled for
// a specific MariaDB connection. The user still needs to enter the password in
// Adminer's login form, but the fields are pre-populated for convenience.
func (s *DatabaseService) AdminerDBURL(id uint) (string, error) {
	item, err := s.dbRepo.Find(id)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(item.Engine), "mariadb") {
		return "", fmt.Errorf("adminer is only for MariaDB connections")
	}
	base := strings.TrimSpace(s.cfg.Integrations.AdminerURL)
	if base == "" {
		return "", fmt.Errorf("ADMINER_URL is not configured")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("server", item.Host)
	q.Set("username", item.Username)
	q.Set("db", item.Database)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// UpdateDatabasePassword changes the password for a managed database user and
// updates the encrypted password stored in the panel database.
func (s *DatabaseService) UpdateDatabasePassword(id uint, newPassword string, actor *uint, ip string) error {
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("password cannot be empty")
	}
	item, err := s.dbRepo.Find(id)
	if err != nil {
		return err
	}
	if err := s.changeManagedDatabasePassword(item, newPassword); err != nil {
		return err
	}
	encrypted, err := utils.EncryptString(s.cfg.Security.SessionSecret, newPassword)
	if err != nil {
		return err
	}
	item.PasswordEnc = encrypted
	if err := s.dbRepo.Update(item); err != nil {
		return err
	}
	s.audit.Record(actor, "database.password_update", "database_connection", fmt.Sprintf("%d", item.ID), ip, map[string]any{"engine": item.Engine})
	return nil
}

func (s *DatabaseService) changeManagedDatabasePassword(item *models.DatabaseConnection, newPassword string) error {
	if s.cfg.Features.PlatformMode == "dryrun" || item == nil || !isManagedLocalHost(item.Host) {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(item.Engine)) {
	case "mariadb":
		return s.changeMariaDBPassword(item, newPassword)
	case "postgres":
		return s.changePostgresPassword(item, newPassword)
	}
	return nil
}

func (s *DatabaseService) changeMariaDBPassword(item *models.DatabaseConnection, newPassword string) error {
	if strings.TrimSpace(s.cfg.Managed.MariaDBAdminUser) == "" {
		return fmt.Errorf("managed MariaDB password update requires MARIADB_ADMIN_USER")
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
	escapedPass := escapeMariaDBString(newPassword)
	for _, host := range []string{"localhost", "127.0.0.1"} {
		stmt := fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY '%s'", item.Username, host, escapedPass)
		_ = db.Exec(stmt).Error
	}
	return db.Exec("FLUSH PRIVILEGES").Error
}

func (s *DatabaseService) changePostgresPassword(item *models.DatabaseConnection, newPassword string) error {
	if strings.TrimSpace(s.cfg.Managed.PostgresAdminUser) == "" {
		return fmt.Errorf("managed PostgreSQL password update requires POSTGRES_ADMIN_USER")
	}
	if s.useLocalPostgresCLI() {
		_, err := s.runPostgresCLI(context.Background(), fmt.Sprintf("ALTER ROLE \"%s\" PASSWORD '%s'", item.Username, escapePostgresString(newPassword)))
		return err
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
	return db.Exec(fmt.Sprintf("ALTER ROLE \"%s\" PASSWORD '%s'", item.Username, escapePostgresString(newPassword))).Error
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

func (s *DatabaseService) ensureLoopbackUIReady(baseURL string, starter func(port int) error) error {
	if err := ensureReachableAddress(baseURL); err == nil {
		return nil
	}
	host, port, loopback, err := parseHelperTarget(baseURL)
	if err != nil {
		return err
	}
	if !loopback {
		return fmt.Errorf("database UI helper is not reachable at %s", host)
	}
	if err := starter(port); err != nil {
		return err
	}
	for i := 0; i < 8; i++ {
		time.Sleep(250 * time.Millisecond)
		if err := ensureReachableAddress(baseURL); err == nil {
			return nil
		}
	}
	return fmt.Errorf("database UI helper is not reachable at %s", host)
}

func ensureReachableAddress(baseURL string) error {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return fmt.Errorf("database UI is not configured")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return err
	}
	host := strings.TrimSpace(u.Host)
	if host == "" {
		return fmt.Errorf("database UI host is not configured")
	}
	if !strings.Contains(host, ":") {
		switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
		case "https":
			host += ":443"
		default:
			host += ":80"
		}
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return fmt.Errorf("database UI helper is not reachable at %s", host)
	}
	_ = conn.Close()
	return nil
}

func parseHelperTarget(baseURL string) (string, int, bool, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", 0, false, err
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return "", 0, false, fmt.Errorf("database UI host is not configured")
	}
	portStr := u.Port()
	if portStr == "" {
		if strings.EqualFold(u.Scheme, "https") {
			portStr = "443"
		} else {
			portStr = "80"
		}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false, fmt.Errorf("invalid database UI port")
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	return net.JoinHostPort(host, portStr), port, loopback, nil
}

func (s *DatabaseService) startPgwebHelper(port int) error {
	if _, err := exec.LookPath("pgweb"); err != nil {
		return fmt.Errorf("pgweb is not installed on the server")
	}
	logFile, err := s.helperLogFile("pgweb")
	if err != nil {
		return err
	}
	cmd := exec.Command("pgweb", "--listen=127.0.0.1", fmt.Sprintf("--port=%d", port), "--sessions")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = cmd.Process.Release()
	return logFile.Close()
}

func (s *DatabaseService) startAdminerHelper(port int) error {
	phpBinary, err := exec.LookPath("php")
	if err != nil {
		return fmt.Errorf("PHP is not installed on the server for Adminer")
	}
	adminerSource := firstExistingPath(
		"/usr/share/adminer/index.php",
		"/usr/share/adminer/adminer.php",
		"/usr/share/php/adminer/adminer.php",
	)
	if adminerSource == "" {
		return fmt.Errorf("Adminer is not installed on the server")
	}
	helperRoot := filepath.Join(s.cfg.Paths.StorageRoot, "generated", "adminer-helper")
	if err := os.MkdirAll(helperRoot, 0o755); err != nil {
		return err
	}
	entrypoint := filepath.Join(helperRoot, "index.php")
	src, err := os.ReadFile(adminerSource)
	if err != nil {
		return err
	}
	if err := os.WriteFile(entrypoint, src, 0o644); err != nil {
		return err
	}
	logFile, err := s.helperLogFile("adminer")
	if err != nil {
		return err
	}
	cmd := exec.Command(phpBinary, "-S", fmt.Sprintf("127.0.0.1:%d", port), "-t", helperRoot)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = cmd.Process.Release()
	return logFile.Close()
}

func (s *DatabaseService) helperLogFile(name string) (*os.File, error) {
	logDir := s.cfg.Paths.LogRoot
	if strings.TrimSpace(logDir) == "" {
		logDir = "./storage/logs"
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(logDir, name+"-helper.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func firstExistingPath(paths ...string) string {
	for _, candidate := range paths {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
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
	escapedPass := escapeMariaDBString(in.Password)
	statements := []string{
		fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", in.Database),
		fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s'", in.Username, escapedPass),
		fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'127.0.0.1' IDENTIFIED BY '%s'", in.Username, escapedPass),
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'", in.Database, in.Username),
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'127.0.0.1'", in.Database, in.Username),
		"FLUSH PRIVILEGES",
	}
	for _, stmt := range statements {
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
	if s.useLocalPostgresCLI() {
		return s.provisionPostgresCLI(in)
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
		if err := db.Exec(fmt.Sprintf("CREATE ROLE \"%s\" LOGIN PASSWORD '%s'", in.Username, escapePostgresString(in.Password))).Error; err != nil {
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
	if s.useLocalPostgresCLI() {
		return s.dropPostgresCLI(item)
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

// postgresDSN builds a Go PostgreSQL DSN using TCP. When POSTGRES_ADMIN_PASSWORD
// is empty on a local host, managed PostgreSQL operations fall back to
// runuser+psql instead of using this DSN.
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

func (s *DatabaseService) useLocalPostgresCLI() bool {
	return strings.TrimSpace(s.cfg.Managed.PostgresAdminPass) == "" && isManagedLocalHost(s.cfg.Managed.PostgresAdminHost)
}

func (s *DatabaseService) runPostgresCLI(ctx context.Context, sql string) (string, error) {
	runuser := strings.TrimSpace(s.cfg.Paths.RunuserBinary)
	if runuser == "" {
		runuser = "/usr/sbin/runuser"
	}
	adminUser := strings.TrimSpace(s.cfg.Managed.PostgresAdminUser)
	adminDB := strings.TrimSpace(s.cfg.Managed.PostgresAdminDB)
	if adminDB == "" {
		adminDB = "postgres"
	}
	cmd := exec.CommandContext(ctx, runuser, "-u", adminUser, "--", "psql", "-d", adminDB, "-v", "ON_ERROR_STOP=1", "-Atqc", sql)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return "", fmt.Errorf("postgres admin command failed: %w; stderr=%s", err, text)
		}
		return "", fmt.Errorf("postgres admin command failed: %w", err)
	}
	return text, nil
}

func (s *DatabaseService) provisionPostgresCLI(in DBConnectionInput) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	roleSQL := fmt.Sprintf(`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN CREATE ROLE "%s" LOGIN PASSWORD '%s'; ELSE ALTER ROLE "%s" PASSWORD '%s'; END IF; END $$;`,
		escapePostgresString(in.Username), in.Username, escapePostgresString(in.Password), in.Username, escapePostgresString(in.Password))
	if _, err := s.runPostgresCLI(ctx, roleSQL); err != nil {
		return err
	}
	exists, err := s.runPostgresCLI(ctx, fmt.Sprintf(`SELECT 1 FROM pg_database WHERE datname = '%s' LIMIT 1;`, escapePostgresString(in.Database)))
	if err != nil {
		return err
	}
	if strings.TrimSpace(exists) == "" {
		if _, err := s.runPostgresCLI(ctx, fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s";`, in.Database, in.Username)); err != nil {
			return err
		}
	}
	return nil
}

func (s *DatabaseService) dropPostgresCLI(item *models.DatabaseConnection) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = s.runPostgresCLI(ctx, fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid();`, escapePostgresString(item.Database)))
	if _, err := s.runPostgresCLI(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s";`, item.Database)); err != nil {
		return err
	}
	if s.canDropDatabaseUser(item) {
		if _, err := s.runPostgresCLI(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS "%s";`, item.Username)); err != nil {
			return err
		}
	}
	return nil
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
		fmt.Sprintf("requirepass \"%s\"", escapeRedisConfString(password)),
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

func (s *DatabaseService) UpdateRedisPassword(id uint, newPassword string, actor *uint, ip string) error {
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("password cannot be empty")
	}
	item, err := s.redisRepo.Find(id)
	if err != nil {
		return err
	}
	enc, err := utils.EncryptString(s.cfg.Security.SessionSecret, newPassword)
	if err != nil {
		return err
	}
	item.PasswordEnc = enc
	if err := s.redisRepo.Update(item); err != nil {
		return err
	}
	if err := s.provisionManagedRedis(item, newPassword, actor, ip); err != nil {
		return err
	}
	s.audit.Record(actor, "redis.password_update", "redis_connection", fmt.Sprintf("%d", item.ID), ip, nil)
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

// escapeMariaDBString escapes a string for safe use inside MySQL/MariaDB single-quoted literals.
// The MySQL driver does not support ? placeholders in DDL statements (CREATE USER, etc.).
func escapeMariaDBString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

// escapePostgresString escapes a string for safe use inside PostgreSQL single-quoted literals.
// The pq/pgx driver does not substitute ? placeholders in DDL statements (CREATE ROLE, etc.).
func escapePostgresString(s string) string {
	return strings.ReplaceAll(s, `'`, `''`)
}

// escapeRedisConfString escapes a password for use inside a double-quoted redis.conf value.
// Redis config parser interprets \\ as backslash and \" as double-quote inside quoted strings.
func escapeRedisConfString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
