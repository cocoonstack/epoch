package server

// Cloud image download tests are integration tests that require a real
// registry backend. The handleCloudImageDownload handler is exercised
// by integration testing. The previous handleImageOrUI disambiguation
// logic was removed in favor of the single /dl/{name} canonical path.
