// Command xsyscpu links golang.org/x/sys/cpu the way libbox does, so
// TestXSysCPUKeepsAndroidMinimalFeatures can check which CPU-feature code the
// android/arm64 linker keeps.
package main

import (
	"fmt"

	"golang.org/x/sys/cpu"
)

func main() {
	fmt.Println(cpu.ARM64.HasAES, cpu.ARM64.HasATOMICS)
}
