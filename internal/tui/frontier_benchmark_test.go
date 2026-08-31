package tui

import (
	"os"
	"runtime"
	"strconv"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

const frontierBenchmarkWidth = 120

// BenchmarkFrontierLayout measures the complete materialised layout. The hostile
// grid grows quickly: N=100/200/500 are approximately 97 MB/392 MB/2.46 GB.
// Those registrations require SITREP_BENCH_FRONTIER_HOSTILE_LARGE=1 before
// fixture or layout materialisation; ordinary benchmark and CI invocations skip
// them safely.
func BenchmarkFrontierLayout(b *testing.B) {
	for _, shape := range []string{"chain", "hub", "hostile"} {
		for _, n := range []int{50, 100, 200, 500} {
			b.Run(shape+"/N="+strconv.Itoa(n), func(b *testing.B) {
				if shape == "hostile" && n > 50 && os.Getenv("SITREP_BENCH_FRONTIER_HOSTILE_LARGE") != "1" {
					b.Skip("set SITREP_BENCH_FRONTIER_HOSTILE_LARGE=1 on a suitably provisioned host")
				}
				tickets, links := benchmarkFrontierFixture(shape, n)
				graph := model.BuildBlockingGraph(tickets, links, model.Capabilities{BlockingLinks: true})
				nodes := frontierNodes(graph, tickets, true)
				b.ReportAllocs()
				b.ResetTimer()
				var layout frontierLayout
				for range b.N {
					layout = layoutFrontier(graph, nodes, frontierLayoutOptions{
						innerWidth: frontierBenchmarkWidth,
						direction:  frontierRanksHorizontal,
					})
				}
				runtime.KeepAlive(layout)
			})
		}
	}
}

func benchmarkFrontierFixture(shape string, n int) ([]model.Ticket, map[model.TicketID][]model.Link) {
	tickets := make([]model.Ticket, n)
	links := make(map[model.TicketID][]model.Link, n)
	for i := range tickets {
		id := model.TicketID("B-" + strconv.Itoa(i))
		tickets[i] = model.Ticket{ID: id, Key: string(id), Title: "benchmark ticket " + strconv.Itoa(i), Status: model.StatusTodo}
		if i == 0 {
			links[id] = nil
			continue
		}
		switch shape {
		case "chain":
			links[id] = blockedBy("B-" + strconv.Itoa(i-1))
		case "hub":
			links[id] = blockedBy("B-0")
		case "hostile":
			blockers := []string{"B-0"}
			if i > 1 {
				blockers = append(blockers, "B-"+strconv.Itoa(i-1))
			}
			links[id] = blockedBy(blockers...)
		}
	}
	return tickets, links
}
