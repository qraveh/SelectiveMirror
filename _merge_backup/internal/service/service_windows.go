// Package service provides Windows Service support for smirror.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "smirror"
const serviceDisplayName = "SelectiveMirror"
const serviceDescription = "Near-real-time selective file mirror built on rclone"

// IsWindowsService returns true if the current process is running as a Windows Service.
func IsWindowsService() bool {
	is, _ := svc.IsWindowsService()
	return is
}

// Run runs smirror as a Windows Service. Called from main when running as service.
// The startFunc is called when the service receives a Start command; it should
// block until ctx is done. The stopFunc is called to signal shutdown.
func Run(startFunc func(), stopFunc func()) error {
	return svc.Run(serviceName, &handler{
		startFunc: startFunc,
		stopFunc:  stopFunc,
	})
}

// handler implements svc.Handler.
type handler struct {
	startFunc func()
	stopFunc  func()
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	// Start smirror in a goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.startFunc()
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				h.stopFunc()
				// Wait for startFunc to return (up to 10s)
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				return false, 0
			}
		case <-done:
			// startFunc returned on its own
			return false, 0
		}
	}
}

// Install registers smirror as a Windows Service.
// configPath is passed as the --config argument.
func Install(configPath string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()

	// Check if already installed
	s, err := m.OpenService(serviceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %q is already installed", serviceName)
	}

	args := []string{"start"}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	s, err = m.CreateService(serviceName, exePath, mgr.Config{
		DisplayName:  serviceDisplayName,
		Description:  serviceDescription,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}, args...)
	if err != nil {
		return fmt.Errorf("creating service: %w", err)
	}
	defer s.Close()

	// Configure recovery: restart on failure with increasing delays
	err = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400) // reset failure count after 24h
	if err != nil {
		// Non-fatal: service will still work, just without auto-recovery
		fmt.Fprintf(os.Stderr, "Warning: could not set recovery actions: %v\n", err)
	}

	// Install event log source
	eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info)

	return nil
}

// Uninstall removes the smirror Windows Service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q not found: %w", serviceName, err)
	}
	defer s.Close()

	// Stop if running
	s.Control(svc.Stop)
	time.Sleep(2 * time.Second)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("deleting service: %w", err)
	}

	eventlog.Remove(serviceName)
	return nil
}

// Start starts the smirror Windows Service via `net start`.
func Start() error {
	cmd := exec.Command("net", "start", serviceName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Stop stops the smirror Windows Service via `net stop`.
func Stop() error {
	cmd := exec.Command("net", "stop", serviceName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
