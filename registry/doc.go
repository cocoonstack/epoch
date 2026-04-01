// Package registry implements the Epoch snapshot registry backed by an
// S3-compatible object store.
//
// # vk-cocoon Integration
//
// vk-cocoon imports this package to automatically pull snapshots before cloning VMs.
//
// Setup in vk-cocoon's main.go or provider initialization:
//
//	import "github.com/cocoonstack/epoch/registry"
//
//	// Create puller — reads object store settings from a k8s ConfigMap.
//	puller, err := registry.NewPuller("/data01/cocoon", "prod", "agent-env")
//	if err != nil {
//	    log.Fatalf("epoch puller: %v", err)
//	}
//
//	// Pre-warm known snapshots at startup (non-blocking).
//	puller.PreWarm([]string{"sre-agent-bot", "sre-agent-diagnosis"})
//
// In the provider's CreatePod, before calling `cocoon vm clone`:
//
//	// Ensure snapshot is available locally before cloning.
//	if err := puller.EnsureSnapshot(ctx, image); err != nil {
//	    return fmt.Errorf("epoch ensure %s: %w", image, err)
//	}
//	// Now safe to: cocoon vm clone --cold <image>
//
// The Puller is thread-safe, idempotent, and caches pull results.
// Subsequent calls for the same snapshot return immediately.
package registry
