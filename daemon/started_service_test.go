//go:build with_clash_api

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
)

func TestCloseServiceAfterFailedStart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		closeErr error
	}{
		{name: "clean rollback"},
		{name: "failed rollback", closeErr: errors.New("outbound close failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := include.OutboundRegistry()
			startErr := errors.New("outbound start failed")
			closeCount := 0
			outbound.Register(registry, "start_failure_fixture", func(_ context.Context, _ adapter.Router, _ log.ContextLogger, tag string, _ struct{}) (adapter.Outbound, error) {
				return &failingStartupOutbound{
					Adapter: outbound.NewAdapter("start_failure_fixture", tag, nil, nil),
					start:   startErr,
					close: func() error {
						closeCount++
						return tc.closeErr
					},
				}, nil
			})
			ctx := box.Context(context.Background(), include.InboundRegistry(), registry, include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry())
			s := NewStartedService(ServiceOptions{Context: ctx})
			t.Cleanup(s.Close)
			cachePath := filepath.Join(t.TempDir(), "cache.db")
			const profile = `{"experimental":{"cache_file":{"path":%q}},"outbounds":[%s]}`
			config := fmt.Sprintf(profile, cachePath, `{"type":"start_failure_fixture","tag":"bad"}`)
			err := s.StartOrReloadService(config, nil)
			if !errors.Is(err, startErr) || !strings.Contains(err.Error(), "start outbound/start_failure_fixture[bad]") {
				t.Fatalf("start returned %v, want original tagged error", err)
			}
			if tc.closeErr != nil && !errors.Is(err, tc.closeErr) {
				t.Fatalf("start returned %v, want rollback failure preserved", err)
			}
			if closeCount != 1 {
				t.Fatalf("rollback closed outbound %d times, want 1", closeCount)
			}
			err = s.CloseService()
			if tc.closeErr != nil {
				if err == nil || s.serviceStatus.Status != ServiceStatus_FATAL {
					t.Fatalf("unconfirmed rollback: CloseService returned %v, status %v", err, s.serviceStatus.Status)
				}
				return
			}
			if err != nil || s.serviceStatus.Status != ServiceStatus_IDLE || s.instance != nil {
				t.Fatalf("confirmed rollback: CloseService returned %v, status %v, instance remains %v", err, s.serviceStatus.Status, s.instance != nil)
			}
			if closeCount != 1 {
				t.Fatalf("CloseService invoked rollback twice: %d closes", closeCount)
			}
			replacement := fmt.Sprintf(profile, cachePath, `{"type":"direct"}`)
			if err := s.StartOrReloadService(replacement, nil); err != nil {
				t.Fatalf("clean replacement did not start: %v", err)
			}
			if err := s.CloseService(); err != nil {
				t.Fatalf("replacement did not stop: %v", err)
			}
		})
	}
}

type failingStartupOutbound struct {
	outbound.Adapter
	start error
	close func() error
}

func (o *failingStartupOutbound) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateStart {
		return o.start
	}
	return nil
}

func (o *failingStartupOutbound) Close() error {
	return o.close()
}

func (o *failingStartupOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, errors.ErrUnsupported
}

func (o *failingStartupOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.ErrUnsupported
}
