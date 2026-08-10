package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareFillsInDefaults(t *testing.T) {
	def, err := Prepare(Definition{Name: "talkeq-hub"})
	if err != nil {
		t.Fatalf("prepare: %s", err)
	}

	if def.DisplayName != "talkeq-hub" {
		t.Errorf("display name = %q, want the service name", def.DisplayName)
	}
	if !filepath.IsAbs(def.ExecutablePath) {
		t.Errorf("executable path %q is not absolute", def.ExecutablePath)
	}
	if !filepath.IsAbs(def.WorkingDirectory) {
		t.Errorf("working directory %q is not absolute", def.WorkingDirectory)
	}

	// The working directory must default beside the binary, not to wherever
	// the installer happened to be run from: a service manager starts the
	// process somewhere else entirely, and the config lives next to the exe.
	if def.WorkingDirectory != filepath.Dir(def.ExecutablePath) {
		t.Errorf("working directory %q is not the executable's directory %q",
			def.WorkingDirectory, filepath.Dir(def.ExecutablePath))
	}
}

func TestPrepareRequiresName(t *testing.T) {
	if _, err := Prepare(Definition{}); err == nil {
		t.Error("a definition with no name was accepted")
	}
}

func TestPrepareRejectsMissingWorkingDirectory(t *testing.T) {
	_, err := Prepare(Definition{
		Name:             "talkeq-hub",
		WorkingDirectory: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err == nil {
		t.Error("a nonexistent working directory was accepted")
	}
}

func TestPrepareMakesPathsAbsolute(t *testing.T) {
	dir := t.TempDir()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %s", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %s", err)
	}
	defer os.Chdir(previous)

	def, err := Prepare(Definition{
		Name:             "talkeq-hub",
		WorkingDirectory: ".",
	})
	if err != nil {
		t.Fatalf("prepare: %s", err)
	}
	if !filepath.IsAbs(def.WorkingDirectory) {
		t.Errorf("working directory %q was left relative; a service would resolve it against the wrong root", def.WorkingDirectory)
	}
}

func TestPlanFirewallMatchesPlatform(t *testing.T) {
	plan := PlanFirewall(34197, "talkeq-hub")

	switch runtime.GOOS {
	case "windows":
		if plan.Tool != "Windows Firewall" {
			t.Errorf("tool = %q on windows", plan.Tool)
		}
		if !strings.Contains(plan.String(), "netsh") {
			t.Errorf("windows plan does not use netsh: %q", plan.String())
		}
		if !strings.Contains(plan.String(), "34197") {
			t.Errorf("windows plan does not mention the port: %q", plan.String())
		}
	case "linux":
		// Which tool depends on what is installed; either a command or manual
		// guidance must be produced, never nothing.
		if plan.Tool == "" && plan.Manual == "" {
			t.Error("linux plan produced neither a command nor guidance")
		}
		if plan.Tool != "" && !strings.Contains(plan.String(), "34197") {
			t.Errorf("linux plan does not mention the port: %q", plan.String())
		}
	default:
		if plan.Manual == "" {
			t.Error("unsupported platform produced no guidance")
		}
	}
}

// A plan with no command must refuse rather than run something arbitrary.
func TestApplyEmptyPlanFails(t *testing.T) {
	plan := &FirewallPlan{}
	if err := plan.Apply(); err == nil {
		t.Error("applying an empty firewall plan succeeded")
	}
}

func TestElevationHintMentionsThePlatformMechanism(t *testing.T) {
	hint := ElevationHint("talkeq-hub", "service", "install")

	if runtime.GOOS == "windows" {
		if !strings.Contains(hint, "administrator") {
			t.Errorf("windows hint does not mention administrator: %q", hint)
		}
	} else if !strings.Contains(hint, "sudo") {
		t.Errorf("unix hint does not mention sudo: %q", hint)
	}
	if !strings.Contains(hint, "service install") {
		t.Errorf("hint does not repeat the command: %q", hint)
	}
}

func TestUnsupportedErrorNamesThePlatform(t *testing.T) {
	err := &ErrUnsupported{GOOS: "plan9"}
	if !strings.Contains(err.Error(), "plan9") {
		t.Errorf("error does not name the platform: %s", err)
	}
}
