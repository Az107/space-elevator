package podman

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/docker/go-connections/nat"
)

// ContainerIP is the network info we collect per container for a given
// service: the IP on its primary network and any host-published port.
type ContainerIP struct {
	ID       string
	Name     string
	IP       string
	Networks []string
	// HostPort is retained for compatibility with older callers. It is the
	// first published port in a stable order, not whichever Docker map entry
	// happens to be visited first.
	HostPort string
	// HostPorts maps container-side port/protocol keys (for example
	// "8080/tcp") to their published host ports. Keeping the mapping lets
	// route resolution select the host port belonging to the actual service
	// port instead of an unrelated port from the same container.
	HostPorts map[string]string
}

// InspectIPs returns the IP addresses and host-published ports of all
// containers matching the given label selector, keyed by the
// "space-elevator.service" label value when present.
func (c *Client) InspectIPs(ctx context.Context, labelSelector string) (map[string]ContainerIP, error) {
	// Only running containers may back a route. Including stopped
	// containers caused startup reconciliation to publish a dead endpoint
	// whenever a stale container object survived a stop or failed update.
	cs, err := c.ListContainersFiltered(ctx, false, map[string][]string{"label": {labelSelector}})
	if err != nil {
		return nil, err
	}
	out := map[string]ContainerIP{}
	for _, ctr := range cs {
		full, err := c.LookupID(ctx, ctr.ID)
		if err != nil {
			// Skip only a container that vanished mid-scan. Swallowing a
			// transport error here silently drops the service, and
			// ResolveForApp then removes its Traefik route — turning a Podman
			// hiccup into a dashboard-visible 404.
			if errors.Is(err, ErrContainerNotFound) {
				continue
			}
			return nil, err
		}
		inspect, err := c.cli.ContainerInspect(ctx, full)
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", ctr.ID, err)
		}
		ip := ContainerIP{ID: full, Name: ctr.Name}
		for netName, net := range inspect.NetworkSettings.Networks {
			ip.Networks = append(ip.Networks, netName)
			if ip.IP == "" && net.IPAddress != "" {
				ip.IP = net.IPAddress
			}
		}
		// Docker exposes published ports as a map, so iterating it directly
		// makes the selected host port nondeterministic. Sort the container
		// port keys and retain both the legacy first value and a precise map.
		ip.HostPorts = make(map[string]string)
		portKeys := make([]string, 0, len(inspect.NetworkSettings.Ports))
		for portKey := range inspect.NetworkSettings.Ports {
			portKeys = append(portKeys, string(portKey))
		}
		sort.Strings(portKeys)
		for _, portKey := range portKeys {
			for _, binding := range inspect.NetworkSettings.Ports[nat.Port(portKey)] {
				if binding.HostPort == "" {
					continue
				}
				ip.HostPorts[portKey] = binding.HostPort
				if ip.HostPort == "" {
					ip.HostPort = binding.HostPort
				}
				break
			}
		}
		if svc, ok := inspect.Config.Labels["space-elevator.service"]; ok {
			out[svc] = ip
		} else {
			out[full] = ip
		}
	}
	return out, nil
}
