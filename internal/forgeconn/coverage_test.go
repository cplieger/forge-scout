package forgeconn_test

import (
	"reflect"
	"testing"

	"github.com/cplieger/forge-scout/internal/forgeconn"
)

// TestMethods_test_every_forge_call fails when a Client method that calls the
// forge has no test in methods, which runs it against every product's fake.
func TestMethods_test_every_forge_call(t *testing.T) {
	t.Parallel()
	// The accessors answer what Open read and send no request.
	accessors := map[string]bool{"Product": true, "Login": true, "CanRerun": true, "Budget": true, "Close": true}
	client := reflect.TypeFor[*forgeconn.Client]()
	for m := range client.Methods() {
		name := m.Name
		if _, ok := methods[name]; !ok && !accessors[name] {
			t.Errorf("Client.%s has no test in methods", name)
		}
	}
	for name := range methods {
		if _, ok := client.MethodByName(name); !ok {
			t.Errorf("methods tests %s, which Client does not have", name)
		}
	}
}
