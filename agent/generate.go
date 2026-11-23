// Package agent has only the commands that make the generated code of the agent.
//
// The Go code of the agent protocol (gen/) is not in Git, so that two pull requests that change
// agent.proto do not conflict in the generated code. Run "go generate" in this directory after a
// clone, after a pull and after a change to a .proto file or to schema/input-rules.json. It needs
// buf (https://buf.build/docs/cli/installation/).
package agent

//go:generate buf generate
//go:generate go run ./cmd/cf-rules
