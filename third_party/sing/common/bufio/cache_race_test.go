package bufio

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type emptyCacheTestConn struct{ net.Conn }

func (emptyCacheTestConn) Read([]byte) (int, error) { return 0, io.EOF }
func (emptyCacheTestConn) Close() error             { return nil }

type emptyCacheTestPacketConn struct{ N.PacketConn }

func (emptyCacheTestPacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, io.EOF
}

func (emptyCacheTestPacketConn) Close() error { return nil }

func TestCachedReadAndClose(t *testing.T) {
	for _, name := range []string{"conn", "reader", "packet"} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				buffer := buf.New()
				if _, err := buffer.WriteString("sniffed"); err != nil {
					t.Fatal(err)
				}
				var read func() error
				var closeCache func() error
				switch name {
				case "conn":
					conn := NewCachedConn(emptyCacheTestConn{}, buffer)
					read = func() error { _, err := conn.Read(make([]byte, 3)); return err }
					closeCache = conn.Close
				case "reader":
					reader := NewCachedReader(emptyCacheTestConn{}, buffer)
					read = func() error { _, err := reader.Read(make([]byte, 3)); return err }
					closeCache = reader.Close
				case "packet":
					conn := NewCachedPacketConn(emptyCacheTestPacketConn{}, buffer, M.Socksaddr{})
					read = func() error {
						packet := buf.New()
						defer packet.Release()
						_, err := conn.ReadPacket(packet)
						return err
					}
					closeCache = conn.Close
				}
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := read(); err != nil && err != io.EOF {
						t.Error(err)
					}
				}()
				err := closeCache()
				wg.Wait()
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCachedWriteToConsumesBuffer(t *testing.T) {
	for _, name := range []string{"conn", "reader"} {
		t.Run(name, func(t *testing.T) {
			buffer := buf.New()
			if _, err := buffer.WriteString("sniffed"); err != nil {
				t.Fatal(err)
			}
			var cache interface {
				io.ReadCloser
				io.WriterTo
			}
			if name == "conn" {
				cache = NewCachedConn(emptyCacheTestConn{}, buffer)
			} else {
				cache = NewCachedReader(emptyCacheTestConn{}, buffer)
			}
			defer cache.Close()
			var output bytes.Buffer
			if n, err := cache.WriteTo(&output); n != 7 || err != nil || output.String() != "sniffed" {
				t.Fatalf("WriteTo = %d, %v, %q", n, err, output.String())
			}
			if n, err := cache.Read(make([]byte, 7)); n != 0 || err != io.EOF {
				t.Fatalf("cache replayed bytes after WriteTo: Read = %d, %v", n, err)
			}
		})
	}
}
