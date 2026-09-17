package staticresponseprovider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	staticresponseprovider "github.com/gabay/static-response-provider"
)

func newProvider(t *testing.T, config *staticresponseprovider.Config) *staticresponseprovider.Provider {
	t.Helper()

	provider, err := staticresponseprovider.New(context.Background(), config, "test")
	if err != nil {
		t.Fatal(err)
	}

	return provider
}

func TestInit_RequiresAtLeastOneResponse(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{})

	if err := provider.Init(); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestInit_RequiresRule(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{Body: "OK"},
		},
	})

	if err := provider.Init(); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestInit_RejectsBodyAndFileTogether(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{Rule: "Host(`example.com`)", Body: "OK", File: "response.txt"},
		},
	})

	if err := provider.Init(); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestProvide_GeneratesRouterAndService(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{
				Rule:   "Host(`example.com`)",
				Body:   "OK",
				Status: 200,
				Headers: map[string]string{
					"Content-Type": "text/plain",
				},
			},
		},
	})

	if err := provider.Init(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := provider.Stop(); err != nil {
			t.Fatal(err)
		}
	})

	cfgChan := make(chan json.Marshaler)

	if err := provider.Provide(cfgChan); err != nil {
		t.Fatal(err)
	}

	payload := <-cfgChan

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	httpCfg, ok := raw["http"].(map[string]any)
	if !ok {
		t.Fatalf("expected http configuration, got %v", raw)
	}

	routers, ok := httpCfg["routers"].(map[string]any)
	if !ok || len(routers) != 1 {
		t.Fatalf("expected exactly one router, got %v", httpCfg["routers"])
	}

	services, ok := httpCfg["services"].(map[string]any)
	if !ok || len(services) != 1 {
		t.Fatalf("expected exactly one service, got %v", httpCfg["services"])
	}

	middlewares, ok := httpCfg["middlewares"].(map[string]any)
	if !ok || len(middlewares) != 1 {
		t.Fatalf("expected exactly one middleware, got %v", httpCfg["middlewares"])
	}
}

func TestServeResponse_Body(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{
				Rule:   "Host(`example.com`)",
				Body:   "hello from body",
				Status: 201,
				Headers: map[string]string{
					"X-Test": "yes",
				},
			},
		},
	})

	if err := provider.Init(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := provider.Stop(); err != nil {
			t.Fatal(err)
		}
	})

	cfgChan := make(chan json.Marshaler)

	if err := provider.Provide(cfgChan); err != nil {
		t.Fatal(err)
	}

	payload := <-cfgChan

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	serviceURL := extractServiceURL(t, raw)

	req, err := http.NewRequest(http.MethodGet, serviceURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Static-Response-Id", "0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		t.Fatalf("expected status 201, got %d", resp.StatusCode)
	}

	if got := resp.Header.Get("X-Test"); got != "yes" {
		t.Fatalf("expected header X-Test=yes, got %q", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(body) != "hello from body" {
		t.Fatalf("expected body %q, got %q", "hello from body", string(body))
	}
}

func TestServeResponse_File(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "response.txt")

	if err := os.WriteFile(filePath, []byte("hello from file"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{
				Rule: "Host(`example.com`)",
				File: filePath,
			},
		},
	})

	if err := provider.Init(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := provider.Stop(); err != nil {
			t.Fatal(err)
		}
	})

	cfgChan := make(chan json.Marshaler)

	if err := provider.Provide(cfgChan); err != nil {
		t.Fatal(err)
	}

	payload := <-cfgChan

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	serviceURL := extractServiceURL(t, raw)

	req, err := http.NewRequest(http.MethodGet, serviceURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Static-Response-Id", "0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(body) != "hello from file" {
		t.Fatalf("expected body %q, got %q", "hello from file", string(body))
	}
}

func extractServiceURL(t *testing.T, raw map[string]any) string {
	t.Helper()

	httpCfg := raw["http"].(map[string]any)
	services := httpCfg["services"].(map[string]any)

	for _, svc := range services {
		svcMap := svc.(map[string]any)
		lb := svcMap["loadBalancer"].(map[string]any)
		servers := lb["servers"].([]any)
		server := servers[0].(map[string]any)

		return server["url"].(string)
	}

	t.Fatal("no service found")

	return ""
}
