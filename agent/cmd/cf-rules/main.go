// Command cf-rules writes the input checks of the agent (Go) and the input rules of the panel
// API (PHP) from one file: schema/input-rules.json.
//
// Run it in the directory "agent" after each change of the rules file:
//
//	go run ./cmd/cf-rules
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/itbm/cloudfactory/agent/internal/rulesgen"
)

func main() {
	in := flag.String("schema", "../schema/input-rules.json", "the rules file")
	goOut := flag.String("go", "internal/validate/rules_gen.go", "the Go file to write")
	phpOut := flag.String("php", "../api/app/Validation/InputRules.php", "the PHP file to write")
	flag.Parse()

	if err := run(*in, *goOut, *phpOut); err != nil {
		fmt.Fprintln(os.Stderr, "cf-rules:", err)
		os.Exit(1)
	}
}

func run(in, goOut, phpOut string) error {
	rules, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	goCode, phpCode, err := rulesgen.Generate(rules)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	if err := os.WriteFile(goOut, goCode, 0o644); err != nil {
		return err
	}
	return os.WriteFile(phpOut, phpCode, 0o644)
}
