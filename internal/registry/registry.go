package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const publicRegistryURL = "https://registry.terraform.io/v1/providers"

type VersionLister interface {
	ListVersions(ctx context.Context, source string) ([]string, error)
}

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type versionsResponse struct {
	Versions []struct {
		Version string `json:"version"`
	} `json:"versions"`
}

func NewClient() *Client {
	return &Client{
		baseURL: publicRegistryURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (client *Client) ListVersions(ctx context.Context, source string) ([]string, error) {
	source = strings.TrimPrefix(source, "registry.terraform.io/")

	parts := strings.Split(source, "/")

	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid public provider source %q", source)
	}

	endpoint := fmt.Sprintf("%s/%s/%s/versions", client.baseURL, parts[0], parts[1])

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)

	if err != nil {
		return nil, fmt.Errorf("create registry request: %w", err)
	}

	response, err := client.httpClient.Do(request)

	if err != nil {
		return nil, fmt.Errorf("request provider versions: %w", err)
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned %s", response.Status)
	}

	var payload versionsResponse

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode registry response: %w", err)
	}

	versions := make([]string, 0, len(payload.Versions))

	for _, item := range payload.Versions {
		if item.Version != "" {
			versions = append(versions, item.Version)
		}
	}

	return versions, nil
}
