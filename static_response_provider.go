// Package staticresponseprovider contains a provider plugin that serves
// static HTTP responses (short-circuiting the request) based on Traefik
// routing rules.
package static_response_provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/traefik/genconf/dynamic"
	"github.com/traefik/genconf/dynamic/tls"
)

// responseIDHeader is an internal-only header used to tell the embedded
// HTTP server (see below) which configured response it must serve for a
// given request. It never leaves the Traefik process.
const responseIDHeader = "X-Static-Response-Id"

// readHeaderTimeout bounds how long the embedded server waits to read
// request headers, mitigating Slowloris-style attacks.
const readHeaderTimeout = 5 * time.Second

// ResponseConfig describes a single static response bound to a routing rule.
type ResponseConfig struct {
	// Rule is a standard Traefik routing rule, e.g. Host(`example.com`).
	Rule string `json:"rule,omitempty"`

	// Priority is the router priority. Falls back to Config.DefaultPriority
	// when zero.
	Priority int `json:"priority,omitempty"`

	// Body is the literal response body. Mutually exclusive with File.
	// Falls back to Config.DefaultBody/DefaultFile when both are empty.
	Body string `json:"body,omitempty"`

	// File is a path to a file whose contents are used as the response
	// body. Mutually exclusive with Body.
	File string `json:"file,omitempty"`

	// Status is the HTTP status code to reply with. Falls back to
	// Config.DefaultStatus, and then to 200, when zero.
	Status int `json:"status,omitempty"`

	// Headers are extra response headers to set. Falls back to
	// Config.DefaultHeaders when empty.
	Headers map[string]string `json:"headers,omitempty"`

	// Middlewares is a list of middleware names to apply to the router,
	// before the request is short-circuited. Falls back to
	// Config.DefaultMiddlewares when empty.
	Middlewares []string `json:"middlewares,omitempty"`
}

