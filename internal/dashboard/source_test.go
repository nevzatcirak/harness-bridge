package dashboard

import (
	"reflect"
	"testing"
)

// The Source interface is the only way the handler reaches data. It must stay
// read-only: every method is a reader, and none can mutate (ADR-023 T11).
func TestSourceInterfaceIsReadOnly(t *testing.T) {
	typ := reflect.TypeOf((*Source)(nil)).Elem()
	allowed := map[string]bool{"Sessions": true, "SessionDetail": true}
	if typ.NumMethod() == 0 {
		t.Fatal("Source has no methods")
	}
	for i := 0; i < typ.NumMethod(); i++ {
		if name := typ.Method(i).Name; !allowed[name] {
			t.Errorf("Source method %q is not on the read-only allowlist", name)
		}
	}
}
