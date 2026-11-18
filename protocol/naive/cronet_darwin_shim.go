//go:build with_naive_outbound && darwin && !ios && cgo && !with_purego

package naive

// VilaNet: Mac App Store guard for the statically linked macOS libcronet.a.
//
// Chromium base/mac/info_plist_data.mm (same text in Cronet 150.0.7871.63 and
// 154.0.8037.49, lines 15-18 and 36-37) imports the private CoreFoundation constant _kCFBundleNumericVersionKey
// and uses it only as a key to remove "CFBundleNumericVersion" from a mutable
// copy of NSBundle.infoDictionary. NSMutableDictionary compares keys by content
// (-hash/-isEqual:), so an equal CFString gives the same result. The App Store
// rejects a binary that imports this private symbol (VilaNet, 2026-04-08), and
// removing info_plist_data.o breaks the link (code_signature.o,
// process_requirement.o and mach_port_rendezvous_mac.o call it).
//
// This definition satisfies the reference inside the link unit, so the final
// Mach-O no longer imports the symbol from CoreFoundation. Hidden visibility
// keeps it out of the export table. If a future libcronet.a defines the symbol
// itself, the link fails with a duplicate symbol: remove this file then.

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>

__attribute__((visibility("hidden")))
const CFStringRef _kCFBundleNumericVersionKey = CFSTR("CFBundleNumericVersion");

// Called from Go so that the linker always loads this object from the libbox
// archive before it searches CoreFoundation for the symbol.
static const void *vilanet_cronet_cf_shim_anchor(void) {
	return _kCFBundleNumericVersionKey;
}
*/
import "C"

func init() {
	if C.vilanet_cronet_cf_shim_anchor() == nil {
		panic("naive: CFBundleNumericVersion shim is nil")
	}
}
