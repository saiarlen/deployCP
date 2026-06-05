package services

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
)

const portInfoMaxWalkEntries = 50000

type PortInfoService struct {
	cfg     *config.Config
	apps    *repositories.GoAppRepository
	adapter platform.Adapter
}

type PortInfoView struct {
	Rows         []PortInfoRow
	Check        PortAvailability
	RunningCount int
	StoppedCount int
}

type PortInfoRow struct {
	Name           string
	Runtime        string
	ProcessManager string
	Host           string
	Port           int
	ServiceName    string
	Status         string
	Running        bool
	PID            int32
	CPU            string
	Memory         string
	Disk           string
	WorkingDir     string
}

type PortAvailability struct {
	Checked   bool
	Query     string
	Port      int
	Available bool
	Message   string
}

func NewPortInfoService(cfg *config.Config, apps *repositories.GoAppRepository, adapter platform.Adapter) *PortInfoService {
	return &PortInfoService{cfg: cfg, apps: apps, adapter: adapter}
}

func (s *PortInfoService) View(ctx context.Context, portQuery string) PortInfoView {
	apps, err := s.apps.List()
	if err != nil {
		return PortInfoView{Check: s.checkPort(ctx, portQuery, nil)}
	}
	sort.SliceStable(apps, func(i, j int) bool {
		if apps[i].Port == apps[j].Port {
			return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
		}
		return apps[i].Port < apps[j].Port
	})
	rows := make([]PortInfoRow, 0, len(apps))
	runningCount := 0
	for _, app := range apps {
		row := s.row(ctx, app)
		if row.Running {
			runningCount++
		}
		rows = append(rows, row)
	}
	return PortInfoView{
		Rows:         rows,
		Check:        s.checkPort(ctx, portQuery, apps),
		RunningCount: runningCount,
		StoppedCount: len(rows) - runningCount,
	}
}

func (s *PortInfoService) row(ctx context.Context, app models.GoApp) PortInfoRow {
	row := PortInfoRow{
		Name:           strings.TrimSpace(app.Name),
		Runtime:        strings.TrimSpace(app.Runtime),
		ProcessManager: strings.TrimSpace(app.ProcessManager),
		Host:           strings.TrimSpace(app.Host),
		Port:           app.Port,
		ServiceName:    strings.TrimSpace(app.ServiceName),
		WorkingDir:     strings.TrimSpace(app.WorkingDirectory),
		Status:         "unknown",
		CPU:            "-",
		Memory:         "-",
		Disk:           "-",
	}
	if row.ProcessManager == "" {
		row.ProcessManager = "systemd"
	}
	if row.Host == "" {
		row.Host = "127.0.0.1"
	}
	if row.Name == "" {
		row.Name = row.ServiceName
	}
	if s.adapter != nil && row.ServiceName != "" {
		if status, err := s.adapter.Services().Status(ctx, row.ServiceName); err == nil {
			row.Running = status.Active
			if status.Active {
				row.Status = "running"
			} else if strings.TrimSpace(status.RawOutput) != "" {
				row.Status = strings.TrimSpace(status.RawOutput)
			} else {
				row.Status = "stopped"
			}
		}
	}
	if pid := s.serviceMainPID(ctx, row.ServiceName); pid > 0 {
		row.PID = pid
		cpu, rss := processTreeUsage(ctx, pid)
		row.CPU = fmt.Sprintf("%.1f%%", cpu)
		row.Memory = humanSize(rss)
	} else if !row.Running {
		row.CPU = "0.0%"
		row.Memory = "0 B"
	}
	if row.WorkingDir != "" {
		size, capped, err := dirSize(row.WorkingDir, portInfoMaxWalkEntries)
		if err == nil {
			row.Disk = humanSize(size)
			if capped {
				row.Disk = ">" + row.Disk
			}
		}
	}
	return row
}

