// Package gpu is the offscreen WebGPU compositor.
//
// It renders one frame at a time by uploading the decoded source frame,
// drawing a full-screen triangle that composites the window — chrome, content, corners
// and shadow, scaled and panned as one unit for the click-zoom effect — over a static
// background, then reading the result back. Readback via CPU is the simple path and is
// fast enough to beat real time; a later phase can keep frames on the GPU and hand them
// straight to the encoder.
package gpu

import (
	_ "embed"
	"fmt"
	"unsafe"

	"github.com/cogentcore/webgpu/wgpu"
	"github.com/waarispierre/recordo/internal/domain/config"
)

//go:embed shader.wgsl
var shaderSource string

// uniforms must match the WGSL Uniforms struct byte for byte. Every field is a float32,
// so Go inserts no padding of its own; the explicit pads reproduce WGSL's alignment rules
// (a vec4 starts on a 16-byte boundary). uniformsLayout in the tests pins this.
type uniforms struct {
	// Fixed content rect within the source texture, normalized. The click-zoom effect
	// never changes this — it resizes and pans the window instead.
	ContentUV [4]float32
	// How far the window's centre is shifted from the canvas centre, in output pixels.
	WinOffset [2]float32
	// The window's fraction of its full, canvas-filling size; 1.0 at full zoom.
	WinScale     float32
	_            float32
	OutSize      [2]float32
	Padding      float32
	CornerRadius float32
	ShadowOffset [2]float32
	ShadowBlur   float32
	ShadowAlpha  float32
	BgTop        [4]float32
	BgBottom     [4]float32
	ChromeBg     [4]float32
	ChromeHeight float32
	ChromeStyle  float32
	_            [2]float32
	PillColor    [4]float32
	BgImageSize  [2]float32
	UseBgImage   float32
	_            float32
	// Webcam overlay: rect in output pixels (xy = origin, zw = size), corner radius in
	// output pixels (half the short side gives a circle from the same SDF the window
	// uses), and whether a frame was uploaded this call.
	CamRect    [4]float32
	CamRadius  float32
	CamEnabled float32
	_          [2]float32
}

func (u *uniforms) bytes() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(u)), unsafe.Sizeof(*u))
}

// WebcamGeometry is the placement and decode size of the webcam overlay, resolved before
// the renderer is built, since textures are fixed-size. Width and Height are also the
// pixel dimensions the caller must decode the camera video to.
type WebcamGeometry struct {
	Width, Height uint32
	// Rect is in output pixels: xy = origin, zw = size.
	Rect [4]float32
	// Radius is in output pixels. Half the short side of Rect gives a circle from the
	// same SDF the window body uses; a smaller value gives a rounded rectangle.
	Radius float32
}

// Image is decoded RGBA pixels plus dimensions.
type Image struct {
	RGBA          []byte
	Width, Height uint32
}

// Unorm rather than Srgb: bytes arrive from and return to ffmpeg already sRGB-encoded, so
// an implicit conversion here would double-apply gamma.
const format = wgpu.TextureFormatRGBA8Unorm

// Renderer composites frames on the GPU. It is not safe for concurrent use.
type Renderer struct {
	instance   *wgpu.Instance
	adapter    *wgpu.Adapter
	device     *wgpu.Device
	queue      *wgpu.Queue
	pipeline   *wgpu.RenderPipeline
	bindGroup  *wgpu.BindGroup
	srcTex     *wgpu.Texture
	target     *wgpu.Texture
	targetView *wgpu.TextureView
	uniformBuf *wgpu.Buffer
	readback   *wgpu.Buffer
	camTex     *wgpu.Texture
	// released holds every GPU object in creation order, so Release can free them all
	// newest first, including after a failed NewRenderer.
	released []interface{ Release() }

	srcW, srcH, outW, outH uint32
	paddedBPR              uint32
	cam                    *WebcamGeometry
	base                   uniforms
}

