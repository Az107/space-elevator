package traefik

import (
	"testing"

	"github.com/albertoruiz/space-elevator/internal/podman"
)

func TestReachablePortUsesMatchingPublishedPort(t *testing.T) {
	ip := podman.ContainerIP{
		IP:       "10.89.0.2",
		HostPort: "40000",
		HostPorts: map[string]string{
			"9000/tcp": "41000",
			"8080/tcp": "42000",
		},
	}
	containerPort, hostPort, ok := reachablePort(ip, []int{8080, 9000}, "10.89.0.1")
	if !ok || containerPort != 8080 || hostPort != "42000" {
		t.Fatalf("reachablePort = (%d, %q, %v), want (8080, 42000, true)", containerPort, hostPort, ok)
	}
}

func TestReachablePortDoesNotUseUnrelatedHostPort(t *testing.T) {
	ip := podman.ContainerIP{IP: "10.89.0.2", HostPort: "40000", HostPorts: map[string]string{"9000/tcp": "41000"}}
	containerPort, hostPort, ok := reachablePort(ip, []int{8080}, "10.89.0.1")
	if !ok || containerPort != 8080 || hostPort != "" {
		t.Fatalf("reachablePort = (%d, %q, %v), want direct IP route", containerPort, hostPort, ok)
	}
}

func TestReachablePortUsesPublishedPortWithoutContainerIP(t *testing.T) {
	ip := podman.ContainerIP{HostPort: "40000", HostPorts: map[string]string{"8080/tcp": "40000"}}
	containerPort, hostPort, ok := reachablePort(ip, []int{8080}, "10.89.0.1")
	if !ok || containerPort != 8080 || hostPort != "40000" {
		t.Fatalf("reachablePort = (%d, %q, %v), want (8080, 40000, true)", containerPort, hostPort, ok)
	}
}
