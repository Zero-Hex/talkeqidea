package main

import (
	"fmt"
	"net"
	"strconv"

	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/service"
)

// serviceName is how the hub registers with systemd or the Windows SCM.
const serviceName = "talkeq-hub"

// runServiceCommand implements `talkeq-hub service ...`.
func runServiceCommand(args []string) error {
	manager, err := service.NewManager()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return usageService(manager)
	}

	switch args[0] {
	case "install":
		return serviceInstall(manager)

	case "uninstall", "remove":
		if err := manager.Uninstall(serviceName); err != nil {
			return err
		}
		fmt.Printf("Removed the %s service.\n", serviceName)
		return nil

	case "start":
		if err := manager.Start(serviceName); err != nil {
			return err
		}
		fmt.Printf("Started %s.\n", serviceName)
		return nil

	case "stop":
		if err := manager.Stop(serviceName); err != nil {
			return err
		}
		fmt.Printf("Stopped %s.\n", serviceName)
		return nil

	case "status":
		return serviceStatus(manager)

	default:
		return usageService(manager)
	}
}

func serviceInstall(manager service.Manager) error {
	if !config.Exists(config.DefaultPath) {
		return fmt.Errorf("no %s found - run 'talkeq-hub setup' before installing the service", config.DefaultPath)
	}

	def, err := service.Prepare(service.Definition{
		Name:        serviceName,
		DisplayName: "TalkEQ Hub",
		Description: "Relays EverQuest chat between game servers and Discord.",
	})
	if err != nil {
		return err
	}

	if err := manager.Install(def); err != nil {
		return err
	}

	fmt.Printf("\nInstalled %s as a %s service.\n\n", serviceName, manager.Kind())
	fmt.Printf("    Executable:         %s\n", def.ExecutablePath)
	fmt.Printf("    Working directory:  %s\n", def.WorkingDirectory)
	fmt.Printf("\n")
	fmt.Printf("Config, certificates and logs are read from and written to the\n")
	fmt.Printf("working directory above.\n\n")
	fmt.Printf("Start it with: talkeq-hub service start\n\n")

	printFirewallGuidance()
	return nil
}

func serviceStatus(manager service.Manager) error {
	status, err := manager.Status(serviceName)
	if err != nil {
		return err
	}

	if !status.IsInstalled {
		fmt.Printf("%s is not installed as a %s service.\n", serviceName, manager.Kind())
		fmt.Printf("Install it with: talkeq-hub service install\n")
		return nil
	}

	state := "stopped"
	if status.IsRunning {
		state = "running"
	}
	fmt.Printf("%s is installed and %s", serviceName, state)
	if status.Detail != "" && status.Detail != state {
		fmt.Printf(" (%s)", status.Detail)
	}
	fmt.Println()
	return nil
}

// printFirewallGuidance shows the command that opens the hub's port, without
// running it. Opening a port is the operator's decision to make knowingly.
func printFirewallGuidance() {
	port := configuredPort()
	plan := service.PlanFirewall(port, serviceName)

	if plan.Manual != "" {
		fmt.Printf("Firewall:\n      %s\n\n", plan.Manual)
		return
	}

	fmt.Printf("Firewall: agents need inbound TCP %d. To open it with %s:\n\n", port, plan.Tool)
	fmt.Printf("    %s\n\n", plan.String())
	fmt.Printf("Run 'talkeq-hub firewall --apply' to do that for you.\n\n")
}

// runFirewallCommand implements `talkeq-hub firewall [--apply]`.
func runFirewallCommand(args []string) error {
	port := configuredPort()
	plan := service.PlanFirewall(port, serviceName)

	if plan.Manual != "" {
		fmt.Println(plan.Manual)
		return nil
	}

	shouldApply := false
	for _, arg := range args {
		if arg == "--apply" || arg == "-apply" {
			shouldApply = true
		}
	}

	if !shouldApply {
		fmt.Printf("%s\n\n", plan.Explanation)
		fmt.Printf("    %s\n\n", plan.String())
		fmt.Printf("Re-run with --apply to execute it.\n")
		return nil
	}

	if err := plan.Apply(); err != nil {
		return err
	}
	fmt.Printf("Opened inbound TCP %d using %s.\n", port, plan.Tool)
	return nil
}

// configuredPort reads the hub's listen port, falling back to the default when
// there is no config yet.
func configuredPort() int {
	fallback := 34197

	if !config.Exists(config.DefaultPath) {
		return fallback
	}
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return fallback
	}

	listen := cfg.Relay.Hub.Listen
	if listen == "" {
		return fallback
	}
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return fallback
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return fallback
	}
	return port
}

func usageService(manager service.Manager) error {
	fmt.Printf("usage: talkeq-hub service <command>\n\n")
	fmt.Printf("Manages the hub as a %s service so it survives reboots.\n\n", manager.Kind())
	fmt.Println("  install     register the service and enable it at boot")
	fmt.Println("  uninstall   stop and remove it")
	fmt.Println("  start       start it now")
	fmt.Println("  stop        stop it now")
	fmt.Println("  status      show whether it is installed and running")
	return nil
}
