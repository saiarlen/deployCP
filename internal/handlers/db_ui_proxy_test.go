package handlers

import (
	"net/http"
	"testing"
)

func TestDatabaseUIProxyRequestsUncompressedAdminerAssets(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8081/?file=default.css", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip, br")
	configureDatabaseUIProxyEncoding(req)
	if got := req.Header.Get("Accept-Encoding"); got != "identity" {
		t.Fatalf("Accept-Encoding = %q, want identity", got)
	}
}
