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
	return openCacheFileWith(t, option.CacheFileOptions{
		Enabled:     true,
		Path:        path,
		StoreFakeIP: true,
	})
}

func openCacheFileWith(t *testing.T, options option.CacheFileOptions) *cachefile.CacheFile {
	t.Helper()
	cacheFile := cachefile.New(context.Background(), options)
	require.NoError(t, cacheFile.Start(adapter.StartStateInitialize))
	return cacheFile
}

// requireMappedBack asserts the invariant the router relies on: an address the
// store hands out is inside its range and maps back to the domain.
func requireMappedBack(t *testing.T, store *fakeip.Store, domain string) netip.Addr {
	t.Helper()
	address, err := store.Create(domain, false)
	require.NoError(t, err)
	require.True(t, store.Contains(address), "%s: address %s is outside the configured range", domain, address)
	mapped, loaded := store.Lookup(address)
	require.True(t, loaded, "%s: address %s has no mapping", domain, address)
	require.Equal(t, domain, mapped)
	return address
}

// startStore opens the cache file at path and starts a store on it. The
// returned function closes both, like a clean shutdown.
func startStore(t *testing.T, path string, inet4Range netip.Prefix, inet6Range netip.Prefix) (*fakeip.Store, func()) {
	t.Helper()
	store, cacheFile := startStoreOnFile(t, path, inet4Range, inet6Range)
	return store, func() {
		require.NoError(t, store.Close())
		require.NoError(t, cacheFile.Close())
	}
}

