package profiling

import (
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"strings"
	"time"
)

func Start() {
	port := os.Getenv("BIFROST_PPROF_PORT")
	if port == "" {
		return // nothing imported touches DefaultServeMux; truly inert
	}

	runtime.SetBlockProfileRate(1000)
	runtime.SetMutexProfileFraction(1000)

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
		if err := pprof.StartCPUProfile(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		time.Sleep(time.Duration(sec) * time.Second)
		pprof.StopCPUProfile()
	})

	// execution trace
	mux.HandleFunc("/debug/pprof/trace", func(w http.ResponseWriter, r *http.Request) {
		sec, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
		if sec <= 0 {
			sec = 1
		}
		if err := trace.Start(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		time.Sleep(time.Duration(sec) * time.Second)
		trace.Stop()
	})

	go func() {
		if err := http.ListenAndServe("0.0.0.0:"+port, mux); err != nil {
			log.Printf("pprof server stopped: %v", err)
		}
	}()
}
