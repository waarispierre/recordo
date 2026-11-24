package config

// DefaultTOML is the commented file written on first run. Its comments double as the
// help text in the settings menu (see Settings), and missing keys are migrated into an
// existing file from here with their comments attached (see addMissingSettings).
//
// It must stay byte-identical to DEFAULT_TOML in the Rust src/config.rs until parity.
const DefaultTOML = `# recordo settings. Delete any line to fall back to its default.

[camera]
# How far to zoom in, as a percentage. 0 disables zooming, 45 means 1.45x.
zoom_percent = 45.0
# Seconds to ramp in, dwell, and ramp back out around each click.
zoom_in_s = 0.45
hold_s = 1.3
zoom_out_s = 0.7
# Cursor-follow spring frequency (Hz). Higher tracks more tightly, lower feels calmer.
follow_hz = 1.1

# How far the cursor may wander before the camera follows it, as a percentage of the
# visible width. The camera ignores everything inside this circle, so small movements
# while pointing or reading do not make it drift. Raise it for a calmer camera, set it
# to 0 to follow continuously.
follow_deadzone_percent = 10.0

[audio]
# Record your voice while you record the screen. Off by default, so a recording never
# picks up the room by surprise. macOS asks for Microphone permission the first time.
microphone = false

# Which microphone, matched on name — "recordo devices" lists them. Empty uses whichever
# input macOS is currently set to.
device = ""

[webcam]
# Show your webcam over the recording, picture-in-picture. Off by default.
enabled = false

# Which camera, matched on name — "recordo devices" lists them. Empty uses the system
# default.
device = ""

# Where it sits: "bottom-left", "bottom-right", "top-left" or "top-right".
position = "bottom-left"

# Distance from that corner, in points.
inset = 32.0

# Extra nudge from the corner, in points, for when the inset alone is not enough.
offset = [0.0, 0.0]

# Height as a percentage of the exported frame's height.
size_percent = 18.0

# "circle" or "rect".
shape = "circle"

# Corner rounding in points. Only used when shape is "rect".
corner_radius = 24.0

# Mirror the image, so it reads the way a mirror does.
mirror = true

# Seconds to show a live framing preview before a recording starts, so you can check you
# are in shot. The same preview stays in the corner while recording. 0 turns both off.
preview_s = 3.0

[style]
# All lengths below are in POINTS and scale with the capture, so the look is the same
# on Retina and non-Retina displays.
padding = 48.0
# Roughly matches macOS's own window corners. Too small leaves dark crescents where the
# captured window's own rounded corners are not fully clipped.
corner_radius = 13.0
shadow_offset = [0.0, 9.0]
shadow_blur = 22.0
shadow_alpha = 0.45

# Background gradient, RGB 0-1. Ignored when background_image is set.
bg_top = [0.36, 0.40, 0.78]
bg_bottom = [0.60, 0.36, 0.72]

# Image behind the window, scaled to cover and centre-cropped. Empty uses the gradient
# above. Accepts ~ and relative paths.
background_image = ""

# Synthetic window frame: "auto", "none", "window", or "browser".
# "auto" gives browsers a mock browser frame with their real tab/address strip cropped
# off (no tabs, bookmarks, avatar or URL history), and leaves native apps alone since
# they already draw their own title bar.
chrome = "auto"
chrome_bg = [0.16, 0.16, 0.18]
pill_color = [0.24, 0.24, 0.27]

# Points of real browser chrome to crop. Raise if tabs still show, lower if page content
# is being cut off.
browser_crop_top = 88.0
`
