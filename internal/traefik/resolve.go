package traefik

import (
	"context"
	"fmt"
	"strconv"

	"github.com/docker/go-connections/nat"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/podman"
)

// ResolveForApp returns the per-service backend routes for an app. For each
// compose service that declares ports, the backend URL is one of:
//
//   - "<rootlessGateway>:<hostPort>"  if a host-published port exists
//     (preferred — works with rootful Traefik even when the container is
//     on an isolated per-app rootless bridge)
//   - "<containerIP>:<containerPort>" otherwise (works when the container
//     shares a network with the rootful Traefik container, e.g. the
//     shared `traefik-net` rootless bridge)
//
// rootlessGateway is typically the rootless podman bridge gateway
// (default "10.89.0.1"); pass "" to fall back to the container IP.
func ResolveForApp(ctx context.Context, cli *podman.Client, appName string, spec *composer.Spec, domains []string, rootlessGateway string) ([]ServiceRoute, error) {
	if cli == nil {
		return nil, fmt.Errorf("Podman client is not configured")
	}
	if spec == nil {
		return nil, fmt.Errorf("compose spec is not configured")
	}
	if appName == "" {
		return nil, fmt.Errorf("app name is required")
	}
	ips, err := cli.InspectIPs(ctx, "space-elevator.app="+appName)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceRoute, 0, len(spec.Services))
	for svcName, svc := range spec.Services {
		if len(svc.Ports) == 0 {
			continue
		}
		ip, ok := ips[svcName]
		if !ok {
			continue
		}
		ports, err := containerPorts(svc.Ports)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svcName, err)
		}
		containerPort, hostPort, ok := reachablePort(ip, ports, rootlessGateway)
		if !ok {
			// A stopped/no-network container is not routable. This also
			// prevents startup reconciliation from retaining a stale route.
			continue
		}
		backendIP := ip.IP
		backendPort := containerPort
		if hostPort != "" && rootlessGateway != "" {
			if n, err := strconv.Atoi(hostPort); err == nil {
				backendIP = rootlessGateway
				backendPort = n
			}
		}
		if backendIP == "" || backendPort == 0 {
			continue
		}
		out = append(out, ServiceRoute{
			AppName: appName,
			Name:    svcName,
			Domain:  domains,
			IP:      backendIP,
			Port:    backendPort,
		})
	}
	return out, nil
}

// containerPorts returns every container-side port in declaration order.
// The host binding is intentionally discarded: it is not necessarily the
// port Traefik should use for a service with multiple exposed ports.
func containerPorts(ports []string) ([]int, error) {
	var out []int
	for _, p := range ports {
		parsed, err := nat.ParsePortSpec(p)
		if err != nil {
			return nil, err
		}
		for _, pm := range parsed {
			if n := pm.Port.Int(); n > 0 {
				out = append(out, n)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no ports declared")
	}
	return out, nil
}

func hostPortFor(ip podman.ContainerIP, containerPort int) string {
	if ip.HostPorts != nil {
		prefix := strconv.Itoa(containerPort) + "/"
		for key, hostPort := range ip.HostPorts {
			if key == strconv.Itoa(containerPort) || (len(key) > len(prefix) && key[:len(prefix)] == prefix) {
				return hostPort
			}
		}
	}
	return ""
}

func reachablePort(ip podman.ContainerIP, ports []int, gateway string) (containerPort int, hostPort string, ok bool) {
	for _, port := range ports {
		host := hostPortFor(ip, port)
		if host != "" && gateway != "" {
			return port, host, true
		}
		if ip.IP != "" {
			return port, "", true
		}
	}
	// Older inspect responses only had the single HostPort field. It is
	// safe to use it when the service declared one port and the container has
	// a usable network address.
	if len(ports) == 1 && ip.HostPort != "" && gateway != "" {
		return ports[0], ip.HostPort, true
	}
	return 0, "", false
}
