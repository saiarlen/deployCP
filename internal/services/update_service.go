package services

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/repositories"
)

const (
	updateInstalledVersionKey = "deploycp_update_installed_version"
	updateLatestVersionKey    = "deploycp_update_latest_version"
	updateLatestURLKey        = "deploycp_update_latest_url"
	updateCheckedAtKey        = "deploycp_update_checked_at"
	updateAvailableKey        = "deploycp_update_available"
)

type UpdateService struct {
	cfg      *config.Config
	settings *repositories.SettingRepository
	audit    *AuditService
	checkMu  sync.Mutex
}

type UpdateView struct {
	CurrentVersion  string `json:"current_version"`
	DisplayVersion  string `json:"display_version"`
	LatestVersion   string `json:"latest_version"`
	LatestURL       string `json:"latest_url"`
	UpdateAvailable bool   `json:"update_available"`
	IsDev           bool   `json:"is_dev"`
	LastCheckedAt   string `json:"last_checked_at"`
	CheckEnabled    bool   `json:"check_enabled"`
	State           string `json:"state"`
	Message         string `json:"message"`
	TargetVersion   string `json:"target_version"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
	LogPath         string `json:"log_path"`
	Log             string `json:"log"`
	UnitName        string `json:"unit_name"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

type updateJobState struct {
	State         string
	Message       string
	Current       string
	Target        string
	StartedAt     string
	FinishedAt    string
	LogPath       string
	UnitName      string
	LatestVersion string
}

func NewUpdateService(cfg *config.Config, settings *repositories.SettingRepository, audit *AuditService) *UpdateService {
	return &UpdateService{cfg: cfg, settings: settings, audit: audit}
}

func (s *UpdateService) Start() {
	s.syncInstalledVersion()
}

func (s *UpdateService) CheckEnabled() bool {
	return !s.isDevVersion() && strings.TrimSpace(s.cfg.App.ReleaseRepo) != ""
}

func (s *UpdateService) CurrentVersion() string {
	return strings.TrimSpace(s.cfg.App.Version)
}

func (s *UpdateService) DisplayVersion() string {
	if s.isDevVersion() {
		return "dev"
	}
	if v := s.CurrentVersion(); v != "" {
		return v
	}
	return "dev"
}

func (s *UpdateService) FooterView() UpdateView {
	return s.readView(false)
}

func (s *UpdateService) FullView() UpdateView {
	return s.readView(true)
}

func (s *UpdateService) CheckNow(ctx context.Context) error {
	if !s.CheckEnabled() {
		return nil
	}
	s.checkMu.Lock()
	defer s.checkMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", s.cfg.App.ReleaseRepo), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "deploycp/"+s.DisplayVersion())

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("github latest release check failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return err
	}
	latest := strings.TrimSpace(release.TagName)
	current := strings.TrimSpace(s.CurrentVersion())
	available := latest != "" && current != "" && normalizeVersion(latest) != normalizeVersion(current)

	_ = s.settings.Set(updateInstalledVersionKey, current, false)
	_ = s.settings.Set(updateLatestVersionKey, latest, false)
	_ = s.settings.Set(updateLatestURLKey, strings.TrimSpace(release.HTMLURL), false)
	_ = s.settings.Set(updateCheckedAtKey, time.Now().UTC().Format(time.RFC3339), false)
	_ = s.settings.Set(updateAvailableKey, boolString(available), false)
	return nil
}

func (s *UpdateService) StartInstall(actor *uint, ip string) error {
	if !s.CheckEnabled() {
		return fmt.Errorf("updates are unavailable in development mode")
	}
	view := s.FullView()
	if view.State == "running" {
		return fmt.Errorf("an update is already running")
	}
	target := strings.TrimSpace(view.LatestVersion)
	if target == "" {
		if err := s.CheckNow(context.Background()); err != nil {
			return err
		}
		view = s.FullView()
		target = strings.TrimSpace(view.LatestVersion)
	}
	if target == "" {
		return fmt.Errorf("unable to resolve latest release version")
	}
	if normalizeVersion(target) == normalizeVersion(s.CurrentVersion()) {
		return fmt.Errorf("already on the latest version")
	}

	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return fmt.Errorf("systemd-run not available on this host")
	}
	coreDir := s.coreDir()
	scriptPath := filepath.Join(coreDir, "scripts", "linux", "self-update.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("update script not found at %s", scriptPath)
	}
	unitName := fmt.Sprintf("deploycp-self-update-%d", time.Now().Unix())
	statusPath := s.statusFilePath()
	logPath := s.logFilePath()
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(logPath, []byte{}, 0o644); err != nil {
		return err
	}
	state := updateJobState{
		State:         "queued",
		Message:       "Waiting for updater to start",
		Current:       s.CurrentVersion(),
		Target:        target,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
		LogPath:       logPath,
		UnitName:      unitName,
		LatestVersion: target,
	}
	if err := s.writeJobState(state); err != nil {
		return err
	}

	cmd := exec.Command(
		systemdRun,
		"--unit", unitName,
		"--property=Type=oneshot",
		"--collect",
		"/bin/bash",
		scriptPath,
		statusPath,
		logPath,
		s.CurrentVersion(),
		target,
		s.cfg.App.ReleaseRepo,
		coreDir,
		unitName,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		state.State = "failed"
		state.Message = "Failed to launch updater"
		state.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = s.writeJobState(state)
		return fmt.Errorf("failed to launch updater: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if s.audit != nil {
		s.audit.Record(actor, "system.update.start", "deploycp_update", target, ip, map[string]any{"current_version": s.CurrentVersion(), "unit": unitName})
	}
	return nil
}

func (s *UpdateService) readView(includeLog bool) UpdateView {
	current := s.CurrentVersion()
	latest := s.getSetting(updateLatestVersionKey)
	latestURL := s.getSetting(updateLatestURLKey)
	checkedAt := s.getSetting(updateCheckedAtKey)
	available := s.CheckEnabled() && current != "" && latest != "" && normalizeVersion(current) != normalizeVersion(latest)
	state := s.readJobState()

	view := UpdateView{
		CurrentVersion:  current,
		DisplayVersion:  s.DisplayVersion(),
		LatestVersion:   latest,
		LatestURL:       latestURL,
		UpdateAvailable: available,
		IsDev:           s.isDevVersion(),
		LastCheckedAt:   checkedAt,
		CheckEnabled:    s.CheckEnabled(),
		State:           state.State,
		Message:         state.Message,
		TargetVersion:   state.Target,
		StartedAt:       state.StartedAt,
		FinishedAt:      state.FinishedAt,
		LogPath:         state.LogPath,
		UnitName:        state.UnitName,
	}
	if includeLog {
		view.Log = s.readLogTail(65536)
	}
	if view.State == "" {
		view.State = "idle"
	}
	return view
}

func (s *UpdateService) syncInstalledVersion() {
	_ = s.settings.Set(updateInstalledVersionKey, s.CurrentVersion(), false)
}

func (s *UpdateService) isDevVersion() bool {
	if strings.EqualFold(strings.TrimSpace(s.cfg.Features.PlatformMode), "dryrun") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(s.cfg.App.Env), "development") {
		return true
	}
	v := strings.TrimSpace(s.cfg.App.Version)
	return v == "" || strings.EqualFold(v, "dev")
}