// NewRenderer builds the GPU pipeline for one export. background and cam may be nil.
func NewRenderer(srcW, srcH, outW, outH uint32, style config.Style, background *Image, cam *WebcamGeometry, contentUV [4]float32) (r *Renderer, err error) {
	r = &Renderer{srcW: srcW, srcH: srcH, outW: outW, outH: outH, cam: cam}
	defer func() {
		if err != nil {
			r.Release()
			r = nil
		}
	}()

	r.instance = wgpu.CreateInstance(nil)
	r.keep(r.instance)
	r.adapter, err = r.instance.RequestAdapter(&wgpu.RequestAdapterOptions{
		PowerPreference: wgpu.PowerPreferenceHighPerformance,
	})
	if err != nil {
		return nil, fmt.Errorf("no suitable GPU adapter: %w", err)
	}
	r.keep(r.adapter)
	r.device, err = r.adapter.RequestDevice(&wgpu.DeviceDescriptor{Label: "recordo-render"})
	if err != nil {
		return nil, fmt.Errorf("create GPU device: %w", err)
	}
	r.keep(r.device)
	r.queue = r.device.GetQueue()
	r.keep(r.queue)

	shader, err := r.device.CreateShaderModule(&wgpu.ShaderModuleDescriptor{
		Label:          "composite",
		WGSLDescriptor: &wgpu.ShaderModuleWGSLDescriptor{Code: shaderSource},
	})
	if err != nil {
		return nil, fmt.Errorf("compile shader: %w", err)
	}
	r.keep(shader)

	sampled := wgpu.TextureUsageTextureBinding | wgpu.TextureUsageCopyDst
	srcView, err := r.texture("source-frame", srcW, srcH, sampled, &r.srcTex)
	if err != nil {
		return nil, err
	}
	r.targetView, err = r.texture("composited-frame", outW, outH,
		wgpu.TextureUsageRenderAttachment|wgpu.TextureUsageCopySrc, &r.target)
	if err != nil {
		return nil, err
	}

	bgW, bgH := uint32(1), uint32(1)
	if background != nil {
		bgW, bgH = max(background.Width, 1), max(background.Height, 1)
	}
	var bgTex *wgpu.Texture
	bgView, err := r.texture("background", bgW, bgH, sampled, &bgTex)
	if err != nil {
		return nil, err
	}

	// Sized to the camera's own decode resolution when enabled, or a 1x1 dummy when not —
	// the same pattern as the background. Unlike the background, this one is uploaded
	// fresh every frame, since it is a video, not a static image.
	camW, camH := uint32(1), uint32(1)
	if cam != nil {
		camW, camH = max(cam.Width, 1), max(cam.Height, 1)
	}
	camView, err := r.texture("webcam", camW, camH, sampled, &r.camTex)
	if err != nil {
		return nil, err
	}

	sampler, err := r.device.CreateSampler(&wgpu.SamplerDescriptor{
		Label:         "linear-clamp",
		AddressModeU:  wgpu.AddressModeClampToEdge,
		AddressModeV:  wgpu.AddressModeClampToEdge,
		AddressModeW:  wgpu.AddressModeClampToEdge,
		MagFilter:     wgpu.FilterModeLinear,
		MinFilter:     wgpu.FilterModeLinear,
		MipmapFilter:  wgpu.MipmapFilterModeNearest,
		LodMaxClamp:   32,
		MaxAnisotropy: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("create sampler: %w", err)
	}
	r.keep(sampler)

	r.base = uniforms{
		ContentUV:    contentUV,
		WinScale:     1,
		OutSize:      [2]float32{float32(outW), float32(outH)},
		Padding:      style.Padding,
		CornerRadius: style.CornerRadius,
		ShadowOffset: style.ShadowOffset,
		ShadowBlur:   style.ShadowBlur,
		ShadowAlpha:  style.ShadowAlpha,
		BgTop:        style.BgTop,
		BgBottom:     style.BgBottom,
		ChromeBg:     style.ChromeBg,
		ChromeHeight: ChromeHeight(style.Chrome, srcH),
		ChromeStyle:  chromeStyle(style.Chrome),
		PillColor:    style.PillColor,
		BgImageSize:  [2]float32{float32(bgW), float32(bgH)},
	}
	if background != nil {
		r.base.UseBgImage = 1
	}
	if cam != nil {
		r.base.CamRect = cam.Rect
		r.base.CamRadius = cam.Radius
	}
	r.uniformBuf, err = r.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "uniforms",
		Contents: r.base.bytes(),
		Usage:    wgpu.BufferUsageUniform | wgpu.BufferUsageCopyDst,
	})
	if err != nil {
		return nil, fmt.Errorf("create uniform buffer: %w", err)
	}
	r.keep(r.uniformBuf)

	tex := wgpu.TextureBindingLayout{
		SampleType:    wgpu.TextureSampleTypeFloat,
		ViewDimension: wgpu.TextureViewDimension2D,
	}
	frag := wgpu.ShaderStageFragment
	bgl, err := r.device.CreateBindGroupLayout(&wgpu.BindGroupLayoutDescriptor{
		Label: "composite-bgl",
		Entries: []wgpu.BindGroupLayoutEntry{
			{Binding: 0, Visibility: frag, Texture: tex},
			{Binding: 1, Visibility: frag, Sampler: wgpu.SamplerBindingLayout{Type: wgpu.SamplerBindingTypeFiltering}},
			{Binding: 2, Visibility: frag, Buffer: wgpu.BufferBindingLayout{Type: wgpu.BufferBindingTypeUniform}},
			{Binding: 3, Visibility: frag, Texture: tex},
			{Binding: 4, Visibility: frag, Texture: tex},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create bind group layout: %w", err)
	}
	r.keep(bgl)

	r.bindGroup, err = r.device.CreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "composite-bg",
		Layout: bgl,
		Entries: []wgpu.BindGroupEntry{
			{Binding: 0, TextureView: srcView},
			{Binding: 1, Sampler: sampler},
			{Binding: 2, Buffer: r.uniformBuf, Size: wgpu.WholeSize},
			{Binding: 3, TextureView: bgView},
			{Binding: 4, TextureView: camView},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create bind group: %w", err)
	}
	r.keep(r.bindGroup)

	layout, err := r.device.CreatePipelineLayout(&wgpu.PipelineLayoutDescriptor{
		Label:            "composite-layout",
		BindGroupLayouts: []*wgpu.BindGroupLayout{bgl},
	})
	if err != nil {
		return nil, fmt.Errorf("create pipeline layout: %w", err)
	}
	r.keep(layout)

	r.pipeline, err = r.device.CreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Label:  "composite-pipeline",
		Layout: layout,
		Vertex: wgpu.VertexState{Module: shader, EntryPoint: "vs_main"},
		Fragment: &wgpu.FragmentState{
			Module:     shader,
			EntryPoint: "fs_main",
			Targets:    []wgpu.ColorTargetState{{Format: format, WriteMask: wgpu.ColorWriteMaskAll}},
		},
		Primitive: wgpu.PrimitiveState{
			Topology:  wgpu.PrimitiveTopologyTriangleList,
			FrontFace: wgpu.FrontFaceCCW,
			CullMode:  wgpu.CullModeNone,
		},
		Multisample: wgpu.MultisampleState{Count: 1, Mask: 0xFFFFFFFF},
	})
	if err != nil {
		return nil, fmt.Errorf("create render pipeline: %w", err)
	}
	r.keep(r.pipeline)

	// CopyTextureToBuffer requires rows padded to 256 bytes.
	const align = wgpu.CopyBytesPerRowAlignment
	r.paddedBPR = (outW*4 + align - 1) / align * align
	r.readback, err = r.device.CreateBuffer(&wgpu.BufferDescriptor{
		Label: "readback",
		Size:  uint64(r.paddedBPR) * uint64(outH),
		Usage: wgpu.BufferUsageCopyDst | wgpu.BufferUsageMapRead,
	})
	if err != nil {
		return nil, fmt.Errorf("create readback buffer: %w", err)
	}
	r.keep(r.readback)

	// Upload the background once; it never changes between frames.
	if background != nil {
		if err := r.upload(bgTex, background.RGBA, bgW, bgH); err != nil {
			return nil, fmt.Errorf("upload background: %w", err)
		}
	}
	return r, nil
}

