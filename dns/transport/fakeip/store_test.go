package fakeip_test

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns/transport/fakeip"
	"github.com/sagernet/sing-box/experimental/cachefile"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

var (
	rangeA = netip.MustParsePrefix("198.20.0.0/15")
	rangeB = netip.MustParsePrefix("198.18.0.0/15")
	range6 = netip.MustParsePrefix("fc00::/18")
)

func openCacheFile(t *testing.T, path string) *cachefile.CacheFile {
	t.Helper()
	cacheFile := cachefile.New(context.Background(), option.CacheFileOptions{
		Enabled:     true,
		Path:        path,
		StoreFakeIP: true,
	})
	require.NoError(t, cacheFile.Start(adapter.StartStateInitialize))
	return cacheFile
}

// startStore opens the cache file at path and starts a store on it. The
// returned function closes both, like a clean shutdown.
func startStore(t *testing.T, path string, inet4Range netip.Prefix, inet6Range netip.Prefix) (*fakeip.Store, func()) {
	t.Helper()
	cacheFile := openCacheFile(t, path)
	ctx := service.ContextWith[adapter.CacheFile](context.Background(), cacheFile)
	store := fakeip.NewStore(ctx, log.StdLogger(), inet4Range, inet6Range)
	require.NoError(t, store.Start())
	return store, func() {
		require.NoError(t, store.Close())
		require.NoError(t, cacheFile.Close())
	}
}

// A range change must leave no address of the old range behind, also when no
// IPv6 address was ever stored (an IPv4-only configuration has no IPv6 range,
// so the IPv6 domain bucket does not exist).
func TestStoreRangeChangeIPv4Only(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")

	store, closeStore := startStore(t, path, rangeA, netip.Prefix{})
	oldAddress, err := store.Create("example.org", false)
	require.NoError(t, err)
	require.True(t, rangeA.Contains(oldAddress))
	closeStore()

	store, closeStore = startStore(t, path, rangeB, netip.Prefix{})
	defer closeStore()
	address, err := store.Create("example.org", false)
	require.NoError(t, err)
	require.True(t, store.Contains(address), "address %s is outside the configured range", address)
	domain, loaded := store.Lookup(address)
	require.True(t, loaded)
	require.Equal(t, "example.org", domain)
	_, loaded = store.Lookup(oldAddress)
	require.False(t, loaded, "the old range was not reset")
}

// The same change with an IPv6 address stored: all three buckets exist.
func TestStoreRangeChangeDualStack(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")

	store, closeStore := startStore(t, path, rangeA, range6)
	_, err := store.Create("example.org", false)
	require.NoError(t, err)
	_, err = store.Create("example.org", true)
	require.NoError(t, err)
	closeStore()

	store, closeStore = startStore(t, path, rangeB, range6)
	defer closeStore()
	address, err := store.Create("example.org", false)
	require.NoError(t, err)
	require.True(t, store.Contains(address), "address %s is outside the configured range", address)
}

// A cache file written before the reset fix can hold an address of another
// range while its metadata already names the current one, so no reset runs.
// Such an address must not be handed out: the router only maps addresses
// inside the current range back to their domain.
func TestStoreIgnoresStoredAddressOutsideRange(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	staleAddress := netip.MustParseAddr("198.20.0.4")

	cacheFile := openCacheFile(t, path)
	require.NoError(t, cacheFile.FakeIPStore(staleAddress, "example.org"))
	require.NoError(t, cacheFile.FakeIPSaveMetadata(&adapter.FakeIPMetadata{
		Inet4Range:   rangeB,
		Inet4Current: netip.MustParseAddr("198.18.0.9"),
	}))
	require.NoError(t, cacheFile.Close())

	store, closeStore := startStore(t, path, rangeB, netip.Prefix{})
	address, err := store.Create("example.org", false)
	require.NoError(t, err)
	require.NotEqual(t, staleAddress, address)
	require.True(t, store.Contains(address), "address %s is outside the configured range", address)
	domain, loaded := store.Lookup(address)
	require.True(t, loaded)
	require.Equal(t, "example.org", domain)
	closeStore()

	// The replacement is persistent and stable.
	store, closeStore = startStore(t, path, rangeB, netip.Prefix{})
	defer closeStore()
	again, err := store.Create("example.org", false)
	require.NoError(t, err)
	require.Equal(t, address, again)
}

// Control: an address inside the range is kept across restarts.
func TestStoreKeepsStoredAddressInsideRange(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")

	store, closeStore := startStore(t, path, rangeB, netip.Prefix{})
	first, err := store.Create("example.org", false)
	require.NoError(t, err)
	other, err := store.Create("example.com", false)
	require.NoError(t, err)
	require.NotEqual(t, first, other)
	closeStore()

	store, closeStore = startStore(t, path, rangeB, netip.Prefix{})
	defer closeStore()
	again, err := store.Create("example.org", false)
	require.NoError(t, err)
	require.Equal(t, first, again)
	next, err := store.Create("example.net", false)
	require.NoError(t, err)
	require.NotEqual(t, first, next)
	require.NotEqual(t, other, next)
}
