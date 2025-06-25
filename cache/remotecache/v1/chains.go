package cacheimport

import (
	"context"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func NewCacheChains() *CacheChains {
	return &CacheChains{visited: map[any]struct{}{}}
}

type CacheChains struct {
	items   []*item
	visited map[any]struct{}

	roots map[digest.Digest]*item
}

var _ solver.CacheExporterTarget = &CacheChains{}

func (c *CacheChains) Add(dgst digest.Digest, deps [][]solver.CacheLink, results []solver.CacheResult) (solver.CacheExporterRecord, bool, error) {
	if strings.HasPrefix(dgst.String(), "random:") {
		return nil, false, nil
	}
	r := &item{
		dgst:    dgst,
		results: results,
	}

	if len(deps) == 0 {
		if r, ok := c.roots[dgst]; ok {
			r.results = append(r.results, results...)
			return r, true, nil
		}
		return r, true, nil
	}

	matchDeps := make([]func() map[*link]struct{}, len(c.items))
	for i, dd := range deps {
		if len(dd) == 0 {
			return nil, false, errors.Errorf("empty dependency for %s", dgst)
		}
		items := make([]*item, len(dd))
		for i, d := range dd {
			it, ok := d.Src.(*item)
			if !ok {
				return nil, false, errors.Errorf("invalid dependency type %T for %s", d.Src, dgst)
			}
			if it.cc != c {
				return nil, false, errors.Errorf("dependency %s is not part of the same cache chain", it.dgst)
			}
			items[i] = it
		}
		matchDeps[i] = func() map[*link]struct{} {
			var candidates map[*link]struct{}
			for _, it := range items {
				maps.Copy(candidates, it.getLinks(dgst, i, dd[0].Selector))
			}
			return candidates
		}
	}
	items := IntersectAll(matchDeps)
	if len(items) > 0 {
		if len(items) > 1 {
			return nil, false, errors.Errorf("TODO: multiple matching dependencies for %s: %v", dgst, items) // merge?
		}
	}

}

func IntersectAll[T comparable](
	funcs []func() map[T]struct{},
) map[T]struct{} {
	if len(funcs) == 0 {
		return nil
	}

	intersection := funcs[0]()

	for _, f := range funcs[1:] {
		next := f()
		for k := range intersection {
			if _, ok := next[k]; !ok {
				delete(intersection, k)
			}
		}
		if len(intersection) == 0 {
			return nil
		}
	}

	return intersection
}

func (c *CacheChains) AddOld(dgst digest.Digest) solver.CacheExporterRecord {
	if strings.HasPrefix(dgst.String(), "random:") {
		// random digests will be different *every* run - so we shouldn't cache
		// it, since there's a zero chance this random digest collides again
		return &nopRecord{}
	}

	it := &item{dgst: dgst, backlinks: map[*item]struct{}{}}
	c.items = append(c.items, it)
	return it
}

func (c *CacheChains) Visit(target any) {
	c.visited[target] = struct{}{}
}

func (c *CacheChains) Visited(target any) bool {
	_, ok := c.visited[target]
	return ok
}

func (c *CacheChains) normalize(ctx context.Context) error {
	st := &normalizeState{
		added: map[*item]*item{},
		links: map[*item]map[nlink]map[digest.Digest]struct{}{},
		byKey: map[digest.Digest]*item{},
	}

	validated := make([]*item, 0, len(c.items))
	for _, it := range c.items {
		it.backlinksMu.Lock()
		it.validate()
		it.backlinksMu.Unlock()
	}
	for _, it := range c.items {
		if !it.invalid {
			validated = append(validated, it)
		}
	}
	c.items = validated

	for _, it := range c.items {
		_, err := normalizeItem(it, st)
		if err != nil {
			return err
		}
	}

	st.removeLoops(ctx)

	items := make([]*item, 0, len(st.byKey))
	for _, it := range st.byKey {
		items = append(items, it)
	}
	c.items = items
	return nil
}

// Marshal converts the cache chains structure into a cache config and a
// collection of providers for reading the results from.
//
// Marshal aims to validate, normalize and sort the output to ensure a
// consistent digest (since cache configs are typically uploaded and stored in
// content-addressable OCI registries).
func (c *CacheChains) Marshal(ctx context.Context) (*CacheConfig, DescriptorProvider, error) {
	if err := c.normalize(ctx); err != nil {
		return nil, nil, err
	}

	st := &marshalState{
		chainsByID:    map[string]int{},
		descriptors:   DescriptorProvider{},
		recordsByItem: map[*item]int{},
	}

	for _, it := range c.items {
		if err := marshalItem(ctx, it, st); err != nil {
			return nil, nil, err
		}
	}

	cc := CacheConfig{
		Layers:  st.layers,
		Records: st.records,
	}
	sortConfig(&cc)

	return &cc, st.descriptors, nil
}

type DescriptorProvider map[digest.Digest]DescriptorProviderPair

type DescriptorProviderPair struct {
	Descriptor   ocispecs.Descriptor
	Provider     content.Provider
	InfoProvider content.InfoProvider
}

func (p DescriptorProviderPair) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	return p.Provider.ReaderAt(ctx, desc)
}

