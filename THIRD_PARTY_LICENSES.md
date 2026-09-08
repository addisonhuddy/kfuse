# Third-party licenses

kfuse is licensed under Apache-2.0 (see `LICENSE`). The Go modules it
depends on, and their licenses, are listed below. All are permissive and
compatible with Apache-2.0; `hashicorp/go-uuid` is MPL-2.0, which applies
only to that module's own source files and is used unmodified.

Regenerate this table with:

```sh
go install github.com/google/go-licenses/v2@latest
go-licenses report ./... | grep -v addisonhuddy/kfuse | sort
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
