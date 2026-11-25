package process

import (
	"fmt"
	"testing"
)

// BenchmarkRouteOrphans is the orphan reaper's Route for a pid nobody
// registered, with the pending table already holding pre entries (it
// fills to pendingMax under an orphan storm).
func BenchmarkRouteOrphans(b *testing.B) {
	for _, pre := range []int{0, 100, 1023, 2000} {
		b.Run(fmt.Sprint(pre), func(b *testing.B) {
			r := NewExitRouter()
			for i := 0; i < pre; i++ {
				r.Route(1000000+i, 0)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.Route(2000000+i, 0)
			}
		})
	}
}

// BenchmarkRouteManaged is a managed child's Register, Route, Unregister.
func BenchmarkRouteManaged(b *testing.B) {
	r := NewExitRouter()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Register(i)
		r.Route(i, 0)
		r.Unregister(i)
	}
}
