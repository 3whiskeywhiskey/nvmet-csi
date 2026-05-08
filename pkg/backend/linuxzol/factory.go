package linuxzol

import (
	"fmt"

	storagebackend "git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
)

type CommandBackendOptions struct {
	Config    Config
	Executor  string
	SSHTarget string
}

func NewCommandBackend(opts CommandBackendOptions) (*Backend, error) {
	host, err := NewCommandHostFromOptions(opts)
	if err != nil {
		return nil, err
	}
	return New(opts.Config, host)
}

func NewCommandHostFromOptions(opts CommandBackendOptions) (*CommandHost, error) {
	executorType := opts.Executor
	if executorType == "" {
		executorType = storagebackend.ExecutorLocal
	}

	var executor Executor
	switch executorType {
	case storagebackend.ExecutorLocal:
		executor = LocalExecutor{}
	case storagebackend.ExecutorHostNamespace:
		executor = HostNamespaceExecutor{}
	case storagebackend.ExecutorSSH:
		if opts.SSHTarget == "" {
			return nil, fmt.Errorf("SSH target is required for %s executor", storagebackend.ExecutorSSH)
		}
		executor = SSHExecutor{Target: opts.SSHTarget}
	default:
		return nil, fmt.Errorf("unsupported Linux ZoL executor %q", executorType)
	}

	return NewCommandHost(opts.Config, executor)
}
