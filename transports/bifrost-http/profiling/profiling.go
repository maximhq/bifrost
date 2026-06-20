package profiling

import (
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"strings"
	"time"
)

// Start launches the pprof debug server when BIFROST_PPROF_PORT is set and
// returns the *http.Server so the caller can shut it down gracefully alongside
// the main server. It returns nil when profiling is disabled.
func Start() *http.Server {
	port := os.Getenv("BIFROST_PPROF_PORT")
	if port == "" {
		return nil // nothing imported touches DefaultServeMux; truly inert
	}

	// Profiling rates are tunable via env so operators can trade overhead for
	// resolution. Defaults are intentionally lighter than full sampling since
	// this server may run alongside production traffic.
	//   BIFROST_PPROF_BLOCK_RATE    nanoseconds blocked per sample (lower = more overhead); default 10µs
	//   BIFROST_PPROF_MUTEX_FRACTION 1/N contention events sampled (lower = more overhead); default 1%
	blockRate := 10000
	if v, err := strconv.Atoi(os.Getenv("BIFROST_PPROF_BLOCK_RATE")); err == nil && v > 0 {
		blockRate = v
	}
	mutexFraction := 100
	if v, err := strconv.Atoi(os.Getenv("BIFROST_PPROF_MUTEX_FRACTION")); err == nil && v > 0 {
		mutexFraction = v
	}
	runtime.SetBlockProfileRate(blockRate)
	runtime.SetMutexProfileFraction(mutexFraction)

	mux := http.NewServeMux()

	// heap, goroutine, allocs, block, mutex, threadcreate
	mux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/debug/pprof/")
		if name == "" {
			_, _ = w.Write([]byte("profiles: heap goroutine allocs block mutex threadcreate; also /profile /trace\n"))
			return
		}
		p := pprof.Lookup(name)
		if p == nil {
			http.Error(w, "unknown profile", http.StatusNotFound)
			return
		}
		debug, _ := strconv.Atoi(r.URL.Query().Get("debug"))
		err := p.WriteTo(w, debug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	// CPU profile (exact path beats the subtree pattern above)
	mux.HandleFunc("/debug/pprof/profile", func(w http.ResponseWriter, r *http.Request) {
		sec, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
		if sec <= 0 {
			sec = 30
		}
		if sec > 60 {
			sec = 60 // cap to bound how long the connection/goroutine is held
		}
		if err := pprof.StartCPUProfile(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Stop as soon as the duration elapses OR the client disconnects /
		// the server shuts down. CPU profiling is process-wide, so leaving it
		// running on a cancelled request would block subsequent /profile calls
		// with "cpu profiling already in use".
		defer pprof.StopCPUProfile()
		select {
		case <-time.After(time.Duration(sec) * time.Second):
		case <-r.Context().Done():
		}
	})

	// execution trace
	mux.HandleFunc("/debug/pprof/trace", func(w http.ResponseWriter, r *http.Request) {
		sec, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
		if sec <= 0 {
			sec = 1
		}
		if sec > 30 {
			sec = 30 // cap to bound how long the connection/goroutine is held
		}
		if err := trace.Start(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Runtime tracing is process-wide; stop on cancellation too so a
		// disconnected client or shutdown doesn't block other trace captures.
		defer trace.Stop()
		select {
		case <-time.After(time.Duration(sec) * time.Second):
		case <-r.Context().Done():
		}
	})

	handler := basicAuth(mux)

	// Bind to localhost by default so profiling data (heap dumps, goroutine
	// stacks, traces) is not exposed on the network unless an operator
	// deliberately opts in via BIFROST_PPROF_HOST (e.g. "0.0.0.0").
	host := os.Getenv("BIFROST_PPROF_HOST")
	if host == "" {
		host = "127.0.0.1"
	}

	// Fail closed: exposing profiling on a non-loopback interface without
	// credentials would publish sensitive runtime data (request bodies,
	// headers, keys, internal state) to anyone who can reach the listener.
	// Loopback-without-auth stays allowed since that's the standard pprof
	// workflow (reach it via SSH tunnel or a sidecar).
	if !isLoopbackHost(host) &&
		(os.Getenv("BIFROST_PPROF_USERNAME") == "" || os.Getenv("BIFROST_PPROF_PASSWORD") == "") {
		log.Printf("pprof: refusing to expose profiling on non-loopback host %q without BIFROST_PPROF_USERNAME and BIFROST_PPROF_PASSWORD", host)
		return nil
	}

	srv := &http.Server{
		Addr:         net.JoinHostPort(host, port),
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 90 * time.Second, // must exceed the 60s CPU-profile cap above
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("pprof server stopped: %v", err)
		}
	}()

	return srv
}

// isLoopbackHost reports whether host refers to the local loopback interface.
// It accepts the literal "localhost" as well as any loopback IP (127.0.0.0/8,
// ::1) so the fail-closed auth check treats all of them as safe-by-default.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// basicAuth wraps h with HTTP Basic Auth when both BIFROST_PPROF_USERNAME and
// BIFROST_PPROF_PASSWORD are set. If either is unset/empty, h is returned
// unwrapped; Start only allows that on a loopback bind (see the fail-closed
// check there), so an open endpoint is reachable from localhost only.
func basicAuth(h http.Handler) http.Handler {
	wantUser := os.Getenv("BIFROST_PPROF_USERNAME")
	wantPass := os.Getenv("BIFROST_PPROF_PASSWORD")
	if wantUser == "" || wantPass == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(wantPass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="pprof"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
