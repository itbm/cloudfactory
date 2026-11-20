package actions

import (
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// Each engine of the units has a driver, and each driver has an engine of the units. An
// engine has SQL or numbered databases.
func TestEachEngineHasADriver(t *testing.T) {
	if len(engineDrivers) != len(quadlet.Engines()) {
		t.Fatalf("%d drivers for %d engines", len(engineDrivers), len(quadlet.Engines()))
	}
	for _, e := range quadlet.Engines() {
		drv, err := driverOf(e.Name)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		_, isSQL := drv.(sqlDriver)
		if isSQL == isNumbered(e.Name) {
			t.Errorf("%s: SQL %v, numbered %v", e.Name, isSQL, isNumbered(e.Name))
		}
	}
	if _, err := driverOf("mongodb"); err == nil || err.Error() != `the database engine "mongodb" is not valid` {
		t.Errorf("driverOf(mongodb) = %v", err)
	}
	if _, err := sqlOf("valkey"); err == nil || err.Error() != "Valkey has no SQL" {
		t.Errorf("sqlOf(valkey) = %v", err)
	}
}
