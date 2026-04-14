package handlers

import (
	"math"
	"runtime"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/services"
)

type DashboardHandler struct {
	base      *BaseHandler
	dashboard *services.DashboardService
}

func NewDashboardHandler(cfg *config.Config, sessions *middleware.SessionManager, dashboard *services.DashboardService) *DashboardHandler {
	return &DashboardHandler{base: &BaseHandler{Config: cfg, Sessions: sessions}, dashboard: dashboard}
}

func (h *DashboardHandler) Index(c *fiber.Ctx) error {
	metrics, err := h.dashboard.Build()
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	vcpu := runtime.NumCPU()
	if vcpu < 1 {
		vcpu = 1
	}
	// Load bar: 1m load vs vCPU (100% ≈ load == vCPU), capped for display.
	loadBarPct := (metrics.Load1 / float64(vcpu)) * 100
	loadBarPct = math.Min(100, math.Max(0, loadBarPct))
	return h.base.Render(c, "dashboard_index", fiber.Map{
		"Title":      "Dashboard",
		"Metrics":    metrics,
		"VCPUCount":  vcpu,
		"LoadBarPct": loadBarPct,
	})
}

func (h *DashboardHandler) Live(c *fiber.Ctx) error {
	metrics, err := h.dashboard.Live()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(metrics)
}

func (h *DashboardHandler) History(c *fiber.Ctx) error {
	rangeKey := c.Query("range", "1h")
	points, err := h.dashboard.History(rangeKey)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{
		"range":  rangeKey,
		"points": points,
	})
}
