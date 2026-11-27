package rule

import (
	"context"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestRuleSetConcurrentReloadAndRead(t *testing.T) {
	ctx := context.Background()
	domain := "example.com"
	rules := []option.HeadlessRule{{
		DefaultOptions: option.DefaultHeadlessRule{Domain: []string{domain}},
	}}
	remoteRules := []byte(`{"version":4,"rules":[{"domain":["example.com"]}]}`)
	local := &LocalRuleSet{ctx: ctx}
	remote := NewRemoteRuleSet(ctx, nil, option.RuleSet{Format: C.RuleSetFormatSource})
	for _, tc := range []struct {
		name   string
		set    adapter.RuleSet
		reload func() error
	}{
		{"local", local, func() error { return local.reloadRules(rules) }},
		{"remote", remote, func() error { return remote.loadBytes(remoteRules) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer tc.set.Close()
			if err := tc.reload(); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			wg.Go(func() {
				for range 100 {
					if !tc.set.Match(&adapter.InboundContext{Domain: domain}) {
						t.Error("reload lost matching rules")
						return
					}
					_ = tc.set.String()
					_ = tc.set.Metadata()
					_ = tc.set.ExtractIPSet()
				}
			})
			defer wg.Wait()
			for range 100 {
				if err := tc.reload(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
