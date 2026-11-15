package pywasi_test

import (
	"bufio"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.chrisrx.dev/pywasi"
)

func newNetPython(t *testing.T, options ...pywasi.Option) *pywasi.Python {
	t.Helper()
	py, err := pywasi.New(context.Background(), append([]pywasi.Option{pywasi.WithArchive(pywasi.DefaultArchive)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { py.Close() })
	return py
}

// startPython runs Python in the background and returns its first line of
// output, and a function that waits for it to exit and returns the rest.
func startPython(t *testing.T, py *pywasi.Python, opts pywasi.RunOptions) (string, func() string) {
	t.Helper()
	r, w := io.Pipe()
	var stderr strings.Builder
	opts.Stdout, opts.Stderr = w, &stderr
	done := make(chan error, 1)
	go func() {
		code, err := py.Run(context.Background(), opts)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit %d", code)
		}
		w.CloseWithError(io.EOF)
		done <- err
	}()
	out := bufio.NewReader(r)
	first, err := out.ReadString('\n')
	if err != nil {
		t.Fatalf("reading output: %v\nstderr:\n%s", err, stderr.String())
	}
	return strings.TrimSpace(first), func() string {
		rest, _ := io.ReadAll(out)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("python: %v\nstderr:\n%s", err, stderr.String())
			}
		case <-time.After(30 * time.Second):
			t.Fatal("python did not exit")
		}
		return string(rest)
	}
}

func TestSocketsDisabled(t *testing.T) {
	py := newNetPython(t)
	code, out, errOut := run(t, py, pywasi.RunOptions{Args: []string{"-c", `
import socket
print(socket.getaddrinfo("localhost", 80, socket.AF_INET, socket.SOCK_STREAM)[0][4])
for f in socket.socket, lambda: socket.getaddrinfo("example.com", 80):
    try:
        f()
    except PermissionError:
        print("denied")
`}})
	if code != 0 || out != "('127.0.0.1', 80)\ndenied\ndenied\n" {
		t.Fatalf("exit %d, output %q\nstderr:\n%s", code, out, errOut)
	}

	// Eval says how to enable it, but not for other PermissionErrors.
	ctx := context.Background()
	var perr *pywasi.Error
	_, err := py.Eval[any](ctx, "import socket; socket.socket()")
	if !errors.As(err, &perr) || !strings.Contains(perr.Message, "WithNetwork") {
		t.Errorf("socket: %v", err)
	}
	_, err = py.Eval[any](ctx, "raise PermissionError(13, 'other')")
	if !errors.As(err, &perr) || strings.Contains(perr.Message, "WithNetwork") {
		t.Errorf("other: %v", err)
	}
}

func TestSocketServer(t *testing.T) {
	py := newNetPython(t, pywasi.WithNetwork(nil))
	port, wait := startPython(t, py, pywasi.RunOptions{Args: []string{"-c", `
import socket
s = socket.create_server(("127.0.0.1", 0))
print(s.getsockname()[1], flush=True)
c, addr = s.accept()
data = c.recv(100)
c.sendall(b"echo " + data + b" from " + addr[0].encode())
c.close()
s.close()
`}})
	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	wait()
	if string(got) != "echo hi from 127.0.0.1" {
		t.Fatalf("got %q", got)
	}
}

func TestHTTPServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("served"), 0o644); err != nil {
		t.Fatal(err)
	}
	py := newNetPython(t, pywasi.WithNetwork(nil))
	port, wait := startPython(t, py, pywasi.RunOptions{
		Args: []string{"-c", `
import functools, http.server
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory="/srv")
server = http.server.HTTPServer(("127.0.0.1", 0), handler)
print(server.server_address[1], flush=True)
server.timeout = 30
server.handle_request()
`},
		Mounts: []pywasi.Mount{{Host: dir, Guest: "/srv", ReadOnly: true}},
	})
	resp, err := http.Get("http://127.0.0.1:" + port + "/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	log := wait()
	if resp.StatusCode != 200 || string(body) != "served" {
		t.Fatalf("status %d, body %q, log %q", resp.StatusCode, body, log)
	}
}

func TestSocketClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "hello %s", r.URL.Path)
	}))
	defer srv.Close()
	var mu sync.Mutex
	var ops []string
	py := newNetPython(t, pywasi.WithNetwork(func(op, address string) error {
		mu.Lock()
		defer mu.Unlock()
		ops = append(ops, op+" "+address)
		if strings.HasSuffix(address, ":1") {
			return errors.New("denied")
		}
		return nil
	}))
	url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	code, out, errOut := run(t, py, pywasi.RunOptions{Args: []string{"-c", `
import socket, sys, urllib.request
print(urllib.request.urlopen(sys.argv[1] + "/blocking").read().decode())
print(urllib.request.urlopen(sys.argv[1] + "/timeout", timeout=10).read().decode())
try:
    socket.create_connection(("127.0.0.1", 1))
except PermissionError:
    print("denied")
`, url}})
	if code != 0 || out != "hello /blocking\nhello /timeout\ndenied\n" {
		t.Fatalf("exit %d, output %q\nstderr:\n%s", code, out, errOut)
	}
	addr := strings.TrimPrefix(srv.URL, "http://")
	want := []string{"connect " + addr, "connect " + addr, "connect 127.0.0.1:1"}
	if fmt.Sprint(ops) != fmt.Sprint(want) {
		t.Errorf("policy saw %q, want %q", ops, want)
	}
}

// Canceling the context stops Python while it waits on a socket.
func TestSocketCancel(t *testing.T) {
	py := newNetPython(t, pywasi.WithNetwork(nil))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err := py.Run(ctx, pywasi.RunOptions{Args: []string{"-c", `
import socket
socket.create_server(("127.0.0.1", 0)).accept()
`}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("stopped after %v", d)
	}
}

// TLS with OpenSSL, verifying the server against a CA file, and the
// shipped CA certificates being the default.
func TestHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "secure %s", r.URL.Path)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), ca, 0o644); err != nil {
		t.Fatal(err)
	}
	py := newNetPython(t, pywasi.WithNetwork(nil))
	code, out, errOut := run(t, py, pywasi.RunOptions{
		Args: []string{"-c", `
import ssl, sys, urllib.request
print(ssl.get_default_verify_paths().cafile, ssl.create_default_context().cert_store_stats()["x509_ca"] > 100)
ctx = ssl.create_default_context(cafile="/certs/ca.pem")
print(urllib.request.urlopen(sys.argv[1] + "/ok", context=ctx).read().decode())
try:
    urllib.request.urlopen(sys.argv[1])
except urllib.error.URLError as e:
    print("unverified", isinstance(e.reason, ssl.SSLCertVerificationError))
`, srv.URL},
		Mounts: []pywasi.Mount{{Host: dir, Guest: "/certs", ReadOnly: true}},
	})
	if code != 0 || out != "/etc/ssl/cert.pem True\nsecure /ok\nunverified True\n" {
		t.Fatalf("exit %d, output %q\nstderr:\n%s", code, out, errOut)
	}
}

func TestAsyncio(t *testing.T) {
	ctx := context.Background()

	// Without network access: the event loop's wakeup socketpair, executors
	// (run inline) and asyncio.run's executor shutdown.
	got, err := pywasi.Eval[[]any](ctx, `
import asyncio, socket, time

async def main():
    await asyncio.sleep(0.01)
    a = await asyncio.to_thread(sum, [1, 2, 3])
    b = await asyncio.get_running_loop().run_in_executor(None, time.monotonic)
    return [a, b > 0]

a, b = socket.socketpair()
a.sendall(b"ping")
[asyncio.run(main()), b.recv(4).decode()]
`, pywasi.WithArchive(pywasi.DefaultArchive))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[[6 true] ping]" {
		t.Fatalf("got %v", got)
	}

	// With it: a connection by host name, resolved in the executor.
	srv, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	go func() {
		c, err := srv.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("hello"))
		_ = c.Close()
	}()
	port := srv.Addr().(*net.TCPAddr).Port
	reply, err := pywasi.Eval[string](ctx, fmt.Sprintf(`
import asyncio, socket

async def main():
    r, w = await asyncio.open_connection("localhost", %d, family=socket.AF_INET)
    data = await r.read()
    w.close()
    return data.decode()

asyncio.run(main())
`, port), pywasi.WithArchive(pywasi.DefaultArchive), pywasi.WithNetwork(nil))
	if err != nil || reply != "hello" {
		t.Fatalf("got %q, %v", reply, err)
	}
}

