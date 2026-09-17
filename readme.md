[![Build Status](https://github.com/gabay/static-response-provider/workflows/Main/badge.svg?branch=master)](https://github.com/gabay/static-response-provider/actions)

# Static Response Provider

A [Traefik](https://traefik.io) **provider plugin** that lets you declare static HTTP
responses directly in the Traefik static configuration. Any request matching a
configured [Traefik rule](https://doc.traefik.io/traefik/routing/routers/#rule) is
short-circuited: it never reaches a real backend, and instead gets back the
configured body (or file contents), status code, and headers.

Traefik plugins are developed using the [Go language](https://golang.org).
Rather than being pre-compiled and linked, however, plugins are executed on the fly by
[Yaegi](https://github.com/traefik/yaegi), an embedded Go interpreter.

## Usage

For a plugin to be active for a given Traefik instance, it must be declared in the static configuration.

```yaml
# Static configuration

experimental:
  plugins:
    static-response-provider:
      moduleName: github.com/gabay/static-response-provider
      version: v0.1.0

providers:
  plugin:
    static-response-provider:
      responses:
        - rule: Host(`example.com`)
          body: 'OK'
          status: 200
          headers:
            content-type: text/plain
        - rule: Host(`another.example.com`)
          file: static-response.txt
          status: 200
```

### Configuration reference

Each entry under `responses` supports:

| Field     | Required | Description                                                                 |
|-----------|----------|-------------------------------------------------------------------------------|
| `rule`    | yes      | A standard Traefik routing rule, e.g. `` Host(`example.com`) ``.              |
| `body`    | no       | The literal response body. Mutually exclusive with `file`.                    |
| `file`    | no       | Path to a file on disk whose contents are used as the response body.          |
| `status`  | no       | HTTP status code to return. Defaults to `200`.                                |
| `headers` | no       | Map of extra response headers to set.                                         |

Exactly one of `body` or `file` may be set for a given response (both may be
omitted for an empty body).

### Local Mode

Traefik also offers a developer mode that can be used for temporary testing of plugins not hosted on GitHub.
To use a plugin in local mode, the Traefik static configuration must define the module name (as is usual for Go packages) and a path to a [Go workspace](https://golang.org/doc/gopath_code.html#Workspaces), which can be the local GOPATH or any directory.

The plugin must be placed in the `./plugins-local` directory, which should be in the working directory of the process running the Traefik binary:

```
./plugins-local/
    └── src
        └── github.com
            └── gabay
                └── static-response-provider
                    ├── static_response.go
                    ├── static_response_test.go
                    ├── go.mod
                    ├── go.sum
                    ├── LICENSE
                    ├── Makefile
                    ├── readme.md
                    └── vendor
                        └── ...
```

```yaml
# Static configuration
# Local mode
entryPoints:
  web:
    address: :80

log:
  level: DEBUG

experimental:
  localPlugins:
    static-response-provider:
      moduleName: github.com/gabay/static-response-provider

providers:
  plugin:
    static-response-provider:
      responses:
        - rule: Host(`example.com`)
          body: 'OK'
```

## How it works

Traefik plugins can only be of a single declared type: either `provider` or
`middleware` (see the [manifest documentation](https://plugins.traefik.io/create)).
A single plugin repository can therefore not register its own inline plugin
*middleware* to perform the short-circuit — doing so would require the same
module to be loaded both as a `provider` and as a `middleware`, which Traefik's
plugin loader does not support (the plugin type is fixed by the single
`.traefik.yml` manifest of the module).

To still get short-circuiting behavior out of a single, provider-only plugin,
this plugin:

1. Starts a tiny HTTP server bound to `127.0.0.1` (loopback only), local to the
   Traefik process, that knows how to render each configured response.
2. Generates, for every configured response, a Traefik `router` (using your
   `rule`), a built-in (non-plugin) `headers` middleware that tags the request
   with which response it matched, and points the router at a single internal
   `service` that targets the embedded server from step 1.

Because the embedded server never talks to your real backends, the effect is
functionally the same as a short-circuiting middleware — matching requests
never leave the Traefik process — while staying within the constraints of a
single provider-type plugin.

If you need this to be a "true" zero-hop middleware short-circuit (no local
HTTP round trip at all), the alternative is to split this into two separate
plugin repositories: one `type: middleware` plugin implementing
`New(ctx, next, config, name) (http.Handler, error)` that writes the static
response directly, and one `type: provider` plugin (or plain static/file
provider configuration) that declares routers referencing that middleware via
the `plugin` field of `dynamic.Middleware`. That requires publishing two
plugins instead of one.

## Defining a Plugin

A provider plugin package must define the following exported Go objects:

- A type `type Config struct { ... }`. The struct fields are arbitrary.
- A function `func CreateConfig() *Config`.
- A function `New(ctx context.Context, config *Config, name string) (*Provider, error)`.

The provider must follow this interface:

```go
type PluginProvider interface {
	Init() error
	Provide(cfgChan chan<- json.Marshaler) error
	Stop() error
}
```

The Go objects used to build the dynamic configuration are in the following repository: https://github.com/traefik/genconf

## Logs

Currently, the only way to send logs to Traefik is to use `os.Stdout.WriteString("...")` or `os.Stderr.WriteString("...")`.

## Plugins Catalog

Traefik plugins are stored and hosted as public GitHub repositories.

Once a day, the Plugins Catalog online service polls Github to find plugins and add them to its catalog.

### Prerequisites

To be recognized by Plugins Catalog, your repository must meet the following criteria:

- The `traefik-plugin` topic must be set.
- The `.traefik.yml` manifest must exist, and be filled with valid contents.

### Tags and Dependencies

Plugins Catalog gets your sources from a Go module proxy, so your plugins need to be versioned with a git tag.

Last but not least, if your plugin has Go package dependencies, you need to vendor them and add them to your GitHub repository.
