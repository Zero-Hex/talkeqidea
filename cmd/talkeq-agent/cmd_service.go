package main

import (
	"fmt"

	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/service"
)

// serviceName is how the agent registers with systemd or the Windows SCM.
const serviceName = "talkeq-agent"

// runServiceCommand implements `talkeq-agent service ...`.
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
		return fmt.Errorf("no %s found - run 'talkeq-agent setup' before installing the service", config.DefaultPath)
	}

	def, err := service.Prepare(service.Definition{
		Name:        serviceName,
		DisplayName: "TalkEQ Agent",
		Description: "Relays this EverQuest server's chat to a TalkEQ hub.",
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
	fmt.Printf("Start it with: talkeq-agent service start\n\n")
	// Worth stating plainly, because operators expect to have to do this and
	// then spend time wondering which port to open.
	fmt.Printf("No firewall change is needed. The agent dials out to the hub and\n")
	fmt.Printf("accepts no inbound connections.\n\n")
	return nil
}

func serviceStatus(manager service.Manager) error {
	status, err := manager.Status(serviceName)
	if err != nil {
		return err
	}

	if !status.IsInstalled {
		fmt.Printf("%s is not installed as a %s service.\n", serviceName, manager.Kind())
		fmt.Printf("Install it with: talkeq-agent service install\n")
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

func usageService(manager service.Manager) error {
	fmt.Printf("usage: talkeq-agent service <command>\n\n")
	fmt.Printf("Manages the agent as a %s service so it survives reboots.\n\n", manager.Kind())
	fmt.Println("  install     register the service and enable it at boot")
	fmt.Println("  uninstall   stop and remove it")
	fmt.Println("  start       start it now")
	fmt.Println("  stop        stop it now")
	fmt.Println("  status      show whether it is installed and running")
	return nil
}
