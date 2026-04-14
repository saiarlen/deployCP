package services

import (
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"

	"deploycp/internal/models"
	"deploycp/internal/repositories"
)

type DashboardMetrics struct {
	WebsitesCount   int64
	AppsCount       int64
	ServicesCount   int64
	DatabasesCount  int64
	RedisCount      int64
	SiteUsersCount  int64
	OperatingSystem string
	Hostname        string
	PublicIPv4      string
	Size            string
	SizeCPU         string
	SizeRAM         string
	SizeDisk        string
	DiskTotal       string
	DiskAvailable   string
	CPUUsage        float64
	MemoryUsedPct   float64
	DiskUsedPct     float64
	Load1           float64
	Uptime          string
}

type DashboardLiveMetrics struct {
	Timestamp    int64     `json:"timestamp"`
	CPUUsage     float64   `json:"cpu_usage"`
	CPUCoreUsage []float64 `json:"cpu_core_usage"`
	MemoryUsed   float64   `json:"memory_used"`
	DiskUsed     float64   `json:"disk_used"`
	Load1        float64   `json:"load_1"`
	Load5        float64   `json:"load_5"`
	Load15       float64   `json:"load_15"`
	NetworkRxBps float64   `json:"network_rx_bps"`
	NetworkTxBps float64   `json:"network_tx_bps"`
}

type DashboardHistoryPoint struct {
	Timestamp    int64   `json:"timestamp"`
	CPUUsage     float64 `json:"cpu_usage"`
	MemoryUsed   float64 `json:"memory_used"`
	DiskUsed     float64 `json:"disk_used"`
	Load1        float64 `json:"load_1"`
	Load5        float64 `json:"load_5"`
	Load15       float64 `json:"load_15"`
	NetworkRxBps float64 `json:"network_rx_bps"`
	NetworkTxBps float64 `json:"network_tx_bps"`
}

type DashboardService struct {
	repos *repositories.Repositories
	once  sync.Once
	mu    sync.Mutex

	lastNetAt time.Time
	lastRx    uint64
	lastTx    uint64

	lastPersistAt time.Time
	lastCleanupAt time.Time

	lastPublicIP        string
	lastPublicIPCheckAt time.Time
}

func NewDashboardService(repos *repositories.Repositories) *DashboardService {
	return &DashboardService{repos: repos}
}

func (s *DashboardService) StartCollector() {
	s.once.Do(func() {
		go s.collectorLoop()
	})
}

func (s *DashboardService) Build() (DashboardMetrics, error) {
	websites, _ := s.repos.Websites.Count()
	apps, _ := s.repos.GoApps.Count()
	svc, _ := s.repos.Services.Count()
	db, _ := s.repos.Databases.Count()
	redis, _ := s.repos.Redis.Count()
	siteUsers, _ := s.repos.SiteUsers.Count()

	cpuUsage := 0.0
	if c, err := cpu.Percent(300*time.Millisecond, false); err == nil && len(c) > 0 {
		cpuUsage = c[0]
	}
	memoryPct := 0.0
	memoryTotal := uint64(0)
	if m, err := mem.VirtualMemory(); err == nil {
		memoryPct = m.UsedPercent
		memoryTotal = m.Total
	}
	diskPct := 0.0
	diskTotal := uint64(0)
	diskFree := uint64(0)
	if d, err := disk.Usage("/"); err == nil {
		diskPct = d.UsedPercent
		diskTotal = d.Total
		diskFree = d.Free
	}
	load1 := 0.0
	if l, err := load.Avg(); err == nil {
		load1 = l.Load1
	}
	uptime := "unknown"
	osName := "unknown"
	hostname := "unknown"
	if h, err := host.Info(); err == nil {
		uptime = humanUptime(h.Uptime)
		hostname = h.Hostname
		osName = stringsTrim(fmt.Sprintf("%s %s", h.Platform, h.PlatformVersion))
	}
	sizeCPU := fmt.Sprintf("%d", runtime.NumCPU())
	sizeRAM := compactHumanBytes(memoryTotal)
	sizeDisk := compactHumanBytes(diskTotal)
	size := strings.Join([]string{
		fmt.Sprintf("%s vCPU", sizeCPU),
		fmt.Sprintf("%s RAM", sizeRAM),
		fmt.Sprintf("%s Disk", sizeDisk),
	}, " / ")
	if sizeCPU == "" || sizeRAM == "" || sizeDisk == "" {
		size = "unknown"
		sizeCPU = "unknown"
		sizeRAM = "unknown"
		sizeDisk = "unknown"
	}

	return DashboardMetrics{
		WebsitesCount:   websites,
		AppsCount:       apps,
		ServicesCount:   svc,
		DatabasesCount:  db,
		RedisCount:      redis,
		SiteUsersCount:  siteUsers,
		OperatingSystem: osName,
		Hostname:        hostname,
		PublicIPv4:      s.resolvePublicIPv4(),
		Size:            size,
		SizeCPU:         sizeCPU,
		SizeRAM:         sizeRAM,
		SizeDisk:        sizeDisk,
		DiskTotal:       humanBytes(diskTotal),
		DiskAvailable:   humanBytes(diskFree),
		CPUUsage:        cpuUsage,
		MemoryUsedPct:   memoryPct,
		DiskUsedPct:     diskPct,
		Load1:           load1,
		Uptime:          uptime,
	}, nil
}