func (s *UpdateService) coreDir() string {
	storageRoot := filepath.Clean(strings.TrimSpace(s.cfg.Paths.StorageRoot))
	if filepath.Base(storageRoot) == "storage" {
		return filepath.Dir(storageRoot)
	}
	return filepath.Dir(filepath.Dir(s.cfg.Database.SQLitePath))
}

func (s *UpdateService) statusFilePath() string {
	return filepath.Join(s.cfg.Paths.StorageRoot, "logs", "self-update.status")
}

func (s *UpdateService) logFilePath() string {
	return filepath.Join(s.cfg.Paths.StorageRoot, "logs", "self-update.log")
}

func (s *UpdateService) getSetting(key string) string {
	if s.settings == nil {
		return ""
	}
	value, err := s.settings.Get(key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *UpdateService) readJobState() updateJobState {
	path := s.statusFilePath()
	file, err := os.Open(path)
	if err != nil {
		return updateJobState{}
	}
	defer file.Close()

	state := updateJobState{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "STATE":
			state.State = strings.TrimSpace(value)
		case "MESSAGE":
			state.Message = strings.TrimSpace(value)
		case "CURRENT_VERSION":
			state.Current = strings.TrimSpace(value)
		case "TARGET_VERSION":
			state.Target = strings.TrimSpace(value)
		case "STARTED_AT":
			state.StartedAt = strings.TrimSpace(value)
		case "FINISHED_AT":
			state.FinishedAt = strings.TrimSpace(value)
		case "LOG_PATH":
			state.LogPath = strings.TrimSpace(value)
		case "UNIT_NAME":
			state.UnitName = strings.TrimSpace(value)
		case "LATEST_VERSION":
			state.LatestVersion = strings.TrimSpace(value)
		}
	}
	if state.LogPath == "" {
		state.LogPath = s.logFilePath()
	}
	return state
}

func (s *UpdateService) writeJobState(state updateJobState) error {
	content := strings.Join([]string{
		"STATE=" + strings.TrimSpace(state.State),
		"MESSAGE=" + strings.TrimSpace(state.Message),
		"CURRENT_VERSION=" + strings.TrimSpace(state.Current),
		"TARGET_VERSION=" + strings.TrimSpace(state.Target),
		"STARTED_AT=" + strings.TrimSpace(state.StartedAt),
		"FINISHED_AT=" + strings.TrimSpace(state.FinishedAt),
		"LOG_PATH=" + strings.TrimSpace(state.LogPath),
		"UNIT_NAME=" + strings.TrimSpace(state.UnitName),
		"LATEST_VERSION=" + strings.TrimSpace(state.LatestVersion),
		"",
	}, "\n")
	return os.WriteFile(s.statusFilePath(), []byte(content), 0o644)
}

func (s *UpdateService) readLogTail(maxBytes int64) string {
	path := s.logFilePath()
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	offset := int64(0)
	if size > maxBytes {
		offset = size - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	return string(data)
}

func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "v")
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
