package cmd

import (
	"os"

	"github.com/cocoonstack/epoch/registryclient"
)

// resolveConfig returns the server URL and token from environment variables.
// An empty serverURL is fine — registryclient.New falls back to its own default.
func resolveConfig() (serverURL, token string) {
	serverURL = os.Getenv("EPOCH_SERVER")
	token = os.Getenv("EPOCH_REGISTRY_TOKEN")
	return
}

// newRegistryClient returns a client configured from the environment.
func newRegistryClient() *registryclient.Client {
	serverURL, token := resolveConfig()
	return registryclient.New(serverURL, token)
}
