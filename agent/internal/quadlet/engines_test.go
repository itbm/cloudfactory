package quadlet

import "testing"

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
