package orgreports

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRawRequestUsesKnownServiceAndAuthentication(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.RequestURI() != "/v3/orgs/org-1/widgets?state=open" {
			t.Fatalf("request URI = %s", r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Fatalf("authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var input map[string]any
		if err := json.Unmarshal(body, &input); err != nil || input["enabled"] != true {
			t.Fatalf("body = %s, err = %v", body, err)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := New(server.URL, func() (string, error) { return "token-1", nil })
	data, err := client.RawRequest(context.Background(), APIServiceCore, http.MethodPatch, "/v3/orgs/org-1/widgets?state=open", json.RawMessage(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("response = %s", data)
	}
}

func TestRawEndpointSelectsServiceOrigins(t *testing.T) {
	client := New("https://api.bitwave.io", func() (string, error) { return "", nil })
	tests := map[string]string{
		APIServiceCore:     "https://api.bitwave.io/v3/orgs/one",
		APIServiceAPI2:     "https://api2.bitwave.io/v2/orgs/one/rate-tables",
		APIServiceApp:      "https://api-app.bitwave.io/graphql",
		APIServicePlatform: "https://api4.bitwave.io/orgs/one",
		APIServiceReports:  "https://api4.bitwave.io/graphql-reports",
		// The retired transactions host must never be dialed; the gateway serves its paths.
		APIServiceTransactions: "https://api.bitwave.io/orgs/one/lookups",
	}
	paths := map[string]string{
		APIServiceCore:         "/v3/orgs/one",
		APIServiceAPI2:         "/v2/orgs/one/rate-tables",
		APIServiceApp:          "/graphql",
		APIServicePlatform:     "/orgs/one",
		APIServiceReports:      "/graphql-reports",
		APIServiceTransactions: "/orgs/one/lookups",
	}
	for service, want := range tests {
		got, err := client.RawEndpoint(service, paths[service])
		if err != nil {
			t.Fatalf("%s: %v", service, err)
		}
		if got != want {
			t.Fatalf("%s endpoint = %s, want %s", service, got, want)
		}
	}
}

func TestRawEndpointRejectsArbitraryHost(t *testing.T) {
	client := New("https://api.bitwave.io", func() (string, error) { return "", nil })
	for _, path := range []string{"https://example.com/steal", "//example.com/steal"} {
		_, err := client.RawEndpoint(APIServiceCore, path)
		if err == nil || !strings.Contains(err.Error(), "arbitrary URLs") {
			t.Fatalf("path %q error = %v", path, err)
		}
	}
}

func TestRawEndpointPreservesSelfHostedBasePath(t *testing.T) {
	client := New("http://localhost:8080/api", func() (string, error) { return "", nil })
	for _, service := range []string{APIServiceCore, APIServiceAPI2, APIServiceApp, APIServicePlatform, APIServiceReports, APIServiceTransactions} {
		got, err := client.RawEndpoint(service, "/widgets")
		if err != nil {
			t.Fatal(err)
		}
		if got != "http://localhost:8080/api/widgets" {
			t.Fatalf("%s endpoint = %s", service, got)
		}
	}
}

func TestRawRequestBytesPreservesBodyAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("content type = %q", got)
		}
		if got := r.Header.Get("X-Import-Mode"); got != "replace" {
			t.Fatalf("import mode = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "raw-body" {
			t.Fatalf("body = %q", body)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client := New(server.URL, func() (string, error) { return "token-1", nil })
	headers := http.Header{"Content-Type": {"application/octet-stream"}, "X-Import-Mode": {"replace"}}
	data, err := client.RawRequestBytes(context.Background(), APIServiceCore, http.MethodPost, "/import", []byte("raw-body"), headers)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("response = %q", data)
	}
}