func TestIPv6(t *testing.T) {
	if ln, err := net.Listen("tcp6", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback:", err)
	} else {
		_ = ln.Close()
	}
	got, err := pywasi.Eval[[]any](context.Background(), `
import socket
s = socket.create_server(("::1", 0), family=socket.AF_INET6)
c = socket.create_connection(("::1", s.getsockname()[1]))
a, peer = s.accept()
c.sendall(b"v6")
[a.recv(2).decode(), peer[0], socket.getaddrinfo("localhost", 80, socket.AF_INET6)[0][4][0]]
`, pywasi.WithArchive(pywasi.DefaultArchive), pywasi.WithNetwork(nil))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[v6 ::1 ::1]" {
		t.Fatalf("got %v", got)
	}
}

func TestUDP(t *testing.T) {
	// A Go echo server.
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = echo.Close() }()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := echo.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteToUDP(append([]byte("echo "), buf[:n]...), from)
		}
	}()
	port := echo.LocalAddr().(*net.UDPAddr).Port

	var ops []string
	var mu sync.Mutex
	policy := func(op, address string) error {
		mu.Lock()
		defer mu.Unlock()
		ops = append(ops, op+" "+address)
		if address == "127.0.0.2:9" {
			return errors.New("no")
		}
		return nil
	}
	got, err := pywasi.Eval[[]any](context.Background(), fmt.Sprintf(`
import asyncio, socket
port = %d
out = []

# sendto and recvfrom, bound on the first send.
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(5)
s.sendto(b"one", ("127.0.0.1", port))
data, addr = s.recvfrom(100)
out += [data.decode(), addr == ("127.0.0.1", port), s.getsockopt(socket.SOL_SOCKET, socket.SO_TYPE) == socket.SOCK_DGRAM]

# Timeouts.
s.settimeout(0.1)
try:
    s.recvfrom(100)
except TimeoutError:
    out.append("timeout")

# Denied by the policy.
try:
    s.sendto(b"x", ("127.0.0.2", 9))
except PermissionError:
    out.append("denied")
s.close()

# connect, send and recv.
c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
c.connect(("127.0.0.1", port))
c.send(b"two")
out += [c.recv(100).decode(), c.getpeername() == ("127.0.0.1", port)]
c.close()

# Bound, then talking to itself, non-blocking.
b = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
b.bind(("127.0.0.1", 0))
b.setblocking(False)
try:
    b.recvfrom(100)
except BlockingIOError:
    out.append("would block")
b.sendto(b"self", b.getsockname())
out.append(b.recvfrom(100)[0].decode())
b.close()

# asyncio.
class Client(asyncio.DatagramProtocol):
    def __init__(self, done):
        self.done = done
    def connection_made(self, transport):
        transport.sendto(b"three")
    def datagram_received(self, data, addr):
        self.done.set_result(data.decode())

async def main():
    done = asyncio.get_running_loop().create_future()
    transport, _ = await asyncio.get_running_loop().create_datagram_endpoint(
        lambda: Client(done), remote_addr=("127.0.0.1", port))
    try:
        return await asyncio.wait_for(done, 5)
    finally:
        transport.close()

out.append(asyncio.run(main()))
out
`, port), pywasi.WithArchive(pywasi.DefaultArchive), pywasi.WithNetwork(policy))
	if err != nil {
		t.Fatal(err)
	}
	want := "[echo one true true timeout denied echo two true would block self echo three]"
	if fmt.Sprint(got) != want {
		t.Fatalf("got %v\nwant %s", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(ops, "listen 127.0.0.1:0") || !slices.Contains(ops, fmt.Sprintf("connect 127.0.0.1:%d", port)) {
		t.Errorf("policy saw %q", ops)
	}
}
