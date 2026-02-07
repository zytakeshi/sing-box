//go:build generate && generate_completions

package main

import "github.com/zytakeshi/vpncheap-core/log"

func main() {
	err := generateCompletions()
	if err != nil {
		log.Fatal(err)
	}
}

func generateCompletions() error {
	err := mainCommand.GenBashCompletionFile("release/completions/vpncheap.bash")
	if err != nil {
		return err
	}
	err = mainCommand.GenFishCompletionFile("release/completions/vpncheap.fish", true)
	if err != nil {
		return err
	}
	err = mainCommand.GenZshCompletionFile("release/completions/vpncheap.zsh")
	if err != nil {
		return err
	}
	return nil
}
