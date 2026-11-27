// Composites one recorded frame: synthetic window chrome, rounded corners, drop shadow,
// gradient background, all viewed through the click-zoom camera.
//
// The scene — the window at its full size, padded on the background — never changes. The
// click-zoom camera magnifies all of it together around a point, as if moving closer to
// the screen: each output pixel is mapped back to the scene point it shows before
// anything is drawn. Only the webcam overlay sits outside the scene, fixed to the frame.
//
// The chrome is drawn rather than captured. That lets a recording of a bare web page be
// presented as a browser window with no tabs, no bookmarks, no profile avatar and no URL
// history — i.e. no identifying detail from the real browser.

struct Uniforms {
    // Fixed content rect within the source texture, normalized: xy = origin, zw = size.
    content_uv: vec4<f32>,
    // The click-zoom camera: the scene point, in output pixels, at the frame's centre,
    // and the magnification around it (1.0 shows the whole scene).
    zoom_centre: vec2<f32>,
    zoom_scale: f32,
    _pad0: f32,
    out_size: vec2<f32>,
    padding: f32,
    corner_radius: f32,
    shadow_offset: vec2<f32>,
    shadow_blur: f32,
    shadow_alpha: f32,
    bg_top: vec4<f32>,
    bg_bottom: vec4<f32>,
    chrome_bg: vec4<f32>,
    // Title bar height in scene pixels; 0 disables the chrome entirely.
    chrome_height: f32,
    // 0 = plain title bar, 1 = also draw a Safari-style URL pill.
    chrome_style: f32,
    pill_color: vec4<f32>,
    // Pixel size of the background image; ignored when use_bg_image is 0.
    bg_image_size: vec2<f32>,
    use_bg_image: f32,
    _pad2: f32,
    // Webcam overlay: rect in output pixels (xy = origin, zw = size).
    cam_rect: vec4<f32>,
    // Output pixels; half the short side of cam_rect gives a circle.
    cam_radius: f32,
    cam_enabled: f32,
    _pad3: vec2<f32>,
};

@group(0) @binding(0) var src_tex: texture_2d<f32>;
@group(0) @binding(1) var src_sampler: sampler;
@group(0) @binding(2) var<uniform> u: Uniforms;
@group(0) @binding(3) var bg_tex: texture_2d<f32>;
@group(0) @binding(4) var cam_tex: texture_2d<f32>;

struct VsOut {
    @builtin(position) pos: vec4<f32>,
    @location(0) uv: vec2<f32>,
};

// Full-screen triangle — no vertex buffer needed.
@vertex
fn vs_main(@builtin(vertex_index) idx: u32) -> VsOut {
    var out: VsOut;
    let x = f32((idx << 1u) & 2u);
    let y = f32(idx & 2u);
    out.uv = vec2<f32>(x, y);
    out.pos = vec4<f32>(x * 2.0 - 1.0, 1.0 - y * 2.0, 0.0, 1.0);
    return out;
}

// Signed distance to a rounded box centred at the origin.
fn rounded_box_sdf(p: vec2<f32>, half_size: vec2<f32>, radius: f32) -> f32 {
    let q = abs(p) - half_size + vec2<f32>(radius);
    return length(max(q, vec2<f32>(0.0))) + min(max(q.x, q.y), 0.0) - radius;
}

// aa is the antialiasing half-width: one output pixel, in scene units.
fn circle(p: vec2<f32>, centre: vec2<f32>, r: f32, aa: f32) -> f32 {
    return 1.0 - smoothstep(r - aa, r + aa, length(p - centre));
}

