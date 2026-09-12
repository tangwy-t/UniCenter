// Package thumb 提供文件管理所需的图片缩略图生成。
//
// 设计取舍:零第三方依赖(仅 image/... 标准库)+ 两级降采样
// (迭代 2×2 盒式滤波 → 双线性插值),几百 KB 的小图毫秒级完成,
// 大图(数千万像素)在几十毫秒内,足够列表缩略图使用。
// 仅支持标准库可解码的格式(JPEG/PNG/GIF 首帧),其余格式由调用方
// 回退为图标展示。
package thumb

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"

	// 注册标准库解码器:image.Decode 按注册格式探测。
	_ "image/gif"
	_ "image/png"
)

// MaxSourcePixels 限制解码前的像素总数(宽×高)上限,防止超大图
// 在缩略图生成时耗尽内存。约合 5000×4000 的图片,对文件管理场景足够。
const MaxSourcePixels = 25_000_000

// Invalid 表示源数据不是可解码的图片(错误 → 调用方回退图标),与
// 系统错误(读取失败)语义分开。
type Invalid struct{ err error }

func (e Invalid) Error() string { return fmt.Sprintf("缩略图源数据不可解码: %v", e.err) }

// Generate 从 r(可回跳)读取图片并生成最长边不超过 maxDim 的 JPEG 缩略图。
// maxDim 会被收敛到 [64, 512];源图小于该尺寸时不做放大。
// r 必须是 io.ReadSeeker:内部先读头部检查尺寸,再回跳全量解码。
func Generate(r io.ReadSeeker, maxDim int) ([]byte, error) {
	if maxDim < 64 {
		maxDim = 64
	}
	if maxDim > 512 {
		maxDim = 512
	}

	// 先解析头部(不全量解码):超像素上限的图直接拒绝,避免内存风险。
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return nil, Invalid{err}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, Invalid{fmt.Errorf("非法尺寸 %dx%d", cfg.Width, cfg.Height)}
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxSourcePixels {
		return nil, Invalid{fmt.Errorf("分辨率过大(%dx%d)", cfg.Width, cfg.Height)}
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// 重新解码(DecodeConfig 已消费 reader 头部)。
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, Invalid{err}
	}

	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()

	// 目标:最长边 = maxDim,保持宽高比,不做放大。
	scale := 1.0
	if srcW > srcH {
		if srcW > maxDim {
			scale = float64(maxDim) / float64(srcW)
		}
	} else if srcH > maxDim {
		scale = float64(maxDim) / float64(srcH)
	}
	dstW := max(1, int(float64(srcW)*scale))
	dstH := max(1, int(float64(srcH)*scale))
	if dstW == srcW && dstH == srcH {
		// 无需缩放:直接转换编码,保证输出为统一 JPEG。
		return encodeJPEG(toRGBA(img))
	}

	// 第一级:2×2 盒式降采样,直到尺寸落在目标的 2× 以内。
	rgb := toRGBA(img)
	for rgb.Bounds().Dx() > dstW*2 || rgb.Bounds().Dy() > dstH*2 {
		rgb = half(rgb)
	}

	// 第二级:双线性插值至精确目标尺寸。
	out := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	rw, rh := rgb.Bounds().Dx(), rgb.Bounds().Dy()
	for y := 0; y < dstH; y++ {
		sy := (float64(y) + 0.5) * float64(rh) / float64(dstH)
		for x := 0; x < dstW; x++ {
			sx := (float64(x) + 0.5) * float64(rw) / float64(dstW)
			out.SetRGBA(x, y, bilinear(rgb, sx, sy))
		}
	}
	return encodeJPEG(out)
}

// toRGBA 将任意 image.Image 归一化为 RGBA(直接绘制,忽略底层编码格式差异)。
func toRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	return rgba
}

// half 做 2×2 盒式滤波降采样(尺寸减半,奇数边向下取整),利用整数
// 累加避免浮点开销;循环执行到接近目标尺寸。
func half(src *image.RGBA) *image.RGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := max(1, w/2), max(1, h/2)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, b, a, n uint32
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					sx, sy := x*2+dx, y*2+dy
					if sx >= w || sy >= h {
						continue
					}
					c := src.RGBAAt(sx, sy)
					r += uint32(c.R)
					g += uint32(c.G)
					b += uint32(c.B)
					a += uint32(c.A)
					n++
				}
			}
			dst.SetRGBA(x, y, colorRGBAFrom(r/n, g/n, b/n, a/n))
		}
	}
	return dst
}

// bilinear 在 (sx, sy) 处做双线性采样(坐标已位于图像内部)。
func bilinear(src *image.RGBA, sx, sy float64) (c color.RGBA) {
	x0, y0 := int(sx), int(sy)
	fx, fy := sx-float64(x0), sy-float64(y0)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)

	c00, c01 := src.RGBAAt(x0, y0), src.RGBAAt(x0, y1)
	c10, c11 := src.RGBAAt(x1, y0), src.RGBAAt(x1, y1)

	return colorRGBAFrom(
		mix(mix(uint32(c00.R), uint32(c10.R), fx), mix(uint32(c01.R), uint32(c11.R), fx), fy),
		mix(mix(uint32(c00.G), uint32(c10.G), fx), mix(uint32(c01.G), uint32(c11.G), fx), fy),
		mix(mix(uint32(c00.B), uint32(c10.B), fx), mix(uint32(c01.B), uint32(c11.B), fx), fy),
		mix(mix(uint32(c00.A), uint32(c10.A), fx), mix(uint32(c01.A), uint32(c11.A), fx), fy),
	)
}

// mix 线性插值 a→b(权重 t∈[0,1],float64)。
func mix(a, b uint32, t float64) uint32 {
	return uint32(float64(a) + (float64(b)-float64(a))*t)
}

func colorRGBAFrom(r, g, b, a uint32) (c color.RGBA) {
	c.R, c.G, c.B, c.A = uint8(r), uint8(g), uint8(b), uint8(a)
	return c
}

// encodeJPEG 以质量 85 编码 JPEG(缩略图观感与体积的平衡点)。
// JPEG 无 alpha 通道:透明像素先平铺到白底,避免缩略图出现黑底。
func encodeJPEG(img image.Image) ([]byte, error) {
	rgba := toRGBA(img)
	pix := rgba.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		a := uint32(pix[i+3])
		pix[i] = uint8((uint32(pix[i])*a + 255*(255-a)) / 255)
		pix[i+1] = uint8((uint32(pix[i+1])*a + 255*(255-a)) / 255)
		pix[i+2] = uint8((uint32(pix[i+2])*a + 255*(255-a)) / 255)
		pix[i+3] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