func (r *Renderer) keep(x interface{ Release() }) { r.released = append(r.released, x) }

// texture creates a 2D texture and its default view, storing the texture in *out.
func (r *Renderer) texture(label string, w, h uint32, usage wgpu.TextureUsage, out **wgpu.Texture) (*wgpu.TextureView, error) {
	t, err := r.device.CreateTexture(&wgpu.TextureDescriptor{
		Label:         label,
		Usage:         usage,
		Dimension:     wgpu.TextureDimension2D,
		Size:          wgpu.Extent3D{Width: w, Height: h, DepthOrArrayLayers: 1},
		Format:        format,
		MipLevelCount: 1,
		SampleCount:   1,
	})
	if err != nil {
		return nil, fmt.Errorf("create %s texture: %w", label, err)
	}
	r.keep(t)
	*out = t
	v, err := t.CreateView(nil)
	if err != nil {
		return nil, fmt.Errorf("create %s view: %w", label, err)
	}
	r.keep(v)
	return v, nil
}

func (r *Renderer) upload(t *wgpu.Texture, rgba []byte, w, h uint32) error {
	return r.queue.WriteTexture(
		&wgpu.ImageCopyTexture{Texture: t, Aspect: wgpu.TextureAspectAll},
		rgba,
		&wgpu.TextureDataLayout{BytesPerRow: w * 4, RowsPerImage: h},
		&wgpu.Extent3D{Width: w, Height: h, DepthOrArrayLayers: 1},
	)
}

