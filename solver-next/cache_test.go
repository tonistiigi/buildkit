package solver

import (
	"context"
	"testing"

	"github.com/moby/buildkit/identity"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func depKeys(cks ...CacheKey) []CacheKeyWithSelector {
	var keys []CacheKeyWithSelector
	for _, ck := range cks {
		keys = append(keys, CacheKeyWithSelector{CacheKey: ExportableCacheKey{CacheKey: ck}})
	}
	return keys
}

func TestInMemoryCache(t *testing.T) {
	ctx := context.TODO()

	m := NewInMemoryCacheManager()

	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), 0, nil), testResult("result0"))
	require.NoError(t, err)

	matches, err := m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	res, err := m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	// another record
	cacheBar, err := m.Save(NewCacheKey(dgst("bar"), 0, nil), testResult("result1"))
	require.NoError(t, err)

	matches, err = m.Query(nil, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	res, err = m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))

	// invalid request
	matches, err = m.Query(nil, 0, dgst("baz"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)

	// second level
	k := NewCacheKey(dgst("baz"), Index(1), []CacheKeyWithSelector{
		{CacheKey: cacheFoo}, {CacheKey: cacheBar},
	})
	cacheBaz, err := m.Save(k, testResult("result2"))
	require.NoError(t, err)

	matches, err = m.Query(nil, 0, dgst("baz"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)

	matches, err = m.Query(depKeys(cacheFoo), 0, dgst("baz"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)

	matches, err = m.Query(depKeys(cacheFoo), 1, dgst("baz"), Index(1))
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)

	matches, err = m.Query(depKeys(cacheFoo), 0, dgst("baz"), Index(1))
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	res, err = m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result2", unwrap(res))

	matches2, err := m.Query(depKeys(cacheBar), 1, dgst("baz"), Index(1))
	require.NoError(t, err)
	require.Equal(t, len(matches2), 1)

	require.Equal(t, matches[0].ID, matches2[0].ID)

	k = NewCacheKey(dgst("baz"), Index(1), []CacheKeyWithSelector{
		{CacheKey: cacheFoo},
	})
	_, err = m.Save(k, testResult("result3"))
	require.NoError(t, err)

	matches, err = m.Query(depKeys(cacheFoo), 0, dgst("baz"), Index(1))
	require.NoError(t, err)
	require.Equal(t, len(matches), 2)

	// combination save
	k2 := NewCacheKey("", 0, []CacheKeyWithSelector{
		{CacheKey: cacheFoo}, {CacheKey: cacheBaz},
	})

	k = NewCacheKey(dgst("bax"), 0, []CacheKeyWithSelector{
		{CacheKey: ExportableCacheKey{CacheKey: k2}}, {CacheKey: cacheBar},
	})
	_, err = m.Save(k, testResult("result4"))
	require.NoError(t, err)

	// foo, bar, baz should all point to result4
	matches, err = m.Query(depKeys(cacheFoo), 0, dgst("bax"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	id := matches[0].ID

	matches, err = m.Query(depKeys(cacheBar), 1, dgst("bax"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)
	require.Equal(t, matches[0].ID, id)

	matches, err = m.Query(depKeys(cacheBaz), 0, dgst("bax"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)
	require.Equal(t, matches[0].ID, id)
}

func TestInMemoryCacheSelector(t *testing.T) {
	ctx := context.TODO()

	m := NewInMemoryCacheManager()

	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), 0, nil), testResult("result0"))
	require.NoError(t, err)

	_, err = m.Save(NewCacheKey(dgst("bar"), 0, []CacheKeyWithSelector{
		{CacheKey: cacheFoo, Selector: dgst("sel0")},
	}), testResult("result1"))
	require.NoError(t, err)

	matches, err := m.Query(depKeys(cacheFoo), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)

	matches, err = m.Query([]CacheKeyWithSelector{{Selector: "sel-invalid", CacheKey: ExportableCacheKey{CacheKey: cacheFoo}}}, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)

	matches, err = m.Query([]CacheKeyWithSelector{{Selector: dgst("sel0"), CacheKey: ExportableCacheKey{CacheKey: cacheFoo}}}, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	res, err := m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))
}

func TestInMemoryCacheSelectorNested(t *testing.T) {
	ctx := context.TODO()

	m := NewInMemoryCacheManager()

	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), 0, nil), testResult("result0"))
	require.NoError(t, err)

	k2 := NewCacheKey("", 0, []CacheKeyWithSelector{
		{CacheKey: cacheFoo, Selector: dgst("sel0")},
		{CacheKey: ExportableCacheKey{CacheKey: NewCacheKey(dgst("second"), 0, nil)}},
	})

	_, err = m.Save(NewCacheKey(dgst("bar"), 0, []CacheKeyWithSelector{
		{CacheKey: ExportableCacheKey{CacheKey: k2}},
	}), testResult("result1"))
	require.NoError(t, err)

	matches, err := m.Query([]CacheKeyWithSelector{{Selector: dgst("sel0"), CacheKey: ExportableCacheKey{CacheKey: cacheFoo}}}, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)
	res, err := m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))

	matches, err = m.Query(depKeys(cacheFoo), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)

	matches, err = m.Query([]CacheKeyWithSelector{{Selector: dgst("bar"), CacheKey: ExportableCacheKey{CacheKey: cacheFoo}}}, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)

	matches, err = m.Query(depKeys(NewCacheKey(dgst("second"), 0, nil)), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)
	res, err = m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))

	matches, err = m.Query(depKeys(NewCacheKey(dgst("second"), 0, nil)), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)
}

