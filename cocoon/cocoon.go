// Package cocoon provides types and path helpers for Cocoon's snapshot storage.
//
// This package has NO dependency on the registry package — it is imported
// by registry for push/pull operations.
package cocoon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cocoonstack/epoch/internal/util"
)

const DefaultRootDir = "/data01/cocoon"

// SnapshotDB matches Cocoon's snapshots.json format.
type SnapshotDB struct {
	Snapshots map[string]*SnapshotRecord `json:"snapshots"`
	Names     map[string]string          `json:"names"`
}

// SnapshotRecord matches Cocoon's snapshot DB record.
type SnapshotRecord struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Image        string              `json:"image,omitempty"`
	ImageBlobIDs map[string]struct{} `json:"image_blob_ids,omitempty"`
	CPU          int                 `json:"cpu,omitempty"`
	Memory       int64               `json:"memory,omitempty"`
	Storage      int64               `json:"storage,omitempty"`
	NICs         int                 `json:"nics,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	Pending      bool                `json:"pending,omitempty"`
	DataDir      string              `json:"data_dir,omitempty"`
}

// Paths holds cocoon storage paths for a given root directory.
type Paths struct {
	RootDir string
}

func NewPaths(rootDir string) *Paths {
	if rootDir == "" {
		rootDir = DefaultRootDir
	}
	return &Paths{RootDir: rootDir}
}

func (p *Paths) SnapshotDBFile() string          { return filepath.Join(p.RootDir, "snapshot", "db", "snapshots.json") }
func (p *Paths) SnapshotDataDir(id string) string { return filepath.Join(p.RootDir, "snapshot", "localfile", id) }
func (p *Paths) CloudimgBlobDir() string          { return filepath.Join(p.RootDir, "cloudimg", "blobs") }

// ReadSnapshotDB reads Cocoon's snapshots.json.
func (p *Paths) ReadSnapshotDB() (*SnapshotDB, error) {
	data, err := os.ReadFile(p.SnapshotDBFile())
	if err != nil {
		if os.IsNotExist(err) {
			return &SnapshotDB{
				Snapshots: make(map[string]*SnapshotRecord),
				Names:     make(map[string]string),
			}, nil
		}
		return nil, err
	}
	var db SnapshotDB
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, err
	}
	if db.Snapshots == nil {
		db.Snapshots = make(map[string]*SnapshotRecord)
	}
	if db.Names == nil {
		db.Names = make(map[string]string)
	}
	return &db, nil
}

// WriteSnapshotDB writes Cocoon's snapshots.json atomically.
func (p *Paths) WriteSnapshotDB(db *SnapshotDB) error {
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.SnapshotDBFile())
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp := p.SnapshotDBFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.SnapshotDBFile())
}

// ResolveSnapshotID resolves a snapshot name to its ID.
func (p *Paths) ResolveSnapshotID(name string) (string, error) {
	db, err := p.ReadSnapshotDB()
	if err != nil {
		return "", err
	}
	if id, ok := db.Names[name]; ok {
		return id, nil
	}
	if _, ok := db.Snapshots[name]; ok {
		return name, nil
	}
	return "", fmt.Errorf("snapshot %q not found in cocoon DB", name)
}

// SnapshotExists checks if a snapshot already exists locally.
func SnapshotExists(paths *Paths, name string) bool {
	db, err := paths.ReadSnapshotDB()
	if err != nil {
		return false
	}
	_, ok := db.Names[name]
	return ok
}

// HumanSize formats a byte count as a human-readable string.
// Delegates to internal/util for the shared implementation.
func HumanSize(b int64) string {
	return util.HumanSize(b)
}
