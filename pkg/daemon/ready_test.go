package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
)

func TestPortAliasesHandler(t *testing.T) {
	alias.ClearAliases()
	t.Cleanup(alias.ClearAliases)
	alias.SetAlias("ens1f0", "ptp0")

	w := httptest.NewRecorder()
	portAliasesHandler{}.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/port-aliases", nil))

	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != "{\"ens1f0\":\"ptp0\"}\n" {
		t.Fatalf("unexpected response: status=%d content-type=%q body=%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}
