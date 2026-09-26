//! Compositing and encoding.
//!
//! This module is the offscreen wgpu compositor: it renders one frame at a time by
//! uploading the decoded source frame, drawing a full-screen triangle that composites the
//! window — chrome, content, corners and shadow, scaled and panned as one unit for the
//! click-zoom effect — over a static background, then reading the result back. Readback
//! via CPU is the simple path and is fast enough to beat real time; a later phase can keep
//! frames on the GPU and hand them straight to the encoder.
//!
//! [`export`] drives it end to end — decode, composite, encode.

pub mod export;

use crate::camera::Crop;
use anyhow::{Context, Result, anyhow};
use wgpu::util::DeviceExt;

#[repr(C)]
#[derive(Clone, Copy, bytemuck::Pod, bytemuck::Zeroable)]
struct Uniforms {
    // Fixed content rect within the source texture, normalized. The click-zoom effect
    // never changes this — it resizes and pans the window instead.
    content_uv: [f32; 4],
    // How far the window's centre is shifted from the canvas centre, in output pixels.
    win_offset: [f32; 2],
    // The window's fraction of its full, canvas-filling size; 1.0 at full zoom.
    win_scale: f32,
    _pad0: f32,
    out_size: [f32; 2],
    padding: f32,
    corner_radius: f32,
    shadow_offset: [f32; 2],
    shadow_blur: f32,
    shadow_alpha: f32,
    bg_top: [f32; 4],
    bg_bottom: [f32; 4],
    chrome_bg: [f32; 4],
    chrome_height: f32,
    chrome_style: f32,
    _pad: [f32; 2],
    pill_color: [f32; 4],
    bg_image_size: [f32; 2],
    use_bg_image: f32,
    _pad2: f32,
    // Webcam overlay: rect in output pixels (xy = origin, zw = size), corner radius in
    // output pixels (half the short side gives a circle from the same SDF the window
    // uses), and whether a frame was uploaded this call.
    cam_rect: [f32; 4],
    cam_radius: f32,
    cam_enabled: f32,
    _pad3: [f32; 2],
}

/// Synthetic window frame drawn around the capture.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Chrome {
    /// No title bar — the capture fills the whole rounded window.
    None,
    /// macOS title bar with traffic lights.
    Window,
    /// macOS title bar with traffic lights and a blank URL pill.
    Browser,
}

impl Chrome {
    /// Title bar height for a given *content* height. Deriving it from the content
    /// rather than the output avoids a circular dependency, since the output height is
    /// itself content + chrome.
    pub fn height(self, content_h: u32) -> f32 {
        match self {
            Chrome::None => 0.0,
            Chrome::Window | Chrome::Browser => (content_h as f32 * 0.042).clamp(28.0, 76.0),
        }
    }
    fn style(self) -> f32 {
        if self == Chrome::Browser { 1.0 } else { 0.0 }
    }
}

/// Visual style. All lengths are in **points**; `scaled()` converts to output pixels.
#[derive(Debug, Clone, Copy)]
pub struct Style {
    pub padding: f32,
    pub corner_radius: f32,
    pub shadow_offset: [f32; 2],
    pub shadow_blur: f32,
    pub shadow_alpha: f32,
    pub bg_top: [f32; 4],
    pub bg_bottom: [f32; 4],
    pub chrome: Chrome,
    pub chrome_bg: [f32; 4],
    pub pill_color: [f32; 4],
}

impl Style {
    /// Converts point lengths to pixels for a capture recorded at `scale`.
    ///
    /// Without this the look changes with the capture scale: at 2x Retina a radius meant
    /// to match macOS's own ~12pt corners renders half-size and reads as a hard edge,
    /// and on native-app captures it fails to clip the window's own rounded corners,
    /// leaving dark crescents.
    pub fn scaled(self, scale: f32) -> Self {
        Self {
            padding: self.padding * scale,
            corner_radius: self.corner_radius * scale,
            shadow_offset: [self.shadow_offset[0] * scale, self.shadow_offset[1] * scale],
            shadow_blur: self.shadow_blur * scale,
            ..self
        }
    }
}

