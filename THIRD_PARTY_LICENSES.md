# Third-party licenses

kfuse is licensed under Apache-2.0 (see `LICENSE`). The Go modules it
depends on, and their licenses, are listed below. All are permissive and
compatible with Apache-2.0; `hashicorp/go-uuid` is MPL-2.0, which applies
only to that module's own source files and is used unmodified.

The full license/notice texts for every dependency are collected under
`third_party/` (one directory per module; MPL-2.0 modules also carry their
own source files, as that license requires). `third_party/` is committed to
the repo and shipped inside release archives and the runtime image at
`/usr/share/doc/kfuse/third_party/`.

Regenerate `third_party/` (and re-run `go-licenses report` to refresh this
table) with:

```sh
make install-tools   # pinned govulncheck + go-licenses
make licenses
```

| Module | License |
| --- | --- |
| github.com/IBM/sarama | MIT |
| github.com/aws/aws-sdk-go-v2 (and submodules) | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/internal/sync/singleflight | BSD-3-Clause |
| github.com/aws/smithy-go | Apache-2.0 |
| github.com/aws/smithy-go/internal/sync/singleflight | BSD-3-Clause |
| github.com/davecgh/go-spew | ISC |
| github.com/eapache/go-resiliency | MIT |
| github.com/hanwen/go-fuse/v2 | BSD-3-Clause |
| github.com/hashicorp/go-uuid | MPL-2.0 |
| github.com/jcmturner/aescts/v2 | Apache-2.0 |
| github.com/jcmturner/dnsutils/v2 | Apache-2.0 |
| github.com/jcmturner/gofork | BSD-3-Clause |
| github.com/jcmturner/gokrb5/v8 | Apache-2.0 |
| github.com/jcmturner/rpc/v2 | Apache-2.0 |
| github.com/klauspost/compress | Apache-2.0 / BSD-3-Clause / MIT |
| github.com/pierrec/lz4/v4 | BSD-3-Clause |
| github.com/rcrowley/go-metrics | BSD-2-Clause |
| github.com/spf13/cobra | Apache-2.0 |
| github.com/spf13/pflag | BSD-3-Clause |
| golang.org/x/crypto | BSD-3-Clause |
| golang.org/x/net | BSD-3-Clause |
| golang.org/x/sys | BSD-3-Clause |
| google.golang.org/protobuf | BSD-3-Clause |
