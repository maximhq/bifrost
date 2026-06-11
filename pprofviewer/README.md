# pprofviewer

Independent web service for quickly inspecting Go pprof files.

## Run

```bash
cd pprofviewer
GOWORK=off go run ./cmd/pprofviewer -addr :7777
```

Open `http://localhost:7777`, upload a pprof dump folder or select multiple profile files, then click Analyze. The viewer auto-detects common filenames:

- `heap.prof`
- `allocs.prof`
- `cpu.prof`
- `goroutine.prof`
- `block.prof`
- `mutex.prof`

From the repository root:

```bash
make pprofviewer
make pprofviewer PPROF_FILES="/tmp/heap.pprof /tmp/cpu.pprof"
make pprofviewer PPROF_HEAP=/tmp/heap.pprof PPROF_CPU=/tmp/cpu.pprof
make pprofviewer PPROF_ALLOCS=/tmp/allocs.prof PPROF_BLOCK=/tmp/block.prof PPROF_MUTEX=/tmp/mutex.prof
```

The Makefile recipe prints direct viewer URLs for each supplied profile path. Direct URLs also render automatically:

```text
http://localhost:7777/?path=/tmp/heap.pprof&sample=inuse_space
```

## API

Analyze a local file from the server process:

```bash
curl "http://localhost:7777/api/analyze?path=/tmp/heap.pprof&sample=inuse_space"
```

Analyze an uploaded profile:

```bash
curl -F "profile=@/tmp/cpu.pprof" "http://localhost:7777/api/analyze"
```

Analyze a multi-profile upload:

```bash
curl \
  -F "profiles=@/tmp/heap.prof" \
  -F "profiles=@/tmp/allocs.prof" \
  -F "profiles=@/tmp/cpu.prof" \
  -F "profiles=@/tmp/goroutine.prof" \
  -F "profiles=@/tmp/block.prof" \
  -F "profiles=@/tmp/mutex.prof" \
  "http://localhost:7777/api/analyze-set"
```

The response includes:

- `nodes`: graph nodes sized by cumulative allocation or CPU sample value
- `edges`: weighted call-path edges
- `leaks`: retained heap candidates with stack paths
- `top_paths`: heaviest stack paths in the profile
