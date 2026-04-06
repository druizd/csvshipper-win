package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
)

func main() {
	isInteractive, err := svc.IsAnInteractiveSession()
	if err != nil {
		log.Fatalf("failed to determine if we are running in an interactive session: %v", err)
	}

	installFlag := flag.Bool("install", false, "Install the service")
	uninstallFlag := flag.Bool("uninstall", false, "Uninstall the service")
	startFlag := flag.Bool("start", false, "Start the service")
	stopFlag := flag.Bool("stop", false, "Stop the service")
	configFlag := flag.String("config", "", "Path to config.json")
	flag.Parse()

	if *installFlag {
		err = installService("SQLShipper", "RabbitMQ SQL Shipper Service")
		if err != nil {
			log.Fatalf("failed to install service: %v", err)
		}
		fmt.Println("Service installed successfully")
		return
	}

	if *uninstallFlag {
		err = removeService("SQLShipper")
		if err != nil {
			log.Fatalf("failed to remove service: %v", err)
		}
		fmt.Println("Service uninstalled successfully")
		return
	}

	if *startFlag {
		err = startService("SQLShipper")
		if err != nil {
			log.Fatalf("failed to start service: %v", err)
		}
		fmt.Println("Service started")
		return
	}

	if *stopFlag {
		err = controlService("SQLShipper", svc.Stop, svc.Stopped)
		if err != nil {
			log.Fatalf("failed to stop service: %v", err)
		}
		fmt.Println("Service stopped")
		return
	}

	cfgFile := *configFlag
	if !isInteractive && cfgFile == "" {
		// When running as a service, the working dir is often system32
		// We should load config from the dir where exe lives
		exePath, _ := os.Executable()
		cfgFile = filepath.Join(filepath.Dir(exePath), "config.json")
	}

	if !isInteractive {
		runService("SQLShipper", false, cfgFile)
		return
	}

	runService("SQLShipper", true, cfgFile)
}

func runGoApp(cfgFile string) {
	log.Println("Starting SQLShipper app...")
	cfg, err := LoadConfig(cfgFile)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Ensure directories exist
	EnsureDir(cfg.SourceDir)
	EnsureDir(cfg.DoneDir)
	EnsureDir(cfg.ErrorDir)

	rc, err := ConnectRabbit(cfg)
	if err != nil {
		log.Fatalf("failed to connect to RabbitMQ: %v", err)
	}
	defer rc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Capture interrupt for graceful shutdown in interactive mode
	// But since runGoApp could be called from the service wrapper,
	// We just block until ctx is canceled by the wrapper or OS
	go rc.SendHeartbeat(ctx)
	RunScannerAndWorkers(ctx, cfg, rc)
}
