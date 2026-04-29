package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/system"
	"deploycp/internal/utils"
)

var cronSchedulePattern = regexp.MustCompile(`^(@(reboot|yearly|annually|monthly|weekly|daily|midnight|hourly)|[^\s]+\s+[^\s]+\s+[^\s]+\s+[^\s]+\s+[^\s]+)$`)

type CronService struct {
	cfg    *config.Config
	runner *system.Runner
	audit  *AuditService
}

func NewCronService(cfg *config.Config, runner *system.Runner, audit *AuditService) *CronService {
	return &CronService{cfg: cfg, runner: runner, audit: audit}
}

func (s *CronService) UpsertWebsiteJob(ctx context.Context, site *models.Website, siteUser *models.SiteUser, job *models.CronJob, actor *uint, ip string) error {
	if site == nil || job == nil {
		return fmt.Errorf("site and cron job are required")
	}
	schedule := strings.TrimSpace(job.Schedule)
	command := strings.TrimSpace(job.Command)
	if !cronSchedulePattern.MatchString(schedule) {
		return fmt.Errorf("invalid cron schedule")
	}
	if command == "" {
		return fmt.Errorf("cron command is required")
	}
	systemUser := "deploycp"
	if siteUser != nil && strings.TrimSpace(siteUser.Username) != "" {
		systemUser = strings.TrimSpace(siteUser.Username)
	}
	scriptPath := s.jobScriptPath(site.ID, job.ID)
	scriptContent := s.renderJobScript(site, command)
	if err := utils.WriteFileAtomic(scriptPath, []byte(scriptContent), 0o750); err != nil {
		return err
	}
	cronPath := s.jobCronPath(site.ID, job.ID)
	entry := strings.Join([]string{
		"SHELL=/bin/bash",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"MAILTO=\"\"",
		fmt.Sprintf("%s root %s -u %s -- /bin/bash %s", schedule, s.cfg.Paths.RunuserBinary, systemUser, scriptPath),
	}, "\n") + "\n"
	if err := utils.WriteFileAtomic(cronPath, []byte(entry), 0o644); err != nil {
		return err
	}
	if s.cfg.Paths.RunuserBinary != "/bin/echo" {
		_, _ = s.runner.Run(ctx, system.CommandRequest{
			Binary:      "/bin/chmod",
			Args:        []string{"0644", cronPath},
			AuditAction: "cron.file.chmod",
			ActorUserID: actor,
			IP:          ip,
		})
	}
	s.audit.Record(actor, "cron.upsert", "cron_job", fmt.Sprintf("%d", job.ID), ip, map[string]any{"website_id": site.ID, "schedule": schedule})
	return nil
}

func (s *CronService) DeleteJob(_ context.Context, jobID uint, actor *uint, ip string) error {
	matches := []string{
		filepath.Join(s.cfg.Paths.CronDir, fmt.Sprintf("deploycp-website-*-%d.cron", jobID)),
		filepath.Join(s.cfg.Paths.StorageRoot, "generated", "cron", fmt.Sprintf("website-*-%d.sh", jobID)),
	}
	for _, pattern := range matches {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		for _, path := range paths {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	s.audit.Record(actor, "cron.delete", "cron_job", fmt.Sprintf("%d", jobID), ip, nil)
	return nil
}

func (s *CronService) DeleteWebsiteJobs(_ context.Context, websiteID uint, actor *uint, ip string) error {
	patterns := []string{
		filepath.Join(s.cfg.Paths.CronDir, fmt.Sprintf("deploycp-website-%d-*.cron", websiteID)),
		filepath.Join(s.cfg.Paths.StorageRoot, "generated", "cron", fmt.Sprintf("website-%d-*.sh", websiteID)),
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		for _, match := range matches {
			if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	s.audit.Record(actor, "cron.delete.website", "website", fmt.Sprintf("%d", websiteID), ip, nil)
	return nil
}

func (s *CronService) renderJobScript(site *models.Website, command string) string {
	platformHome := platformHomeFromWebRoot(site.RootPath)
	runtimeEnv := filepath.Join(platformHome, ".deploycp", "runtime.env")
	lines := []string{
		"#!/bin/bash",
		"set -euo pipefail",
		fmt.Sprintf("cd %q", platformHome),
		fmt.Sprintf("if [ -f %q ]; then . %q; fi", runtimeEnv, runtimeEnv),
		command,
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *CronService) jobCronPath(websiteID, jobID uint) string {
	return filepath.Join(s.cfg.Paths.CronDir, fmt.Sprintf("deploycp-website-%d-%d.cron", websiteID, jobID))
}

func (s *CronService) jobScriptPath(websiteID, jobID uint) string {
	return filepath.Join(s.cfg.Paths.StorageRoot, "generated", "cron", fmt.Sprintf("website-%d-%d.sh", websiteID, jobID))
}
