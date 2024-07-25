package boltutil

import (
	"io/fs"
	"sync"

	"github.com/moby/buildkit/util/db"
	bolt "go.etcd.io/bbolt"
)

func Open(p string, mode fs.FileMode, options *bolt.Options) (db.DB, error) {
	bdb, err := bolt.Open(p, mode, options)
	if err != nil {
		return nil, err
	}
	return &inMemoryDB{db: bdb}, nil
}

type inMemoryDB struct {
	db     *bolt.DB
	mu     sync.Mutex
	tx     *bolt.Tx
	closed bool
}

func (i *inMemoryDB) Update(fn func(*bolt.Tx) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.closed {
		return bolt.ErrDatabaseNotOpen
	}

	if i.tx == nil {
		tx, err := i.db.Begin(true)
		if err != nil {
			return err
		}
		i.tx = tx
	}
	return fn(i.tx)
}

func (i *inMemoryDB) View(fn func(*bolt.Tx) error) error {
	return i.Update(fn)
}

func (i *inMemoryDB) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.tx != nil {
		if err := i.tx.Commit(); err != nil {
			return err
		}
		i.tx = nil
	}
	if err := i.db.Close(); err != nil {
		return err
	}
	i.closed = true
	return nil
}
