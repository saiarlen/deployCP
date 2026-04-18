package models

import "time"

type User struct {
	ID           uint   `gorm:"primaryKey"`
	Username     string `gorm:"size:80;uniqueIndex;not null"`
	Email        string `gorm:"size:150;uniqueIndex;not null"`
	Name         string `gorm:"size:150;not null;default:''"`
	PasswordHash string `gorm:"size:255;not null"`
	Role         string `gorm:"size:40;index;not null;default:admin"`
	IsAdmin      bool   `gorm:"not null;default:true"`
	IsActive     bool   `gorm:"not null;default:true"`
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type UserPlatformAccess struct {
	ID         uint `gorm:"primaryKey"`
	UserID     uint `gorm:"not null;index;uniqueIndex:idx_user_platform_access,priority:1"`
	PlatformID uint `gorm:"not null;index;uniqueIndex:idx_user_platform_access,priority:2"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type AuthSession struct {
	ID        uint      `gorm:"primaryKey"`
	SessionID string    `gorm:"size:255;uniqueIndex;not null"`
	UserID    uint      `gorm:"index;not null"`
	IP        string    `gorm:"size:64"`
	UserAgent string    `gorm:"size:255"`
	ExpiresAt time.Time `gorm:"index"`
	CreatedAt time.Time
	UpdatedAt time.Time
	User      User `gorm:"constraint:OnDelete:CASCADE"`
}

type Website struct {
	ID               uint   `gorm:"primaryKey"`
	Name             string `gorm:"size:120;index;not null"`
	RootPath         string `gorm:"size:255;not null"`
	Type             string `gorm:"size:30;not null;default:static"` // static|php|proxy
	AppRuntime       string `gorm:"size:30"`                         // go|python|node|binary (only for proxy sites)
	ExecutionMode    string `gorm:"size:16;index"`                   // compiled|interpreted
	ProcessManager   string `gorm:"size:24;index"`                   // systemd|pm2|gunicorn|uwsgi
	BinaryPath       string `gorm:"size:255"`
	EntryPoint       string `gorm:"size:255"`
	Host             string `gorm:"size:64"`
	Port             int
	StartArgs        string `gorm:"type:text"`
	HealthPath       string `gorm:"size:255"`
	RestartPolicy    string `gorm:"size:32"`
	Workers          int
	WorkerClass      string `gorm:"size:32"`
	MaxMemory        string `gorm:"size:16"`
	Timeout          int
	ExecMode         string `gorm:"size:16"`
	StdoutLogPath    string `gorm:"size:255"`
	StderrLogPath    string `gorm:"size:255"`
	ServiceName      string `gorm:"size:180;index"`
	PHPVersion       string `gorm:"size:16"`
	ProxyTarget      string `gorm:"size:255"`
	CustomDirectives string `gorm:"type:text"`
	PhpSettings      string `gorm:"type:text"` // JSON blob for PHP tuning
	Enabled          bool   `gorm:"not null;default:true"`
	SSLReady         bool   `gorm:"not null;default:false"`
	SiteUserID       *uint  `gorm:"index"`
	AccessLogPath    string `gorm:"size:255"`
	ErrorLogPath     string `gorm:"size:255"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Domains          []WebsiteDomain
	SiteUser         *SiteUser `gorm:"foreignKey:SiteUserID"`
}

func (Website) TableName() string {
	return "platforms"
}

type WebsiteDomain struct {
	ID        uint   `gorm:"primaryKey"`
	WebsiteID uint   `gorm:"index;not null"`
	Domain    string `gorm:"size:190;index;not null"`
	Primary   bool   `gorm:"not null;default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SiteUser struct {
	ID                uint   `gorm:"primaryKey"`
	Username          string `gorm:"size:80;uniqueIndex;not null"`
	HomeDirectory     string `gorm:"size:255;not null"`
	AllowedRoot       string `gorm:"size:255;not null"`
	Shell             string `gorm:"size:255;not null"`
	RestrictedPolicy  string `gorm:"size:80;not null;default:restricted-shell"`
	UID               *int
	GID               *int
	IsActive          bool  `gorm:"not null;default:true"`
	SSHEnabled        bool  `gorm:"not null;default:true"`
	WebsiteID         *uint `gorm:"index"`
	LastPasswordReset *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type GoApp struct {
	ID               uint   `gorm:"primaryKey"`
	Name             string `gorm:"size:120;index;not null"`
	Runtime          string `gorm:"column:app_runtime;size:32;index"` // go|node|python|php|binary
	ExecutionMode    string `gorm:"size:16;index"`                    // compiled|interpreted
	ProcessManager   string `gorm:"size:24;index"`                    // systemd|pm2|gunicorn|uwsgi
	BinaryPath       string `gorm:"size:255"`
	EntryPoint       string `gorm:"size:255"` // required for interpreted runtimes
	WorkingDirectory string `gorm:"column:root_path;size:255;not null"`
	Type             string `gorm:"size:30;not null;default:proxy"`
	ProxyTarget      string `gorm:"size:255"`
	Host             string `gorm:"size:64"`
	Port             int
	StartArgs        string `gorm:"type:text"`
	HealthPath       string `gorm:"size:255"`
	RestartPolicy    string `gorm:"size:32"`
	Workers          int
	WorkerClass      string `gorm:"size:32"`
	MaxMemory        string `gorm:"size:16"`
	Timeout          int
	ExecMode         string `gorm:"size:16"`
	StdoutLogPath    string `gorm:"size:255"`
	StderrLogPath    string `gorm:"size:255"`
	ServiceName      string `gorm:"size:180;index"`
	WebsiteID        *uint  `gorm:"-"`
	Enabled          bool   `gorm:"not null;default:true"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	EnvVars          []AppEnvVar
	Website          *Website `gorm:"-"`
}

func (GoApp) TableName() string {
	return "platforms"
}

type AppEnvVar struct {
	ID        uint   `gorm:"primaryKey"`
	GoAppID   uint   `gorm:"index;not null"`
	Key       string `gorm:"size:128;not null"`
	Value     string `gorm:"type:text"`
	Masked    bool   `gorm:"not null;default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ManagedService struct {
	ID           uint   `gorm:"primaryKey"`
	Name         string `gorm:"size:180;uniqueIndex;not null"`
	Type         string `gorm:"size:80;index;not null"` // go-app|nginx|system
	PlatformName string `gorm:"size:180;not null"`
	UnitPath     string `gorm:"size:255"`
	Tags         string `gorm:"size:255"`
	Description  string `gorm:"size:500"`
	Enabled      bool   `gorm:"not null;default:true"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type DatabaseConnection struct {
	ID          uint   `gorm:"primaryKey"`
	Label       string `gorm:"size:120;index;not null"`
	Engine      string `gorm:"size:20;index;not null"` // mariadb|postgres
	Host        string `gorm:"size:190;not null"`
	Port        int    `gorm:"not null"`
	Database    string `gorm:"size:120;not null"`
	Username    string `gorm:"size:120;not null"`
	PasswordEnc string `gorm:"type:text;not null"`
	Environment string `gorm:"size:40;index;not null;default:production"`
	WebsiteID   *uint  `gorm:"index"`
	GoAppID     *uint  `gorm:"index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type RedisConnection struct {
	ID          uint   `gorm:"primaryKey"`
	Label       string `gorm:"size:120;index;not null"`
	Host        string `gorm:"size:190;not null"`
	Port        int    `gorm:"not null"`
	PasswordEnc string `gorm:"type:text"`
	DB          int    `gorm:"not null;default:0"`
	Environment string `gorm:"size:40;index;not null;default:production"`
	WebsiteID   *uint  `gorm:"index"`
	GoAppID     *uint  `gorm:"index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SSLCertificate struct {
	ID            uint   `gorm:"primaryKey"`
	Domain        string `gorm:"size:190;index;not null"`
	Issuer        string `gorm:"size:190"`
	CertPath      string `gorm:"size:255"`
	KeyPath       string `gorm:"size:255"`
	NotBefore     *time.Time
	NotAfter      *time.Time
	Status        string `gorm:"size:40;index;not null;default:pending"`
	AutoRenew     bool   `gorm:"not null;default:true"`
	RenewalLastAt *time.Time
	RenewalNextAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type AuditLog struct {
	ID          uint      `gorm:"primaryKey"`
	ActorUserID *uint     `gorm:"index"`
	Action      string    `gorm:"size:120;index;not null"`
	Resource    string    `gorm:"size:120;index;not null"`
	ResourceID  string    `gorm:"size:120;index;not null"`
	Payload     string    `gorm:"type:text"`
	IP          string    `gorm:"size:64"`
	CreatedAt   time.Time `gorm:"index"`
}

type ActivityLog struct {
	ID        uint      `gorm:"primaryKey"`
	Type      string    `gorm:"size:80;index;not null"`
	Title     string    `gorm:"size:200;not null"`
	Body      string    `gorm:"type:text"`
	Level     string    `gorm:"size:20;index;not null;default:info"`
	RefType   string    `gorm:"size:80;index"`
	RefID     string    `gorm:"size:120;index"`
	CreatedAt time.Time `gorm:"index"`
}

type NginxSiteConfig struct {
	ID              uint   `gorm:"primaryKey"`
	WebsiteID       uint   `gorm:"index;not null"`
	ConfigPath      string `gorm:"size:255;not null"`
	EnabledPath     string `gorm:"size:255"`
	Checksum        string `gorm:"size:80"`
	Enabled         bool   `gorm:"not null;default:false"`
	LastValidatedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Setting struct {
	ID        uint   `gorm:"primaryKey"`
	Key       string `gorm:"size:120;uniqueIndex;not null"`
	Value     string `gorm:"type:text;not null"`
	Secret    bool   `gorm:"not null;default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type UserPreference struct {
	ID        uint   `gorm:"primaryKey"`
	UserID    uint   `gorm:"not null;index;uniqueIndex:idx_user_pref_key,priority:1"`
	Key       string `gorm:"size:120;not null;uniqueIndex:idx_user_pref_key,priority:2"`
	Value     string `gorm:"type:text;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type PanelFirewallRule struct {
	ID          uint   `gorm:"primaryKey"`
	Name        string `gorm:"size:120;index;not null"`
	Protocol    string `gorm:"size:20;index;not null;default:tcp"` // tcp|udp|icmp|any
	Port        string `gorm:"size:64;not null"`                   // single port, range, or "any"
	Source      string `gorm:"size:190;not null;default:0.0.0.0/0"`
	Action      string `gorm:"size:20;index;not null;default:allow"` // allow|deny
	Description string `gorm:"type:text"`
	Enabled     bool   `gorm:"not null;default:true"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SystemMetricSnapshot struct {
	ID           uint      `gorm:"primaryKey"`
	CPUUsage     float64   `gorm:"not null;default:0"`
	MemoryUsed   float64   `gorm:"not null;default:0"`
	DiskUsed     float64   `gorm:"not null;default:0"`
	Load1        float64   `gorm:"not null;default:0"`
	Load5        float64   `gorm:"not null;default:0"`
	Load15       float64   `gorm:"not null;default:0"`
	NetworkRxBps float64   `gorm:"not null;default:0"`
	NetworkTxBps float64   `gorm:"not null;default:0"`
	CreatedAt    time.Time `gorm:"index"`
}

type CronJob struct {
	ID        uint   `gorm:"primaryKey"`
	WebsiteID uint   `gorm:"index;not null"`
	Schedule  string `gorm:"size:60;not null"`
	Command   string `gorm:"type:text;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type VarnishConfig struct {
	ID             uint   `gorm:"primaryKey"`
	WebsiteID      uint   `gorm:"uniqueIndex;not null"`
	Enabled        bool   `gorm:"not null;default:false"`
	Server         string `gorm:"size:120;not null;default:127.0.0.1:6081"`
	CacheLifetime  int    `gorm:"not null;default:604800"`
	CacheTagPrefix string `gorm:"size:60"`
	ExcludedParams string `gorm:"size:255"`
	Excludes       string `gorm:"type:text"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type IPBlock struct {
	ID        uint   `gorm:"primaryKey"`
	WebsiteID uint   `gorm:"index;not null"`
	IP        string `gorm:"size:60;not null"`
	CreatedAt time.Time
}

type BotBlock struct {
	ID        uint   `gorm:"primaryKey"`
	WebsiteID uint   `gorm:"index;not null"`
	BotName   string `gorm:"size:120;not null"`
	CreatedAt time.Time
}

type BasicAuth struct {
	ID             uint   `gorm:"primaryKey"`
	WebsiteID      uint   `gorm:"uniqueIndex;not null"`
	Enabled        bool   `gorm:"not null;default:false"`
	Username       string `gorm:"size:120"`
	PasswordEnc    string `gorm:"column:password_enc;type:text"`
	Password       string `gorm:"-"`
	WhitelistedIPs string `gorm:"type:text"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type FTPUser struct {
	ID          uint   `gorm:"primaryKey"`
	WebsiteID   uint   `gorm:"index;not null"`
	Username    string `gorm:"size:80;not null"`
	PasswordEnc string `gorm:"column:password_enc;type:text"`
	Password    string `gorm:"-"`
	HomeDir     string `gorm:"size:255"`
	IsActive    bool   `gorm:"not null;default:true"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
