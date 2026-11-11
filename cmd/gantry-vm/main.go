// Command gantry-vm runs one Container VM on Apple's Virtualization
// framework. The app starts it (one process per machine) and talks to it by
// the machine's control socket; it is a binary of its own so the window's
// toolkit and the Virtualization binding do not share one binary.
//
//	gantry-vm <machine>
package main

import (
	"fmt"
	"os"

	"gantry/internal/cvm"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Fprintln(os.Stderr, "uso: gantry-vm <máquina>")
		os.Exit(2)
	}
	if err := cvm.RunMachine(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
