package incus

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// ExecResult holds the outcome of a command run inside an instance.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// WaitForAgent polls the instance with a trivial command until the
// incus-agent inside the guest responds, or the context is done. The agent
// typically isn't up yet even after WaitForIPv4 returns (network comes up
// before the agent connects), so call this before the first real Exec.
func (c *Client) WaitForAgent(ctx context.Context, name string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		_, err := c.Exec(ctx, name, []string{"true"}, nil)
		if err == nil {
			return nil
		}

		if !strings.Contains(err.Error(), "agent isn't currently running") {
			return fmt.Errorf("waiting for agent on instance %q: %w", name, err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for agent on instance %q: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Exec runs a non-interactive command inside the named instance and waits
// for it to finish, capturing stdout/stderr and the exit code. Requires the
// incus-agent to be running inside the guest (present on the k8s VM image).
func (c *Client) Exec(ctx context.Context, name string, command []string, env map[string]string) (*ExecResult, error) {
	var stdout, stderr bytes.Buffer

	req := api.InstanceExecPost{
		Command:     command,
		Environment: env,
		WaitForWS:   true,
		Interactive: false,
	}

	dataDone := make(chan bool)
	args := incusclient.InstanceExecArgs{
		Stdin:    bytes.NewReader(nil),
		Stdout:   &stdout,
		Stderr:   &stderr,
		DataDone: dataDone,
	}

	op, err := callWithContext(ctx, func() (incusclient.Operation, error) {
		return c.server.ExecInstance(name, req, &args)
	})
	if err != nil {
		return nil, fmt.Errorf("exec in instance %q: %w", name, err)
	}

	waitErr := op.WaitContext(ctx)

	// Wait for I/O to be flushed before reading the buffers, even if the
	// operation itself failed.
	<-dataDone

	result := &ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if opAPI := op.Get(); opAPI.Metadata != nil {
		if exitCode, ok := opAPI.Metadata["return"].(float64); ok {
			result.ExitCode = int(exitCode)
		}
	}

	if waitErr != nil {
		return result, fmt.Errorf("exec in instance %q: %w", name, waitErr)
	}

	return result, nil
}

// Run behaves like Exec but additionally treats a non-zero exit code as an
// error (including stderr for debuggability). Use Exec directly when the
// caller needs to inspect a non-zero exit itself (e.g. polling for
// readiness where "not ready yet" is expected, not exceptional).
func (c *Client) Run(ctx context.Context, name string, command []string) (*ExecResult, error) {
	result, err := c.Exec(ctx, name, command, nil)
	if err != nil {
		return result, err
	}

	if result.ExitCode != 0 {
		return result, fmt.Errorf("command %q in instance %q exited %d: %s", strings.Join(command, " "), name, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	return result, nil
}
