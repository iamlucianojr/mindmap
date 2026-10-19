package main

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Prompt carries the trail from the root as well as the question. "What does
// this mean?" is unanswerable on its own; with "5 Attack / Attack lines / the
// outage rule" above it, it is not.
type Prompt struct {
	Question string
	Path     []string
}

func (p Prompt) Text() string {
	var b strings.Builder
	if len(p.Path) > 1 {
		b.WriteString("This question is about a node in a mind map.\n")
		b.WriteString("Its place in the map, outermost first:\n\n  ")
		b.WriteString(strings.Join(p.Path, "  >  "))
		b.WriteString("\n\n")
	}
	b.WriteString(p.Question)
	return b.String()
}

// Model is the only thing the queue knows about answering. Swapping the fake
// for something real must not touch a line of queue code.
type Model interface {
	Ask(ctx context.Context, p Prompt, out chan<- string) (string, error)
}

// FakeModel answers instantly and for free, which is what makes the queue
// testable without credentials, cost, or waiting.
type FakeModel struct{ Delay time.Duration }

func (f FakeModel) Ask(ctx context.Context, p Prompt, out chan<- string) (string, error) {
	words := strings.Fields(fmt.Sprintf(
		"Answering %q with no model attached. The queue, the worker and the stream are real; only this text is not. Swap in a model with -model=claude.",
		p.Question))

	var sb strings.Builder
	for i, w := range words {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(f.Delay):
		}
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(w)
		out <- w + " "
	}
	return sb.String(), nil
}

// ClaudeCLI shells out, which is slower to start than the HTTP API but reads
// the credentials already on this machine instead of holding a token that
// expires in eight hours.
type ClaudeCLI struct{ Bin string }

func (c ClaudeCLI) Ask(ctx context.Context, p Prompt, out chan<- string) (string, error) {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, "-p", p.Text())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", err
	}

	var sb strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		sb.WriteString(line)
		sb.WriteString("\n")
		out <- line + "\n"
	}
	if err := cmd.Wait(); err != nil {
		// The exit code alone says nothing useful; stderr says what happened.
		return "", fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(sb.String()), nil
}

// PiAgent answers through the agent already configured on this machine.
//
// Three reasons it beats talking to the API directly. Its credentials are the
// ones a working session already refreshes, so there is no token to hold and
// expire. It brings the house context with it, which means an answer about a
// payments flow knows what the payments flow is. And it has tools, so a job
// can read a repository rather than guess at one from its prompt.
//
// Tools are off for a question and on for a station, because a question wants
// an opinion and a station has to go and look.
type PiAgent struct {
	Bin   string
	Tools bool
}

func (a PiAgent) Ask(ctx context.Context, p Prompt, out chan<- string) (string, error) {
	bin := a.Bin
	if bin == "" {
		bin = "pi"
	}
	args := []string{"-p"}
	if !a.Tools {
		// -ne keeps the startup quiet as well as fast: extension discovery is
		// most of the time a short question takes.
		args = append(args, "-ne", "-nt")
	}
	args = append(args, p.Text())

	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	// Model-pattern warnings are written to stderr on every run and are not
	// errors. They are kept only to explain a failure, never shown on success.
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", err
	}

	var sb strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		sb.WriteString(line)
		sb.WriteString("\n")
		out <- line + "\n"
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", bin, err, lastLines(stderr.String(), 3))
	}

	answer := strings.TrimSpace(sb.String())
	if answer == "" {
		// An empty answer with a zero exit is the shape an auth failure takes.
		return "", fmt.Errorf("%s produced no answer: %s", bin, lastLines(stderr.String(), 3))
	}
	return answer, nil
}

// lastLines keeps the end of a noisy stream, where the real complaint is.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}
