package stores_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationVersionsUnique(t *testing.T) {
	paths, err := filepath.Glob("../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, path := range paths {
		version := strings.SplitN(filepath.Base(path), "_", 2)[0]
		if previous, ok := seen[version]; ok {
			t.Fatalf("migration version %s reused by %s and %s", version, previous, path)
		}
		seen[version] = path
	}
}
