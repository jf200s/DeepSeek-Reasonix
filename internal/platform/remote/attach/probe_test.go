package attach

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The probe's cookie is an identity check, not decoration: a kernel that
// answers with another token's 401 is not this pane's kernel and has to be
// reported as gone so the relaunch path runs.
func TestProbeEndpointRequiresTheKernelsOwnToken(t *testing.T) {
	const token = "kernel-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		cookie, err := r.Cookie(remoteTokenCookie)
		if err != nil || cookie.Value != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()
	if !probeEndpoint(context.Background(), addr, token) {
		t.Fatal("the kernel answering its own token was reported dead")
	}
	if probeEndpoint(context.Background(), addr, "another-kernel-token") {
		t.Fatal("a 401 from another kernel's token was read as this kernel")
	}
}

// A recycled port whose process answers below 500 is still not this kernel:
// only the status route's own 200 proves the forward reached a serve.
func TestProbeEndpointRejectsAnswersThatAreNotTheStatusRoute(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		addr := srv.Listener.Addr().String()
		if probeEndpoint(context.Background(), addr, "kernel-token") {
			t.Errorf("status %d was read as a live kernel", status)
		}
		srv.Close()
	}
}
