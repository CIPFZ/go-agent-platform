package screen

import (
	"image"
	"image/color"
	"sync"
)

// arrowShape is a classic 12x19 pointer: 'X' outline, '.' fill.
var arrowShape = []string{
	"X",
	"XX",
	"X.X",
	"X..X",
	"X...X",
	"X....X",
	"X.....X",
	"X......X",
	"X.......X",
	"X........X",
	"X.........X",
	"X..........X",
	"X......XXXXX",
	"X...X..X",
	"X..XX..X",
	"X.X  X..X",
	"XX   X..X",
	"X     X..X",
	"      XXXX",
}

var (
	arrowOnce sync.Once
	arrow     *CursorImage
)

// DefaultArrow is the built-in pointer drawn when the platform cursor shape
// cannot be read (spec section 10).
func DefaultArrow() *CursorImage {
	arrowOnce.Do(func() {
		img := image.NewRGBA(image.Rect(0, 0, 12, len(arrowShape)))
		for y, row := range arrowShape {
			for x, ch := range row {
				switch ch {
				case 'X':
					img.SetRGBA(x, y, color.RGBA{0, 0, 0, 255})
				case '.':
					img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
				}
			}
		}
		arrow = &CursorImage{Image: img}
	})
	return arrow
}
