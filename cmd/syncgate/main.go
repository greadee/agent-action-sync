package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"syncgate/internal/config"
)

func main() {
	configPath := flag.String("config", "config.example.json", "path to syncgate JSON config")
	flag.Parse()

	cfg, err := config.LoadFile(context.Background(), *configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "syncgate: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("syncgate config ok: device=%s shares=%d\n", cfg.DeviceName, len(cfg.Shares))
}
