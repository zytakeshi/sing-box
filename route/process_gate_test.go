package route

import (
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestProcessNameGateNames(t *testing.T) {
	nameRule := option.Rule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{ProcessName: []string{"qbittorrent.exe", "uTorrent.exe"}}}}
	pathRule := option.Rule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{ProcessPath: []string{`C:\x\y.exe`}}}}
	nested := option.Rule{Type: C.RuleTypeLogical, LogicalOptions: option.LogicalRule{RawLogicalRule: option.RawLogicalRule{Rules: []option.Rule{nameRule}}}}
	nestedPath := option.Rule{Type: C.RuleTypeLogical, LogicalOptions: option.LogicalRule{RawLogicalRule: option.RawLogicalRule{Rules: []option.Rule{pathRule}}}}

	names, ok := processNameGateNames([]option.Rule{nameRule}, nil)
	if !ok || len(names) != 2 {
		t.Fatalf("plain process_name rule: ok=%v names=%v", ok, names)
	}
	if names, ok = processNameGateNames([]option.Rule{nested}, nil); !ok || len(names) != 2 {
		t.Fatalf("nested process_name rule: ok=%v names=%v", ok, names)
	}
	if _, ok = processNameGateNames([]option.Rule{nameRule, pathRule}, nil); ok {
		t.Fatal("a process_path rule must disable the gate")
	}
	if _, ok = processNameGateNames([]option.Rule{nestedPath}, nil); ok {
		t.Fatal("a nested process_path rule must disable the gate")
	}
	dnsPkg := option.DNSRule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultDNSRule{RawDefaultDNSRule: option.RawDefaultDNSRule{PackageName: []string{"a.b"}}}}
	if _, ok = processNameGateNames([]option.Rule{nameRule}, []option.DNSRule{dnsPkg}); ok {
		t.Fatal("a DNS package_name rule must disable the gate")
	}
	if names, ok = processNameGateNames(nil, nil); !ok || len(names) != 0 {
		t.Fatalf("no process rule: ok=%v names=%v", ok, names)
	}
}

func TestLowerNameSetIsCaseInsensitiveSuperset(t *testing.T) {
	set := lowerNameSet([]string{"uTorrent.exe"})
	if _, ok := set["utorrent.exe"]; !ok {
		t.Fatal("folded name missing")
	}
}
