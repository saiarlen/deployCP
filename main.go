package main

import (
	"context"
	"flag"
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
	case "runtime":
		if app.PlatformOps == nil {
			log.Fatalf("platform operations service unavailable")
		}
		if len(os.Args) < 3 {
			log.Fatalf("usage: deploycp runtime restart [--platform <id|domain>]")
		}
		subcmd := strings.ToLower(strings.TrimSpace(os.Args[2]))
		switch subcmd {
		case "restart":
			fs := flag.NewFlagSet("runtime restart", flag.ExitOnError)
			platformRef := fs.String("platform", os.Getenv("DEPLOYCP_PLATFORM"), "platform id, name, or domain")
			_ = fs.Parse(os.Args[3:])
			id, err := app.PlatformOps.ResolvePlatformID(*platformRef, "")
			if err != nil {
				log.Fatalf("resolve platform failed: %v", err)
			}
			if err := app.PlatformOps.RestartRuntime(context.Background(), id, nil, "cli"); err != nil {
				log.Fatalf("runtime restart failed: %v", err)
			}
			fmt.Printf("runtime restarted for platform %d\n", id)
			return
		default:
			log.Fatalf("unknown runtime command %q (supported: restart)", subcmd)
		}
	case "deploy":
		if app.PlatformOps == nil {
			log.Fatalf("platform operations service unavailable")
		}
		fs := flag.NewFlagSet("deploy", flag.ExitOnError)
		platformRef := fs.String("platform", os.Getenv("DEPLOYCP_PLATFORM"), "platform id, name, or domain")
		branch := fs.String("branch", os.Getenv("DEPLOYCP_BRANCH"), "branch override")
		_ = fs.Parse(os.Args[2:])
		id, err := app.PlatformOps.ResolvePlatformID(*platformRef, "")
		if err != nil {
			log.Fatalf("resolve platform failed: %v", err)
		}
		result, err := app.PlatformOps.Deploy(context.Background(), id, *branch, nil, "cli")
		if result != nil && strings.TrimSpace(result.Output) != "" {
			fmt.Println(result.Output)
		}
		if err != nil {
			log.Fatalf("deploy failed: %v", err)
		}
		fmt.Printf("deploy completed for platform %d\n", id)
		return
	case "health-check":
		if app.PlatformOps == nil {
			log.Fatalf("platform operations service unavailable")
		}
		fs := flag.NewFlagSet("health-check", flag.ExitOnError)
		platformRef := fs.String("platform", os.Getenv("DEPLOYCP_PLATFORM"), "platform id, name, or domain")
		_ = fs.Parse(os.Args[2:])
		id, err := app.PlatformOps.ResolvePlatformID(*platformRef, "")
		if err != nil {
			log.Fatalf("resolve platform failed: %v", err)
		}
		check, err := app.PlatformOps.CheckHealth(context.Background(), id, nil, "cli")
		if err != nil {
			log.Fatalf("health check failed: %v", err)
		}
		fmt.Printf("[%s] platform %d: %s\n", strings.ToUpper(check.Status), id, check.Message)
		if check.Status == "critical" {
			os.Exit(1)
		}
		return
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
		log.Fatalf("unknown command %q (supported: serve, bootstrap-host, teardown-managed, verify-host, reconcile-managed, runtime restart, deploy, health-check)", cmd)
	}
	addr := fmt.Sprintf("%s:%d", app.Config.App.Host, app.Config.App.Port)
	log.Printf("%s running on %s", app.Config.App.Name, addr)
	if err := app.Fiber.Listen(addr); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