func (s *DashboardService) Live() (DashboardLiveMetrics, error) {
	metrics, err := s.sampleLive(true)
	if err != nil {
		return DashboardLiveMetrics{}, err
	}
	_ = s.persistSnapshot(metrics, false)
	return metrics, nil
}

func (s *DashboardService) History(rangeKey string) ([]DashboardHistoryPoint, error) {
	if s.repos.SystemData == nil {
		return []DashboardHistoryPoint{}, nil
	}
	window, maxPoints := resolveHistoryRange(rangeKey)
	since := time.Now().Add(-window)

	rows, err := s.repos.SystemData.ListSince(since, 0)
	if err != nil {
		return nil, err
	}
	points := downsampleHistory(rows, maxPoints)
	if len(points) == 0 {
		live, liveErr := s.sampleLive(false)
		if liveErr == nil {
			points = append(points, DashboardHistoryPoint{
				Timestamp:    live.Timestamp,
				CPUUsage:     live.CPUUsage,
				MemoryUsed:   live.MemoryUsed,
				DiskUsed:     live.DiskUsed,
				Load1:        live.Load1,
				Load5:        live.Load5,
				Load15:       live.Load15,
				NetworkRxBps: live.NetworkRxBps,
				NetworkTxBps: live.NetworkTxBps,
			})
		}
	}
	return points, nil
}

func (s *DashboardService) collectorLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	initial, err := s.sampleLive(false)
	if err == nil {
		_ = s.persistSnapshot(initial, true)
	}
	_ = s.cleanupSnapshots(false)

	for range ticker.C {
		metrics, mErr := s.sampleLive(false)
		if mErr == nil {
			_ = s.persistSnapshot(metrics, true)
		}
		_ = s.cleanupSnapshots(false)
	}
}

func (s *DashboardService) sampleLive(withCore bool) (DashboardLiveMetrics, error) {
	now := time.Now()
	perCore := []float64{}
	total := 0.0
	if withCore {
		if values, err := cpu.Percent(200*time.Millisecond, true); err == nil {
			perCore = values
			total = avg(values)
		}
	} else if values, err := cpu.Percent(200*time.Millisecond, false); err == nil && len(values) > 0 {
		total = values[0]
	}
	memoryPct := 0.0
	if m, err := mem.VirtualMemory(); err == nil {
		memoryPct = m.UsedPercent
	}
	diskPct := 0.0
	if d, err := disk.Usage("/"); err == nil {
		diskPct = d.UsedPercent
	}
	load1 := 0.0
	load5 := 0.0
	load15 := 0.0
	if l, err := load.Avg(); err == nil {
		load1 = l.Load1
		load5 = l.Load5
		load15 = l.Load15
	}

	rxRate := 0.0
	txRate := 0.0
	if counters, err := safeIOCounters(); err == nil && len(counters) > 0 {
		curr := counters[0]
		s.mu.Lock()
		if !s.lastNetAt.IsZero() {
			seconds := now.Sub(s.lastNetAt).Seconds()
			if seconds > 0 {
				if curr.BytesRecv >= s.lastRx {
					rxRate = float64(curr.BytesRecv-s.lastRx) / seconds
				}
				if curr.BytesSent >= s.lastTx {
					txRate = float64(curr.BytesSent-s.lastTx) / seconds
				}
			}
		}
		s.lastNetAt = now
		s.lastRx = curr.BytesRecv
		s.lastTx = curr.BytesSent
		s.mu.Unlock()
	}

	return DashboardLiveMetrics{
		Timestamp:    now.Unix(),
		CPUUsage:     total,
		CPUCoreUsage: perCore,
		MemoryUsed:   memoryPct,
		DiskUsed:     diskPct,
		Load1:        load1,
		Load5:        load5,
		Load15:       load15,
		NetworkRxBps: rxRate,
		NetworkTxBps: txRate,
	}, nil
}

func (s *DashboardService) persistSnapshot(metrics DashboardLiveMetrics, force bool) error {
	if s.repos.SystemData == nil {
		return nil
	}
	now := time.Now()
	s.mu.Lock()
	if !force && !s.lastPersistAt.IsZero() && now.Sub(s.lastPersistAt) < time.Minute {
		s.mu.Unlock()
		return nil
	}
	s.lastPersistAt = now
	s.mu.Unlock()

	return s.repos.SystemData.Create(&models.SystemMetricSnapshot{
		CPUUsage:     metrics.CPUUsage,
		MemoryUsed:   metrics.MemoryUsed,
		DiskUsed:     metrics.DiskUsed,
		Load1:        metrics.Load1,
		Load5:        metrics.Load5,
		Load15:       metrics.Load15,
		NetworkRxBps: metrics.NetworkRxBps,
		NetworkTxBps: metrics.NetworkTxBps,
		CreatedAt:    time.Now(),
	})
}

