package services

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/system"
)

type SystemPackageService struct {
	cfg    *config.Config
	runner *system.Runner
}

func NewSystemPackageService(cfg *config.Config, runner *system.Runner) *SystemPackageService {
	return &SystemPackageService{cfg: cfg, runner: runner}
}

func (s *SystemPackageService) DetectManager() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	candidates := []struct {
		name string
		bin  string
	}{
		{name: "apt", bin: "apt-get"},
		{name: "dnf", bin: "dnf"},
		{name: "yum", bin: "yum"},
		{name: "zypper", bin: "zypper"},
		{name: "pacman", bin: "pacman"},
	}
	for _, item := range candidates {
		if _, err := exec.LookPath(item.bin); err == nil {
			return item.name
		}
	}
	return ""
}

func (s *SystemPackageService) KnownService(name string) bool {
	_, ok := s.serviceSpec(name)
	return ok
}

func (s *SystemPackageService) EnsureInstalled(ctx context.Context, serviceName string, actor *uint, ip string) error {
	if s == nil || s.cfg == nil || s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	spec, ok := s.serviceSpec(serviceName)
	if !ok {
		return nil
	}
	manager := s.DetectManager()
	if manager == "" {
		return fmt.Errorf("no supported linux package manager found")
	}
	pkg := spec.packageFor(manager)
	if pkg == "" {
		return nil
	}
	installed, err := s.packageInstalled(ctx, manager, pkg)
	if err == nil && installed {
		return nil
	}
	req := system.CommandRequest{
		Timeout:     10 * time.Minute,
		AuditAction: "package.install",
		ActorUserID: actor,
		IP:          ip,
	}
	switch manager {
	case "apt":
		if _, err := s.runner.Run(ctx, system.CommandRequest{
			Binary:      "apt-get",
			Args:        []string{"update", "-y"},
			Timeout:     10 * time.Minute,
			AuditAction: "package.install.update",
			ActorUserID: actor,
			IP:          ip,
		}); err != nil {
			return err
		}
		req.Binary = "apt-get"
		req.Args = []string{"install", "-y", pkg}
	case "dnf":
		req.Binary = "dnf"
		req.Args = []string{"install", "-y", pkg}
	case "yum":
		req.Binary = "yum"
		req.Args = []string{"install", "-y", pkg}
	case "zypper":
		req.Binary = "zypper"
		req.Args = []string{"--non-interactive", "install", pkg}
	case "pacman":
		req.Binary = "pacman"
		req.Args = []string{"-Sy", "--noconfirm", pkg}
	default:
		return fmt.Errorf("unsupported package manager")
	}
	_, err = s.runner.Run(ctx, req)
	return err
}

func (s *SystemPackageService) IsInstalled(ctx context.Context, serviceName string) bool {
	if s == nil || s.cfg == nil || s.cfg.Features.PlatformMode == "dryrun" {
		return true
	}
	spec, ok := s.serviceSpec(serviceName)
	if !ok {
		return true
	}
	manager := s.DetectManager()
	if manager == "" {
		return false
	}
	pkg := spec.packageFor(manager)
	if pkg == "" {
		return false
	}
	installed, err := s.packageInstalled(ctx, manager, pkg)
	return err == nil && installed
}

func (s *SystemPackageService) ResolveServiceUnit(ctx context.Context, serviceName string) string {
	if s == nil || s.cfg == nil || s.cfg.Features.PlatformMode == "dryrun" {
		return serviceName
	}
	spec, ok := s.serviceSpec(serviceName)
	if !ok {
		return serviceName
	}
	for _, candidate := range spec.unitCandidates(s.DetectManager()) {
		if candidate == "" {
			continue
		}
		res, err := s.runner.Run(ctx, system.CommandRequest{
			Binary:  s.cfg.Paths.SystemctlBinary,
			Args:    []string{"list-unit-files", candidate + ".service", "--no-legend"},
			Timeout: 8 * time.Second,
		})
		if err == nil && strings.Contains(res.Stdout, candidate+".service") {
			return candidate
		}
	}
	return serviceName
}

func (s *SystemPackageService) packageInstalled(ctx context.Context, manager, pkg string) (bool, error) {
	req := system.CommandRequest{Timeout: 15 * time.Second}
	switch manager {
	case "apt":
		req.Binary = "dpkg"
		req.Args = []string{"-s", pkg}
	case "dnf", "yum", "zypper":
		req.Binary = "rpm"
		req.Args = []string{"-q", pkg}
	case "pacman":
		req.Binary = "pacman"
		req.Args = []string{"-Q", pkg}
	default:
		return false, fmt.Errorf("unsupported package manager")
	}
	_, err := s.runner.Run(ctx, req)
	return err == nil, nil
}

