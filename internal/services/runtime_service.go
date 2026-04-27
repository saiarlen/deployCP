package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/system"
	"deploycp/internal/utils"
)

type RuntimeService struct {
	cfg    *config.Config
	runner *system.Runner
	audit  *AuditService
}

func NewRuntimeService(cfg *config.Config, runner *system.Runner, audit *AuditService) *RuntimeService {
	return &RuntimeService{cfg: cfg, runner: runner, audit: audit}
}

func (s *RuntimeService) InstallVersion(ctx context.Context, runtime, version string, actor *uint, ip string) error {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if runtime == "" || version == "" {
		return fmt.Errorf("runtime and version are required")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return s.ensureRuntimeBinDir(runtime, version)
	}
	script, err := s.helperScriptPath()
	if err != nil {
		return err
	}
	if _, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      "/bin/bash",
		Args:        []string{script, "install", runtime, version, s.cfg.Paths.RuntimeRoot},
		Timeout:     15 * time.Minute,
		AuditAction: "runtime.install",
		ActorUserID: actor,
		IP:          ip,
	}); err != nil {
		return err
	}
	s.audit.Record(actor, "runtime.install", "runtime_version", runtime+":"+version, ip, nil)
	return nil
}

func (s *RuntimeService) RemoveVersion(ctx context.Context, runtime, version string, actor *uint, ip string) error {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if runtime == "" || version == "" {
		return fmt.Errorf("runtime and version are required")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return os.RemoveAll(s.runtimeVersionDir(runtime, version))
	}
	script, err := s.helperScriptPath()
	if err != nil {
		return err
	}
	if _, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      "/bin/bash",
		Args:        []string{script, "remove", runtime, version, s.cfg.Paths.RuntimeRoot},
		Timeout:     10 * time.Minute,
		AuditAction: "runtime.remove",
		ActorUserID: actor,
		IP:          ip,
	}); err != nil {
		return err
	}
	s.audit.Record(actor, "runtime.remove", "runtime_version", runtime+":"+version, ip, nil)
	return nil
}

func (s *RuntimeService) ApplyPlatformRuntime(rootPath, runtime, version string, actor *uint, ip string) error {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if rootPath == "" || rootPath == "." {
		return nil
	}
	runtimeEnvPath := filepath.Join(rootPath, ".deploycp", "runtime.env")
	if runtime == "" || version == "" {
		if err := os.Remove(runtimeEnvPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	binDir := filepath.Join(s.runtimeVersionDir(runtime, version), "bin")
	lines := []string{
		"# DeployCP runtime selection",
		fmt.Sprintf("export DEPLOYCP_RUNTIME=%q", runtime),
		fmt.Sprintf("export DEPLOYCP_RUNTIME_VERSION=%q", version),
	}
	if runtime == "go" {
		lines = append(lines, fmt.Sprintf("export GOROOT=%q", s.runtimeVersionDir(runtime, version)))
	}
	lines = append(lines, fmt.Sprintf("export PATH=%q:$PATH", binDir))
	if err := utils.WriteFileAtomic(runtimeEnvPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	s.audit.Record(actor, "runtime.apply", "runtime_env", rootPath, ip, map[string]string{"runtime": runtime, "version": version})
	return nil
}

func (s *RuntimeService) ResolveBinary(runtime, version, requestedBinary string) (string, error) {
	binary := strings.TrimSpace(requestedBinary)
	if binary == "" {
		return "", fmt.Errorf("binary path is required")
	}
	version = strings.TrimSpace(version)
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	if version != "" {
		if preferred := s.preferredRuntimeBinary(runtime, binary); preferred != "" {
			candidate := filepath.Join(s.runtimeVersionDir(runtime, version), "bin", preferred)
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				return candidate, nil
			}
		}
	}
	if filepath.IsAbs(binary) {
		return binary, nil
	}
	if version != "" {
		candidate := filepath.Join(s.runtimeVersionDir(runtime, version), "bin", filepath.Base(binary))
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	lookedUp, err := exec.LookPath(binary)
	if err == nil {
		return lookedUp, nil
	}
	return binary, nil
}

func (s *RuntimeService) preferredRuntimeBinary(runtime, requestedBinary string) string {
	switch strings.TrimSpace(requestedBinary) {
	case "env", "/usr/bin/env", "/bin/env":
		switch runtime {
		case "node":
			return "node"
		case "python":
			return "python3"
		case "php":
			return "php"
		}
	}
	return ""
}

func (s *RuntimeService) MergeRuntimeEnv(runtime, version string, env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+4)
	for k, v := range env {
		out[k] = v
	}
	version = strings.TrimSpace(version)
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	if runtime == "" || version == "" {
		return out
	}
	binDir := filepath.Join(s.runtimeVersionDir(runtime, version), "bin")
	if existing := strings.TrimSpace(out["PATH"]); existing != "" {
		out["PATH"] = binDir + ":" + existing
	} else {
		out["PATH"] = binDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	out["DEPLOYCP_RUNTIME"] = runtime
	out["DEPLOYCP_RUNTIME_VERSION"] = version
	if runtime == "go" {
		out["GOROOT"] = s.runtimeVersionDir(runtime, version)
	}
	return out
}

func (s *RuntimeService) helperScriptPath() (string, error) {
	candidates := []string{
		filepath.Join(".", "scripts", "linux", "runtime-manager.sh"),
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "scripts", "linux", "runtime-manager.sh"))
	}
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		if st, err := os.Stat(clean); err == nil && !st.IsDir() {
			return clean, nil
		}
	}
	return "", fmt.Errorf("runtime helper script not found")
}

func (s *RuntimeService) ensureRuntimeBinDir(runtime, version string) error {
	return os.MkdirAll(filepath.Join(s.runtimeVersionDir(runtime, version), "bin"), 0o755)
}

func (s *RuntimeService) runtimeVersionDir(runtime, version string) string {
	return filepath.Join(s.cfg.Paths.RuntimeRoot, strings.ToLower(strings.TrimSpace(runtime)), strings.TrimSpace(version))
}
