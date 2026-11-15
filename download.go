package pywasi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Downloads (archives, kits, bundles, the catalog, PyPI and uv) have no
// overall time limit, since a large file on a slow link can take a while,
// but they fail when the server stops answering: on connecting, on the
// response headers, and when the body stops arriving for stallTimeout.
var (
	stallTimeout = 60 * time.Second
	httpClient   = func() *http.Client {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = stallTimeout
		return &http.Client{Transport: t}
	}()
)

func httpGet(ctx context.Context, url, accept string) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b := &stallReader{body: resp.Body, url: url, cancel: cancel}
	b.timer = time.AfterFunc(stallTimeout, func() {
		b.stalled.Store(true)
		cancel()
	})
	return b, nil
}

// stallReader cancels a download when no data arrives for stallTimeout.
type stallReader struct {
	body    io.ReadCloser
	url     string
	cancel  context.CancelFunc
	timer   *time.Timer
	stalled atomic.Bool
}

func (r *stallReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if r.stalled.Load() {
		return n, fmt.Errorf("GET %s: no data for %s", r.url, stallTimeout)
	}
	if n > 0 {
		r.timer.Reset(stallTimeout)
	}
	return n, err
}

func (r *stallReader) Close() error {
	r.timer.Stop()
	r.cancel()
	return r.body.Close()
}
