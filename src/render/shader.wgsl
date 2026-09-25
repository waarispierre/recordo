// Composites one recorded frame: crop-window zoom, synthetic window chrome, rounded
// corners, drop shadow, gradient background.
//
// Zoom is done by sampling a sub-rectangle of the source texture (crop-window sampling)
// rather than scaling an already-decoded frame, so magnified output stays as sharp as
// the source allows.
//
// The chrome is drawn rather than captured. That lets a recording of a bare web page be
// presented as a browser window with no tabs, no bookmarks, no profile avatar and no URL
// history — i.e. no identifying detail from the real browser.

struct Uniforms {
    // Crop rect in normalized source coords: xy = origin, zw = size.
    crop: vec4<f32>,
    out_size: vec2<f32>,
    padding: f32,
    corner_radius: f32,
    shadow_offset: vec2<f32>,
    shadow_blur: f32,
    shadow_alpha: f32,
    bg_top: vec4<f32>,
    bg_bottom: vec4<f32>,
    chrome_bg: vec4<f32>,
    // Title bar height in output pixels; 0 disables the chrome entirely.
    chrome_height: f32,
    // 0 = plain title bar, 1 = also draw a Safari-style URL pill.
    chrome_style: f32,
    pill_color: vec4<f32>,
    // Pixel size of the background image; ignored when use_bg_image is 0.
    bg_image_size: vec2<f32>,
    use_bg_image: f32,
    _pad2: f32,
};

@group(0) @binding(0) var src_tex: texture_2d<f32>;
@group(0) @binding(1) var src_sampler: sampler;
@group(0) @binding(2) var<uniform> u: Uniforms;
@group(0) @binding(3) var bg_tex: texture_2d<f32>;

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

fn circle(p: vec2<f32>, centre: vec2<f32>, r: f32) -> f32 {
    return 1.0 - smoothstep(r - 1.0, r + 1.0, length(p - centre));
}

@fragment
fn fs_main(in: VsOut) -> @location(0) vec4<f32> {
    let p = in.uv * u.out_size;
    let centre = u.out_size * 0.5;
    let half_size = centre - vec2<f32>(u.padding);
    let radius = min(u.corner_radius, min(half_size.x, half_size.y));

    var color = mix(u.bg_top, u.bg_bottom, in.uv.y);
    if (u.use_bg_image > 0.5) {
        // Cover fit: scale so the image fills both axes, then centre-crop the overflow.
        let scale = max(u.out_size.x / u.bg_image_size.x, u.out_size.y / u.bg_image_size.y);
        let drawn = u.bg_image_size * scale;
        let offset = (drawn - u.out_size) * 0.5;
        let bg_uv = (p + offset) / drawn;
        color = textureSampleLevel(bg_tex, src_sampler, bg_uv, 0.0);
    }

    // Shadow: the same rounded box, offset and softened.
    let shadow_d = rounded_box_sdf(p - centre - u.shadow_offset, half_size, radius);
    let shadow = (1.0 - smoothstep(0.0, u.shadow_blur, shadow_d)) * u.shadow_alpha;
    color = mix(color, vec4<f32>(0.0, 0.0, 0.0, 1.0), shadow);

    // The window is the full rounded box; the chrome occupies its top strip and the
    // captured content fills the remainder.
    let win_min = centre - half_size;
    let win_size = half_size * 2.0;
    let win_d = rounded_box_sdf(p - centre, half_size, radius);

    var window_color = u.chrome_bg;
    let content_top = win_min.y + u.chrome_height;

    if (p.y >= content_top) {
        // Map into the content area, then through the crop window.
        let local = vec2<f32>(
            (p.x - win_min.x) / win_size.x,
            (p.y - content_top) / max(win_size.y - u.chrome_height, 1.0),
        );
        let src_uv = u.crop.xy + clamp(local, vec2<f32>(0.0), vec2<f32>(1.0)) * u.crop.zw;
        window_color = textureSampleLevel(src_tex, src_sampler, src_uv, 0.0);
    } else if (u.chrome_height > 0.0) {
        // Traffic lights, evenly spaced from the left.
        let cy = win_min.y + u.chrome_height * 0.5;
        // Sized from the bar height rather than a fixed pixel cap: chrome_height is in
        // output pixels, so a constant would halve the dots on a Retina capture. macOS
        // draws roughly 12pt dots spaced 20pt apart in a ~28pt bar.
        let r = u.chrome_height * 0.19;
        let gap = r * 3.1;
        let x0 = win_min.x + gap + r;
        window_color = mix(window_color, vec4<f32>(1.0, 0.37, 0.34, 1.0),
            circle(p, vec2<f32>(x0, cy), r));
        window_color = mix(window_color, vec4<f32>(1.0, 0.74, 0.20, 1.0),
            circle(p, vec2<f32>(x0 + gap, cy), r));
        window_color = mix(window_color, vec4<f32>(0.16, 0.79, 0.25, 1.0),
            circle(p, vec2<f32>(x0 + gap * 2.0, cy), r));

        // Safari-style URL pill, centred in the title bar.
        if (u.chrome_style > 0.5) {
            let pill_h = u.chrome_height * 0.52;
            let pill_w = min(win_size.x * 0.34, 420.0);
            let pill_d = rounded_box_sdf(
                p - vec2<f32>(centre.x, cy),
                vec2<f32>(pill_w * 0.5, pill_h * 0.5),
                pill_h * 0.5,
            );
            window_color = mix(window_color, u.pill_color,
                1.0 - smoothstep(-1.0, 1.0, pill_d));
        }
    }

    // One-pixel antialiased edge so corners do not stair-step.
    let edge = 1.0 - smoothstep(-1.0, 1.0, win_d);
    color = mix(color, window_color, edge);

    return vec4<f32>(color.rgb, 1.0);
}