func TestInMemoryCacheReleaseParent(t *testing.T) {
	storage := NewInMemoryCacheStorage()
	results := NewInMemoryResultStorage()
	m := NewCacheManager(identity.NewID(), storage, results)

	res0 := testResult("result0")
	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), 0, nil), res0)
	require.NoError(t, err)

	res1 := testResult("result1")
	_, err = m.Save(NewCacheKey(dgst("bar"), 0, []CacheKeyWithSelector{
		{CacheKey: cacheFoo},
	}), res1)
	require.NoError(t, err)

	matches, err := m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	require.True(t, matches[0].Loadable)

	err = storage.Release(res0.ID())
	require.NoError(t, err)

	// foo becomes unloadable
	matches, err = m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	require.False(t, matches[0].Loadable)

	matches, err = m.Query(depKeys(matches[0].CacheKey), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	require.True(t, matches[0].Loadable)

	// releasing bar releases both foo and bar
	err = storage.Release(res1.ID())
	require.NoError(t, err)

	matches, err = m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 0)
}

// TestInMemoryCacheRestoreOfflineDeletion deletes a result while the
// cachemanager is not running and checks that it syncs up on restore
func TestInMemoryCacheRestoreOfflineDeletion(t *testing.T) {
	storage := NewInMemoryCacheStorage()
	results := NewInMemoryResultStorage()
	m := NewCacheManager(identity.NewID(), storage, results)

	res0 := testResult("result0")
	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), 0, nil), res0)
	require.NoError(t, err)

	res1 := testResult("result1")
	_, err = m.Save(NewCacheKey(dgst("bar"), 0, []CacheKeyWithSelector{
		{CacheKey: cacheFoo},
	}), res1)
	require.NoError(t, err)

	results2 := NewInMemoryResultStorage()
	_, err = results2.Save(res1) // only add bar
	require.NoError(t, err)

	m = NewCacheManager(identity.NewID(), storage, results2)

	matches, err := m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)
	require.False(t, matches[0].Loadable)

	matches, err = m.Query(depKeys(matches[0].CacheKey), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	require.True(t, matches[0].Loadable)
}

func TestCarryOverFromSublink(t *testing.T) {
	storage := NewInMemoryCacheStorage()
	results := NewInMemoryResultStorage()
	m := NewCacheManager(identity.NewID(), storage, results)

	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), 0, nil), testResult("resultFoo"))
	require.NoError(t, err)

	k := NewCacheKey("", 0, []CacheKeyWithSelector{
		{CacheKey: cacheFoo, Selector: dgst("sel0")},
		{CacheKey: ExportableCacheKey{CacheKey: NewCacheKey(dgst("content0"), 0, nil)}},
	})

	_, err = m.Save(NewCacheKey(dgst("res"), 0, []CacheKeyWithSelector{{CacheKey: ExportableCacheKey{CacheKey: k}}}), testResult("result0"))
	require.NoError(t, err)

	cacheBar, err := m.Save(NewCacheKey(dgst("bar"), 0, nil), testResult("resultBar"))
	require.NoError(t, err)

	k3 := NewCacheKey("", 0, []CacheKeyWithSelector{
		{CacheKey: cacheBar, Selector: dgst("sel0")},
		{CacheKey: ExportableCacheKey{CacheKey: NewCacheKey(dgst("content0"), 0, nil)}},
	})

	matches, err := m.Query(depKeys(k3), 0, dgst("res"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)

	matches, err = m.Query([]CacheKeyWithSelector{{Selector: dgst("sel0"), CacheKey: ExportableCacheKey{CacheKey: cacheBar}}}, 0, dgst("res"), 0)
	require.NoError(t, err)
	require.Equal(t, len(matches), 1)
}

func dgst(s string) digest.Digest {
	return digest.FromBytes([]byte(s))
}

func testResult(v string) Result {
	return &dummyResult{
		id:    identity.NewID(),
		value: v,
	}
}
