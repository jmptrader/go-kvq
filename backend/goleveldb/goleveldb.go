package goleveldb

import (
	"os"

	"github.com/johnsto/leviq"
	"github.com/johnsto/leviq/backend"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// DB encapsulates a LevelDB instance.
type DB struct {
	levelDB *leveldb.DB
}

// Open creates or opens an existing DB at the given path.
func Open(path string) (*leviq.DB, error) {
	levelDB, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	return New(levelDB), nil
}

// Destroy destroys the DB at the given path.
func Destroy(path string) error {
	return os.RemoveAll(path)
}

// New returns a DB from the given LevelDB instance.
func New(db *leveldb.DB) *leviq.DB {
	return leviq.NewDB(&DB{db})
}

// NewMem creates a new DB backed by memory only (i.e. not persistent)
func NewMem() (*leviq.DB, error) {
	storage := storage.NewMemStorage()
	levelDB, err := leveldb.Open(storage, nil)
	if err != nil {
		return nil, err
	}
	return New(levelDB), nil
}

// Bucket returns a queue in the given namespace.
func (db *DB) Bucket(name string) (backend.Bucket, error) {
	return &Bucket{
		db: db,
		ns: []byte(name),
	}, nil
}

// Close closes the database and releases any resources.
func (db *DB) Close() {
	db.levelDB.Close()
}

// Bucket represents a goleveldb-backed queue.
type Bucket struct {
	db *DB
	ns []byte
}

// ForEach iterates through keys in the queue. If the iteration function
// returns a non-nil error, iteration stops and the error is returned to
// the caller.
func (q *Bucket) ForEach(fn func(k, v []byte) error) error {
	keyRange := util.BytesPrefix(q.ns)
	it := q.db.levelDB.NewIterator(keyRange, nil)

	for it.Next() {
		kk, v := it.Key(), it.Value()
		k := kk[len(q.ns):]
		if err := fn(k, v); err != nil {
			return err
		}
	}

	return nil
}

// Batch enacts a number of operations in one atomic go. If the batch
// function returns a non-nil error, the batch is discarded and the error
// is returned to the caller. If the batch function returns nil, the batch
// is committed to the queue.
func (q *Bucket) Batch(fn func(backend.Batch) error) error {
	batch := &Batch{
		ns:         q.ns,
		levelDB:    q.db.levelDB,
		levelBatch: &leveldb.Batch{},
	}
	defer batch.Close()
	if err := fn(batch); err != nil {
		return err
	}
	return batch.Write()
}

// Get returns the value stored at key `k`.
func (q *Bucket) Get(k []byte) ([]byte, error) {
	kk := append(q.ns[:], k...)
	return q.db.levelDB.Get(kk, nil)
}

// Clear removes all items from this queue.
func (q *Bucket) Clear() error {
	keyRange := util.BytesPrefix(q.ns)
	it := q.db.levelDB.NewIterator(keyRange, nil)

	b := &leveldb.Batch{}

	for it.Next() {
		kk := it.Key()
		k := kk[len(q.ns):]
		b.Delete(k)
	}

	wo := &opt.WriteOptions{Sync: true}
	return q.db.levelDB.Write(b, wo)
}

// Batch represents a set of put/delete operations to perform on a Bucket.
type Batch struct {
	levelDB    *leveldb.DB
	levelBatch *leveldb.Batch
	ns         []byte
}

func (b *Batch) Put(k, v []byte) error {
	kk := append(b.ns[:], k...)
	b.levelBatch.Put(kk, v)
	return nil
}

func (b *Batch) Delete(k []byte) error {
	kk := append(b.ns[:], k...)
	b.levelBatch.Delete(kk)
	return nil
}

func (b *Batch) Write() error {
	wo := &opt.WriteOptions{Sync: true}
	return b.levelDB.Write(b.levelBatch, wo)
}

func (b *Batch) Clear() {
	b.levelBatch.Reset()
}

func (b *Batch) Close() {
	b.levelBatch.Reset()
}