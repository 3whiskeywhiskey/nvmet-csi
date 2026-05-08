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
	args := []string{e.Target, remoteCommandString(cmd)}
	return LocalExecutor{}.Run(ctx, Command{
		Name:  "ssh",
		Args:  args,
		Stdin: cmd.Stdin,
	})
}

func remoteCommandString(cmd Command) string {
	parts := make([]string, 0, 1+len(cmd.Args))
	parts = append(parts, shellQuote(cmd.Name))
	for _, arg := range cmd.Args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

type HostNamespaceExecutor struct {
	TargetPID int
	HostPath  string
}

func (e HostNamespaceExecutor) Run(ctx context.Context, cmd Command) (string, error) {
	return LocalExecutor{}.Run(ctx, e.command(cmd))
}

func (e HostNamespaceExecutor) command(cmd Command) Command {
	targetPID := e.TargetPID
	if targetPID <= 0 {
		targetPID = 1
	}
	hostPath := e.HostPath
	if hostPath == "" {
		hostPath = "/run/current-system/sw/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}

	args := []string{
		"-t", fmt.Sprintf("%d", targetPID),
		"-m",
		"--",
		"env", "PATH=" + hostPath,
		cmd.Name,
	}
	args = append(args, cmd.Args...)

	return Command{
		Name:  "nsenter",
		Args:  args,
		Stdin: cmd.Stdin,
	}
}