impl Default for Style {
    fn default() -> Self {
        Self {
            padding: 48.0,
            corner_radius: 13.0,
            shadow_offset: [0.0, 9.0],
            shadow_blur: 22.0,
            shadow_alpha: 0.45,
            bg_top: [0.36, 0.40, 0.78, 1.0],
            bg_bottom: [0.60, 0.36, 0.72, 1.0],
            chrome: Chrome::Browser,
            chrome_bg: [0.16, 0.16, 0.18, 1.0],
            pill_color: [0.24, 0.24, 0.27, 1.0],
        }
    }
}

/// Placement and decode size of the webcam overlay, resolved before the renderer is
/// built, since texture creation is fixed-size. `width`/`height` are also the pixel
/// dimensions the caller must decode the camera video to.
#[derive(Debug, Clone, Copy)]
pub struct WebcamGeometry {
    pub width: u32,
    pub height: u32,
    /// Output-pixel rect: xy = origin, zw = size.
    pub rect: [f32; 4],
    /// Output pixels. Half the short side of `rect` gives a circle from the same SDF the
    /// window body uses; a smaller value gives a rounded rectangle.
    pub radius: f32,
}

/// Decoded background image: RGBA pixels plus dimensions.
pub struct BackgroundImage {
    pub rgba: Vec<u8>,
    pub width: u32,
    pub height: u32,
}

impl BackgroundImage {
    pub fn load(path: &std::path::Path) -> Result<Self> {
        let img = image::open(path)
            .with_context(|| format!("open background image {}", path.display()))?
            .to_rgba8();
        let (width, height) = img.dimensions();
        Ok(Self {
            rgba: img.into_raw(),
            width,
            height,
        })
    }
}

pub struct Renderer {
    device: wgpu::Device,
    queue: wgpu::Queue,
    pipeline: wgpu::RenderPipeline,
    bind_group: wgpu::BindGroup,
    src_tex: wgpu::Texture,
    target: wgpu::Texture,
    target_view: wgpu::TextureView,
    uniform_buf: wgpu::Buffer,
    readback: wgpu::Buffer,
    src_w: u32,
    src_h: u32,
    out_w: u32,
    out_h: u32,
    padded_bpr: u32,
    style: Style,
    bg_size: [f32; 2],
    use_bg_image: f32,
    cam_tex: wgpu::Texture,
    cam: Option<WebcamGeometry>,
    content_uv: [f32; 4],
}

/// Output dimensions that hold the content at 1:1 plus chrome and padding.
/// Both are rounded to even numbers, which yuv420p encoding requires.
pub fn output_size(src_w: u32, src_h: u32, style: &Style) -> (u32, u32) {
    let pad = style.padding.round() as u32 * 2;
    let chrome = style.chrome.height(src_h).round() as u32;
    (((src_w + pad) + 1) & !1, ((src_h + chrome + pad) + 1) & !1)
}

/// Whole-window scale and pan for one output frame: how much of its full, canvas-filling
/// size the window (chrome, content, corners and shadow, as one unit) should be drawn at,
/// and how far its centre should shift from the canvas centre.
#[derive(Debug, Clone, Copy)]
pub struct WindowZoom {
    pub scale: f32,
    pub offset: [f32; 2],
}

/// Derives a [`WindowZoom`] from a content-space zoom crop (see [`crate::camera::solve`]).
///
/// `scale` is `crop`'s magnification relative to `max_zoom`, so the window is at its
/// smallest, resting size when the crop is the full frame and grows to fill the canvas
/// exactly as the crop reaches its tightest. The pan offset comes from how far the crop's
/// centre has drifted from the content's centre — zero at rest, since an unzoomed crop is
/// always the full frame — applied against whatever room is actually left on each axis
/// once the window's own size is subtracted from the canvas. That room shrinks to a
/// constant (the padding margin) as scale reaches 1.0, so the window can never grow past
/// the canvas and its shadow and rounded corners are never clipped.
pub fn window_zoom(
    crop: Crop,
    content_w: f64,
    content_h: f64,
    max_zoom: f64,
    out_w: u32,
    out_h: u32,
    padding: f32,
) -> WindowZoom {
    let zoom = content_w / crop.w.max(1e-6);
    let scale = ((zoom / max_zoom.max(1e-6)) as f32).clamp(0.0, 1.0);

    // Signed fraction of content size the crop's centre has drifted from the content's
    // own centre; bounded to roughly +/-0.5 by the crop always fitting inside the content.
    let fx = ((crop.x + crop.w / 2.0) - content_w / 2.0) / content_w;
    let fy = ((crop.y + crop.h / 2.0) - content_h / 2.0) / content_h;

    let out_half = (out_w as f32 / 2.0, out_h as f32 / 2.0);
    let native_half = (out_half.0 - padding, out_half.1 - padding);
    let win_half = (native_half.0 * scale, native_half.1 * scale);
    let avail = (out_half.0 - win_half.0, out_half.1 - win_half.1);

    WindowZoom {
        scale,
        offset: [
            (fx as f32 * 2.0).clamp(-1.0, 1.0) * avail.0,
            (fy as f32 * 2.0).clamp(-1.0, 1.0) * avail.1,
        ],
    }
}

