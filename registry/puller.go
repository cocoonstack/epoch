package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/projecteru2/core/log"
	"golang.org/x/sync/singleflight"

	"github.com/cocoonstack/epoch/cocoon"
	"github.com/cocoonstack/epoch/objectstore"
)

// Puller is a high-level helper for vk-cocoon integration.
// It provides automatic snapshot pulling with caching and pre-warming.
type Puller struct {
	reg   *Registry
	paths *cocoon.Paths

	mu     sync.Mutex
	pulled map[string]bool // name:tag → pulled
	sf     singleflight.Group
}

// NewPuller creates a Puller for use by vk-cocoon.
// It reads object store credentials from the given k8s ConfigMap.
func NewPuller(cocoonRootDir, namespace, configmap string) (*Puller, error) {
	cfg, err := objectstore.ConfigFromConfigMap(namespace, configmap, "epoch/")
	if err != nil {
		cfg, err = objectstore.ConfigFromEnv("epoch/")
		if err != nil {
			return nil, fmt.Errorf("object store credentials not available: %w", err)
		}
	}
	client, err := objectstore.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Puller{
		reg:    New(client),
		paths:  cocoon.NewPaths(cocoonRootDir),
		pulled: make(map[string]bool),
	}, nil
}

// NewPullerFromConfig creates a Puller with explicit object store config.
func NewPullerFromConfig(cfg *objectstore.Config, cocoonRootDir string) (*Puller, error) {
	cfg.Prefix = "epoch/"
	client, err := objectstore.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("init object store client: %w", err)
	}
	return &Puller{
		reg:    New(client),
		paths:  cocoon.NewPaths(cocoonRootDir),
		pulled: make(map[string]bool),
	}, nil
}

// EnsureSnapshot ensures a snapshot is available locally.
// If not present, pulls it from Epoch. Thread-safe and idempotent.
func (p *Puller) EnsureSnapshot(ctx context.Context, name string) error {
	return p.EnsureSnapshotTag(ctx, name, "latest")
}

// EnsureSnapshotTag ensures a specific tag of a snapshot is available locally.
func (p *Puller) EnsureSnapshotTag(ctx context.Context, name, tag string) error {
	ref := name + ":" + tag

	p.mu.Lock()
	if p.pulled[ref] {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	_, err, _ := p.sf.Do(ref, func() (any, error) {
		if cocoon.SnapshotExists(p.paths, name) {
			p.mu.Lock()
			p.pulled[ref] = true
			p.mu.Unlock()
			return nil, nil
		}

		logger := log.WithFunc("registry.EnsureSnapshotTag")
		logger.Infof(ctx, "pulling snapshot %s ...", ref)
		start := time.Now()
		_, err := p.reg.Pull(ctx, p.paths, name, tag, func(msg string) {
			logger.Info(ctx, msg)
		})
		if err != nil {
			return nil, fmt.Errorf("epoch pull %s: %w", ref, err)
		}
		logger.Infof(ctx, "snapshot %s pulled in %s", ref, time.Since(start).Round(time.Second))

		p.mu.Lock()
		p.pulled[ref] = true
		p.mu.Unlock()
		return nil, nil
	})
	return err
}

// PreWarm pulls multiple snapshots concurrently at startup.
// Non-blocking — runs in the background.
func (p *Puller) PreWarm(ctx context.Context, snapshots []string) {
	go func() {
		logger := log.WithFunc("registry.PreWarm")
		var wg sync.WaitGroup
		for _, name := range snapshots {
			wg.Add(1)
			go func(n string) {
				defer wg.Done()
				if err := p.EnsureSnapshot(ctx, n); err != nil {
					logger.Warnf(ctx, "pre-warm %s failed: %v", n, err)
				}
			}(name)
		}
		wg.Wait()
		logger.Infof(ctx, "pre-warm complete (%d snapshots)", len(snapshots))
	}()
}

// ListRemote returns all repositories in the remote registry.
func (p *Puller) ListRemote(ctx context.Context) ([]string, error) {
	cat, err := p.reg.GetCatalog(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cat.Repositories))
	for n := range cat.Repositories {
		names = append(names, n)
	}
	return names, nil
}