// OutFrameBytes is the size of one composited RGBA frame.
func (r *Renderer) OutFrameBytes() int { return int(r.outW * r.outH * 4) }

// Render composites one source frame into out, which is resized to OutFrameBytes.
//
// src must be srcW*srcH*4 bytes. camRGBA, when the webcam overlay is enabled, must be
// exactly the geometry's Width*Height*4 bytes; nil for a frame (the camera track ran out,
// or never started) simply leaves the overlay off for that frame rather than freezing a
// stale picture on screen.
func (r *Renderer) Render(src []byte, win WindowZoom, camRGBA []byte, out []byte) ([]byte, error) {
	if want := int(r.srcW * r.srcH * 4); len(src) != want {
		return out, fmt.Errorf("frame is %d bytes, expected %d", len(src), want)
	}
	if err := r.upload(r.srcTex, src, r.srcW, r.srcH); err != nil {
		return out, fmt.Errorf("upload frame: %w", err)
	}

	u := r.base
	u.WinOffset = win.Offset
	u.WinScale = win.Scale
	if camRGBA != nil && r.cam != nil {
		if err := r.upload(r.camTex, camRGBA, r.cam.Width, r.cam.Height); err != nil {
			return out, fmt.Errorf("upload webcam frame: %w", err)
		}
		u.CamEnabled = 1
	}
	if err := r.queue.WriteBuffer(r.uniformBuf, 0, u.bytes()); err != nil {
		return out, fmt.Errorf("write uniforms: %w", err)
	}

	enc, err := r.device.CreateCommandEncoder(&wgpu.CommandEncoderDescriptor{Label: "frame"})
	if err != nil {
		return out, fmt.Errorf("create encoder: %w", err)
	}
	defer enc.Release()
	pass := enc.BeginRenderPass(&wgpu.RenderPassDescriptor{
		Label: "composite-pass",
		ColorAttachments: []wgpu.RenderPassColorAttachment{{
			View:       r.targetView,
			LoadOp:     wgpu.LoadOpClear,
			StoreOp:    wgpu.StoreOpStore,
			ClearValue: wgpu.Color{A: 1},
		}},
	})
	pass.SetPipeline(r.pipeline)
	pass.SetBindGroup(0, r.bindGroup, nil)
	pass.Draw(3, 1, 0, 0)
	if err := pass.End(); err != nil {
		pass.Release()
		return out, fmt.Errorf("end render pass: %w", err)
	}
	pass.Release()

	if err := enc.CopyTextureToBuffer(
		&wgpu.ImageCopyTexture{Texture: r.target, Aspect: wgpu.TextureAspectAll},
		&wgpu.ImageCopyBuffer{Buffer: r.readback, Layout: wgpu.TextureDataLayout{BytesPerRow: r.paddedBPR, RowsPerImage: r.outH}},
		&wgpu.Extent3D{Width: r.outW, Height: r.outH, DepthOrArrayLayers: 1},
	); err != nil {
		return out, fmt.Errorf("copy to readback: %w", err)
	}
	cmd, err := enc.Finish(nil)
	if err != nil {
		return out, fmt.Errorf("finish encoder: %w", err)
	}
	r.queue.Submit(cmd)
	cmd.Release()

	size := uint64(r.paddedBPR) * uint64(r.outH)
	mapped := false
	status := wgpu.BufferMapAsyncStatusUnknown
	if err := r.readback.MapAsync(wgpu.MapModeRead, 0, size, func(s wgpu.BufferMapAsyncStatus) {
		mapped, status = true, s
	}); err != nil {
		return out, fmt.Errorf("map readback: %w", err)
	}
	// Blocks until the submitted work is done, which is also when the map callback runs.
	r.device.Poll(true, nil)
	if !mapped || status != wgpu.BufferMapAsyncStatusSuccess {
		return out, fmt.Errorf("map readback failed: status %d", status)
	}

	data := r.readback.GetMappedRange(0, uint(size))
	row := int(r.outW * 4)
	out = out[:0]
	// Strip the 256-byte row padding the copy required.
	for y := range int(r.outH) {
		start := y * int(r.paddedBPR)
		out = append(out, data[start:start+row]...)
	}
	if err := r.readback.Unmap(); err != nil {
		return out, fmt.Errorf("unmap readback: %w", err)
	}
	return out, nil
}

// Release frees every GPU object, newest first. The Renderer cannot be used afterwards.
func (r *Renderer) Release() {
	for i := len(r.released) - 1; i >= 0; i-- {
		r.released[i].Release()
	}
	r.released = nil
}
