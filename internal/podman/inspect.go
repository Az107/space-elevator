package podman

import (
	"context"
	"fmt"
)

// ContainerIP is the network info we collect per container for a given
// service: the IP on its primary network and any host-published port.
type ContainerIP struct {
	ID       string
	Name     string
	IP       string
	Networks []string
	// HostPort is the first host port Podman published for the container's
	// declared port (e.g. "34909" when the compose says `ports: ["8080"]`).
	// Empty when no host port is published.
	HostPort string
}

// InspectIPs returns the IP addresses and host-published ports of all
// containers matching the given label selector, keyed by the
// "space-elevator.service" label value when present.
func (c *Client) InspectIPs(ctx context.Context, labelSelector string) (map[string]ContainerIP, error) {
	cs, err := c.ListContainersFiltered(ctx, true, map[string][]string{"label": {labelSelector}})
	if err != nil {
		return nil, err
	}
	out := map[string]ContainerIP{}
	for _, ctr := range cs {
		full, err := c.LookupID(ctx, ctr.ID)
		if err != nil {
			continue
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
		// First declared port, in order — compose ports are usually one
		// service → one port. NetworkSettings.Ports is keyed by the
		// container-side "port/proto" string; values are host bindings.
		for _, bindings := range inspect.NetworkSettings.Ports {
			if len(bindings) == 0 {
				continue
			}
			ip.HostPort = bindings[0].HostPort
			break
		}
		if svc, ok := inspect.Config.Labels["space-elevator.service"]; ok {
			out[svc] = ip
		} else {
			out[full] = ip
		}
	}
	return out, nil
}
