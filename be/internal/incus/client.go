// Package incus wraps the Incus client SDK to provide instance lifecycle
// management and in-VM command execution over the shared unix socket
// exposed by the incus container (see meta/incusDocker/entrypoint.sh, which
// proxies /var/lib/incus/unix.socket to /shared-socket/incus.sock).
package incus

import (
	"context"
	"fmt"
	"net/http"
	"time"

	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// Client wraps a connection to an Incus daemon.
type Client struct {
	server incusclient.InstanceServer
}

// New connects to the Incus daemon over a unix socket at socketPath. If
// socketPath is empty, the SDK falls back to $INCUS_SOCKET, then
// $INCUS_DIR/unix.socket, then the standard system socket paths.
func New(socketPath string) (*Client, error) {
	server, err := incusclient.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to incus at %q: %w", socketPath, err)
	}

	return &Client{server: server}, nil
}

// rootStoragePool is the only storage pool the appliance configures (see
// meta/incusDocker/incusStuff/incus_admin_config.yaml).
const rootStoragePool = "default"

// callWithContext runs fn — a blocking Incus SDK call that takes no context
// of its own (CreateInstance, GetInstance, ExecInstance, and friends all
// fall into this category; only the operation-wait step that typically
// follows one of these takes a context) — and returns as soon as either fn
// completes or ctx is done, whichever comes first.
//
// If ctx wins, fn's goroutine is left running in the background rather than
// truly aborted: cancelling the underlying HTTP request would mean touching
// the shared http.Client all calls funnel through, including long-running
// operation waits that legitimately need no deadline, and that's a much
// larger blast radius than the actual problem here. The actual problem is
// narrower: without this, a wedged Incus daemon blocks the calling job
// goroutine forever with no way to fail the job or clean up — this at least
// unblocks the caller so it can do that, even though the abandoned
// goroutine and its request linger until the daemon itself responds.
func callWithContext[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		val T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		val, err := fn()
		ch <- result{val, err}
	}()

	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case res := <-ch:
		return res.val, res.err
	}
}

// Launch creates a new instance from the given image alias, attaches its
// eth0 device to the given network (overriding whatever network the
// default profile would otherwise assign), sizes it with the given CPU
// count, memory limit, and root disk size (Incus config value format, e.g.
// "2", "2GiB", "20GiB" — the default profile's limits are too small for
// kubeadm's preflight checks), and starts it, blocking until the operation
// completes. The VM image grows its root filesystem to fill this disk size
// on first boot (see incus-growpart in meta/incusDocker).
func (c *Client) Launch(ctx context.Context, name, imageAlias, network, cpu, memory, diskSize string, vm bool) error {
	instanceType := api.InstanceTypeContainer
	if vm {
		instanceType = api.InstanceTypeVM
	}

	req := api.InstancesPost{
		Name:  name,
		Type:  instanceType,
		Start: true,
		Source: api.InstanceSource{
			Type:  "image",
			Alias: imageAlias,
		},
		InstancePut: api.InstancePut{
			Config: api.ConfigMap{
				"limits.cpu":    cpu,
				"limits.memory": memory,
			},
			Devices: api.DevicesMap{
				"eth0": {
					"type":    "nic",
					"network": network,
				},
				"root": {
					"type": "disk",
					"pool": rootStoragePool,
					"path": "/",
					"size": diskSize,
				},
			},
		},
	}

	op, err := callWithContext(ctx, func() (incusclient.Operation, error) {
		return c.server.CreateInstance(req)
	})
	if err != nil {
		return fmt.Errorf("create instance %q: %w", name, err)
	}

	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("launch instance %q: %w", name, err)
	}

	return nil
}

// Start starts a stopped instance.
func (c *Client) Start(ctx context.Context, name string) error {
	return c.setState(ctx, name, "start", 30, false)
}

// Stop stops a running instance. If force is false, Incus asks the guest
// OS to shut down cleanly (via ACPI/agent) before the timeout elapses.
func (c *Client) Stop(ctx context.Context, name string, force bool) error {
	return c.setState(ctx, name, "stop", 30, force)
}

func (c *Client) setState(ctx context.Context, name, action string, timeoutSeconds int, force bool) error {
	op, err := callWithContext(ctx, func() (incusclient.Operation, error) {
		return c.server.UpdateInstanceState(name, api.InstanceStatePut{
			Action:  action,
			Timeout: timeoutSeconds,
			Force:   force,
		}, "")
	})
	if err != nil {
		return fmt.Errorf("%s instance %q: %w", action, name, err)
	}

	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("%s instance %q: %w", action, name, err)
	}

	return nil
}

// Delete stops (forcefully, if needed) and deletes an instance. It's a
// no-op if the instance is already gone, so retrying a failed deletion job
// is safe even if some instances were already torn down.
func (c *Client) Delete(ctx context.Context, name string) error {
	instance, err := callWithContext(ctx, func() (*api.Instance, error) {
		instance, _, err := c.server.GetInstance(name)
		return instance, err
	})
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusNotFound) {
			return nil
		}
		return fmt.Errorf("get instance %q: %w", name, err)
	}

	if instance.StatusCode != api.Stopped {
		if err := c.Stop(ctx, name, true); err != nil {
			return err
		}
	}

	op, err := callWithContext(ctx, func() (incusclient.Operation, error) {
		return c.server.DeleteInstance(name)
	})
	if err != nil {
		return fmt.Errorf("delete instance %q: %w", name, err)
	}

	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("delete instance %q: %w", name, err)
	}

	return nil
}

// Get returns the current state of a single instance.
func (c *Client) Get(ctx context.Context, name string) (*api.Instance, error) {
	instance, err := callWithContext(ctx, func() (*api.Instance, error) {
		instance, _, err := c.server.GetInstance(name)
		return instance, err
	})
	if err != nil {
		return nil, fmt.Errorf("get instance %q: %w", name, err)
	}

	return instance, nil
}

// List returns all instances known to the daemon.
func (c *Client) List(ctx context.Context) ([]api.Instance, error) {
	instances, err := callWithContext(ctx, func() ([]api.Instance, error) {
		return c.server.GetInstances(api.InstanceTypeAny)
	})
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}

	return instances, nil
}

// WaitForIPv4 polls the instance state until the Incus agent reports a
// global-scope IPv4 address (excluding loopback), or the context is done.
// VM images must include incus-agent for this to work (the k8s image does).
func (c *Client) WaitForIPv4(ctx context.Context, name string) (string, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		state, err := callWithContext(ctx, func() (*api.InstanceState, error) {
			state, _, err := c.server.GetInstanceState(name)
			return state, err
		})
		if err == nil {
			for iface, net := range state.Network {
				if iface == "lo" {
					continue
				}

				for _, addr := range net.Addresses {
					if addr.Family == "inet" && addr.Scope == "global" {
						return addr.Address, nil
					}
				}
			}
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for IPv4 address of instance %q: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}