func (s *PortInfoService) checkPort(_ context.Context, query string, apps []models.GoApp) PortAvailability {
	query = strings.TrimSpace(query)
	if query == "" {
		return PortAvailability{}
	}
	check := PortAvailability{Checked: true, Query: query}
	port, err := strconv.Atoi(query)
	if err != nil || port < 1 || port > 65535 {
		check.Message = "Enter a valid TCP port between 1 and 65535."
		return check
	}
	check.Port = port
	for _, app := range apps {
		if app.Port == port {
			check.Message = fmt.Sprintf("Port %d is already assigned to platform %s.", port, strings.TrimSpace(app.Name))
			return check
		}
	}
	if err := canBindPort("127.0.0.1", port); err != nil {
		check.Message = fmt.Sprintf("Port %d is occupied on this server.", port)
		return check
	}
	check.Available = true
	check.Message = fmt.Sprintf("Port %d is available for a platform runtime.", port)
	return check
}

func canBindPort(host string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return ln.Close()
}

func (s *PortInfoService) serviceMainPID(ctx context.Context, serviceName string) int32 {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" || strings.ContainsAny(serviceName, `/\`) {
		return 0
	}
	binary := "systemctl"
	if s.cfg != nil && strings.TrimSpace(s.cfg.Paths.SystemctlBinary) != "" {
		binary = strings.TrimSpace(s.cfg.Paths.SystemctlBinary)
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, binary, "show", serviceName, "--property=MainPID").CombinedOutput()
	if err != nil {
		return 0
	}
	line := strings.TrimSpace(string(out))
	line = strings.TrimPrefix(line, "MainPID=")
	pid, err := strconv.ParseInt(strings.TrimSpace(line), 10, 32)
	if err != nil || pid <= 0 {
		return 0
	}
	return int32(pid)
}

func processTreeUsage(ctx context.Context, pid int32) (float64, uint64) {
	root, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return 0, 0
	}
	seen := map[int32]struct{}{}
	procs := collectProcesses(ctx, root, seen)
	totalCPU := 0.0
	totalRSS := uint64(0)
	for _, proc := range procs {
		totalCPU += processAverageCPU(ctx, proc)
		if info, err := proc.MemoryInfoWithContext(ctx); err == nil && info != nil {
			totalRSS += info.RSS
		}
	}
	return totalCPU, totalRSS
}

func collectProcesses(ctx context.Context, proc *process.Process, seen map[int32]struct{}) []*process.Process {
	if proc == nil {
		return nil
	}
	if _, ok := seen[proc.Pid]; ok {
		return nil
	}
	seen[proc.Pid] = struct{}{}
	out := []*process.Process{proc}
	children, err := proc.ChildrenWithContext(ctx)
	if err != nil {
		return out
	}
	for _, child := range children {
		out = append(out, collectProcesses(ctx, child, seen)...)
	}
	return out
}

func processAverageCPU(ctx context.Context, proc *process.Process) float64 {
	times, err := proc.TimesWithContext(ctx)
	if err != nil || times == nil {
		return 0
	}
	startMS, err := proc.CreateTimeWithContext(ctx)
	if err != nil || startMS <= 0 {
		return 0
	}
	elapsed := time.Since(time.UnixMilli(startMS)).Seconds()
	if elapsed <= 0 {
		return 0
	}
	numCPU := runtime.NumCPU()
	if numCPU <= 0 {
		numCPU = 1
	}
	return ((times.User + times.System) / elapsed) / float64(numCPU) * 100
}

func dirSize(root string, maxEntries int) (uint64, bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, false, fmt.Errorf("empty path")
	}
	entries := 0
	total := uint64(0)
	capped := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if maxEntries > 0 && entries > maxEntries {
			capped = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode().IsRegular() && info.Size() > 0 {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, capped, err
}

func humanSize(v uint64) string {
	if v == 0 {
		return "0 B"
	}
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := uint64(unit), 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(v)/float64(div), "KMGTPE"[exp])
}
