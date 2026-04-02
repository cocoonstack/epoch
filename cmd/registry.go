package cmd

import (
	"os"

	"github.com/cocoonstack/epoch/internal/registryclient"
)

const defaultServerURL = "http://127.0.0.1:4300"

// resolveConfig returns the server URL and token from environment variables.
func resolveConfig() (serverURL, token string) {
	serverURL = os.Getenv("EPOCH_SERVER")
	if serverURL == "" {
		serverURL = defaultServerURL
	}
	token = os.Getenv("EPOCH_REGISTRY_TOKEN")
	return
}

// newRegistryClient returns a client configured from the environment.
func newRegistryClient() *registryclient.Client {
	serverURL, token := resolveConfig()
	return registryclient.New(serverURL, token)
}
