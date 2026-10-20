// traust-cli is the front door to the traust security workflow: one binary
// that sets up the environment, drives harness skills headlessly through the
// agent of your choice, validates the output, and shows the results.
package main

import "github.com/traust-security/traust-cli/cmd"

func main() {
	cmd.Execute()
}
