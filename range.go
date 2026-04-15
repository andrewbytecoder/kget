package pget

import "fmt"

// Range represents a byte range [low, high] inclusive.
type Range struct {
	low  int64
	high int64
}

func (r Range) BytesRange() string {
	return fmt.Sprintf("bytes=%d-%d", r.low, r.high)
}

