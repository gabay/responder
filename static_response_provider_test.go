package static_response_provider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	staticresponseprovider "github.com/gabay/static-response-provider"
)

const (
	testBodyOK         = "OK"
	testRuleExample    = "Host(`example.com`)"
	testDefaultBody    = "default body"
	testDefaultHeaderY = "yes"
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
			{Body: testBodyOK},
		},
	})

	if err := provider.Init(); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestInit_RejectsBodyAndFileTogether(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{Rule: testRuleExample, Body: testBodyOK, File: "response.txt"},
		},
	})

	if err := provider.Init(); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// provideConfig starts the provider and returns its generated dynamic
// configuration as a generic map, along with a cleanup func that stops it.
func provideConfig(t *testing.T, provider *staticresponseprovider.Provider) map[string]any {
	t.Helper()

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

	return raw
}

func TestProvide_GeneratesOneServiceAndOneRouterMiddlewarePerResponse(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{Rule: "Host(`a.example.com`)", Body: "A"},
			{Rule: "Host(`b.example.com`)", Body: "B"},
		},
	})

	raw := provideConfig(t, provider)

	httpCfg := raw["http"].(map[string]any)

	routers := httpCfg["routers"].(map[string]any)
	if len(routers) != 2 {
		t.Fatalf("expected 2 routers, got %v", routers)
	}

	// A single shared service/server is used for every response.
	services := httpCfg["services"].(map[string]any)
	if len(services) != 1 {
		t.Fatalf("expected exactly 1 shared service, got %v", services)
	}

	// One tagging middleware is generated per response.
	middlewares := httpCfg["middlewares"].(map[string]any)
	if len(middlewares) != 2 {
		t.Fatalf("expected 2 middlewares, got %v", middlewares)
	}
}

func TestProvide_RouterUsesPriorityAndMiddlewares(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{
				Rule:        testRuleExample,
				Body:        testBodyOK,
				Priority:    42,
				Middlewares: []string{"my-middleware@file"},
			},
		},
	})

	raw := provideConfig(t, provider)

	httpCfg := raw["http"].(map[string]any)
	routers := httpCfg["routers"].(map[string]any)

	var router map[string]any
	for _, r := range routers {
		router = r.(map[string]any)
	}

	if router["priority"].(float64) != 42 {
		t.Fatalf("expected priority 42, got %v", router["priority"])
	}

	middlewares := router["middlewares"].([]any)
	if len(middlewares) != 2 {
		t.Fatalf("expected 2 middlewares (user + tag), got %v", middlewares)
	}

	if middlewares[0].(string) != "my-middleware@file" {
		t.Fatalf("expected user middleware to run first, got %v", middlewares)
	}
}

func TestProvide_AppliesDefaults(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		DefaultPriority:    7,
		DefaultBody:        testDefaultBody,
		DefaultStatus:      201,
		DefaultHeaders:     map[string]string{"X-Default": testDefaultHeaderY},
		DefaultMiddlewares: []string{"default-middleware@file"},
		Responses: []staticresponseprovider.ResponseConfig{
			{Rule: testRuleExample},
		},
	})

	raw := provideConfig(t, provider)

	resp := doRequest(t, raw, 0)
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		t.Fatalf("expected status 201, got %d", resp.StatusCode)
	}

	if got := resp.Header.Get("X-Default"); got != testDefaultHeaderY {
		t.Fatalf("expected header X-Default=yes, got %q", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(body) != testDefaultBody {
		t.Fatalf("expected body %q, got %q", testDefaultBody, string(body))
	}

	httpCfg := raw["http"].(map[string]any)
	routers := httpCfg["routers"].(map[string]any)

	var router map[string]any
	for _, r := range routers {
		router = r.(map[string]any)
	}

	if router["priority"].(float64) != 7 {
		t.Fatalf("expected default priority 7, got %v", router["priority"])
	}

	middlewares := router["middlewares"].([]any)
	if len(middlewares) != 2 || middlewares[0].(string) != "default-middleware@file" {
		t.Fatalf("expected default middlewares, got %v", middlewares)
	}
}

func TestProvide_ResponseOverridesDefaults(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		DefaultBody:   testDefaultBody,
		DefaultStatus: 201,
		Responses: []staticresponseprovider.ResponseConfig{
			{Rule: testRuleExample, Body: "overridden body", Status: 202},
		},
	})

	raw := provideConfig(t, provider)

	resp := doRequest(t, raw, 0)
	defer resp.Body.Close()

	if resp.StatusCode != 202 {
		t.Fatalf("expected status 202, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(body) != "overridden body" {
		t.Fatalf("expected body %q, got %q", "overridden body", string(body))
	}
}

func TestServeResponse_Body(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{
				Rule:   testRuleExample,
				Body:   "hello from body",
				Status: 201,
				Headers: map[string]string{
					"X-Test": "yes",
				},
			},
		},
	})

	raw := provideConfig(t, provider)

	resp := doRequest(t, raw, 0)
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

	if err := os.WriteFile(filePath, []byte("hello from file"), 0o600); err != nil {
		t.Fatal(err)
	}

	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{
				Rule: testRuleExample,
				File: filePath,
			},
		},
	})

	raw := provideConfig(t, provider)

	resp := doRequest(t, raw, 0)
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

func TestServeResponse_MultipleResponsesShareOneServer(t *testing.T) {
	provider := newProvider(t, &staticresponseprovider.Config{
		Responses: []staticresponseprovider.ResponseConfig{
			{Rule: "Host(`a.example.com`)", Body: "A", Status: 200},
			{Rule: "Host(`b.example.com`)", Body: "B", Status: 200},
		},
	})

	raw := provideConfig(t, provider)

	httpCfg := raw["http"].(map[string]any)
	services := httpCfg["services"].(map[string]any)
	if len(services) != 1 {
		t.Fatalf("expected exactly 1 shared service, got %v", services)
	}

	respA := doRequest(t, raw, 0)
	defer respA.Body.Close()
	bodyA, _ := io.ReadAll(respA.Body)
	if string(bodyA) != "A" {
		t.Fatalf("expected body %q, got %q", "A", string(bodyA))
	}

	respB := doRequest(t, raw, 1)
	defer respB.Body.Close()
	bodyB, _ := io.ReadAll(respB.Body)
	if string(bodyB) != "B" {
		t.Fatalf("expected body %q, got %q", "B", string(bodyB))
	}
}

// doRequest sends a request directly to the shared embedded server, tagging
// it with X-Static-Response-Id, exactly like the generated "headers"
// middleware would.
func doRequest(t *testing.T, raw map[string]any, responseIndex int) *http.Response {
	t.Helper()

	httpCfg := raw["http"].(map[string]any)
	services := httpCfg["services"].(map[string]any)

	var serviceURL string
	for _, svc := range services {
		svcMap := svc.(map[string]any)
		lb := svcMap["loadBalancer"].(map[string]any)
		servers := lb["servers"].([]any)
		server := servers[0].(map[string]any)
		serviceURL = server["url"].(string)
	}

	req, err := http.NewRequest(http.MethodGet, serviceURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Static-Response-Id", strconv.Itoa(responseIndex))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	return resp
}
