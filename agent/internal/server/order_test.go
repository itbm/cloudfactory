package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"slices"
	"testing"
)

// TestMethodOrder checks that the methods of the agent service are in alphabetical order, in
// agent.proto and in server.go. Then two pull requests that add methods put them in different
// places, and do not conflict (docs/development.md#parallel-pull-requests).
func TestMethodOrder(t *testing.T) {
	proto, err := os.ReadFile("../../proto/cloudfactory/agent/v1/agent.proto")
	if err != nil {
		t.Fatal(err)
	}
	// The methods that make a job come first, then the methods that give the result at once.
	var jobs, now []string
	for _, m := range regexp.MustCompile(`(?m)^  rpc (\w+)\(\w+\) returns \((\w+)\);`).FindAllSubmatch(proto, -1) {
		if string(m[2]) == "JobAccepted" {
			if len(now) > 0 {
				t.Errorf("agent.proto: put %s with the other methods that make a job", m[1])
			}
			jobs = append(jobs, string(m[1]))
		} else {
			now = append(now, string(m[1]))
		}
	}
	sorted(t, "agent.proto: the methods that make a job", jobs)
	sorted(t, "agent.proto: the methods that do not make a job", now)

	// In server.go, the methods that only submit a job.
	file, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var submits []string
	for _, d := range file.Decls {
		f, ok := d.(*ast.FuncDecl)
		if !ok || f.Recv == nil || len(f.Body.List) != 1 {
			continue
		}
		ret, ok := f.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		if call, ok := ret.Results[0].(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "submit" {
				submits = append(submits, f.Name.Name)
			}
		}
	}
	if len(submits) == 0 {
		t.Fatal("server.go has no method that submits a job")
	}
	sorted(t, "server.go: the methods that make a job", submits)
}

func sorted(t *testing.T, what string, names []string) {
	t.Helper()
	want := slices.Clone(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("%s are not in alphabetical order. Use this order:\n%v", what, want)
	}
}
