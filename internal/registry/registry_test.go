package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestClientListVersions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", request.Method)
		}
		if request.URL.Path != "/hashicorp/aws/versions" {
			t.Errorf("path = %q, want %q", request.URL.Path, "/hashicorp/aws/versions")
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"versions": [
				{"version": "6.40.0"},
				{"version": ""},
				{"version": "6.63.0"}
			]
		}`))
	}))
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	versions, err := client.ListVersions(context.Background(), "hashicorp/aws")
	if err != nil {
		t.Fatalf("ListVersions() error = %v", err)
	}

	want := []string{"6.40.0", "6.63.0"}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("ListVersions() = %#v, want %#v", versions, want)
	}
}

func TestClientAcceptsFullPublicProviderSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/hashicorp/aws/versions" {
			t.Errorf("path = %q, want %q", request.URL.Path, "/hashicorp/aws/versions")
		}
		_, _ = writer.Write([]byte(`{"versions":[{"version":"6.63.0"}]}`))
	}))
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	_, err := client.ListVersions(
		context.Background(),
		"registry.terraform.io/hashicorp/aws",
	)
	if err != nil {
		t.Fatalf("ListVersions() error = %v", err)
	}
}

func TestClientRejectsInvalidProviderSource(t *testing.T) {
	client := NewClient()

	_, err := client.ListVersions(context.Background(), "aws")
	if err == nil || !strings.Contains(err.Error(), "invalid public provider source") {
		t.Fatalf("ListVersions() error = %v, want invalid source error", err)
	}
}

func TestClientReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	_, err := client.ListVersions(context.Background(), "hashicorp/missing")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("ListVersions() error = %v, want HTTP 404 error", err)
	}
}

func TestClientReportsInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"versions":`))
	}))
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	_, err := client.ListVersions(context.Background(), "hashicorp/aws")
	if err == nil || !strings.Contains(err.Error(), "decode registry response") {
		t.Fatalf("ListVersions() error = %v, want JSON decoding error", err)
	}
}
