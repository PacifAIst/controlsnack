**ControlSnack {{TAG}}.** An early release of free software (AGPL-3.0-only): a throwaway Linux desktop in a virtual machine on your own computer, which a vision model can operate through a checked and audited tool API. Read [what has and has not been run](https://github.com/PacifAIst/controlsnack#what-has-and-has-not-been-run) before you rely on it.

## Which file

| Your system | File |
|---|---|
| Windows 10 or 11, 64-bit | `controlsnack_{{TAG}}_windows_amd64.zip` |
| Linux, 64-bit Intel or AMD | `controlsnack_{{TAG}}_linux_amd64.tar.gz` |
| Linux, 64-bit ARM | `controlsnack_{{TAG}}_linux_arm64.tar.gz` |
| macOS, Apple silicon | `controlsnack_{{TAG}}_darwin_arm64.tar.gz` |
| macOS, Intel | `controlsnack_{{TAG}}_darwin_amd64.tar.gz` |

Each archive unpacks to a folder with the program (`controlsnack`, or `controlsnack.exe` on Windows), the license, the third-party notices, the README and TESTING.md. There is nothing to install: it is one file. You do need QEMU and a base image; the [guide for newcomers](https://github.com/PacifAIst/controlsnack/blob/main/docs/GETTING-STARTED.md) goes from nothing to a first task, and the [documentation](https://controlsnack.smartcityjaen.workers.dev/docs) describes every command and flag.

## Check what you downloaded

`SHA256SUMS` lists the SHA-256 of each archive. In the folder where you saved them:

```
sha256sum -c SHA256SUMS --ignore-missing                       # Linux, macOS (shasum -a 256 -c ...), Git Bash
Get-FileHash .\controlsnack_{{TAG}}_windows_amd64.zip          # Windows PowerShell: compare with the line in SHA256SUMS
```

The archives are built by GitHub Actions from the tagged commit with `go run ./tools/dev release`, and the same commit gives the same files.

## The binaries are not signed

- **Windows** may say the publisher is unknown: choose "More info", then "Run anyway", or build from source.
- **macOS** puts a downloaded program in quarantine: `xattr -d com.apple.quarantine ./controlsnack`.

## What has been run

Windows 10 with the Windows hypervisor (a local 4B model and a hosted Qwen model, whole sessions), and Linux in WSL2; the test suite passes on GitHub's Ubuntu, Windows and macOS machines. The ARM builds and the macOS builds have never been run by a person, and no ARM guest has ever been started. Anything that does not work is a bug worth reporting: `controlsnack version` and `controlsnack doctor` say what a report needs.
