package services

import (
	"context"
	"fmt"
	"strings"

	"deploycp/internal/repositories"
)

type ReconcileResult struct {
	Steps []string
}

type ReconcileService struct {
	repos    *repositories.Repositories
	websites *WebsiteService
	apps     *AppService
	firewall *FirewallService
	cron     *CronService
	ftp      *FTPService
	varnish  *VarnishService
	database *DatabaseService
}

func NewReconcileService(
	repos *repositories.Repositories,
	websites *WebsiteService,
	apps *AppService,
	firewall *FirewallService,
	cron *CronService,
	ftp *FTPService,
	varnish *VarnishService,
	database *DatabaseService,
) *ReconcileService {
	return &ReconcileService{
		repos:    repos,
		websites: websites,
		apps:     apps,
		firewall: firewall,
		cron:     cron,
		ftp:      ftp,
		varnish:  varnish,
		database: database,
	}
}

func (s *ReconcileService) Run(ctx context.Context, actor *uint, ip string) (*ReconcileResult, error) {
	result := &ReconcileResult{}
	add := func(msg string) { result.Steps = append(result.Steps, msg) }

	if s.ftp != nil {
		if err := s.ftp.ReconcileUsers(ctx, actor, ip); err != nil {
			return result, err
		}
		add("reconciled ftp users and proftpd config")
	}

	if s.database != nil {
		if err := s.database.ReconcileManagedRedis(ctx, actor, ip); err != nil {
			return result, err
		}
		add("reconciled managed redis instances")
	}

	if s.repos != nil && s.firewall != nil {
		rules, err := s.repos.Firewalls.List()
		if err != nil {
			return result, err
		}
		for i := range rules {
			_ = s.firewall.DeleteRule(ctx, &rules[i], actor, ip)
			if !rules[i].Enabled {
				continue
			}
			if err := s.firewall.ApplyRule(ctx, &rules[i], actor, ip); err != nil {
				return result, err
			}
		}
		add(fmt.Sprintf("reconciled %d firewall rules", len(rules)))
	}

	websites, err := s.repos.Websites.List()
	if err != nil {
		return result, err
	}
	for i := range websites {
		site := websites[i]
		if s.websites != nil {
			if err := s.websites.RefreshConfig(ctx, site.ID); err != nil {
				if isBusyUsermodError(err) {
					add(fmt.Sprintf("skipped website %q user sync because the site user has running processes", site.Name))
					continue
				}
				return result, err
			}
		}
		if s.varnish != nil && s.repos.Varnish != nil {
			if cfg, err := s.repos.Varnish.FindByWebsite(site.ID); err == nil && cfg != nil {
				if err := s.varnish.ApplyWebsiteConfig(ctx, &site, cfg, actor, ip); err != nil {
					return result, err
				}
			}
		}
		if s.cron != nil && s.repos.CronJobs != nil {
			jobs, err := s.repos.CronJobs.ListByWebsite(site.ID)
			if err != nil {
				return result, err
			}
			siteUser := site.SiteUser
			for j := range jobs {
				if err := s.cron.UpsertWebsiteJob(ctx, &site, siteUser, &jobs[j], actor, ip); err != nil {
					return result, err
				}
			}
		}
	}
	add(fmt.Sprintf("reconciled %d websites", len(websites)))

	apps, err := s.repos.GoApps.List()
	if err != nil {
		return result, err
	}
	for i := range apps {
		if s.apps != nil {
			if err := s.apps.Reconcile(ctx, apps[i].ID, actor, ip); err != nil {
				return result, err
			}
		}
	}
	add(fmt.Sprintf("reconciled %d applications", len(apps)))

	return result, nil
}

func isBusyUsermodError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "usermod: user ") && strings.Contains(msg, " is currently used by process")
}
