package mem

import (
	"path/filepath"
	"sync"

	"github.com/xichan96/cortex/dino/chatstore"
	"github.com/xichan96/cortex/pkg/memkit"
	"github.com/xichan96/cortex/pkg/memkit/sqlite"
)

var (
	sharedMemMu   sync.Mutex
	sharedMemByDB = map[string]memkit.Manager{}
)

func SharedSQLiteManager(persistDir, sqliteFile string) (memkit.Manager, error) {
	dir := filepath.Clean(persistDir)
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	dbPath, err := filepath.Abs(filepath.Join(absDir, sqliteFile))
	if err != nil {
		return nil, err
	}
	sharedMemMu.Lock()
	defer sharedMemMu.Unlock()
	if m, ok := sharedMemByDB[dbPath]; ok {
		return m, nil
	}
	db, err := chatstore.OpenSharedChatStore(absDir, sqliteFile)
	if err != nil {
		return nil, err
	}
	store, err := sqlite.NewSQLiteStoreFromDB(db)
	if err != nil {
		return nil, err
	}
	mgr := memkit.NewManagerWithStores(
		sqlite.NewSQLitePreferenceStore(store),
		sqlite.NewSQLiteKnowledgeStore(store),
		sqlite.NewSQLiteIndexStore(store),
		memkit.DefaultMemoryConfig(),
		sqlite.NewSQLitePageIndexStore(store),
	)
	sharedMemByDB[dbPath] = mgr
	return mgr, nil
}