type servicePackageSpec struct {
	basePackages map[string]string
	baseUnits    map[string][]string
}

func (s servicePackageSpec) packageFor(manager string) string {
	return strings.TrimSpace(s.basePackages[manager])
}

func (s servicePackageSpec) unitCandidates(manager string) []string {
	out := append([]string{}, s.baseUnits[manager]...)
	seen := map[string]struct{}{}
	filtered := make([]string, 0, len(out))
	for _, item := range out {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		filtered = append(filtered, item)
	}
	return filtered
}

var phpFPMServicePattern = regexp.MustCompile(`^php([0-9]+(?:\.[0-9]+){1,2})-fpm$`)

func (s *SystemPackageService) serviceSpec(serviceName string) (servicePackageSpec, bool) {
	name := strings.ToLower(strings.TrimSpace(serviceName))
	switch name {
	case "nginx":
		return servicePackageSpec{
			basePackages: map[string]string{"apt": "nginx", "dnf": "nginx", "yum": "nginx", "zypper": "nginx", "pacman": "nginx"},
			baseUnits:    map[string][]string{"apt": {"nginx"}, "dnf": {"nginx"}, "yum": {"nginx"}, "zypper": {"nginx"}, "pacman": {"nginx"}},
		}, true
	case "mariadb", "mysql", "mysqld":
		return servicePackageSpec{
			basePackages: map[string]string{"apt": "mariadb-server", "dnf": "mariadb-server", "yum": "mariadb-server", "zypper": "mariadb", "pacman": "mariadb"},
			baseUnits:    map[string][]string{"apt": {"mariadb", "mysql"}, "dnf": {"mariadb", "mysqld"}, "yum": {"mariadb", "mysqld"}, "zypper": {"mariadb"}, "pacman": {"mariadb"}},
		}, true
	case "postgresql":
		return servicePackageSpec{
			basePackages: map[string]string{"apt": "postgresql", "dnf": "postgresql-server", "yum": "postgresql-server", "zypper": "postgresql-server", "pacman": "postgresql"},
			baseUnits:    map[string][]string{"apt": {"postgresql"}, "dnf": {"postgresql"}, "yum": {"postgresql"}, "zypper": {"postgresql"}, "pacman": {"postgresql"}},
		}, true
	case "redis-server", "redis":
		return servicePackageSpec{
			basePackages: map[string]string{"apt": "redis-server", "dnf": "redis", "yum": "redis", "zypper": "redis", "pacman": "redis"},
			baseUnits:    map[string][]string{"apt": {"redis-server"}, "dnf": {"redis", "redis-server"}, "yum": {"redis", "redis-server"}, "zypper": {"redis"}, "pacman": {"redis"}},
		}, true
	case "varnish":
		return servicePackageSpec{
			basePackages: map[string]string{"apt": "varnish", "dnf": "varnish", "yum": "varnish", "zypper": "varnish", "pacman": "varnish"},
			baseUnits:    map[string][]string{"apt": {"varnish"}, "dnf": {"varnish"}, "yum": {"varnish"}, "zypper": {"varnish"}, "pacman": {"varnish"}},
		}, true
	case "rabbitmq-server", "rabbitmq":
		return servicePackageSpec{
			basePackages: map[string]string{"apt": "rabbitmq-server", "dnf": "rabbitmq-server", "yum": "rabbitmq-server", "zypper": "rabbitmq-server", "pacman": "rabbitmq"},
			baseUnits:    map[string][]string{"apt": {"rabbitmq-server"}, "dnf": {"rabbitmq-server"}, "yum": {"rabbitmq-server"}, "zypper": {"rabbitmq-server"}, "pacman": {"rabbitmq"}},
		}, true
	case "docker":
		return servicePackageSpec{
			basePackages: map[string]string{"apt": "docker.io", "dnf": "docker", "yum": "docker", "zypper": "docker", "pacman": "docker"},
			baseUnits:    map[string][]string{"apt": {"docker"}, "dnf": {"docker"}, "yum": {"docker"}, "zypper": {"docker"}, "pacman": {"docker"}},
		}, true
	}
	if matches := phpFPMServicePattern.FindStringSubmatch(name); len(matches) == 2 {
		version := matches[1]
		return servicePackageSpec{
			basePackages: map[string]string{
				"apt":    "php" + version + "-fpm",
				"dnf":    "php-fpm",
				"yum":    "php-fpm",
				"zypper": "php-fpm",
				"pacman": "php-fpm",
			},
			baseUnits: map[string][]string{
				"apt":    {name},
				"dnf":    {name, "php-fpm"},
				"yum":    {name, "php-fpm"},
				"zypper": {name, "php-fpm"},
				"pacman": {name, "php-fpm"},
			},
		}, true
	}
	return servicePackageSpec{}, false
}
