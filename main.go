package main

import (
	"fmt"
	"log"

	"deploycp/internal/bootstrap"
)

func main() {
	app, err := bootstrap.Build()
	if err != nil {
		log.Fatalf("failed to initialize app: %v", err)
	}
	addr := fmt.Sprintf("%s:%d", app.Config.App.Host, app.Config.App.Port)
	log.Printf("%s running on %s", app.Config.App.Name, addr)
	if err := app.Fiber.Listen(addr); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
