//go:build windows

// Package service provides Windows Service Control Manager (SCM) integration
// for smirror, using golang.org/x/sys/windows/svc.
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "smirror"
const serviceDisplayName = "SelectiveMirror"
const serviceDescription = "Real-time selective file synchronization engine"

// CmdSyncNow is a user-defined service control code (128-255 range).
// Sent by `smirror sync-now` to request the running service to perform
// an immediate full sync without stopping.
const CmdSyncNow = svc.Cmd(128)

// IsWindowsService reports whether the current process was started by the
// Windows Service Control Manager (as opposed to being run interactively).
func IsWindowsService() bool {
	is, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return is
}

// handler implements svc.Handler for the SCM run loop.
type handler struct {
	startFunc   func()
	stopFunc    func()
	syncNowFunc func() // called when CmdSyncNow control code received
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}

	// Start the application in a goroutine
	done := make(chan struct{})
	go func() {
		h.startFunc()
		close(done)
	}()

	s <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				h.stopFunc()
				// Wait for startFunc to return (it should exit when context is cancelled)
				select {
				case <-done:
				case <-time.After(30 * time.Second):
				}
				return false, 0
			case CmdSyncNow:
				if h.syncNowFunc != nil {
					go h.syncNowFunc()
				}
			case svc.Interrogate:
				s <- c.CurrentStatus
			}
		case <-done:
			// startFunc returned on its own (unexpected)
			return false, 0
		}
	}
}

// Run executes smirror under SCM control. startFunc is called when the service
// receives a Start command; stopFunc is called on Stop/Shutdown; syncNowFunc
// is called when the CmdSyncNow custom control code is received (from `smirror sync-now`).
// This function blocks until the service is stopped.
func Run(startFunc, stopFunc, syncNowFunc func()) error {
	return svc.Run(serviceName, &handler{
		startFunc:   startFunc,
		stopFunc:    stopFunc,
		syncNowFunc: syncNowFunc,
	})
}

// SendSyncNow sends the CmdSyncNow custom control code to the running smirror service.
// This triggers an immediate full sync without stopping the service.
func SendSyncNow() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to Service Control Manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()

	_, err = s.Control(CmdSyncNow)
	if err != nil {
		return fmt.Errorf("cannot send sync-now signal: %w", err)
	}
	return nil
}

// Install registers smirror as a Windows service. The service is configured
// to run the current executable with "start --config <configPath>".
// Requires Administrator privileges.
func Install(configPath string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to Service Control Manager (run as Administrator): %w", err)
	}
	defer m.Disconnect()

	// Check if already installed
	s, err := m.OpenService(serviceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %q is already installed — uninstall first with: smirror service uninstall", serviceName)
	}

	cfg := mgr.Config{
		DisplayName:  serviceDisplayName,
		Description:  serviceDescription,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}

	// The service binary path includes the --config flag so SCM passes it on start.
	// Note: We do NOT include "start" because serviceMain() is entered via
	// IsWindowsService() detection, not via the "start" subcommand.
	s, err = m.CreateService(serviceName, exePath, cfg, "--config", configPath)
	if err != nil {
		return fmt.Errorf("cannot create service: %w", err)
	}
	defer s.Close()

	// Configure recovery: restart on first 3 failures, then do nothing.
	err = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400) // reset failure count after 24 hours
	if err != nil {
		// Non-fatal: service is installed, just without recovery policy
		fmt.Fprintf(os.Stderr, "Warning: could not set recovery actions: %v\n", err)
	}

	return nil
}

// Uninstall removes the smirror Windows service registration.
// The service must be stopped before uninstalling.
// Requires Administrator privileges.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to Service Control Manager (run as Administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()

	// Try to stop it first if running
	status, err := s.Query()
	if err == nil && status.State != svc.Stopped {
		if _, stopErr := s.Control(svc.Stop); stopErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not stop service before uninstall: %v\n", stopErr)
		}
		// Wait briefly for it to stop
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			status, err = s.Query()
			if err != nil || status.State == svc.Stopped {
				break
			}
		}
	}

	err = s.Delete()
	if err != nil {
		return fmt.Errorf("cannot delete service: %w", err)
	}
	return nil
}

// IsInstalled reports whether the smirror Windows service is registered.
// Does not require Administrator privileges (read-only SCM query).
func IsInstalled() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// IsRunning reports whether the smirror Windows service is currently running.
// Returns (installed, running). Does not require Administrator for query.
func IsRunning() (installed bool, running bool) {
	m, err := mgr.Connect()
	if err != nil {
		return false, false
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return false, false
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return true, false
	}
	return true, status.State == svc.Running
}

// Start sends a start signal to the smirror Windows service.
// Requires Administrator privileges.
func Start() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to Service Control Manager (run as Administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed — install with: smirror service install", serviceName)
	}
	defer s.Close()

	err = s.Start()
	if err != nil {
		return fmt.Errorf("cannot start service: %w", err)
	}

	return nil
}

// Stop sends a stop signal to the smirror Windows service.
// Requires Administrator privileges.
func Stop() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to Service Control Manager (run as Administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()

	// Check if already stopped before sending control signal
	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("cannot query service state: %w", err)
	}
	if status.State == svc.Stopped {
		return fmt.Errorf("service has not been started")
	}

	status, err = s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("cannot stop service: %w", err)
	}

	// Wait for it to actually stop
	for i := 0; i < 20; i++ {
		if status.State == svc.Stopped {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			break
		}
	}

	if status.State != svc.Stopped {
		return fmt.Errorf("service stop signal sent but still running after 10s")
	}
	return nil
}
