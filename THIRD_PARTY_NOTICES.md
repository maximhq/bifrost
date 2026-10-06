# Third-Party Notices

Bifrost is licensed under the [Apache License, Version 2.0](LICENSE). This file lists third-party components with license terms beyond the common MIT/BSD/Apache-2.0 permissive set — specifically, code carrying Mozilla Public License 2.0 (MPL-2.0) terms — along with third-party source code embedded directly in this repository. All components below are used unmodified and combined as a "Larger Work" per MPL-2.0 Section 3.3; no Bifrost source files are themselves MPL-licensed.

This file does not enumerate the full dependency tree (see `go.sum` / `package-lock.json` in each module for that) — only the entries that need attribution beyond what those permissive licenses require.

## Embedded source code

### `framework/migrator/migrator.go`

Portions of this file are derived from [go-gormigrate/gormigrate](https://github.com/go-gormigrate/gormigrate).

```
MIT License
Copyright (c) 2016 Andrey Nering
```

Full license text is preserved in the file's header comment.

### `core/providers/antigravity/`, `core/providers/kiro/`

The Antigravity and Kiro providers port wire formats, OAuth flows and request/response conversion logic from [lidge-jun/opencodex](https://github.com/lidge-jun/opencodex) (TypeScript) to Go.

```
MIT License
Copyright (c) 2026 opencodex contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## MPL-2.0 components — Go (compiled into the binary)

### `github.com/cyphar/filepath-securejoin`

Dual-licensed: most files are BSD-3-Clause; a subset of files (see the package's own `COPYING.md`) are MPL-2.0. Used unmodified as an upstream dependency. Source: https://github.com/cyphar/filepath-securejoin

### `github.com/hashicorp/go-version`

Licensed under MPL-2.0. Copyright IBM Corp. Used unmodified as an upstream dependency. Source: https://github.com/hashicorp/go-version

## MPL-2.0 / dual-licensed components — npm (build tooling only, not shipped)

### `lightningcss`

Licensed under MPL-2.0. Pulled in transitively via `vite`/`tailwindcss` as a build-time devDependency — it runs during the UI build and is never bundled into the built output shipped to end users. Source: https://github.com/parcel-bundler/lightningcss

### `dompurify`

Dual-licensed `MPL-2.0 OR Apache-2.0`. Bifrost elects the **Apache-2.0** option; no MPL-2.0 obligations apply to Bifrost's use of this package. Source: https://github.com/cure53/DOMPurify

---

*This file covers MPL-2.0 attribution only. It is not a complete open-source license inventory and is not a substitute for legal review.*
