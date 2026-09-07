package traefik

import (
	"context"
	"fmt"

	"github.com/albertoruiz/space-elevator/internal/composer"
	"github.com/albertoruiz/space-elevator/internal/podman"
)

// AppOptions bundles the per-app inputs that every call site needs in order
// to (re)write a Traefik dynamic file. Wrapping them in one struct keeps the
// call sites uniform and lets us add fields without touching every caller.
type AppOptions struct {
	Writer          *Writer
	Client          *podman.Client
	AppName         string
	Spec            *composer.Spec
	Domains         []string
	PublicHost      string
	AppPathPrefix   string
	RootlessGateway string
}

// ApplyAppRoute regenerates the dynamic file for an app. It returns:
//   - (true, nil)  if a file was written
//   - (false, nil) if the app has neither a custom domain nor a path-prefix
//     configured, in which case any existing file is removed
//   - (false, err) on failure
func ApplyAppRoute(ctx context.Context, opts AppOptions) (bool, error) {
	routes, err := ResolveForApp(ctx, opts.Client, opts.AppName, opts.Spec, opts.Domains, opts.RootlessGateway)
	if err != nil {
		return false, fmt.Errorf("resolve: %w", err)
	}
	if len(routes) == 0 {
		return false, opts.Writer.Remove(opts.AppName)
	}
	if len(opts.Domains) == 0 && (opts.PublicHost == "" || opts.AppPathPrefix == "") {
		return false, opts.Writer.Remove(opts.AppName)
	}
	cfg := AppRouteConfig{
		Routes:        routes,
		PublicHost:    opts.PublicHost,
		AppPathPrefix: opts.AppPathPrefix,
	}
	if err := opts.Writer.Write(opts.AppName, cfg); err != nil {
		return false, err
	}
	return true, nil
}