@fragment
fn fs_main(in: VsOut) -> @location(0) vec4<f32> {
    let frame_p = in.uv * u.out_size;
    let centre = u.out_size * 0.5;

    // The camera: the scene point this output pixel shows. Everything below except the
    // webcam is drawn in scene space, so the whole picture zooms together.
    let p = u.zoom_centre + (frame_p - centre) / u.zoom_scale;
    let aa = 1.0 / u.zoom_scale;

    var color = mix(u.bg_top, u.bg_bottom, p.y / u.out_size.y);
    if (u.use_bg_image > 0.5) {
        // Cover fit: scale so the image fills both axes, then centre-crop the overflow.
        let scale = max(u.out_size.x / u.bg_image_size.x, u.out_size.y / u.bg_image_size.y);
        let drawn = u.bg_image_size * scale;
        let offset = (drawn - u.out_size) * 0.5;
        let bg_uv = (p + offset) / drawn;
        color = textureSampleLevel(bg_tex, src_sampler, bg_uv, 0.0);
    }

    // The window fills the canvas inside the padding.
    let win_half = centre - vec2<f32>(u.padding);
    let win_centre = centre;
    let radius = min(u.corner_radius, min(win_half.x, win_half.y));

    // Shadow: the same rounded box, offset and softened.
    let shadow_d = rounded_box_sdf(p - win_centre - u.shadow_offset, win_half, radius);
    let shadow = (1.0 - smoothstep(0.0, u.shadow_blur, shadow_d)) * u.shadow_alpha;
    color = mix(color, vec4<f32>(0.0, 0.0, 0.0, 1.0), shadow);

    // The window is the full rounded box; the chrome occupies its top strip and the
    // captured content fills the remainder.
    let win_min = win_centre - win_half;
    let win_size = win_half * 2.0;
    let win_d = rounded_box_sdf(p - win_centre, win_half, radius);
    let chrome_height = u.chrome_height;

    var window_color = u.chrome_bg;
    let content_top = win_min.y + chrome_height;

    if (p.y >= content_top) {
        // Map into the content area, then onto the fixed content rect.
        let local = vec2<f32>(
            (p.x - win_min.x) / win_size.x,
            (p.y - content_top) / max(win_size.y - chrome_height, 1.0),
        );
        let src_uv = u.content_uv.xy + clamp(local, vec2<f32>(0.0), vec2<f32>(1.0)) * u.content_uv.zw;
        window_color = textureSampleLevel(src_tex, src_sampler, src_uv, 0.0);
    } else if (u.chrome_height > 0.0) {
        // Traffic lights, evenly spaced from the left.
        let cy = win_min.y + chrome_height * 0.5;
        // Sized from the bar height rather than a fixed pixel cap: chrome_height is in
        // output pixels, so a constant would halve the dots on a Retina capture. macOS
        // draws roughly 12pt dots spaced 20pt apart in a ~28pt bar.
        let r = chrome_height * 0.19;
        let gap = r * 3.1;
        let x0 = win_min.x + gap + r;
        window_color = mix(window_color, vec4<f32>(1.0, 0.37, 0.34, 1.0),
            circle(p, vec2<f32>(x0, cy), r, aa));
        window_color = mix(window_color, vec4<f32>(1.0, 0.74, 0.20, 1.0),
            circle(p, vec2<f32>(x0 + gap, cy), r, aa));
        window_color = mix(window_color, vec4<f32>(0.16, 0.79, 0.25, 1.0),
            circle(p, vec2<f32>(x0 + gap * 2.0, cy), r, aa));

        // Safari-style URL pill, centred in the title bar.
        if (u.chrome_style > 0.5) {
            let pill_h = chrome_height * 0.52;
            let pill_w = min(win_size.x * 0.34, 420.0);
            let pill_d = rounded_box_sdf(
                p - vec2<f32>(win_centre.x, cy),
                vec2<f32>(pill_w * 0.5, pill_h * 0.5),
                pill_h * 0.5,
            );
            window_color = mix(window_color, u.pill_color,
                1.0 - smoothstep(-aa, aa, pill_d));
        }
    }

    // One-pixel antialiased edge so corners do not stair-step.
    let edge = 1.0 - smoothstep(-aa, aa, win_d);
    color = mix(color, window_color, edge);

    // Webcam picture-in-picture, drawn last so it sits above everything else, and in frame
    // space so the zoom leaves it alone. It reuses the same rounded-box SDF the window
    // uses — a circle is simply a radius of half the short side, so no separate "is this
    // a circle" branch is needed.
    if (u.cam_enabled > 0.5) {
        let cam_centre = u.cam_rect.xy + u.cam_rect.zw * 0.5;
        let cam_half = u.cam_rect.zw * 0.5;
        let cam_d = rounded_box_sdf(frame_p - cam_centre, cam_half, u.cam_radius);

        // A soft shadow behind the camera, the same shape offset and blurred, so it
        // reads as floating above the content rather than pasted onto it.
        let cam_shadow_d = rounded_box_sdf(frame_p - cam_centre - vec2<f32>(0.0, 4.0), cam_half, u.cam_radius);
        let cam_shadow = (1.0 - smoothstep(0.0, 16.0, cam_shadow_d)) * 0.35;
        color = mix(color, vec4<f32>(0.0, 0.0, 0.0, 1.0), cam_shadow);

        let cam_uv = (frame_p - u.cam_rect.xy) / u.cam_rect.zw;
        let cam_color = textureSampleLevel(cam_tex, src_sampler, cam_uv, 0.0);
        let cam_edge = 1.0 - smoothstep(-1.0, 1.0, cam_d);
        color = mix(color, cam_color, cam_edge);

        // A thin border ring so the overlay reads as a deliberate frame rather than a
        // sharp cutout, especially over a busy background.
        let ring_d = abs(cam_d) - 1.5;
        let ring = (1.0 - smoothstep(0.0, 1.5, ring_d)) * 0.5;
        color = mix(color, vec4<f32>(1.0, 1.0, 1.0, 1.0), ring * step(cam_d, 4.0));
    }

    return vec4<f32>(color.rgb, 1.0);
}
