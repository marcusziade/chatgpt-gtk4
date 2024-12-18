package main

import (
	"log"
	"os"

	"chatgpt-gtk4/internal/app"
)

func main() {
	app := app.New()

	if code := app.Run(); code > 0 {
		log.Printf("Application exited with code: %d", code)
		os.Exit(code)
	}
}
