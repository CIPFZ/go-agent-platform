package protocol

// Point is a position in physical pixels of the virtual desktop, whose origin
// is the top-left corner of the primary display (spec D1, section 6.2).
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Rect is a rectangle in the same coordinate system as Point.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Empty reports whether r has no area.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Intersect returns the largest rectangle contained in both r and s.
func (r Rect) Intersect(s Rect) Rect {
	x0, y0 := max(r.X, s.X), max(r.Y, s.Y)
	x1, y1 := min(r.X+r.W, s.X+s.W), min(r.Y+r.H, s.Y+s.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// Union returns the smallest rectangle containing r and s.
func (r Rect) Union(s Rect) Rect {
	if r.Empty() {
		return s
	}
	if s.Empty() {
		return r
	}
	x0, y0 := min(r.X, s.X), min(r.Y, s.Y)
	x1, y1 := max(r.X+r.W, s.X+s.W), max(r.Y+r.H, s.Y+s.H)
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// Contains reports whether p lies inside r.
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.X < r.X+r.W && p.Y >= r.Y && p.Y < r.Y+r.H
}
