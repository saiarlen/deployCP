package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"deploycp/internal/bootstrap"
)

func main() {
	app, err := bootstrap.Build()
	if err != nil {
		log.Fatalf("failed to initialize app: %v", err)
	}
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = strings.ToLower(strings.TrimSpace(os.Args[1]))
	}
	switch cmd {
	case "", "serve", "server":
	case "bootstrap-host":
		if app.HostLifecycle == nil {
			log.Fatalf("host lifecycle service unavailable")
		}
		result, err := app.HostLifecycle.Bootstrap(context.Background(), nil, "")
		if err != nil {
			log.Fatalf("bootstrap-host failed: %v", err)
		}
		for _, step := range result.Steps {
			fmt.Println(step)
		}
		return
	case "teardown-managed":
		if app.HostLifecycle == nil {
			log.Fatalf("host lifecycle service unavailable")
		}
		result, err := app.HostLifecycle.TeardownManaged(context.Background(), nil, "")
		if err != nil {
			log.Fatalf("teardown-managed failed: %v", err)
		}
		for _, step := range result.Steps {
			fmt.Println(step)
		}
		return
	case "verify-host":
		if app.PreflightService == nil {
			log.Fatalf("preflight service unavailable")
		}
		report := app.PreflightService.Run(nil)
		hasFailures := false
		for _, item := range report.Checks {
			fmt.Printf("[%s] %s: %s\n", strings.ToUpper(item.Status), item.Name, item.Detail)
			if item.Status == "fail" {
				hasFailures = true
			}
		}
		if hasFailures {
			os.Exit(1)
		}
		return
	case "reconcile-managed":
		if app.ReconcileService == nil {
			log.Fatalf("reconcile service unavailable")
		}
		result, err := app.ReconcileService.Run(context.Background(), nil, "")
		if err != nil {
			log.Fatalf("reconcile failed: %v", err)
		}
		for _, step := range result.Steps {
			fmt.Println(step)
		}
		return
	default:
		log.Fatalf("unknown command %q (supported: serve, bootstrap-host, teardown-managed, verify-host, reconcile-managed)", cmd)
	}
	addr := fmt.Sprintf("%s:%d", app.Config.App.Host, app.Config.App.Port)
	log.Printf("%s running on %s", app.Config.App.Name, addr)
	if err := app.Fiber.Listen(addr); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
