package linuxzol

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Command struct {
	Name  string
	Args  []string
	Stdin string
}

func (c Command) String() string {
	if len(c.Args) == 0 {
		return c.Name
	}
	return c.Name + " " + strings.Join(c.Args, " ")
}

type Executor interface {
	Run(ctx context.Context, cmd Command) (string, error)
}

type LocalExecutor struct{}

func (e LocalExecutor) Run(ctx context.Context, cmd Command) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	if cmd.Stdin != "" {
		command.Stdin = strings.NewReader(cmd.Stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("%s failed: %s: %w", cmd.String(), msg, err)
		}
		return stdout.String(), fmt.Errorf("%s failed: %w", cmd.String(), err)
	}

	return stdout.String(), nil
}

type SSHExecutor struct {
	Target string
}

func (e SSHExecutor) Run(ctx context.Context, cmd Command) (string, error) {
	if e.Target == "" {
		return "", fmt.Errorf("SSH target is required")
	}
	args := append([]string{e.Target, cmd.Name}, cmd.Args...)
	return LocalExecutor{}.Run(ctx, Command{
		Name:  "ssh",
		Args:  args,
		Stdin: cmd.Stdin,
	})
}
