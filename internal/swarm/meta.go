package swarm

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
)

// MaxBlob is the largest blob the swarm keeps. Bytes pass through the data directory and
// are held in full, so this is deliberately not unlimited.
const MaxBlob = 64 << 20

// GetMeta reads a task's metadata. It goes through the writer only to say 404 for an
// unknown task.
func (s *Service) GetMeta(ctx context.Context, id string) (meta.Meta, error) {
	if _, err := s.Engine.Get(ctx, id); err != nil {
		return meta.Meta{}, err
	}
	m, _, err := s.Store.GetMeta(id)
	return m, err
}

// PatchMeta changes a task's properties.
func (s *Service) PatchMeta(ctx context.Context, id string, p meta.Patch) (meta.Meta, error) {
	if _, err := s.Engine.Get(ctx, id); err != nil {
		return meta.Meta{}, err
	}
	return s.Store.PatchMeta(id, p, time.Now())
}

// PutBlob stores bytes and returns their reference.
func (s *Service) PutBlob(ctx context.Context, r io.Reader, name, mediaType string) (meta.BlobRef, error) {
	return s.Store.PutBlob(ctx, r, name, mediaType, MaxBlob)
}

// OpenBlob reads a blob.
func (s *Service) OpenBlob(ctx context.Context, sha string) (io.ReadCloser, int64, error) {
	if !meta.ValidSHA(sha) {
		return nil, 0, fmt.Errorf("%w: blob name %q", store.ErrNotFound, sha)
	}
	return s.Store.OpenBlob(ctx, sha)
}

// FetchResource brings a foreign object's bytes into the swarm, once. Later calls return
// the kept copy without contacting the other system. Connected resources use that
// connection's credentials, and everything else goes through the guarded plain fetcher.
func (s *Service) FetchResource(ctx context.Context, id, name string) (meta.BlobRef, error) {
	m, err := s.GetMeta(ctx, id)
	if err != nil {
		return meta.BlobRef{}, err
	}
	p, ok := m.Props[name]
	if !ok || p.Resource == nil {
		return meta.BlobRef{}, fmt.Errorf("%w: %s has no resource named %s", store.ErrNotFound, id, name)
	}
	r := *p.Resource
	if r.Blob != nil {
		return *r.Blob, nil
	}
	body, mediaType, err := s.downloader(r).Download(ctx, r)
	if err != nil {
		return meta.BlobRef{}, err
	}
	defer body.Close()
	if r.MediaType != "" {
		mediaType = r.MediaType
	}
	ref, err := s.PutBlob(ctx, body, r.Name, mediaType)
	if err != nil {
		return meta.BlobRef{}, err
	}
	r.Blob, r.FetchedAt = &ref, time.Now()
	if _, err := s.Store.PatchMeta(id, meta.Patch{Props: map[string]*meta.Prop{name: {Resource: &r}}}, time.Now()); err != nil {
		return meta.BlobRef{}, err
	}
	return ref, nil
}

func (s *Service) downloader(r meta.Resource) meta.Downloader {
	if s.Connections != nil {
		if d := s.Connections(r.Connection); d != nil {
			return d
		}
	}
	return s.plainFetcher
}
