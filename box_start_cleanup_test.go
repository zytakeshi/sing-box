package box_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

// A failed Start already closes the box. An embedder must be able to read
// that cleanup's result instead of calling Close again (which only says
// os.ErrClosed and so cannot confirm anything).
func TestFailedStartKeepsItsCleanupResult(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := uint16(busy.Addr().(*net.TCPAddr).Port)

	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry())
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log: &option.LogOptions{Disabled: true},
			Inbounds: []option.Inbound{{
				Type: "mixed",
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     addrPtr("127.0.0.1"),
						ListenPort: port,
					},
				},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err == nil {
		instance.Close()
		t.Fatal("start on a port in use must fail")
	}
	if cleanup := instance.StartCleanupError(); cleanup != nil {
		t.Fatalf("the failed start's own cleanup should have closed cleanly, got %v", cleanup)
	}
	if err := instance.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("a second Close must report the box as already closed, got %v", err)
	}
}

func TestSuccessfulStartHasNoCleanupError(t *testing.T) {
	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry())
	instance, err := box.New(box.Options{Context: ctx, Options: option.Options{Log: &option.LogOptions{Disabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if instance.StartCleanupError() != nil {
		t.Fatal("a started box has no start-cleanup error")
	}
}

func addrPtr(s string) *badoption.Addr {
	addr := badoption.Addr(netip.MustParseAddr(s))
	return &addr
}
