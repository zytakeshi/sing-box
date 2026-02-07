//go:build !with_clash_api

package include

import (
	"context"

	"github.com/zytakeshi/vpncheap-core/adapter"
	"github.com/zytakeshi/vpncheap-core/experimental"
	"github.com/zytakeshi/vpncheap-core/log"
	"github.com/zytakeshi/vpncheap-core/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func init() {
	experimental.RegisterClashServerConstructor(func(ctx context.Context, logFactory log.ObservableFactory, options option.ClashAPIOptions) (adapter.ClashServer, error) {
		return nil, E.New(`clash api is not included in this build, rebuild with -tags with_clash_api`)
	})
}