// Config is the plugin configuration.
type Config struct {
	Responses []ResponseConfig `json:"responses,omitempty"`

	// DefaultPriority is used for any response that doesn't set Priority.
	DefaultPriority int `json:"defaultPriority,omitempty"`

	// DefaultBody is used for any response that sets neither Body nor File.
	DefaultBody string `json:"defaultBody,omitempty"`

	// DefaultFile is used for any response that sets neither Body nor File.
	DefaultFile string `json:"defaultFile,omitempty"`

	// DefaultStatus is used for any response that doesn't set Status.
	DefaultStatus int `json:"defaultStatus,omitempty"`

	// DefaultHeaders is used for any response that doesn't set Headers.
	DefaultHeaders map[string]string `json:"defaultHeaders,omitempty"`

	// DefaultMiddlewares is used for any response that doesn't set
	// Middlewares.
	DefaultMiddlewares []string `json:"defaultMiddlewares,omitempty"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{}
}

// Provider is the static response provider plugin.
//
// Traefik plugins can only be of a single type (either "provider" or
// "middleware"), so this plugin cannot register its own plugin middleware
// to perform the short-circuit inline. Instead, it runs a single tiny HTTP
// server local to the Traefik process (bound to 127.0.0.1) that knows how
// to render every configured response, and it generates dynamic
// configuration that routes matching requests to it. A lightweight
// built-in (non-plugin) "headers" middleware is used to tag each request
// with the index of the response configuration it matched, so the embedded
// server knows what to serve. From the outside, the effect is the same as
// a short-circuiting middleware: the real backend is never contacted.
type Provider struct {
	name      string
	responses []ResponseConfig

	listener net.Listener
	server   *http.Server
	cancel   func()
}

// New creates a new Provider plugin.
func New(_ context.Context, config *Config, name string) (*Provider, error) {
	responses := make([]ResponseConfig, 0, len(config.Responses))
	for _, r := range config.Responses {
		responses = append(responses, applyDefaults(r, config))
	}

	return &Provider{
		name:      name,
		responses: responses,
	}, nil
}

// applyDefaults fills in the fields of a response that weren't set, using
// the plugin-wide defaults.
func applyDefaults(r ResponseConfig, config *Config) ResponseConfig {
	if r.Priority == 0 {
		r.Priority = config.DefaultPriority
	}

	if r.Body == "" && r.File == "" {
		r.Body = config.DefaultBody
		r.File = config.DefaultFile
	}

	if r.Status == 0 {
		r.Status = config.DefaultStatus
	}

	if len(r.Headers) == 0 {
		r.Headers = config.DefaultHeaders
	}

	if len(r.Middlewares) == 0 {
		r.Middlewares = config.DefaultMiddlewares
	}

	return r
}

// Init the provider.
func (p *Provider) Init() error {
	if len(p.responses) == 0 {
		return fmt.Errorf("at least one response must be configured")
	}

	for i, r := range p.responses {
		if r.Rule == "" {
			return fmt.Errorf("response %d: rule is required", i)
		}

		if r.Body != "" && r.File != "" {
			return fmt.Errorf("response %d: body and file are mutually exclusive", i)
		}

		if r.Status < 0 {
			return fmt.Errorf("response %d: invalid status %d", i, r.Status)
		}
	}

	return nil
}

// Provide creates and sends the dynamic configuration, and starts the
// embedded HTTP server that serves the static responses.
func (p *Provider) Provide(cfgChan chan<- json.Marshaler) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start internal listener: %w", err)
	}
	p.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/", p.serveResponse)
	p.server = &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}

	go func() {
		if serveErr := p.server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "static-response-provider: server error: %v\n", serveErr)
		}
	}()

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Fprintf(os.Stderr, "static-response-provider: panic: %v\n", rec)
			}
		}()

		cfgChan <- &dynamic.JSONPayload{Configuration: p.generateConfiguration()}

		<-ctx.Done()
	}()

	return nil
}

// Stop stops the provider and the embedded HTTP server.
func (p *Provider) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}

	if p.server != nil {
		return p.server.Close()
	}

	return nil
}

// serveResponse is the handler of the embedded HTTP server. It looks up
// which response configuration matched (via responseIDHeader, set by a
// dedicated "headers" middleware in the generated dynamic configuration)
// and writes it out.
func (p *Provider) serveResponse(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.Header.Get(responseIDHeader))
	if err != nil || idx < 0 || idx >= len(p.responses) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	resp := p.responses[idx]

	body := []byte(resp.Body)
	if resp.File != "" {
		content, readErr := os.ReadFile(resp.File)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "static-response-provider: failed to read file %q: %v\n", resp.File, readErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		body = content
	}

	for name, value := range resp.Headers {
		w.Header().Set(name, value)
	}

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}

	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (p *Provider) generateConfiguration() *dynamic.Configuration {
	configuration := &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers:           make(map[string]*dynamic.Router),
			Middlewares:       make(map[string]*dynamic.Middleware),
			Services:          make(map[string]*dynamic.Service),
			ServersTransports: make(map[string]*dynamic.ServersTransport),
		},
		TCP: &dynamic.TCPConfiguration{
			Routers:  make(map[string]*dynamic.TCPRouter),
			Services: make(map[string]*dynamic.TCPService),
		},
		TLS: &dynamic.TLSConfiguration{
			Stores:  make(map[string]tls.Store),
			Options: make(map[string]tls.Options),
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  make(map[string]*dynamic.UDPRouter),
			Services: make(map[string]*dynamic.UDPService),
		},
	}

	const serviceName = "static-response-service"

	configuration.HTTP.Services[serviceName] = &dynamic.Service{
		LoadBalancer: &dynamic.ServersLoadBalancer{
			Servers: []dynamic.Server{
				{URL: "http://" + p.listener.Addr().String()},
			},
			PassHostHeader: boolPtr(true),
		},
	}

	for i, resp := range p.responses {
		routerName := fmt.Sprintf("static-response-router-%d", i)
		middlewareName := fmt.Sprintf("static-response-headers-%d", i)

		configuration.HTTP.Middlewares[middlewareName] = &dynamic.Middleware{
			Headers: &dynamic.Headers{
				CustomRequestHeaders: map[string]string{
					responseIDHeader: strconv.Itoa(i),
				},
			},
		}

		// The user-configured middlewares run first (e.g. auth), and the
		// tagging middleware runs last, right before the request reaches
		// the embedded server.
		middlewares := make([]string, 0, len(resp.Middlewares)+1)
		middlewares = append(middlewares, resp.Middlewares...)
		middlewares = append(middlewares, middlewareName)

		configuration.HTTP.Routers[routerName] = &dynamic.Router{
			Rule:        resp.Rule,
			Priority:    resp.Priority,
			Service:     serviceName,
			Middlewares: middlewares,
		}
	}

	return configuration
}

func boolPtr(v bool) *bool {
	return &v
}