func (p DescriptorProviderPair) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if p.InfoProvider != nil {
		return p.InfoProvider.Info(ctx, dgst)
	}
	if dgst != p.Descriptor.Digest {
		return content.Info{}, errors.Errorf("content not found %s", dgst)
	}
	return content.Info{
		Digest: p.Descriptor.Digest,
		Size:   p.Descriptor.Size,
	}, nil
}

func (p DescriptorProviderPair) UnlazySession(desc ocispecs.Descriptor) session.Group {
	type unlazySession interface {
		UnlazySession(ocispecs.Descriptor) session.Group
	}
	if cd, ok := p.Provider.(unlazySession); ok {
		return cd.UnlazySession(desc)
	}
	return nil
}

func (p DescriptorProviderPair) SnapshotLabels(descs []ocispecs.Descriptor, index int) map[string]string {
	type snapshotLabels interface {
		SnapshotLabels([]ocispecs.Descriptor, int) map[string]string
	}
	if cd, ok := p.Provider.(snapshotLabels); ok {
		return cd.SnapshotLabels(descs, index)
	}
	return nil
}

// item is an implementation of a record in the cache chain. After validation,
// normalization and marshalling into the cache config, the item results form
// into the "layers", while the digests and the links form into the "records".
type item struct {
	// dgst is the unique identifier for each record.
	// This *roughly* corresponds to an edge (vertex cachekey + index) in the
	// solver - however, a single vertex can produce multiple unique cache keys
	// (e.g. fast/slow), so it's a one-to-many relation.
	dgst digest.Digest

	// links are what connect records to each other (with an optional selector),
	// organized by input index (which correspond to vertex inputs).
	// We can have multiple links for each index, since *any* of these could be
	// used to get to this item (e.g. we could retrieve by fast/slow key).
	links []map[link]struct{}

	// backlinks are the inverse of a link - these don't actually get directly
	// exported, but they're internally used to help efficiently navigate the
	// graph.
	backlinks   map[*item]struct{}
	backlinksMu sync.Mutex

	// result is the result of computing the edge - this is the target of the
	// data we actually want to store in the cache chain.
	result     *solver.Remote
	resultTime time.Time

	results []solver.CacheResult

	invalid bool

	cc *CacheChains
}

// link is a pointer to an item, with an optional selector.
type link struct {
	src      *item
	selector string
}

func (c *item) getLinks(dgst digest.Digest, index int, selector string) map[*link]struct{} {
	if index >= len(c.links) {
		return nil
	}

	links := c.links[index]
	if len(links) == 0 {
		return nil
	}

	found := map[*link]struct{}{}
	for l := range links {
		if l.src.dgst == dgst && l.selector == selector {
			found[&l] = struct{}{}
		}
	}
	if len(found) == 0 {
		return nil
	}
	return found
}

func (c *item) removeLink(src *item) bool {
	found := false
	for idx := range c.links {
		for l := range c.links[idx] {
			if l.src == src {
				delete(c.links[idx], l)
				found = true
			}
		}
	}
	for idx := range c.links {
		if len(c.links[idx]) == 0 {
			c.links = nil
			break
		}
	}
	return found
}

func (c *item) AddResult(_ digest.Digest, _ int, createdAt time.Time, result *solver.Remote) {
	c.resultTime = createdAt
	c.result = result
}

func (c *item) LinkFrom(rec solver.CacheExporterRecord, index int, selector string) {
	src, ok := rec.(*item)
	if !ok {
		return
	}

	for index >= len(c.links) {
		c.links = append(c.links, map[link]struct{}{})
	}

	c.links[index][link{src: src, selector: selector}] = struct{}{}
	src.backlinksMu.Lock()
	src.backlinks[c] = struct{}{}
	src.backlinksMu.Unlock()
}

// validate checks if an item is valid (i.e. each index has at least one link)
// and marks it as such.
//
// Essentially, if an index has no links, it means that this cache record is
// unreachable by the cache importer, so we should remove it. Once we've marked
// an item as invalid, we remove it from it's backlinks and check it's
// validity again - since now this linked item may be unreachable too.
func (c *item) validate() {
	if c.invalid {
		// early exit, if the item is already invalid, we've already gone
		// through the backlinks
		return
	}

	for _, m := range c.links {
		// if an index has no links, there's no way to access this record, so
		// mark it as invalid
		if len(m) == 0 {
			c.invalid = true
			break
		}
	}

	if c.invalid {
		for bl := range c.backlinks {
			// remove ourselves from the backlinked item
			changed := false
			for _, m := range bl.links {
				for l := range m {
					if l.src == c {
						delete(m, l)
						changed = true
					}
				}
			}

			// if we've removed ourselves, we need to check it again
			if changed {
				bl.validate()
			}
		}
	}
}

func (c *item) walkAllResults(fn func(i *item) error, visited map[*item]struct{}) error {
	if _, ok := visited[c]; ok {
		return nil
	}
	visited[c] = struct{}{}
	if err := fn(c); err != nil {
		return err
	}
	for _, links := range c.links {
		for l := range links {
			if err := l.src.walkAllResults(fn, visited); err != nil {
				return err
			}
		}
	}
	return nil
}

// nopRecord is used to discard cache results that we're not interested in storing.
type nopRecord struct {
}

func (c *nopRecord) AddResult(_ digest.Digest, _ int, createdAt time.Time, result *solver.Remote) {
}

func (c *nopRecord) LinkFrom(rec solver.CacheExporterRecord, index int, selector string) {
}
