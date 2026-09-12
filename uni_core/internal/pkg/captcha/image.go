package captcha

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/rand/v2"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	imgWidth  = 200
	imgHeight = 80
)

var (
	once      sync.Once
	goFont    *opentype.Font
	goFontErr error
)

// generateMathQuestion creates a random math question (addition or subtraction
// of two integers in [1,20]) and returns the question string and expected answer.
// Subtraction results are guaranteed to be >= 0.
func generateMathQuestion() (question string, answer int) {
	a := rand.IntN(20) + 1
	b := rand.IntN(20) + 1
	if rand.IntN(2) == 0 {
		// addition
		return fmt.Sprintf("%d + %d = ?", a, b), a + b
	}
	// subtraction: ensure result >= 0
	if a < b {
		a, b = b, a
	}
	return fmt.Sprintf("%d - %d = ?", a, b), a - b
}

// renderImage renders the math question as a 200x80 PNG image with noise
// and returns the raw PNG bytes.
func renderImage(question string) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, imgWidth, imgHeight))

	// background
	bgColor := color.RGBA{R: 0xF0, G: 0xF0, B: 0xF0, A: 0xFF}
	draw.Draw(img, img.Bounds(), &image.Uniform{bgColor}, image.Point{}, draw.Src)

	// draw random noise dots
	for i := 0; i < 50; i++ {
		x := rand.IntN(imgWidth)
		y := rand.IntN(imgHeight)
		img.Set(x, y, color.RGBA{R: uint8(rand.IntN(256)), G: uint8(rand.IntN(256)), B: uint8(rand.IntN(256)), A: 0xFF})
	}

	// draw random interference lines
	lineColor := color.RGBA{R: uint8(rand.IntN(200)), G: uint8(rand.IntN(200)), B: uint8(rand.IntN(200)), A: 0xFF}
	for i := 0; i < rand.IntN(3)+3; i++ {
		x1 := rand.IntN(imgWidth)
		y1 := rand.IntN(imgHeight)
		x2 := rand.IntN(imgWidth)
		y2 := rand.IntN(imgHeight)
		drawLine(img, x1, y1, x2, y2, lineColor)
	}

	// draw math question text centered
	textColor := color.RGBA{R: 0x33, G: 0x33, B: 0x33, A: 0xFF}
	fontSize := 24.0
	once.Do(func() {
		goFont, goFontErr = opentype.Parse(gobold.TTF)
	})
	if goFontErr != nil {
		return nil, fmt.Errorf("Captcha font parse error: %w", goFontErr)
	}
	face, err := opentype.NewFace(goFont, &opentype.FaceOptions{
		Size: fontSize,
		DPI:  72,
	})
	if err != nil {
		return nil, err
	}
	defer face.Close()
	drawer := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{textColor},
		Face: face,
	}
	// Measure text width to center
	textWidth := drawer.MeasureString(question).Ceil()
	x := (imgWidth - textWidth) / 2
	y := imgHeight/2 + int(fontSize/3)
	drawer.Dot = fixed.P(x, y)
	drawer.DrawString(question)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawLine draws a line on the image using Bresenham's algorithm.
func drawLine(img *image.RGBA, x1, y1, x2, y2 int, c color.RGBA) {
	dx := abs(x2 - x1)
	dy := -abs(y2 - y1)
	sx := 1
	sy := 1
	if x1 >= x2 {
		sx = -1
	}
	if y1 >= y2 {
		sy = -1
	}
	err := dx + dy
	for {
		if x1 >= 0 && x1 < imgWidth && y1 >= 0 && y1 < imgHeight {
			img.Set(x1, y1, c)
		}
		if x1 == x2 && y1 == y2 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			if x1 == x2 {
				break
			}
			err += dy
			x1 += sx
		}
		if e2 <= dx {
			if y1 == y2 {
				break
			}
			err += dx
			y1 += sy
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
