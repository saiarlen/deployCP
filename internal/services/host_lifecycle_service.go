package services

import (
	"context"
	"fmt"
	"strings"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
)

type HostLifecycleResult struct {
	Steps []string
}

type HostLifecycleService struct {
	cfg       *config.Config
	repos     *repositories.Repositories
	platform  platform.Adapter
	websites  *WebsiteService
	apps      *AppService
	siteUsers *SiteUserService
	database  *DatabaseService
	firewall  *FirewallService
	ftp       *FTPService
	ssl       *SSLService
}

func NewHostLifecycleService(
	cfg *config.Config,
	repos *repositories.Repositories,
	platform platform.Adapter,
	websites *WebsiteService,
	apps *AppService,
	siteUsers *SiteUserService,
	database *DatabaseService,
	firewall *FirewallService,
	ftp *FTPService,
	ssl *SSLService,
) *HostLifecycleService {
	return &HostLifecycleService{
		cfg:       cfg,
		repos:     repos,
		platform:  platform,
		websites:  websites,
		apps:      apps,
		siteUsers: siteUsers,
		database:  database,
		firewall:  firewall,
		ftp:       ftp,
		ssl:       ssl,
	}
}

func (s *HostLifecycleService) Bootstrap(ctx context.Context, actor *uint, ip string) (*HostLifecycleResult, error) {
	result := &HostLifecycleResult{}
	add := func(msg string) { result.Steps = append(result.Steps, msg) }

	add("database migrations and bootstrap admin were applied during startup")
	if s.platform != nil && strings.TrimSpace(s.cfg.Paths.RestrictedShellPath) != "" {
		if err := s.platform.Users().EnsureRestrictedShell(ctx, s.cfg.Paths.RestrictedShellPath); err != nil {
			return result, err
		}
		add("restricted shell prepared")
	}
	if s.ftp != nil {
		if err := s.ftp.ReconcileConfig(ctx, actor, ip); err != nil {
			return result, err
		}
		add("ftp server config reconciled")
	}
	return result, nil
}

func (s *HostLifecycleService) TeardownManaged(ctx context.Context, actor *uint, ip string) (*HostLifecycleResult, error) {
	result := &HostLifecycleResult{}
	add := func(msg string) { result.Steps = append(result.Steps, msg) }

	if s.repos != nil && s.firewall != nil {
		rules, err := s.repos.Firewalls.List()
		if err != nil {
			return result, err
		}
		for i := range rules {
			_ = s.firewall.DeleteRule(ctx, &rules[i], actor, ip)
			if err := s.repos.Firewalls.Delete(rules[i].ID); err != nil {
				return result, err
			}
		}
		add(fmt.Sprintf("removed %d managed firewall rules", len(rules)))
	}

	if s.websites != nil {
		items, err := s.websites.List()
		if err != nil {
			return result, err
		}
		for i := range items {
			if err := s.websites.Delete(ctx, items[i].ID, actor, ip); err != nil {
				return result, err
			}
		}
		add(fmt.Sprintf("deleted %d managed websites/platforms", len(items)))
	}

	if s.apps != nil {
		items, err := s.apps.List()
		if err != nil {
			return result, err
		}
		for i := range items {
			if err := s.apps.Delete(ctx, items[i].ID, actor, ip); err != nil {
				return result, err
			}
		}
		add(fmt.Sprintf("deleted %d remaining applications", len(items)))
	}

	if s.siteUsers != nil {
		items, err := s.siteUsers.List()
		if err != nil {
			return result, err
		}
		for i := range items {
			if err := s.siteUsers.Delete(ctx, items[i].ID, actor, ip); err != nil {
				return result, err
			}
		}
		add(fmt.Sprintf("deleted %d remaining site users", len(items)))
	}

	if s.database != nil && s.repos != nil {
		dbItems, err := s.repos.Databases.List()
		if err != nil {
			return result, err
		}
		for i := range dbItems {
			if err := s.database.DeleteDatabaseRecord(&dbItems[i], actor, ip); err != nil {
				return result, err
			}
		}
		add(fmt.Sprintf("deleted %d remaining managed database connections", len(dbItems)))

		redisItems, err := s.repos.Redis.List()
		if err != nil {
			return result, err
		}
		for i := range redisItems {
			if err := s.database.DeleteRedisRecord(&redisItems[i], actor, ip); err != nil {
				return result, err
			}
		}
		add(fmt.Sprintf("deleted %d remaining managed redis connections", len(redisItems)))
	}

	if s.ssl != nil {
		items, err := s.ssl.List()
		if err != nil {
			return result, err
		}
		for i := range items {
			if err := s.ssl.Delete(items[i].ID, actor, ip); err != nil {
				return result, err
			}
		}
		add(fmt.Sprintf("deleted %d remaining ssl certificates", len(items)))
	}

	if s.repos != nil && s.platform != nil {
		services, err := s.repos.Services.List()
		if err != nil {
			return result, err
		}
		removed := 0
		for i := range services {
			if !isDeployCPManagedService(services[i]) {
				continue
			}
			_ = s.platform.Services().Stop(ctx, services[i].Name)
			_ = s.platform.Services().Disable(ctx, services[i].Name)
			if err := removeServiceUnitFile(s.cfg, s.platform.Name(), services[i].Name, services[i].UnitPath); err != nil {
				return result, err
			}
			if err := s.repos.Services.Delete(services[i].ID); err != nil {
				return result, err
			}
			removed++
		}
		add(fmt.Sprintf("removed %d managed service unit records", removed))
	}

	return result, nil
}

func isDeployCPManagedService(item models.ManagedService) bool {
	name := strings.ToLower(strings.TrimSpace(item.Name))
	return strings.HasPrefix(name, "deploycp-app-") || strings.HasPrefix(name, "deploycp-redis-")
}