// startStoreOnFile also returns the cache file, so a test can end the session
// without Store.Close: the stored metadata is consumed by Start and only
// written back by Close, so that is what an unclean stop leaves behind.
func startStoreOnFile(t *testing.T, path string, inet4Range netip.Prefix, inet6Range netip.Prefix) (*fakeip.Store, *cachefile.CacheFile) {
	t.Helper()
	cacheFile := openCacheFile(t, path)
	ctx := service.ContextWith[adapter.CacheFile](context.Background(), cacheFile)
	store := fakeip.NewStore(ctx, log.StdLogger(), inet4Range, inet6Range)
	require.NoError(t, store.Start())
	return store, cacheFile
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

// After an unclean stop the metadata is missing. The mappings must survive:
// clients may still hold the old answers, so a domain keeps its address and a
// new domain never receives an address that another domain owns.
func TestStoreUncleanStopKeepsMappings(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")

	store, cacheFile := startStoreOnFile(t, path, rangeB, netip.Prefix{})
	first, err := store.Create("example.org", false)
	require.NoError(t, err)
	second, err := store.Create("example.com", false)
	require.NoError(t, err)
	require.NoError(t, cacheFile.Close()) // no store.Close()

	store, closeStore := startStore(t, path, rangeB, netip.Prefix{})
	defer closeStore()
	// Ask in the opposite order: a reset table would hand example.org's
	// address to example.com.
	again, err := store.Create("example.com", false)
	require.NoError(t, err)
	require.Equal(t, second, again)
	again, err = store.Create("example.org", false)
	require.NoError(t, err)
	require.Equal(t, first, again)
	third, err := store.Create("example.net", false)
	require.NoError(t, err)
	require.NotEqual(t, first, third)
	require.NotEqual(t, second, third)
	domain, loaded := store.Lookup(first)
	require.True(t, loaded)
	require.Equal(t, "example.org", domain)
	domain, loaded = store.Lookup(second)
	require.True(t, loaded)
	require.Equal(t, "example.com", domain)
}

// Replacing an out-of-range address must not take an address that another
// domain owns, also when the stored pointer is behind the stored addresses.
func TestStoreReplacementDoesNotDisplaceAnotherDomain(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	liveAddress := netip.MustParseAddr("198.18.0.2")
	staleAddress := netip.MustParseAddr("198.20.0.2")

	cacheFile := openCacheFile(t, path)
	require.NoError(t, cacheFile.FakeIPStore(liveAddress, "live.example"))
	require.NoError(t, cacheFile.FakeIPStore(staleAddress, "stale.example"))
	require.NoError(t, cacheFile.FakeIPSaveMetadata(&adapter.FakeIPMetadata{
		Inet4Range:   rangeB,
		Inet4Current: netip.MustParseAddr("198.18.0.1"),
	}))
	require.NoError(t, cacheFile.Close())

	store, closeStore := startStore(t, path, rangeB, netip.Prefix{})
	defer closeStore()
	address, err := store.Create("stale.example", false)
	require.NoError(t, err)
	require.True(t, store.Contains(address))
	require.NotEqual(t, liveAddress, address)
	domain, loaded := store.Lookup(liveAddress)
	require.True(t, loaded)
	require.Equal(t, "live.example", domain)
	again, err := store.Create("live.example", false)
	require.NoError(t, err)
	require.Equal(t, liveAddress, again)
	fresh, err := store.Create("new.example", false)
	require.NoError(t, err)
	require.NotEqual(t, liveAddress, fresh)
	require.NotEqual(t, address, fresh)
}

// A full range still answers: addresses are reused, as before.
func TestStoreReusesAddressesWhenRangeIsFull(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	smallRange := netip.MustParsePrefix("198.18.0.0/29") // usable: .2 to .6

	store, closeStore := startStore(t, path, smallRange, netip.Prefix{})
	defer closeStore()
	seen := make(map[netip.Addr]bool)
	for _, domain := range []string{"a.example", "b.example", "c.example", "d.example", "e.example"} {
		address, err := store.Create(domain, false)
		require.NoError(t, err)
		require.True(t, smallRange.Contains(address))
		require.False(t, seen[address], "address %s was handed out twice while the range had room", address)
		seen[address] = true
	}
	for _, domain := range []string{"f.example", "g.example"} {
		address, err := store.Create(domain, false)
		require.NoError(t, err)
		require.True(t, seen[address], "address %s is outside the usable range", address)
		owner, loaded := store.Lookup(address)
		require.True(t, loaded)
		require.Equal(t, domain, owner)
	}
}

// The cache ID partitions the selector and rule-set state, not the FakeIP
// table: a session with another cache ID keeps answering the same addresses.
func TestStoreCacheIDChangeKeepsMappings(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	open := func(cacheID string) (*fakeip.Store, func()) {
		cacheFile := openCacheFileWith(t, option.CacheFileOptions{Enabled: true, Path: path, StoreFakeIP: true, CacheID: cacheID})
		ctx := service.ContextWith[adapter.CacheFile](context.Background(), cacheFile)
		store := fakeip.NewStore(ctx, log.StdLogger(), rangeB, netip.Prefix{})
		require.NoError(t, store.Start())
		return store, func() {
			require.NoError(t, store.Close())
			require.NoError(t, cacheFile.Close())
		}
	}

	store, closeStore := open("first")
	first := requireMappedBack(t, store, "example.org")
	second := requireMappedBack(t, store, "example.com")
	closeStore()

	store, closeStore = open("second")
	defer closeStore()
	require.Equal(t, second, requireMappedBack(t, store, "example.com"))
	require.Equal(t, first, requireMappedBack(t, store, "example.org"))
	third := requireMappedBack(t, store, "example.net")
	require.NotEqual(t, first, third)
	require.NotEqual(t, second, third)
}

// A session without store_fakeip uses a table in memory. Going back to the
// stored table must keep every answer inside the range and mapped back, and a
// domain that was only seen in memory must not take a stored domain's address.
func TestStoreFakeIPStorageSwitchStaysConsistent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")

	store, closeStore := startStore(t, path, rangeB, netip.Prefix{})
	stored := requireMappedBack(t, store, "stored.example")
	closeStore()

	cacheFile := openCacheFileWith(t, option.CacheFileOptions{Enabled: true, Path: path})
	ctx := service.ContextWith[adapter.CacheFile](context.Background(), cacheFile)
	memoryStore := fakeip.NewStore(ctx, log.StdLogger(), rangeB, netip.Prefix{})
	require.NoError(t, memoryStore.Start())
	requireMappedBack(t, memoryStore, "memory.example")
	requireMappedBack(t, memoryStore, "stored.example")
	require.NoError(t, memoryStore.Close())
	require.NoError(t, cacheFile.Close())

	store, closeStore = startStore(t, path, rangeB, netip.Prefix{})
	defer closeStore()
	require.Equal(t, stored, requireMappedBack(t, store, "stored.example"))
	fresh := requireMappedBack(t, store, "memory.example")
	require.NotEqual(t, stored, fresh)
	mapped, loaded := store.Lookup(stored)
	require.True(t, loaded)
	require.Equal(t, "stored.example", mapped)
}