func (s *DashboardService) cleanupSnapshots(force bool) error {
	if s.repos.SystemData == nil {
		return nil
	}
	now := time.Now()
	s.mu.Lock()
	if !force && !s.lastCleanupAt.IsZero() && now.Sub(s.lastCleanupAt) < time.Hour {
		s.mu.Unlock()
		return nil
	}
	s.lastCleanupAt = now
	s.mu.Unlock()

	cutoff := now.Add(-90 * 24 * time.Hour)
	return s.repos.SystemData.DeleteOlderThan(cutoff)
}

func (s *DashboardService) resolvePublicIPv4() string {
	s.mu.Lock()
	if s.lastPublicIP != "" && time.Since(s.lastPublicIPCheckAt) < 10*time.Minute {
		ip := s.lastPublicIP
		s.mu.Unlock()
		return ip
	}
	s.mu.Unlock()

	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return s.rememberPublicIP("Unavailable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return s.rememberPublicIP("Unavailable")
	}
	ipText := stringsTrim(string(raw))
	ip := net.ParseIP(ipText)
	if ip == nil || ip.To4() == nil {
		return s.rememberPublicIP("Unavailable")
	}
	return s.rememberPublicIP(ipText)
}

func (s *DashboardService) rememberPublicIP(ip string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPublicIP = ip
	s.lastPublicIPCheckAt = time.Now()
	return ip
}

func humanUptime(seconds uint64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func humanBytes(v uint64) string {
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

func compactHumanBytes(v uint64) string {
	return strings.ReplaceAll(humanBytes(v), " ", "")
}

func avg(items []float64) float64 {
	if len(items) == 0 {
		return 0
	}
	total := 0.0
	for _, v := range items {
		total += v
	}
	return total / float64(len(items))
}

func stringsTrim(v string) string {
	return strings.TrimSpace(v)
}

func safeIOCounters() (stats []gnet.IOCountersStat, err error) {
	defer func() {
		if r := recover(); r != nil {
			stats = nil
			err = fmt.Errorf("network counters unavailable")
		}
	}()
	return gnet.IOCounters(false)
}

func resolveHistoryRange(rangeKey string) (time.Duration, int) {
	switch rangeKey {
	case "1h":
		return 1 * time.Hour, 360
	case "7d":
		return 7 * 24 * time.Hour, 320
	case "30d":
		return 30 * 24 * time.Hour, 360
	case "90d":
		return 90 * 24 * time.Hour, 360
	default:
		return 24 * time.Hour, 288
	}
}

func downsampleHistory(rows []models.SystemMetricSnapshot, maxPoints int) []DashboardHistoryPoint {
	if len(rows) == 0 {
		return []DashboardHistoryPoint{}
	}
	if maxPoints <= 0 || len(rows) <= maxPoints {
		out := make([]DashboardHistoryPoint, 0, len(rows))
		for _, r := range rows {
			out = append(out, DashboardHistoryPoint{
				Timestamp:    r.CreatedAt.Unix(),
				CPUUsage:     r.CPUUsage,
				MemoryUsed:   r.MemoryUsed,
				DiskUsed:     r.DiskUsed,
				Load1:        r.Load1,
				Load5:        r.Load5,
				Load15:       r.Load15,
				NetworkRxBps: r.NetworkRxBps,
				NetworkTxBps: r.NetworkTxBps,
			})
		}
		return out
	}

	step := int(math.Ceil(float64(len(rows)) / float64(maxPoints)))
	if step < 1 {
		step = 1
	}

	out := make([]DashboardHistoryPoint, 0, maxPoints)
	for i := 0; i < len(rows); i += step {
		end := i + step
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[i:end]
		if len(chunk) == 0 {
			continue
		}

		totalCPU := 0.0
		totalMem := 0.0
		totalDisk := 0.0
		totalLoad1 := 0.0
		totalLoad5 := 0.0
		totalLoad15 := 0.0
		totalRx := 0.0
		totalTx := 0.0
		for _, c := range chunk {
			totalCPU += c.CPUUsage
			totalMem += c.MemoryUsed
			totalDisk += c.DiskUsed
			totalLoad1 += c.Load1
			totalLoad5 += c.Load5
			totalLoad15 += c.Load15
			totalRx += c.NetworkRxBps
			totalTx += c.NetworkTxBps
		}
		count := float64(len(chunk))
		last := chunk[len(chunk)-1]

		out = append(out, DashboardHistoryPoint{
			Timestamp:    last.CreatedAt.Unix(),
			CPUUsage:     totalCPU / count,
			MemoryUsed:   totalMem / count,
			DiskUsed:     totalDisk / count,
			Load1:        totalLoad1 / count,
			Load5:        totalLoad5 / count,
			Load15:       totalLoad15 / count,
			NetworkRxBps: totalRx / count,
			NetworkTxBps: totalTx / count,
		})
	}

	return out
}
