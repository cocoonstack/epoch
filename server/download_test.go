package server

// /dl/{name} download tests are integration tests that require a real
// registry backend (S3 + manifests + blobs). The handleArtifactDownload
// handler is exercised by integration testing against a live epoch
// instance — see cocoon-specs/tests/vk-epoch-pull-tests.md.