impl Renderer {
    #[allow(clippy::too_many_arguments)]
    pub fn new(
        src_w: u32,
        src_h: u32,
        out_w: u32,
        out_h: u32,
        style: Style,
        background: Option<BackgroundImage>,
        cam: Option<WebcamGeometry>,
        content_uv: [f32; 4],
    ) -> Result<Self> {
        let instance = wgpu::Instance::new(wgpu::InstanceDescriptor::new_without_display_handle());
        let adapter = pollster::block_on(instance.request_adapter(&wgpu::RequestAdapterOptions {
            power_preference: wgpu::PowerPreference::HighPerformance,
            force_fallback_adapter: false,
            compatible_surface: None,
            apply_limit_buckets: false,
        }))
        .map_err(|e| anyhow!("no suitable GPU adapter: {e}"))?;

        let (device, queue) = pollster::block_on(adapter.request_device(&wgpu::DeviceDescriptor {
            label: Some("recordo-render"),
            ..Default::default()
        }))
        .map_err(|e| anyhow!("failed to create device: {e}"))?;

        let shader = device.create_shader_module(wgpu::ShaderModuleDescriptor {
            label: Some("composite"),
            source: wgpu::ShaderSource::Wgsl(include_str!("shader.wgsl").into()),
        });

        // Unorm rather than Srgb: bytes arrive from and return to ffmpeg already
        // sRGB-encoded, so an implicit conversion here would double-apply gamma.
        const FORMAT: wgpu::TextureFormat = wgpu::TextureFormat::Rgba8Unorm;

        let src_tex = device.create_texture(&wgpu::TextureDescriptor {
            label: Some("source-frame"),
            size: wgpu::Extent3d {
                width: src_w,
                height: src_h,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: FORMAT,
            usage: wgpu::TextureUsages::TEXTURE_BINDING | wgpu::TextureUsages::COPY_DST,
            view_formats: &[],
        });
        let src_view = src_tex.create_view(&wgpu::TextureViewDescriptor::default());

        let target = device.create_texture(&wgpu::TextureDescriptor {
            label: Some("composited-frame"),
            size: wgpu::Extent3d {
                width: out_w,
                height: out_h,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: FORMAT,
            usage: wgpu::TextureUsages::RENDER_ATTACHMENT | wgpu::TextureUsages::COPY_SRC,
            view_formats: &[],
        });
        let target_view = target.create_view(&wgpu::TextureViewDescriptor::default());

        let (bg_w, bg_h) = background
            .as_ref()
            .map_or((1, 1), |b| (b.width.max(1), b.height.max(1)));
        let bg_tex = device.create_texture(&wgpu::TextureDescriptor {
            label: Some("background"),
            size: wgpu::Extent3d {
                width: bg_w,
                height: bg_h,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: FORMAT,
            usage: wgpu::TextureUsages::TEXTURE_BINDING | wgpu::TextureUsages::COPY_DST,
            view_formats: &[],
        });
        let bg_view = bg_tex.create_view(&wgpu::TextureViewDescriptor::default());
        let use_bg_image = if background.is_some() { 1.0 } else { 0.0 };

        // Sized to the camera's own decode resolution when enabled, or a 1x1 dummy when
        // not — the same pattern as `bg_tex` above. Unlike the background, this one is
        // uploaded fresh every frame, since it is a video, not a static image.
        let (cam_w, cam_h) = cam.map_or((1, 1), |g| (g.width.max(1), g.height.max(1)));
        let cam_tex = device.create_texture(&wgpu::TextureDescriptor {
            label: Some("webcam"),
            size: wgpu::Extent3d {
                width: cam_w,
                height: cam_h,
                depth_or_array_layers: 1,
            },
            mip_level_count: 1,
            sample_count: 1,
            dimension: wgpu::TextureDimension::D2,
            format: FORMAT,
            usage: wgpu::TextureUsages::TEXTURE_BINDING | wgpu::TextureUsages::COPY_DST,
            view_formats: &[],
        });
        let cam_view = cam_tex.create_view(&wgpu::TextureViewDescriptor::default());

        let sampler = device.create_sampler(&wgpu::SamplerDescriptor {
            label: Some("linear-clamp"),
            address_mode_u: wgpu::AddressMode::ClampToEdge,
            address_mode_v: wgpu::AddressMode::ClampToEdge,
            address_mode_w: wgpu::AddressMode::ClampToEdge,
            mag_filter: wgpu::FilterMode::Linear,
            min_filter: wgpu::FilterMode::Linear,
            mipmap_filter: wgpu::MipmapFilterMode::Nearest,
            ..Default::default()
        });

        let uniform_buf = device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
            label: Some("uniforms"),
            contents: bytemuck::bytes_of(&Uniforms {
                content_uv,
                win_offset: [0.0, 0.0],
                win_scale: 1.0,
                _pad0: 0.0,
                out_size: [out_w as f32, out_h as f32],
                padding: style.padding,
                corner_radius: style.corner_radius,
                shadow_offset: style.shadow_offset,
                shadow_blur: style.shadow_blur,
                shadow_alpha: style.shadow_alpha,
                bg_top: style.bg_top,
                bg_bottom: style.bg_bottom,
                chrome_bg: style.chrome_bg,
                chrome_height: style.chrome.height(src_h),
                chrome_style: style.chrome.style(),
                _pad: [0.0; 2],
                pill_color: style.pill_color,
                bg_image_size: [bg_w as f32, bg_h as f32],
                use_bg_image,
                _pad2: 0.0,
                cam_rect: cam.map_or([0.0; 4], |g| g.rect),
                cam_radius: cam.map_or(0.0, |g| g.radius),
                cam_enabled: 0.0,
                _pad3: [0.0; 2],
            }),
            usage: wgpu::BufferUsages::UNIFORM | wgpu::BufferUsages::COPY_DST,
        });

        let bgl = device.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor {
            label: Some("composite-bgl"),
            entries: &[
                wgpu::BindGroupLayoutEntry {
                    binding: 0,
                    visibility: wgpu::ShaderStages::FRAGMENT,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 1,
                    visibility: wgpu::ShaderStages::FRAGMENT,
                    ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering),
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 2,
                    visibility: wgpu::ShaderStages::FRAGMENT,
                    ty: wgpu::BindingType::Buffer {
                        ty: wgpu::BufferBindingType::Uniform,
                        has_dynamic_offset: false,
                        min_binding_size: None,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 3,
                    visibility: wgpu::ShaderStages::FRAGMENT,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
                wgpu::BindGroupLayoutEntry {
                    binding: 4,
                    visibility: wgpu::ShaderStages::FRAGMENT,
                    ty: wgpu::BindingType::Texture {
                        sample_type: wgpu::TextureSampleType::Float { filterable: true },
                        view_dimension: wgpu::TextureViewDimension::D2,
                        multisampled: false,
                    },
                    count: None,
                },
            ],
        });

        let bind_group = device.create_bind_group(&wgpu::BindGroupDescriptor {
            label: Some("composite-bg"),
            layout: &bgl,
            entries: &[
                wgpu::BindGroupEntry {
                    binding: 0,
                    resource: wgpu::BindingResource::TextureView(&src_view),
                },
                wgpu::BindGroupEntry {
                    binding: 1,
                    resource: wgpu::BindingResource::Sampler(&sampler),
                },
                wgpu::BindGroupEntry {
                    binding: 2,
                    resource: uniform_buf.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 3,
                    resource: wgpu::BindingResource::TextureView(&bg_view),
                },
                wgpu::BindGroupEntry {
                    binding: 4,
                    resource: wgpu::BindingResource::TextureView(&cam_view),
                },
            ],
        });

        let layout = device.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor {
            label: Some("composite-layout"),
            bind_group_layouts: &[Some(&bgl)],
            immediate_size: 0,
        });

        let pipeline = device.create_render_pipeline(&wgpu::RenderPipelineDescriptor {
            label: Some("composite-pipeline"),
            layout: Some(&layout),
            vertex: wgpu::VertexState {
                module: &shader,
                entry_point: Some("vs_main"),
                buffers: &[],
                compilation_options: Default::default(),
            },
            fragment: Some(wgpu::FragmentState {
                module: &shader,
                entry_point: Some("fs_main"),
                targets: &[Some(wgpu::ColorTargetState {
                    format: FORMAT,
                    blend: None,
                    write_mask: wgpu::ColorWrites::ALL,
                })],
                compilation_options: Default::default(),
            }),
            primitive: wgpu::PrimitiveState::default(),
            depth_stencil: None,
            multisample: wgpu::MultisampleState::default(),
            multiview_mask: None,
            cache: None,
        });

        // copy_texture_to_buffer requires rows padded to 256 bytes.
        let align = wgpu::COPY_BYTES_PER_ROW_ALIGNMENT;
        let padded_bpr = (out_w * 4).div_ceil(align) * align;

        let readback = device.create_buffer(&wgpu::BufferDescriptor {
            label: Some("readback"),
            size: (padded_bpr * out_h) as u64,
            usage: wgpu::BufferUsages::COPY_DST | wgpu::BufferUsages::MAP_READ,
            mapped_at_creation: false,
        });

        // Upload the background once; it never changes between frames.
        if let Some(bg) = &background {
            queue.write_texture(
                wgpu::TexelCopyTextureInfo {
                    texture: &bg_tex,
                    mip_level: 0,
                    origin: wgpu::Origin3d::ZERO,
                    aspect: wgpu::TextureAspect::All,
                },
                &bg.rgba,
                wgpu::TexelCopyBufferLayout {
                    offset: 0,
                    bytes_per_row: Some(bg.width * 4),
                    rows_per_image: Some(bg.height),
                },
                wgpu::Extent3d {
                    width: bg_w,
                    height: bg_h,
                    depth_or_array_layers: 1,
                },
            );
        }

        Ok(Self {
            device,
            queue,
            pipeline,
            bind_group,
            src_tex,
            target,
            target_view,
            uniform_buf,
            readback,
            src_w,
            src_h,
            out_w,
            out_h,
            padded_bpr,
            style,
            bg_size: [bg_w as f32, bg_h as f32],
            use_bg_image,
            cam_tex,
            cam,
            content_uv,
        })
    }

