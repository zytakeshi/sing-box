package fakeip

import (
	"context"
	"net/netip"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"
)

var _ adapter.FakeIPStore = (*Store)(nil)

type Store struct {
	ctx        context.Context
	logger     logger.Logger
	inet4Range netip.Prefix
	inet6Range netip.Prefix
	inet4Last  netip.Addr
	inet6Last  netip.Addr
	storage    adapter.FakeIPStorage

	addressAccess  sync.Mutex
	inet4Current   netip.Addr
	inet6Current   netip.Addr
	inet4Exhausted bool
	inet6Exhausted bool
}

func NewStore(ctx context.Context, logger logger.Logger, inet4Range netip.Prefix, inet6Range netip.Prefix) *Store {
	store := &Store{
		ctx:        ctx,
		logger:     logger,
		inet4Range: inet4Range,
		inet6Range: inet6Range,
	}
	if inet4Range.IsValid() {
		store.inet4Last = broadcastAddress(inet4Range)
	}
	if inet6Range.IsValid() {
		store.inet6Last = broadcastAddress(inet6Range)
	}
	return store
}

func broadcastAddress(prefix netip.Prefix) netip.Addr {
	addr := prefix.Addr()
	raw := addr.As16()
	bits := prefix.Bits()
	if addr.Is4() {
		bits += 96
	}
	for i := bits; i < 128; i++ {
		raw[i/8] |= 1 << (7 - i%8)
	}
	if addr.Is4() {
		return netip.AddrFrom4([4]byte(raw[12:]))
	}
	return netip.AddrFrom16(raw)
}

func nextAddress(addressRange netip.Prefix, last netip.Addr, current netip.Addr) netip.Addr {
	address := current.Next()
	if address == last || !addressRange.Contains(address) {
		address = addressRange.Addr().Next().Next()
	}
	return address
}

// nextFreeAddress returns the next address after current that no other domain
// owns, so an allocation never takes over an address that a client may still
// hold for another domain. After a full cycle without a free address the range
// is exhausted: addresses are then reused in order, as before, and the cycle
// is not walked again until the store is restarted or reset.
func (s *Store) nextFreeAddress(addressRange netip.Prefix, last netip.Addr, current netip.Addr, domain string, exhausted *bool) netip.Addr {
	first := nextAddress(addressRange, last, current)
	if *exhausted {
		return first
	}
	address := first
	for {
		owner, loaded := s.storage.FakeIPLoad(address)
		if !loaded || owner == domain {
			return address
		}
		address = nextAddress(addressRange, last, address)
		if address == first {
			*exhausted = true
			return first
		}
	}
}

func (s *Store) Start() error {
	var storage adapter.FakeIPStorage
	cacheFile := service.FromContext[adapter.CacheFile](s.ctx)
	if cacheFile != nil && cacheFile.StoreFakeIP() {
		storage = cacheFile
	}
	if storage == nil {
		storage = NewMemoryStorage()
	}
	metadata := storage.FakeIPMetadata()
	if metadata != nil && metadata.Inet4Range == s.inet4Range && metadata.Inet6Range == s.inet6Range {
		s.inet4Current = metadata.Inet4Current
		s.inet6Current = metadata.Inet6Current
	} else {
		if s.inet4Range.IsValid() {
			s.inet4Current = s.inet4Range.Addr().Next()
		}
		if s.inet6Range.IsValid() {
			s.inet6Current = s.inet6Range.Addr().Next()
		}
		// The stored metadata is consumed when it is read, so it is also
		// missing after an unclean stop. Only a recorded range change resets
		// the table. Without a record the mappings are kept, because clients
		// may still hold them: Create replaces the ones outside the range and
		// steps over the addresses the others occupy.
		if metadata != nil {
			err := storage.FakeIPReset()
			if err != nil {
				s.logger.Warn("reset FakeIP cache: ", err)
			}
		}
	}
	s.storage = storage
	return nil
}

func (s *Store) Contains(address netip.Addr) bool {
	return s.inet4Range.Contains(address) || s.inet6Range.Contains(address)
}

func (s *Store) Close() error {
	if s.storage == nil {
		return nil
	}
	s.addressAccess.Lock()
	metadata := &adapter.FakeIPMetadata{
		Inet4Range:   s.inet4Range,
		Inet6Range:   s.inet6Range,
		Inet4Current: s.inet4Current,
		Inet6Current: s.inet6Current,
	}
	s.addressAccess.Unlock()
	return s.storage.FakeIPSaveMetadata(metadata)
}

func (s *Store) Create(domain string, isIPv6 bool) (netip.Addr, error) {
	addressRange := s.inet4Range
	if isIPv6 {
		addressRange = s.inet6Range
	}
	// A stored address outside the configured range is replaced: the router
	// only maps addresses inside the range back to their domain.
	if address, loaded := s.storage.FakeIPLoadDomain(domain, isIPv6); loaded && addressRange.Contains(address) {
		return address, nil
	}

	s.addressAccess.Lock()
	defer s.addressAccess.Unlock()

	// Double-check after acquiring lock
	if address, loaded := s.storage.FakeIPLoadDomain(domain, isIPv6); loaded && addressRange.Contains(address) {
		return address, nil
	}

	var address netip.Addr
	if !isIPv6 {
		if !s.inet4Current.IsValid() {
			return netip.Addr{}, E.New("missing IPv4 fakeip address range")
		}
		address = s.nextFreeAddress(s.inet4Range, s.inet4Last, s.inet4Current, domain, &s.inet4Exhausted)
		s.inet4Current = address
	} else {
		if !s.inet6Current.IsValid() {
			return netip.Addr{}, E.New("missing IPv6 fakeip address range")
		}
		address = s.nextFreeAddress(s.inet6Range, s.inet6Last, s.inet6Current, domain, &s.inet6Exhausted)
		s.inet6Current = address
	}
	err := s.storage.FakeIPStore(address, domain)
	if err != nil {
		s.logger.Warn("save FakeIP cache: ", err)
	}
	s.storage.FakeIPSaveMetadataAsync(&adapter.FakeIPMetadata{
		Inet4Range:   s.inet4Range,
		Inet6Range:   s.inet6Range,
		Inet4Current: s.inet4Current,
		Inet6Current: s.inet6Current,
	})
	return address, nil
}

func (s *Store) Lookup(address netip.Addr) (string, bool) {
	return s.storage.FakeIPLoad(address)
}

func (s *Store) Reset() error {
	s.addressAccess.Lock()
	s.inet4Exhausted = false
	s.inet6Exhausted = false
	s.addressAccess.Unlock()
	return s.storage.FakeIPReset()
}
