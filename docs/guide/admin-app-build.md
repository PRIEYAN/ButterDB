# Building and packaging the admin app

The admin app is a Fyne desktop program in `apps/admin` (module
`github.com/PRIEYAN/ButterDB/apps/admin`). It needs cgo and OpenGL, so it is
built on the operating system it targets, or in a container for Linux and
Windows. This guide is the operator view of `docs/spec/admin-gui.md` section 17.

## What the release program does

`apps/admin/packaging/cmd/release` is a Go program. It runs the pinned `fyne`
packager for the host operating system, moves the result into
`apps/admin/dist/` under a stable name, and records a SHA-256 line for it in
`apps/admin/dist/SHA256SUMS`. Nothing is left in the source tree and
`apps/admin/FyneApp.toml` is restored byte for byte, because `fyne package`
increments `Build` in that file on every run.

Run it from the repository root:

```
go -C apps/admin run ./packaging/cmd/release -version 0.1.0
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-os` | host OS | `linux`, `windows` or `darwin` |
| `-arch` | host architecture, or every release architecture with `-cross` | `amd64` or `arm64` |
| `-version` | latest `v*` git tag, then `Version` in `FyneApp.toml` | `x.y.z` |
| `-cross` | off | build with `fyne-cross` inside a container instead of natively |
| `-out` | `dist` | output directory, relative to `-dir` |
| `-dir` | `.` | admin module directory |
| `-dry-run` | off | print the commands and the files that would be written, run nothing |

Preview a build without a toolchain:

```
go -C apps/admin run ./packaging/cmd/release -os linux -dry-run
```

## Artifacts

| Host | Command the program runs | File in `dist/` |
| --- | --- | --- |
| Linux | `go -C apps/admin run fyne.io/tools/cmd/fyne package -os linux -release --app-version <v>` | `butterdb-admin_<v>_linux_<arch>.tar.xz` holding the binary, the `.desktop` file, the icon and a Makefile installer |
| Windows | same with `-os windows` | `butterdb-admin_<v>_windows_amd64.zip` holding `butterdb-admin.exe` with embedded icon and manifest |
| macOS | same with `-os darwin` | `butterdb-admin_<v>_darwin_<arch>.zip` holding `butterDB Admin.app` |

The `fyne` tool is the version pinned in `apps/admin/go.mod`
(`fyne.io/tools v1.7.0`), so `go run` needs no separate install. The release
program refuses to run when the packager's output file already exists in
`apps/admin`; delete the stale file and run again.

`dist/` is ignored by git. Never commit build outputs or signing material.

## Native build requirements

| OS | Build requirements | Runtime requirements |
| --- | --- | --- |
| Linux (Debian, Ubuntu) | `gcc`, `pkg-config`, `libgl1-mesa-dev`, `xorg-dev` | OpenGL 2.1 driver, X11 or XWayland, a session D-Bus and a Secret Service provider for the keychain |
| Linux (Fedora) | `gcc`, `libXcursor-devel`, `libXrandr-devel`, `mesa-libGL-devel`, `libXi-devel`, `libXinerama-devel`, `libXxf86vm-devel` | As above |
| Linux (Arch) | `base-devel`, `libxcursor`, `libxrandr`, `libxinerama`, `libxi`, `mesa` | As above |
| macOS | Xcode Command Line Tools (`xcode-select --install`) | macOS 11 or newer |
| Windows | A MinGW-w64 GCC on `PATH` (MSYS2 `mingw-w64-x86_64-gcc` or TDM-GCC) and `CGO_ENABLED=1` | An OpenGL 2.1 driver; in a VM without one, place Mesa's `opengl32.dll` beside the exe |

Release builds use X11 or XWayland on Linux. A native Wayland build adds
`-tags wayland` and needs `libwayland-dev`, `libxkbcommon-dev` and
`wayland-protocols`; that is not a release target.

Check a Linux host before building:

```
cc --version
pkg-config --exists gl x11 xrandr xcursor xinerama xi
```

Native builds target the host architecture only, because cgo needs a matching C
toolchain. Build the other architecture on a host of that architecture, or use a
cross build.

## Cross builds

`fyne-cross` builds inside Docker or Podman images that carry the C toolchain.
Pass `-cross` and the release program runs the pinned version
`github.com/fyne-io/fyne-cross@v1.6.3` through `go run`, then collects each
archive from `apps/admin/fyne-cross/dist/<os>-<arch>/` into `dist/` and removes
the `fyne-cross` directory.

| Target | Command |
| --- | --- |
| Linux amd64 and arm64 | `go -C apps/admin run ./packaging/cmd/release -os linux -cross` runs `fyne-cross linux -arch=amd64,arm64 -app-id io.github.prieyan.butterdb.admin -app-version <v>` |
| Windows amd64 | `go -C apps/admin run ./packaging/cmd/release -os windows -cross` runs `fyne-cross windows -arch=amd64 -app-id io.github.prieyan.butterdb.admin -app-version <v>` |
| macOS | Build natively on a Mac. `fyne-cross darwin` needs a macOS SDK image extracted from Xcode, which the owner must provide under Apple's licence |

`fyne-cross` needs a running container engine, and its first run downloads
images. The release program stops with an error when a cross archive is missing
or ambiguous rather than guessing.

## Icon

`apps/admin/packaging/icon.svg` is the vector source and
`apps/admin/packaging/icon.png` is its 1024 by 1024 rendering. `FyneApp.toml`
points at the PNG. When the SVG changes, render it again at 1024 by 1024 and
commit both files together.

## Signing and first run

Release archives are unsigned in v0.1.0. Code signing and notarisation are an
owner gate for a later release because they need the owner's certificates.

- macOS Gatekeeper blocks the first launch of an unsigned app. Control-click
  `butterDB Admin.app`, choose Open, and confirm. From a terminal,
  `xattr -d com.apple.quarantine "butterDB Admin.app"` removes the quarantine
  flag.
- Windows SmartScreen shows a warning. Choose More info, then Run anyway.
- Linux needs no step. Extract the archive and run `make user-install` or
  `sudo make install` from the extracted directory to add the desktop entry.

Check a download against `SHA256SUMS` before running it:

```
sha256sum -c SHA256SUMS --ignore-missing
```

## What was verified

The Linux amd64 native build was run on a Linux host and produced the archive
described above. The Windows and macOS native paths and the `fyne-cross` paths
are covered by unit tests with a fake command runner and by `-dry-run`; they
were not run against a real Windows host, macOS host or container engine.
