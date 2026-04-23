package cmd

import (
	"os"

	"github.com/cocoonstack/epoch/registryclient"
)

func resolveConfig() (serverURL, token string) {
	serverURL = os.Getenv("EPOCH_SERVER")
	token = os.Getenv("EPOCH_REGISTRY_TOKEN")
	return
}

func newRegistryClient() (*registryclient.Client, error) {
	serverURL, token := resolveConfig()
	var opts []registryclient.Option
	if ca := os.Getenv("EPOCH_CA_CERT"); ca != "" {
		opts = append(opts, registryclient.WithCACert(ca))
	}
	return registryclient.New(serverURL, token, opts...)
}
