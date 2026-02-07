//go:build !generate

package main

import "github.com/zytakeshi/vpncheap-core/log"

func main() {
	if err := mainCommand.Execute(); err != nil {
		log.Fatal(err)
	}
}
