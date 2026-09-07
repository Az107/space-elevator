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
		if !ok || ip.IP == "" {
			continue
		}
		containerPort, err := firstContainerPort(svc.Ports)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svcName, err)
		}
		// Prefer gateway+host-port so rootful Traefik can reach the
		// rootless container via the host's published port.
		backendIP := ip.IP
		backendPort := containerPort
		if ip.HostPort != "" && rootlessGateway != "" {
			if n, err := strconv.Atoi(ip.HostPort); err == nil {
				backendIP = rootlessGateway
				backendPort = n
			}
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

// firstContainerPort returns the first container port from a compose ports list,
// preserving the numeric port and ignoring host binding.
func firstContainerPort(ports []string) (int, error) {
	for _, p := range ports {
		parsed, err := nat.ParsePortSpec(p)
		if err != nil {
			return 0, err
		}
		for _, pm := range parsed {
			return pm.Port.Int(), nil
		}
	}
	return 0, fmt.Errorf("no ports declared")
}
