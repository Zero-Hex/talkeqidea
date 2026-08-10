package service

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// FirewallPlan is the command needed to let agents reach the hub.
//
// The commands are shown before they are run, and running them is always
// optional. Editing an operator's firewall is the kind of change that should
// never be a silent side effect of a setup wizard.
type FirewallPlan struct {
	// Tool names the firewall, e.g. "ufw".
	Tool string
	// Command is the argv to execute.
	Command []string
	// Explanation is a one-line description for the operator.
	Explanation string
	// Manual is guidance shown when no supported tool was detected.
	Manual string
}

// String renders the command as it would be typed.
func (p *FirewallPlan) String() string {
	if len(p.Command) == 0 {
		return ""
	}
	return strings.Join(p.Command, " ")
}

// PlanFirewall works out how to open the hub's port on this machine.
//
// It detects rather than assumes: a box may run ufw, firewalld, plain
// iptables, or nothing at all, and guessing wrong either fails loudly or, far
// worse, appears to succeed while the port stays closed.
func PlanFirewall(port int, serviceName string) *FirewallPlan {
	switch runtime.GOOS {
	case "windows":
		return &FirewallPlan{
			Tool: "Windows Firewall",
			Command: []string{
				"netsh", "advfirewall", "firewall", "add", "rule",
				fmt.Sprintf("name=%s", serviceName),
				"dir=in", "action=allow", "protocol=TCP",
				fmt.Sprintf("localport=%d", port),
			},
			Explanation: fmt.Sprintf("Allow inbound TCP %d through Windows Firewall", port),
		}

	case "linux":
		if _, err := exec.LookPath("ufw"); err == nil {
			return &FirewallPlan{
				Tool:        "ufw",
				Command:     []string{"ufw", "allow", fmt.Sprintf("%d/tcp", port)},
				Explanation: fmt.Sprintf("Allow inbound TCP %d through ufw", port),
			}
		}
		if _, err := exec.LookPath("firewall-cmd"); err == nil {
			return &FirewallPlan{
				Tool:        "firewalld",
				Command:     []string{"firewall-cmd", "--permanent", fmt.Sprintf("--add-port=%d/tcp", port)},
				Explanation: fmt.Sprintf("Allow inbound TCP %d through firewalld (run 'firewall-cmd --reload' after)", port),
			}
		}
		return &FirewallPlan{
			Tool: "",
			Manual: fmt.Sprintf(
				"No ufw or firewalld found. If this box filters inbound traffic, allow TCP %d yourself.\n"+
					"      Also check any firewall in front of it, such as a cloud provider's security group.", port),
		}

	default:
		return &FirewallPlan{
			Tool:   "",
			Manual: fmt.Sprintf("Allow inbound TCP %d if this machine filters traffic.", port),
		}
	}
}

// Apply runs the planned command.
func (p *FirewallPlan) Apply() error {
	if len(p.Command) == 0 {
		return fmt.Errorf("no firewall command is available for this system")
	}
	if !IsElevated() {
		return fmt.Errorf("changing firewall rules needs elevated privileges. %s", ElevationHint(p.String()))
	}

	cmd := exec.Command(p.Command[0], p.Command[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			return fmt.Errorf("%s: %w", p.String(), err)
		}
		return fmt.Errorf("%s: %w: %s", p.String(), err, detail)
	}
	return nil
}
