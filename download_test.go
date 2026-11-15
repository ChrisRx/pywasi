package pywasi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadStall(t *testing.T) {
	old := stallTimeout
	stallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stallTimeout = old })

	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		if r.URL.Path == "/slow" {
			// Data keeps arriving, just slowly: not a stall.
			for range 5 {
				time.Sleep(100 * time.Millisecond)
				_, _ = w.Write([]byte("."))
				w.(http.Flusher).Flush()
			}
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	body, err := httpGet(context.Background(), srv.URL+"/stall", "")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = io.ReadAll(body)
	_ = body.Close()
	if err == nil || !strings.Contains(err.Error(), "no data for") {
		t.Fatalf("stalled download: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("stall noticed after %s", d)
	}

	body, err = httpGet(context.Background(), srv.URL+"/slow", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(data) != "partial....." {
		t.Fatalf("slow download: %q, %v", data, err)
	}
}
