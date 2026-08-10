// Package setup provides the interactive wizards that configure a hub or an
// agent.
//
// The prompts are deliberately plain line-oriented reads rather than a
// full-screen TUI. Server operators run these two ways — over SSH, sometimes on
// a flaky link, and by double-clicking an exe on Windows — and a line-oriented
// prompt degrades gracefully in both, where a full-screen interface can leave
// the terminal wedged.
package setup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Prompter reads answers from an operator.
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
}

// NewPrompter builds a prompter over stdin and stdout.
func NewPrompter() *Prompter {
	return &Prompter{
		in:  bufio.NewReader(os.Stdin),
		out: os.Stdout,
	}
}

// NewPrompterFrom builds a prompter over supplied streams, for tests.
func NewPrompterFrom(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

// ErrAborted is returned when the operator cancels, or when input ends before
// a required answer was given.
var ErrAborted = fmt.Errorf("setup aborted")

// Printf writes to the prompter's output.
func (p *Prompter) Printf(format string, args ...any) {
	fmt.Fprintf(p.out, format, args...)
}

// Section prints a heading, so a long wizard reads as a sequence of steps
// rather than an undifferentiated wall of questions.
func (p *Prompter) Section(title string) {
	fmt.Fprintf(p.out, "\n%s\n%s\n\n", title, strings.Repeat("-", len(title)))
}

// Note prints an indented explanatory line.
func (p *Prompter) Note(format string, args ...any) {
	fmt.Fprintf(p.out, "  "+format+"\n", args...)
}

// readLine reads one line, returning ErrAborted at end of input.
func (p *Prompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil {
		// A final line without a trailing newline is still an answer.
		if err == io.EOF && strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line), nil
		}
		return "", ErrAborted
	}
	return strings.TrimSpace(line), nil
}

// String asks for text, returning fallback when the operator presses enter.
func (p *Prompter) String(question, fallback string) (string, error) {
	for {
		if fallback != "" {
			fmt.Fprintf(p.out, "%s [%s]: ", question, fallback)
		} else {
			fmt.Fprintf(p.out, "%s: ", question)
		}

		answer, err := p.readLine()
		if err != nil {
			return "", err
		}
		if answer == "" {
			if fallback != "" {
				return fallback, nil
			}
			p.Note("An answer is required.")
			continue
		}
		return answer, nil
	}
}

// Secret asks for a value that should not be echoed back in the summary.
//
// The input itself is not masked: doing so portably means either a cgo
// terminal dependency or Windows console API calls, and an operator pasting a
// bot token into their own terminal is not the threat being defended against.
// What matters is that the value is not repeated in the confirmation output.
func (p *Prompter) Secret(question string) (string, error) {
	return p.String(question, "")
}

// Optional asks for text that may be left blank.
func (p *Prompter) Optional(question, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", question, fallback)
	} else {
		fmt.Fprintf(p.out, "%s (optional): ", question)
	}

	answer, err := p.readLine()
	if err != nil {
		return "", err
	}
	if answer == "" {
		return fallback, nil
	}
	return answer, nil
}

// Confirm asks a yes or no question.
func (p *Prompter) Confirm(question string, fallback bool) (bool, error) {
	hint := "y/N"
	if fallback {
		hint = "Y/n"
	}

	for {
		fmt.Fprintf(p.out, "%s [%s]: ", question, hint)

		answer, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return fallback, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			p.Note("Please answer y or n.")
		}
	}
}

// Int asks for a number within an inclusive range.
func (p *Prompter) Int(question string, fallback, min, max int) (int, error) {
	for {
		fmt.Fprintf(p.out, "%s [%d]: ", question, fallback)

		answer, err := p.readLine()
		if err != nil {
			return 0, err
		}
		if answer == "" {
			return fallback, nil
		}

		value, err := strconv.Atoi(answer)
		if err != nil {
			p.Note("That is not a number.")
			continue
		}
		if value < min || value > max {
			p.Note("Please choose a number between %d and %d.", min, max)
			continue
		}
		return value, nil
	}
}

// Choose presents a numbered list and returns the chosen index.
func (p *Prompter) Choose(question string, options []string, fallback int) (int, error) {
	fmt.Fprintf(p.out, "%s\n", question)
	for i, option := range options {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, option)
	}

	choice, err := p.Int("Choice", fallback+1, 1, len(options))
	if err != nil {
		return 0, err
	}
	return choice - 1, nil
}

// Pause waits for the operator to acknowledge, so a double-clicked window on
// Windows does not vanish before its output can be read.
func (p *Prompter) Pause(message string) {
	fmt.Fprintf(p.out, "\n%s", message)
	_, _ = p.readLine()
}
