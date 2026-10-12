# Third-party notices

## ffuf

h3fuzz is a fork of
[`github.com/ffuf/ffuf`](https://github.com/ffuf/ffuf), originally created by
Joona Hoikkala and contributors. ffuf is distributed under the MIT License.
The repository's root [`LICENSE`](LICENSE) retains ffuf's copyright notice and
the complete MIT License text. Unless a file or directory states otherwise,
the h3fuzz modifications are distributed under the same license.

## Bundled default wordlist

[`final.txt`](final.txt) is a deterministic derivative of Assetnote
Wordlists' `raft-large-words.txt`, which the upstream catalog describes as a
Raft wordlist sourced from
[SecLists](https://github.com/danielmiessler/SecLists/tree/master/Discovery/Web-Content).
The source artifact is byte-for-byte identical to SecLists'
`Discovery/Web-Content/raft-large-words.txt` at the time recorded below.
Assetnote Wordlists distributes its content under the Apache License 2.0;
SecLists is distributed under the MIT License and carries the copyright notice
reproduced below.

Source:
`https://wordlists-cdn.assetnote.io/data/manual/raft-large-words.txt`

The source artifact used for this release contains 119,600 lines and has
SHA-256
`0d51eae9b3a8dd56cbba37a42c8baa3b1c4b811319c8c5e0628c35db585fdae6`.
The transformation removes source entries beginning with `.` and emits each
remaining entry twice: once unchanged and once with `.%EXT%` appended. The
result contains 233,582 lines and has SHA-256
`1335d7ff3a5624bc2941b3fa0a42bb5eada5b4ea42b10d3b061aeeb1a4289af7`.

The Apache License 2.0 text is reproduced in
[`THIRD_PARTY_LICENSES.txt`](THIRD_PARTY_LICENSES.txt). The repository's root
MIT License does not replace the license terms that apply to `final.txt`.

### SecLists license

MIT License

Copyright (c) 2018 Daniel Miessler

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

## PortSwigger research and reference implementations

h3fuzz implements techniques described by Tom Stacey and PortSwigger in
[HTTP/3 in Burp Suite: it's time to find a bigger wordlist](https://portswigger.net/research/http3-in-burp-suite).
Its documentation also compares behavior with
[Turbo Intruder](https://github.com/PortSwigger/turbo-intruder) and
[HTTP3-Adapter](https://github.com/t0xodile-swig/HTTP3-Adapter).

Turbo Intruder and HTTP3-Adapter are not vendored, linked, or runtime
dependencies of h3fuzz. Their source licenses remain separate and are not
replaced by h3fuzz's MIT License. Both repositories state the GNU Affero
General Public License version 3 at the revisions used for the documented
source comparison. Those revisions are identified in
[`docs/performance/http3-100k-analysis.md`](docs/performance/http3-100k-analysis.md).

## quic-go

This repository contains a modified source copy of
[`github.com/quic-go/quic-go`](https://github.com/quic-go/quic-go), based on
release `v0.63.0` (commit `9d085cc690f7c96451e8ae5659eb0e64671da47a`).
It is stored in `third_party/quic-go` and used through a local Go module
replacement.

The h3fuzz-specific changes add reusable HTTP/3 request-header preparation for
the A42 high-throughput request path. The complete delta from the unmodified
upstream release is recorded in `third_party/quic-go-a42.patch`; its source
archive, revision, checksums, and changed-file allowlist are recorded in
`third_party/quic-go.UPSTREAM.json`. Run
`scripts/verify-third-party-quic-go.sh` to reconstruct the tree and verify it
byte-for-byte. The upstream `LICENSE` file is retained in the copied module.

The upstream `third_party/quic-go/assets` directory is not covered by
quic-go's MIT License. Its logo and trademark terms are retained verbatim in
[`third_party/quic-go/assets/LICENSE.md`](third_party/quic-go/assets/LICENSE.md).
Those assets are not used as h3fuzz branding.

## quic-go license

MIT License

Copyright (c) 2016 the quic-go authors & Google, Inc.

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

## Other Go dependencies

Other unmodified Go dependencies are declared in `go.mod` and authenticated by
`go.sum`; their source and license texts remain available from their respective
upstream modules. This notice does not alter or replace those license terms.

## Generated dependency license bundle

[`THIRD_PARTY_LICENSES.txt`](THIRD_PARTY_LICENSES.txt) is generated from the
Go packages linked into the h3fuzz executable and the installed frontend
production dependency graph. It also includes the pinned licenses for the
bundled default wordlist. The bundle contains a deterministic package and
version manifest, an explicit package-to-text mapping, and the verbatim license
and notice texts. Byte-identical texts are emitted once while every package
remains mapped to the full SHA-256 identifier for that text.

The published packages `react-remove-scroll-bar@2.3.8` and
`victory-vendor@37.3.6` omit a root license file. Their declared licenses,
pinned upstream license sources, checksums, and the D3 licenses bundled by
`victory-vendor` are handled explicitly by the audited configuration in
`scripts/licenses/overrides.json`. The generator fails if those declarations
or package layouts change.

After installing the frontend dependencies, regenerate the bundle with
`node scripts/generate-third-party-licenses.mjs`. Continuous integration and
the release workflow regenerate it and reject any diff.
