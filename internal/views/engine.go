package views

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	jet "github.com/gofiber/template/jet/v2"

	"deploycp/internal/config"
	"deploycp/internal/utils"
)

func NewEngine(cfg *config.Config) *jet.Engine {
	engine := jet.New(resolveTemplateDir(), ".jet.html")
	_ = cfg
	engine.Reload(true)
	engine.AddFunc("formatTime", func(t time.Time) string {
		return t.In(time.Local).Format("2006-01-02 15:04:05")
	})
	engine.AddFunc("formatTimePtr", func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.In(time.Local).Format("2006-01-02 15:04:05")
	})
	engine.AddFunc("formatPct", func(v float64) string {
		return fmt.Sprintf("%.1f%%", v)
	})
	engine.AddFunc("formatFloat1", func(v float64) string {
		return fmt.Sprintf("%.1f", v)
	})
	engine.AddFunc("websiteStackIconURL", WebsiteStackIconURL)
	engine.AddFunc("appStackIconURL", AppStackIconURL)
	engine.AddFunc("deployCPFaviconURL", DeployCPFaviconURL)
	engine.AddFunc("platformShowURL", func(kind string, id any) string {
		v, ok := anyToUint(id)
		if !ok {
			return "/platforms"
		}
		return utils.PlatformShowURL(kind, v)
	})
	engine.AddFunc("authUserLabel", authUserLabel)
	return engine
}

func anyToUint(v any) (uint, bool) {
	switch t := v.(type) {
	case uint:
		return t, t > 0
	case uint8:
		return uint(t), t > 0
	case uint16:
		return uint(t), t > 0
	case uint32:
		return uint(t), t > 0
	case uint64:
		return uint(t), t > 0
	case int:
		if t <= 0 {
			return 0, false
		}
		return uint(t), true
	case int8:
		if t <= 0 {
			return 0, false
		}
		return uint(t), true
	case int16:
		if t <= 0 {
			return 0, false
		}
		return uint(t), true
	case int32:
		if t <= 0 {
			return 0, false
		}
		return uint(t), true
	case int64:
		if t <= 0 {
			return 0, false
		}
		return uint(t), true
	case string:
		n, err := strconv.ParseUint(t, 10, 64)
		if err != nil || n == 0 {
			return 0, false
		}
		return uint(n), true
	}

	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return 0, false
	}
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return 0, false
		}
		return anyToUint(rv.Elem().Interface())
	}
	return 0, false
}

func authUserLabel(v any) string {
	fields := []string{"Username", "Name", "Email"}
	for _, field := range fields {
		if value := stringField(v, field); value != "" {
			lower := strings.ToLower(strings.TrimSpace(value))
			if lower != "null" && lower != "<nil>" {
				return value
			}
		}
	}
	return "Account"
}

func stringField(v any, field string) string {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return ""
	}
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	fv := rv.FieldByName(field)
	if !fv.IsValid() || fv.Kind() != reflect.String {
		return ""
	}
	return strings.TrimSpace(fv.String())
}

func resolveTemplateDir() string {
	candidates := []string{
		"./frontend/templates",
		"../frontend/templates",
		"../../frontend/templates",
		"../../../frontend/templates",
		"./templates",
		"../templates",
		"../../templates",
		"../../../templates",
	}
	for _, c := range candidates {
		if stat, err := os.Stat(filepath.Clean(c)); err == nil && stat.IsDir() {
			return filepath.Clean(c)
		}
	}
	return "./templates"
}
