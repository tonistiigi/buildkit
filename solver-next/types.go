package solver

import (
	"context"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

// Vertex is one node in the build graph
type Vertex interface {
	// Digest is a content-addressable vertex identifier
	Digest() digest.Digest
	// Sys returns an internal value that is used to execute the vertex. Usually
	// this is capured by the operation resolver method during solve.
	Sys() interface{}
	// FIXME(AkihiroSuda): we should not import pb pkg here.
	// TODO(tonistiigi): reenable strict metadata CacheManager, cache_ignore
	// Metadata() *pb.OpMetadata
	// Array of edges current vertex depends on.
	Inputs() []Edge
	Name() string
}

// Index is a index value for output edge
type Index int

// Edge is a path to a specific output of the vertex
type Edge struct {
	Index  Index
	Vertex Vertex
}

// Result is an abstract return value for a solve
type Result interface {
	ID() string
	Release(context.Context) error
	Sys() interface{}
}

// CachedResult is a result connected with its cache key
type CachedResult interface {
	Result
	CacheKey() CacheKey
	// ExportCache(context.Context, content.Store) (*ocispec.Descriptor, error)
}

// Op is an implementation for running a vertex
type Op interface {
	// CacheMap returns structure describing how the operation is cached
	CacheMap(context.Context) (*CacheMap, error)
	// Exec runs an operation given results from previous operations.
	Exec(ctx context.Context, inputs []Result) (outputs []Result, err error)
}

type ResultBasedCacheFunc func(context.Context, Result) (digest.Digest, error)

type CacheMap struct {
	// Digest is a base digest for operation that needs to be combined with
	// inputs cache or selectors for dependencies.
	Digest digest.Digest
	Deps   []struct {
		// Optional digest that is merged with the cache key of the input
		// TODO(tonistiigi): not implemented
		Selector digest.Digest
		// Optional function that returns a digest for the input based on its
		// return value
		ComputeDigestFunc ResultBasedCacheFunc
	}
}

// CacheKey is an identifier for storing/loading build cache
type CacheKey interface {
	// Deps are dependant cache keys
	Deps() []CacheKey
	// Base digest for operation. Usually CacheMap.Digest
	Digest() digest.Digest
	// Index for the output that is cached
	Output() Index
	// Helpers for implementations for adding internal metadata
	SetValue(key, value interface{})
	GetValue(key interface{}) interface{}
}

// CacheRecord is an identifier for loading in cache
type CacheRecord struct {
	ID       string
	CacheKey CacheKey
	// Loadable bool
	// Size int
	// CreatedAt time.Time
}

// CacheManager implements build cache backend
type CacheManager interface {
	// Query searches for cache paths from one cache key to the output of a possible match.
	Query(inp []CacheKey, inputIndex Index, dgst digest.Digest, outputIndex Index) ([]*CacheRecord, error)
	// Load pulls and returns the cached result
	Load(ctx context.Context, rec *CacheRecord) (Result, error)
	// Save saves a result based on a cache key
	Save(key CacheKey, s Result) (CacheKey, error)
}

// NewCacheKey creates a new cache key for a specific output index
func NewCacheKey(dgst digest.Digest, index Index, deps []CacheKey) CacheKey {
	return &cacheKey{
		dgst:   dgst,
		deps:   deps,
		index:  index,
		values: &sync.Map{},
	}
}

type cacheKey struct {
	dgst   digest.Digest
	index  Index
	deps   []CacheKey
	values *sync.Map
}

func (ck *cacheKey) SetValue(key, value interface{}) {
	ck.values.Store(key, value)
}

func (ck *cacheKey) GetValue(key interface{}) interface{} {
	v, _ := ck.values.Load(key)
	return v
}

func (ck *cacheKey) Deps() []CacheKey {
	return ck.deps
}

func (ck *cacheKey) Digest() digest.Digest {
	return ck.dgst
}

func (ck *cacheKey) Output() Index {
	return ck.index
}
