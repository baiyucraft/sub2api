# Third-Party Notices

## Scope

This plugin records three pinned source references below. This is source-baseline and license evidence only, not an audit of upstream releases, binaries, installers, or this plugin's release artifacts. Repository license titles alone are not treated as proof of an "only" or "or-later" grant. This document is not a complete release SBOM.

The plugin follows the repository's GNU LGPL v3 license context. LICENSE contains the repository LGPL v3 text and the GNU GPL v3 text incorporated by it. The latter is reproduced from https://www.gnu.org/licenses/gpl-3.0.txt; including that license text does not import ccodex source code. Third-party components retain their own licenses and copyright notices.

## Pinned Source References

### happy-loki/sub2api-codex-turn-state

- Repository: https://github.com/happy-loki/sub2api-codex-turn-state
- Baseline: `c8e5483e06ab9de199c3848d9d46fccfc063f88a`
- License evidence: `LICENSE`, GNU Lesser General Public License, version 3.
- License blob: `153d416dc8d2d60076698ec3cbfce34d91436a03`.
- Role: implementation and behavior reference. This entry is not a claim that its entire tree is included. Any future code adoption requires a file-level provenance record and preservation of applicable notices.

### wangyunjeff/sub2api-state-kit

- Repository: https://github.com/wangyunjeff/sub2api-state-kit
- Baseline: `9f2d20ba7db0f76a558ce2c28eefb20128eae562`
- License evidence: `LICENSE`, GNU Lesser General Public License, version 3.
- License blob: `153d416dc8d2d60076698ec3cbfce34d91436a03`.
- Role: implementation and behavior reference, subject to the same file-level provenance requirements before code adoption.

### gylive/ccodex-sleep-state

- Repository: https://github.com/gylive/ccodex-sleep-state
- Baseline: `b18fabf9ad8e9d7af7d9d0306b623ba6091a39d6`
- License evidence: `LICENSE`, GNU General Public License, version 3.
- License blob: `f288702d2fa16d3cdf0035b15a9fcbc552cd88e7`.
- Role: design reference only. The plugin is independently implemented; ccodex source code, patches, packaging code and runtime dependencies must not be imported or distributed as part of this plugin.

Full immutable license URLs and update policy are in `sources.lock.json`. Before every update, inspect all three repositories' then-current HEAD and releases, resolve release tags to full commits, and review their diff from these baselines. No automatic overwrite is authorized.

## Bundled UI Runtime Dependencies

The UI lockfile pins Vue 3.5.26 and lucide-vue-next 0.468.0. Notices below are reproduced from their installed package LICENSE files. Vue runtime packages use the Vue MIT license. Lucide's notice identifies its Feather-derived portions. Check the actual built dependency set again before distribution; build-tool licenses are not a substitute for runtime notices.

### Vue 3.5.26

Source: https://github.com/vuejs/core
Package: `vue`, license: MIT.

```text
The MIT License (MIT)

Copyright (c) 2018-present, Yuxi (Evan) You

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

### lucide-vue-next 0.468.0

Source: https://github.com/lucide-icons/lucide
Package: `lucide-vue-next`, license: ISC; original Feather portions identified below.

```text
ISC License

Copyright (c) for portions of Lucide are held by Cole Bemis 2013-2022 as part of Feather (MIT). All other copyright (c) for Lucide are held by Lucide Contributors 2022.

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```

## Runtime and Release Checklist

Go dependencies are pinned separately by `go.mod` / `go.sum`. The linked Go module graph and its applicable licenses, attribution requirements, and source delivery must be inventoried for the exact release binary. This initial source-reference record does not claim that the Go transitive dependency notice set, SBOM, or any release package audit is complete.

Before distribution, preserve applicable upstream copyright headers; check the exact linked and bundled components; provide the corresponding source, build information, and notices required by their licenses. Do not use a valid package hash or signature as evidence that these checks have already been performed. The package tool includes this file, LICENSE, sources.lock.json and MAINTENANCE.md, but does not itself perform a license or security audit.
