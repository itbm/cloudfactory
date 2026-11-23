package quadlet

import (
	"strings"
	"testing"
)

// The engines of this agent version are complete, and their port ranges do not overlap.
func TestTheEnginesAreComplete(t *testing.T) {
	if err := checkEngineSpecs(engineSpecs); err != nil {
		t.Fatal(err)
	}
	if got := EngineNames(); len(got) != 3 || got[0] != "mariadb" || got[1] != "postgresql" || got[2] != "valkey" {
		t.Errorf("EngineNames() = %v", got)
	}
	overlap := append(Engines(), EngineSpec{Name: "other", Label: "Other", Port: MariaDBPort + 10, Host: "other.internal", Lines: valkeyLines})
	if checkEngineSpecs(overlap) == nil {
		t.Error("two engines with ports in common were accepted")
	}
	same := append(Engines(), EngineSpec{Name: "other", Label: "Other", Port: 27017, Host: MariaDBHost, Lines: valkeyLines})
	if checkEngineSpecs(same) == nil {
		t.Error("two engines with the same host name were accepted")
	}
	// An engine that is not there gets the port of the first engine, as before the table.
	if DatabasePort("mongodb") != MariaDBPort {
		t.Errorf("DatabasePort(mongodb) = %d", DatabasePort("mongodb"))
	}
}

// The engines of the database packs join the built-in engines. A pack engine cannot replace a
// built-in engine, and its database servers have host ports in the block of the pack engines.
func TestThePackEngines(t *testing.T) {
	t.Cleanup(func() { _ = SetPackEngines(nil) })
	mysql := EngineSpec{Name: "mysql", Label: "MySQL", Port: 3306, Lines: PackEngineLines("/var/lib/mysql"), Pack: "mysql 1.0.0"}
	for name, specs := range map[string][]EngineSpec{
		"a built-in name": {{Name: "mariadb", Label: "MySQL", Port: 3306, Lines: mysql.Lines, Pack: "mariadb 1.0.0"}},
		"two packs":       {mysql, mysql},
		"a host name":     {{Name: "mysql", Label: "MySQL", Port: 3306, Host: "mysql.internal", Lines: mysql.Lines, Pack: "mysql 1.0.0"}},
		"no pack":         {{Name: "mysql", Label: "MySQL", Port: 3306, Lines: mysql.Lines}},
		"a low port":      {{Name: "mysql", Label: "MySQL", Port: 80, Lines: mysql.Lines, Pack: "mysql 1.0.0"}},
	} {
		if SetPackEngines(specs) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := SetPackEngines([]EngineSpec{mysql}); err != nil {
		t.Fatal(err)
	}
	if got := EngineNames(); len(got) != 4 || got[3] != "mysql" || !IsPackEngine("mysql") || IsPackEngine("mariadb") {
		t.Errorf("EngineNames() = %v", got)
	}
	if PackDatabasePortMin != 20000 || PackDatabasePortMax != 22499 {
		t.Errorf("the block of the pack engines is %d to %d", PackDatabasePortMin, PackDatabasePortMax)
	}
	u := DatabaseUnit(Database{Engine: "mysql", ID: "01jbq3z5k8n6t4w2x9y7r5p3d6", Customer: "c", Address: "169.254.80.2", Port: 20001,
		Image: "ghcr.io/example/cf-mysql:8.4@sha256:" + strings.Repeat("0", 64), DataDir: "/srv/data", EnvFile: "/srv/mysql.env"})
	for _, want := range []string{"PublishPort=169.254.80.2:20001:3306\n", "Volume=/srv/data:/var/lib/mysql:Z\n", "EnvironmentFile=/srv/mysql.env\n", "Label=io.cloudfactory.service=mysql\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit does not have %q:\n%s", want, u)
		}
	}
	if err := SetPackEngines(nil); err != nil || len(EngineNames()) != 3 {
		t.Errorf("no packs: %v %v", EngineNames(), err)
	}
}
