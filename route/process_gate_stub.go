//go:build !windows

package route

import "github.com/sagernet/sing-box/log"

// Only the Windows searcher pays a full TCP-table dump per lookup; other
// platforms keep looking up every connection.
func newProcessNameGate(_ []string, _ log.ContextLogger) processNameGate {
	return nil
}
