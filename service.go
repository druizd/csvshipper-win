package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type sqlService struct {
	cfgFile string
}

func (m *sqlService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	var err error

	cfg, err := LoadConfig(m.cfgFile)
	if err != nil {
		log.Printf("Failed to load config in service: %v", err)
		return
	}

	EnsureDir(cfg.SourceDir)
	EnsureDir(cfg.DoneDir)
	EnsureDir(cfg.ErrorDir)

	rc, err := ConnectRabbit(cfg)
	if err != nil {
		log.Printf("Failed to connect to rabbitmq: %v", err)
		return
	}

	go rc.SendHeartbeat(ctx)
	go RunScannerAndWorkers(ctx, cfg, rc)

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

loop:
	for {
		c := <-r
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			log.Println("Received stop request")
			log.Println("Canceling context and stopping workers...")
			cancel()
			// Wait a bit to drain tasks
			time.Sleep(3 * time.Second)
			rc.Close()
			break loop
		default:
			log.Printf("unexpected control request #%d", c)
		}
	}
	changes <- svc.Status{State: svc.StopPending}
	return
}

func runService(name string, isDebug bool, cfgFile string) {
	var err error
	if isDebug {
		log.Println("Running in debug mode (interactive session)")
		// In debug mode, just use a basic runner, could use ctrl+C trap here
		runGoApp(cfgFile)
		return
	}

	// Create log file
	logFile, err := os.OpenFile("C:\\Windows\\Temp\\sqlshipper.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}

	log.Println("Starting service")
	err = svc.Run(name, &sqlService{cfgFile: cfgFile})
	if err != nil {
		log.Printf("%s service failed: %v", name, err)
		return
	}
	log.Printf("%s service stopped", name)
}

func exePath() (string, error) {
	prog := os.Args[0]
	p, err := os.Executable()
	if err != nil {
		return prog, nil
	}
	return p, nil
}

func installService(name, desc string) error {
	exepath, err := exePath()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", name)
	}

	s, err = m.CreateService(name, exepath, mgr.Config{DisplayName: desc, StartType: mgr.StartAutomatic})
	if err != nil {
		return err
	}
	defer s.Close()
	return nil
}

func removeService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s is not installed", name)
	}
	defer s.Close()

	err = s.Delete()
	if err != nil {
		return err
	}
	return nil
}

func startService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("could not access service: %v", err)
	}
	defer s.Close()

	err = s.Start()
	if err != nil {
		return fmt.Errorf("could not start service: %v", err)
	}
	return nil
}

func controlService(name string, c svc.Cmd, to svc.State) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("could not access service: %v", err)
	}
	defer s.Close()

	status, err := s.Control(c)
	if err != nil {
		return fmt.Errorf("could not send control=%d: %v", c, err)
	}

	timeout := time.Now().Add(10 * time.Second)
	for status.State != to {
		if timeout.Before(time.Now()) {
			return fmt.Errorf("timeout waiting for service to go to state=%d", to)
		}
		time.Sleep(300 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("could not retrieve service status: %v", err)
		}
	}
	return nil
}
