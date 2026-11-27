package transport

import (
	"bytes"
	"sync"
	"testing"

	mDNS "github.com/miekg/dns"
)

func TestWriteMessageDoesNotMutateSharedEDNS(t *testing.T) {
	message := new(mDNS.Msg)
	message.SetQuestion("example.com.", mDNS.TypeA)
	message.SetEdns0(1232, false)
	message.Rcode = mDNS.RcodeBadVers
	before := message.IsEdns0().Hdr.Ttl

	var wg sync.WaitGroup
	for id := uint16(1); id <= 2; id++ {
		wg.Go(func() {
			var wire bytes.Buffer
			if err := WriteMessage(&wire, id, message); err != nil {
				t.Error(err)
				return
			}
			var packed mDNS.Msg
			if err := packed.Unpack(wire.Bytes()[2:]); err != nil {
				t.Error(err)
				return
			}
			if packed.Id != id || packed.Rcode != mDNS.RcodeBadVers {
				t.Errorf("wrong wire ID or RCODE: %d, %d", packed.Id, packed.Rcode)
			}
		})
	}
	wg.Wait()
	if after := message.IsEdns0().Hdr.Ttl; after != before {
		t.Fatalf("packing changed the shared EDNS TTL: %#x → %#x", before, after)
	}
}
