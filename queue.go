package leviq

import (
	"sync"
	"time"

	"github.com/jmhodges/levigo"
	"github.com/johnsto/leviq/internal"
)

const (
	MaxQueue int = 1e6
)

// Queue encapsulates a namespaced queue held by a DB.
type Queue struct {
	ns    []byte // namespace (key prefix)
	db    *DB
	mutex *sync.Mutex
	ids   *internal.IDHeap // IDs in queue
	sync  bool             // true if transactions should be synced
	c     chan struct{}    // item availability channel
}

// init populates the queue with all the IDs from the saved database.
func (q *Queue) init() error {
	ro := levigo.NewReadOptions()
	defer ro.Close()

	it := q.db.db.NewIterator(ro)
	defer it.Close()

	// Seek to first key within namespace
	if q.ns == nil {
		it.SeekToFirst()
	} else {
		it.Seek(q.ns)
	}

	// Populate with read keys
	for it.Valid() {
		k := splitKey(q.ns, it.Key())
		if k == nil {
			// Key doesn't match namespace => past end
			break
		}
		id, err := internal.KeyToID(k)
		if err != nil {
			return err
		}
		q.ids.PushID(id)
		q.c <- struct{}{}
		it.Next()
	}

	return nil
}

// SetSync specifies if the LevelDB database should be sync'd to disk before
// returning from any commit operations. Set this to true for increased
// data durability at the cost of transaction commit time.
func (q *Queue) SetSync(sync bool) {
	q.sync = sync
}

// Clear removes all entries in the DB. Do not call if any transactions are in
// progress.
func (q *Queue) Clear() error {
	ro := levigo.NewReadOptions()
	defer ro.Close()

	b := levigo.NewWriteBatch()
	it := q.db.db.NewIterator(ro)

	// Seek to first key within namespace
	if q.ns == nil {
		it.SeekToFirst()
	} else {
		it.Seek(q.ns)
	}

	// Delete each key within namespace
	for it.Valid() {
		k := splitKey(q.ns, it.Key())
		if k == nil {
			break
		}
		b.Delete(it.Key())
	}

	// Write to disk
	wo := levigo.NewWriteOptions()
	wo.SetSync(q.sync)
	defer wo.Close()

	return q.db.db.Write(wo, b)
}

// Transaction starts a new transaction on the queue.
func (q *Queue) Transaction() *Txn {
	return &Txn{
		queue: q,
		puts:  internal.NewIDHeap(),
		takes: internal.NewIDHeap(),
		mutex: &sync.Mutex{},
	}
}

// putKeys adds the ID(s) to the queue, indicating entries that are immediately
// available for taking.
func (q *Queue) putKey(ids ...internal.ID) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	for _, id := range ids {
		q.ids.PushID(id)
		q.c <- struct{}{}
	}
}

// awaitKey finds the first key available for taking, removes it from the set of
// keys and returns it to the caller. If the duration argument is greater than
// 0, it will wait the prescribed time for a key to arrive before returning nil.
func (q *Queue) awaitKey(t time.Duration) []byte {
	select {
	case <-q.c:
		// Item immediately available
		q.mutex.Lock()
		defer q.mutex.Unlock()
		return q.ids.PopID().Key()
	default:
		// Return immediately if user specified no timeout, otherwise wait
		if t == 0 {
			return nil
		} else {
			b := q.awaitKeys(1, t)
			if len(b) == 1 {
				return b[0]
			} else {
				return nil
			}
		}
	}
}

// awaitKeys returns `n` keys available for taking, removing them from the set
// of keys and returns them to the caller, waiting at most the specified amount
// of time forkeys to become available before before returning nil.
func (q *Queue) awaitKeys(n int, t time.Duration) [][]byte {
	cancel := make(chan struct{}, 0)
	timeout := time.AfterFunc(t, func() {
		close(cancel)
	})
	defer timeout.Stop()

	b := [][]byte{}
	for {
		select {
		case <-q.c:
			q.mutex.Lock()
			k := q.ids.PopID().Key()
			q.mutex.Unlock()
			b = append(b, k)
			if len(b) == n {
				return b
			}
		case <-cancel:
			// Timed out
			return b
		}
	}
}

// take takes `n` elements from the queue, waiting at most `t` to retrieve them.
func (q *Queue) take(n int, t time.Duration) (ids []internal.ID, keys [][]byte, values [][]byte, err error) {
	// get next available key
	keys = q.awaitKeys(n, t)

	n = len(keys)
	ids = make([]internal.ID, n)
	values = make([][]byte, n)

	ro := levigo.NewReadOptions()
	for i, k := range keys {
		// retrieve value
		dbk := joinKey(q.ns, k)
		values[i], err = q.db.db.Get(ro, dbk)
		if err != nil {
			return nil, nil, nil, err
		}

		// key => id
		ids[i], err = internal.KeyToID(k)
	}

	return ids, keys, values, err
}