    pub fn out_frame_bytes(&self) -> usize {
        (self.out_w * self.out_h * 4) as usize
    }

    /// Composites one source frame. `src_rgba` must be `src_w * src_h * 4` bytes;
    /// `out` is overwritten with `out_w * out_h * 4` bytes. `cam_rgba`, when the webcam
    /// overlay is enabled, must be exactly the geometry's `width * height * 4` bytes;
    /// `None` for a frame (the camera track ran out, or never started) simply leaves the
    /// overlay off for that frame rather than freezing a stale picture on screen.
    pub fn render(
        &self,
        src_rgba: &[u8],
        win: WindowZoom,
        cam_rgba: Option<&[u8]>,
        out: &mut Vec<u8>,
    ) -> Result<()> {
        let expected = (self.src_w * self.src_h * 4) as usize;
        if src_rgba.len() != expected {
            return Err(anyhow!(
                "frame is {} bytes, expected {expected}",
                src_rgba.len()
            ));
        }

        self.queue.write_texture(
            wgpu::TexelCopyTextureInfo {
                texture: &self.src_tex,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            src_rgba,
            wgpu::TexelCopyBufferLayout {
                offset: 0,
                bytes_per_row: Some(self.src_w * 4),
                rows_per_image: Some(self.src_h),
            },
            wgpu::Extent3d {
                width: self.src_w,
                height: self.src_h,
                depth_or_array_layers: 1,
            },
        );

        let cam_enabled = if let (Some(rgba), Some(geo)) = (cam_rgba, self.cam) {
            self.queue.write_texture(
                wgpu::TexelCopyTextureInfo {
                    texture: &self.cam_tex,
                    mip_level: 0,
                    origin: wgpu::Origin3d::ZERO,
                    aspect: wgpu::TextureAspect::All,
                },
                rgba,
                wgpu::TexelCopyBufferLayout {
                    offset: 0,
                    bytes_per_row: Some(geo.width * 4),
                    rows_per_image: Some(geo.height),
                },
                wgpu::Extent3d {
                    width: geo.width,
                    height: geo.height,
                    depth_or_array_layers: 1,
                },
            );
            1.0
        } else {
            0.0
        };

        let u = Uniforms {
            content_uv: self.content_uv,
            win_offset: win.offset,
            win_scale: win.scale,
            _pad0: 0.0,
            out_size: [self.out_w as f32, self.out_h as f32],
            padding: self.style.padding,
            corner_radius: self.style.corner_radius,
            shadow_offset: self.style.shadow_offset,
            shadow_blur: self.style.shadow_blur,
            shadow_alpha: self.style.shadow_alpha,
            bg_top: self.style.bg_top,
            bg_bottom: self.style.bg_bottom,
            chrome_bg: self.style.chrome_bg,
            chrome_height: self.style.chrome.height(self.src_h),
            chrome_style: self.style.chrome.style(),
            _pad: [0.0; 2],
            pill_color: self.style.pill_color,
            bg_image_size: self.bg_size,
            use_bg_image: self.use_bg_image,
            _pad2: 0.0,
            cam_rect: self.cam.map_or([0.0; 4], |g| g.rect),
            cam_radius: self.cam.map_or(0.0, |g| g.radius),
            cam_enabled,
            _pad3: [0.0; 2],
        };
        self.queue
            .write_buffer(&self.uniform_buf, 0, bytemuck::bytes_of(&u));

        let mut encoder = self
            .device
            .create_command_encoder(&wgpu::CommandEncoderDescriptor {
                label: Some("frame"),
            });
        {
            let mut pass = encoder.begin_render_pass(&wgpu::RenderPassDescriptor {
                label: Some("composite-pass"),
                color_attachments: &[Some(wgpu::RenderPassColorAttachment {
                    view: &self.target_view,
                    depth_slice: None,
                    resolve_target: None,
                    ops: wgpu::Operations {
                        load: wgpu::LoadOp::Clear(wgpu::Color::BLACK),
                        store: wgpu::StoreOp::Store,
                    },
                })],
                depth_stencil_attachment: None,
                timestamp_writes: None,
                occlusion_query_set: None,
                multiview_mask: None,
            });
            pass.set_pipeline(&self.pipeline);
            pass.set_bind_group(0, &self.bind_group, &[]);
            pass.draw(0..3, 0..1);
        }

        encoder.copy_texture_to_buffer(
            wgpu::TexelCopyTextureInfo {
                texture: &self.target,
                mip_level: 0,
                origin: wgpu::Origin3d::ZERO,
                aspect: wgpu::TextureAspect::All,
            },
            wgpu::TexelCopyBufferInfo {
                buffer: &self.readback,
                layout: wgpu::TexelCopyBufferLayout {
                    offset: 0,
                    bytes_per_row: Some(self.padded_bpr),
                    rows_per_image: Some(self.out_h),
                },
            },
            wgpu::Extent3d {
                width: self.out_w,
                height: self.out_h,
                depth_or_array_layers: 1,
            },
        );
        self.queue.submit(Some(encoder.finish()));

        let slice = self.readback.slice(..);
        let (tx, rx) = std::sync::mpsc::channel();
        slice.map_async(wgpu::MapMode::Read, move |r| {
            let _ = tx.send(r);
        });
        self.device
            .poll(wgpu::PollType::wait_indefinitely())
            .map_err(|e| anyhow!("device poll failed: {e}"))?;
        rx.recv()
            .map_err(|e| anyhow!("readback channel closed: {e}"))?
            .map_err(|e| anyhow!("buffer map failed: {e}"))?;

        {
            let data = slice
                .get_mapped_range()
                .map_err(|e| anyhow!("map range failed: {e}"))?;
            let row = (self.out_w * 4) as usize;
            out.clear();
            out.reserve(self.out_frame_bytes());
            // Strip the 256-byte row padding the copy required.
            for y in 0..self.out_h as usize {
                let start = y * self.padded_bpr as usize;
                out.extend_from_slice(&data[start..start + row]);
            }
        }
        self.readback.unmap();
        Ok(())
    }
}
