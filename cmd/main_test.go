package main

import "testing"

func TestPluginNamesTrimWhitespace(t *testing.T) {
	value := "e810,e825,e830,ntpfailover,phc-sync-workaround "
	var plugins []string
	for _, name := range splitPluginNames(value) {
		plugins = append(plugins, name)
	}

	if got := plugins[len(plugins)-1]; got != "phc-sync-workaround" {
		t.Fatalf("last plugin = %q, want %q", got, "phc-sync-workaround")
	}
}
