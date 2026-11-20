package actions

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/gate"
)

// The trial of /cf/manage: the agent asks the MariaDB image for the sizes only with the
// switch on, and uses its own query when the image gives no valid answer.
func TestTheManageTrial(t *testing.T) {
	d, f := paritySite(t)
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", databaseServer("mariadb")))); err != nil {
		t.Fatal(err)
	}
	prefix := execOf("mariadb", mariaServerKey)
	manage := prefix + "/cf/manage sizes"
	query := []execx.Rule{{Prefix: prefix + "mariadb ", Output: "acme_x7k2_shop\t4096\n"}}
	sizes := func(rules ...execx.Rule) (string, []string) {
		f.Calls, f.Inputs, f.Envs = nil, nil, nil
		f.Rules = append(rules, query...)
		var got []string
		for _, s := range d.databaseSizes(t.Context()) {
			got = append(got, s.Engine+" "+s.Name+" "+strconv.FormatInt(s.SizeBytes, 10))
		}
		return strings.Join(got, ","), f.Calls
	}

	got, calls := sizes(execx.Rule{Prefix: manage, Output: `{"sizes":[{"name":"acme_x7k2_shop","bytes":8192}]}`})
	if got != "mariadb acme_x7k2_shop 4096" || slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, manage) }) {
		t.Errorf("trial off: %s %v", got, calls)
	}

	d.ManageTrial = true
	got, calls = sizes(execx.Rule{Prefix: manage, Output: `{"sizes":[{"name":"acme_x7k2_shop","bytes":8192}]}`})
	if got != "mariadb acme_x7k2_shop 8192" || slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix+"mariadb ") }) {
		t.Errorf("trial on: %s %v", got, calls)
	}
	for name, rule := range map[string]execx.Rule{
		"no program":     {Prefix: manage, Err: errors.New("exec: /cf/manage: not found")},
		"not JSON":       {Prefix: manage, Output: "sizes: 8192"},
		"unknown field":  {Prefix: manage, Output: `{"sizes":[],"x":1}`},
		"negative bytes": {Prefix: manage, Output: `{"sizes":[{"name":"acme_x7k2_shop","bytes":-1}]}`},
		"no bytes":       {Prefix: manage, Output: `{"sizes":[{"name":"acme_x7k2_shop"}]}`},
	} {
		if got, _ := sizes(rule); got != "mariadb acme_x7k2_shop 4096" {
			t.Errorf("%s: %s, want the result of the query of the agent", name, got)
		}
	}
	// The policy of the root helper accepts only this program, in a database container.
	if err := rootOf(d).Allow(gate.Command{Argv: strings.Fields(prefix + "/cf/manage sizes")}); err != nil {
		t.Errorf("the policy refuses /cf/manage: %v", err)
	}
	if err := rootOf(d).Allow(gate.Command{Argv: strings.Fields(prefix + "/cf/other sizes")}); err == nil {
		t.Error("the policy accepts a different program")
	}
}
