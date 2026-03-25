package performance

import (
	"fmt"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// BenchmarkServiceSetAdd measures adding services to a ServiceSet.
func BenchmarkServiceSetAdd(b *testing.B) {
	for _, count := range []int{10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("services_%d", count), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				logger := logging.New(logging.LevelError)
				set := service.NewServiceSet(logger)
				for j := 0; j < count; j++ {
					svc := service.NewInternalService(set, fmt.Sprintf("svc-%04d", j))
					set.AddService(svc)
				}
			}
		})
	}
}

// BenchmarkServiceSetFind measures service lookup by name.
func BenchmarkServiceSetFind(b *testing.B) {
	for _, count := range []int{10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("services_%d", count), func(b *testing.B) {
			logger := logging.New(logging.LevelError)
			set := service.NewServiceSet(logger)
			for j := 0; j < count; j++ {
				svc := service.NewInternalService(set, fmt.Sprintf("svc-%04d", j))
				set.AddService(svc)
			}

			target := fmt.Sprintf("svc-%04d", count/2)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				set.FindService(target, false)
			}
		})
	}
}

// BenchmarkDependencyChain measures starting a service with a deep dependency chain.
func BenchmarkDependencyChain(b *testing.B) {
	for _, depth := range []int{5, 10, 25, 50} {
		b.Run(fmt.Sprintf("depth_%d", depth), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				logger := logging.New(logging.LevelError)
				set := service.NewServiceSet(logger)

				var prev service.Service
				for j := 0; j < depth; j++ {
					svc := service.NewInternalService(set, fmt.Sprintf("chain-%04d", j))
					set.AddService(svc)
					if prev != nil {
						svc.Record().AddDep(prev, service.DepWaitsFor)
					}
					prev = svc
				}
				prev.Record().Start()
				set.ProcessQueues()
			}
		})
	}
}

// BenchmarkDependencyFanOut measures starting a service that depends on N siblings.
func BenchmarkDependencyFanOut(b *testing.B) {
	for _, fanout := range []int{5, 10, 25, 50, 100} {
		b.Run(fmt.Sprintf("fanout_%d", fanout), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				logger := logging.New(logging.LevelError)
				set := service.NewServiceSet(logger)

				root := service.NewInternalService(set, "root")
				set.AddService(root)

				for j := 0; j < fanout; j++ {
					leaf := service.NewInternalService(set, fmt.Sprintf("leaf-%04d", j))
					set.AddService(leaf)
					root.Record().AddDep(leaf, service.DepWaitsFor)
				}

				root.Record().Start()
				set.ProcessQueues()
			}
		})
	}
}

// BenchmarkServiceLoad measures end-to-end config loading via DirLoader.
func BenchmarkServiceLoad(b *testing.B) {
	for _, count := range []int{10, 50, 100, 500} {
		b.Run(fmt.Sprintf("services_%d", count), func(b *testing.B) {
			dir := b.TempDir()
			// Fan-out: root depends on all others (avoids depth limit)
			for j := 0; j < count; j++ {
				writeServiceFile(dir, fmt.Sprintf("svc-%04d", j), "type = internal\n")
			}
			// Root service waits-for all others
			rootContent := "type = internal\n"
			for j := 0; j < count; j++ {
				rootContent += fmt.Sprintf("waits-for: svc-%04d\n", j)
			}
			writeServiceFile(dir, "root", rootContent)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				logger := logging.New(logging.LevelError)
				set := service.NewServiceSet(logger)
				loader := config.NewDirLoader(set, []string{dir})
				set.SetLoader(loader)

				_, err := loader.LoadService("root")
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkProcessQueues measures queue processing with many pending services.
func BenchmarkProcessQueues(b *testing.B) {
	for _, count := range []int{10, 50, 100, 500} {
		b.Run(fmt.Sprintf("services_%d", count), func(b *testing.B) {
			logger := logging.New(logging.LevelError)
			set := service.NewServiceSet(logger)

			for j := 0; j < count; j++ {
				svc := service.NewInternalService(set, fmt.Sprintf("svc-%04d", j))
				set.AddService(svc)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, svc := range set.ListServices() {
					svc.Record().Start()
				}
				set.ProcessQueues()
			}
		})
	}
}
