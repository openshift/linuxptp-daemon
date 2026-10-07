package mapping

import "testing"

func TestPhcFirstStepPluginRegistered(t *testing.T) {
	constructor, ok := PluginMapping["phc-first-step"]
	if !ok {
		t.Fatal("phc-first-step is not registered")
	}
	instance, data := constructor("phc-first-step")
	if instance == nil || data == nil || instance.Name != "phc-first-step" {
		t.Fatalf("unexpected phc-first-step registration: plugin=%+v data=%v", instance, data)
	}
}
