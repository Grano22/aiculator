package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Grano22/aiculator/internal/meta"
)

// These buckets are created on first use. A swarm that does not use them pays nothing,
// not even at startup.
type lazyBuckets struct {
	mu    sync.Mutex
	meta  jetstream.KeyValue
	state jetstream.KeyValue
	quar  jetstream.KeyValue
	mem   jetstream.KeyValue
	blobs jetstream.ObjectStore
}

func (s *Store) memoryKV() (jetstream.KeyValue, error) {
	s.lazy.mu.Lock()
	defer s.lazy.mu.Unlock()
	if s.lazy.mem != nil {
		return s.lazy.mem, nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	kv, err := s.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "memory", History: 1, Storage: jetstream.FileStorage})
	if err != nil {
		return nil, fmt.Errorf("memory bucket: %w", err)
	}
	s.lazy.mem = kv
	return kv, nil
}

// PutMemory stores one shared-memory record under a key, as raw JSON. The memory package owns
// the format; the store only keeps it durable, in a bucket made the first time it is used.
func (s *Store) PutMemory(key string, value []byte) error {
	kv, err := s.memoryKV()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	_, err = kv.Put(ctx, key, value)
	return err
}

// DeleteMemory removes a record.
func (s *Store) DeleteMemory(key string) error {
	kv, err := s.memoryKV()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	err = kv.Delete(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil
	}
	return err
}

// ListMemory calls fn with every record.
func (s *Store) ListMemory(ctx context.Context, fn func(key string, value []byte)) error {
	kv, err := s.memoryKV()
	if err != nil {
		return err
	}
	lister, err := kv.ListKeys(ctx)
	if err != nil {
		return err
	}
	var keys []string
	for k := range lister.Keys() {
		keys = append(keys, k)
	}
	for _, k := range keys {
		e, err := kv.Get(ctx, k)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		fn(k, e.Value())
	}
	return nil
}

func (s *Store) quarantineKV() (jetstream.KeyValue, error) {
	s.lazy.mu.Lock()
	defer s.lazy.mu.Unlock()
	if s.lazy.quar != nil {
		return s.lazy.quar, nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	kv, err := s.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "quarantine", History: 1, Storage: jetstream.FileStorage})
	if err != nil {
		return nil, fmt.Errorf("quarantine bucket: %w", err)
	}
	s.lazy.quar = kv
	return kv, nil
}

// ErrTooLarge means a blob is over the size limit.
var ErrTooLarge = errors.New("blob is too large")

func (s *Store) metaKV() (jetstream.KeyValue, error) {
	s.lazy.mu.Lock()
	defer s.lazy.mu.Unlock()
	if s.lazy.meta != nil {
		return s.lazy.meta, nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	kv, err := s.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "taskmeta", History: 1, Storage: jetstream.FileStorage})
	if err != nil {
		return nil, fmt.Errorf("metadata bucket: %w", err)
	}
	s.lazy.meta = kv
	return kv, nil
}

func (s *Store) blobStore() (jetstream.ObjectStore, error) {
	s.lazy.mu.Lock()
	defer s.lazy.mu.Unlock()
	if s.lazy.blobs != nil {
		return s.lazy.blobs, nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	b, err := s.js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: "blobs", Storage: jetstream.FileStorage})
	if err != nil {
		return nil, fmt.Errorf("blob bucket: %w", err)
	}
	s.lazy.blobs = b
	return b, nil
}

// GetMeta returns a task's metadata; a task with none gets an empty value.
func (s *Store) GetMeta(id string) (meta.Meta, uint64, error) {
	kv, err := s.metaKV()
	if err != nil {
		return meta.Meta{}, 0, err
	}
	ctx, cancel := opCtx()
	defer cancel()
	e, err := kv.Get(ctx, "m."+id)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return meta.Meta{}, 0, nil
	}
	if err != nil {
		return meta.Meta{}, 0, err
	}
	var m meta.Meta
	if err := json.Unmarshal(e.Value(), &m); err != nil {
		return meta.Meta{}, 0, fmt.Errorf("decode metadata of %s: %w", id, err)
	}
	return m, e.Revision(), nil
}

// PatchMeta applies a patch with compare-and-swap, retrying if another writer got in
// first. Writers changing different properties therefore never lose each other's work.
func (s *Store) PatchMeta(id string, p meta.Patch, now time.Time) (meta.Meta, error) {
	kv, err := s.metaKV()
	if err != nil {
		return meta.Meta{}, err
	}
	for range 16 {
		cur, rev, err := s.GetMeta(id)
		if err != nil {
			return meta.Meta{}, err
		}
		next, err := cur.Apply(p, now)
		if err != nil {
			return meta.Meta{}, err
		}
		b, err := json.Marshal(next)
		if err != nil {
			return meta.Meta{}, err
		}
		ctx, cancel := opCtx()
		if rev == 0 {
			_, err = kv.Create(ctx, "m."+id, b)
		} else {
			_, err = kv.Update(ctx, "m."+id, b, rev)
		}
		cancel()
		if err == nil {
			return next, nil
		}
		if !errors.Is(mapErr(err), ErrConflict) {
			return meta.Meta{}, err
		}
	}
	return meta.Meta{}, fmt.Errorf("%w: metadata of %s kept changing", ErrConflict, id)
}

// PutBlob stores bytes under their hash. It spools to disk first, because the hash names
// the object and is only known at the end. limit is enforced while reading, so an endless
// body costs at most that much.
func (s *Store) PutBlob(ctx context.Context, r io.Reader, name, mediaType string, limit int64) (meta.BlobRef, error) {
	obj, err := s.blobStore()
	if err != nil {
		return meta.BlobRef{}, err
	}
	f, err := os.CreateTemp(s.dir, "blob-*")
	if err != nil {
		return meta.BlobRef{}, err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, limit+1))
	if err != nil {
		return meta.BlobRef{}, err
	}
	if n > limit {
		return meta.BlobRef{}, fmt.Errorf("%w: over %d bytes", ErrTooLarge, limit)
	}
	ref := meta.BlobRef{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, Name: name, MediaType: mediaType}
	if _, err := obj.GetInfo(ctx, ref.SHA256); err == nil {
		return ref, nil // The same bytes are already here.
	} else if !errors.Is(err, jetstream.ErrObjectNotFound) {
		return meta.BlobRef{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return meta.BlobRef{}, err
	}
	if _, err := obj.Put(ctx, jetstream.ObjectMeta{Name: ref.SHA256}, f); err != nil {
		return meta.BlobRef{}, fmt.Errorf("store blob: %w", err)
	}
	return ref, nil
}

// OpenBlob returns a blob's bytes. The caller closes it.
func (s *Store) OpenBlob(ctx context.Context, sha string) (io.ReadCloser, int64, error) {
	obj, err := s.blobStore()
	if err != nil {
		return nil, 0, err
	}
	res, err := obj.Get(ctx, sha)
	if errors.Is(err, jetstream.ErrObjectNotFound) {
		return nil, 0, fmt.Errorf("%w: blob %s", ErrNotFound, sha)
	}
	if err != nil {
		return nil, 0, err
	}
	info, err := res.Info()
	if err != nil {
		res.Close()
		return nil, 0, err
	}
	return res, int64(info.Size), nil
}

// HashHex is exposed for tests that want to predict a blob's name.
func HashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
