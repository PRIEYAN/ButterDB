# Object viewer

The owner requested double-clicking an uploaded object, choosing Open in an
in-app popup, and viewing images or streaming video/music inside butterDB.
Other files appear as a file card. The owner selected native MPV playback.

## Approaches

- Native Fyne images and embedded MPV playback: selected by the owner. Reuses
  the installed player and supports its codecs without a browser runtime.
- Embedded browser: considered, rejected in favor of native playback.

## Behavior

Single-click retains selection and details. Double-click or Enter on an
object opens an Open/Cancel popup showing its name, type, and readable size.
Folders retain their existing navigation. Open creates a butterDB viewer
window with loading and error states. Images render with preserved aspect
ratio. Video and audio use MPV's play/pause, seek, volume and fullscreen
controls. Unsupported formats show a file card without interpreting bytes.

The implementation choice is a child MPV surface embedded in a Fyne-owned
window. X11/XWayland and Windows expose compatible window handles; unsupported
native display backends show an explicit error rather than launching an
external player. MPV is a runtime dependency for audio/video only. No new Go
module or C binding is required. The Linux build uses X11 by default.

## Streaming and lifetime

Each playback gets a loopback-only server bound to a random port with an
unpredictable path. The bridge forwards the selected object's byte ranges
through the existing authenticated client, pins its ETag, and exposes no admin
credentials. HEAD, full GET, bounded/open/suffix byte ranges, empty objects,
and invalid ranges have explicit handling. Video/audio begin playing before
the full file downloads. MPV disk caching and user scripts/config are disabled.

Closing the viewer, leaving the Objects page, or closing the project cancels
the player, active requests, and bridge. Images retain the existing 20 MiB and
40 megapixel decoding ceilings. No object data is written to a temporary file.

## Boundaries and validation

Objects consumes a FileOpener port. A new admin-only internal/viewer package
owns presentation, image decoding, and the private streaming bridge. The app
composition root injects its authenticated client and an MPV process adapter.

Tests cover double-click/Open/Cancel, folder behavior, image rendering and
failure, generic cards, media dispatch, range correctness, overwrite errors,
and cancellation. A real MPV smoke test checks native embedding and streaming
with generated local media, without touching Telegram or user objects.
