package incus

import (
	"context"
	"fmt"

	"github.com/lxc/incus/v7/shared/api"
)

// ListNetworks returns every network known to the Incus daemon, including
// ones this application didn't create (e.g. the appliance's own bridge).
func (c *Client) ListNetworks(ctx context.Context) ([]api.Network, error) {
	networks, err := callWithContext(ctx, func() ([]api.Network, error) {
		return c.server.GetNetworks()
	})
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}

	return networks, nil
}

// GetNetwork returns a single network by name.
func (c *Client) GetNetwork(ctx context.Context, name string) (*api.Network, error) {
	network, err := callWithContext(ctx, func() (*api.Network, error) {
		network, _, err := c.server.GetNetwork(name)
		return network, err
	})
	if err != nil {
		return nil, fmt.Errorf("get network %q: %w", name, err)
	}

	return network, nil
}

// CreateNetwork creates a new bridge network with the given config (e.g.
// "ipv4.address": "10.10.0.1/24").
func (c *Client) CreateNetwork(ctx context.Context, name string, config map[string]string) error {
	req := api.NetworksPost{
		Name: name,
		Type: "bridge",
		NetworkPut: api.NetworkPut{
			Config: config,
		},
	}

	_, err := callWithContext(ctx, func() (struct{}, error) {
		return struct{}{}, c.server.CreateNetwork(req)
	})
	if err != nil {
		return fmt.Errorf("create network %q: %w", name, err)
	}

	return nil
}

// DeleteNetwork deletes a network by name.
func (c *Client) DeleteNetwork(ctx context.Context, name string) error {
	_, err := callWithContext(ctx, func() (struct{}, error) {
		return struct{}{}, c.server.DeleteNetwork(name)
	})
	if err != nil {
		return fmt.Errorf("delete network %q: %w", name, err)
	}

	return nil
}
