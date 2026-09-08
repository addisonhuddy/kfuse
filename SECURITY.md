# Security policy

## Reporting a vulnerability

Please do not open a public issue for security problems.

Report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/addisonhuddy/kfuse/security/advisories/new)
or by email to addisonhuddy@gmail.com. Include the affected version or commit,
a description of the issue, and reproduction steps if you have them.

You should get an acknowledgement within a few days. Once a fix is ready we will
publish a patched release and a GitHub Security Advisory crediting the reporter
unless you prefer to stay anonymous.

## Scope

kfuse mounts a FUSE filesystem and talks to Kafka and S3 using credentials from
the environment. Issues of particular interest:

- privilege escalation or escape from the mount into the host
- credential leakage (to logs, Kafka, S3, or the state directory)
- data corruption or loss in the commit or checkpoint path
- bypass of single-writer fencing (S3 lease)

## Supported versions

Only the latest release receives security fixes.
