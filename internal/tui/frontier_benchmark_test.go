package tui

import (
	"runtime"
	"strconv"
	"testing"

	"github.com/niekcandaele/sitrep/internal/model"
)

const frontierBenchmarkWidth = 120

// BenchmarkFrontierLayout measures admitted safe materialisation only. Hostile
// fixtures belong to the bounded projection benchmark below and never reach the
// private materialiser.
func BenchmarkFrontierLayout(b *testing.B) {
	for _, shape := range []string{"chain", "hub"} {
		for _, n := range []int{50, 100, 200, 500} {
			b.Run(shape+"/N="+strconv.Itoa(n), func(b *testing.B) {
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
				b.ReportMetric(float64(len(layout.routes)), "routes")
				b.ReportMetric(float64(len(layout.strokes)), "stroke_slots")
				runtime.KeepAlive(layout)
			})
		}
	}
}

// BenchmarkFrontierProjectionRefusal measures hostile production policy. N=50
// already exceeds the hard ceiling; every larger fixture remains projection-only
// and retains no grid, route, incident, or stroke metadata.
func BenchmarkFrontierProjectionRefusal(b *testing.B) {
	for _, n := range []int{50, 100, 200, 500, 1000} {
		b.Run("hostile/N="+strconv.Itoa(n), func(b *testing.B) {
			tickets, links := benchmarkFrontierFixture("hostile", n)
			graph := model.BuildBlockingGraph(tickets, links, model.Capabilities{BlockingLinks: true})
			nodes := frontierNodes(graph, tickets, true)
			probe := projectFrontierRanks(graph, nodes, frontierBenchmarkWidth)
			direction := chooseFrontierDirection(probe.candidates, frontierBenchmarkWidth, 30)
			if refusal := refuseFrontierCandidate(probe.candidates[direction], len(nodes), len(graph.Ghosts())); refusal == nil {
				b.Fatalf("hostile N=%d selected an admitted %dx%d candidate", n,
					probe.candidates[direction].width, probe.candidates[direction].height)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var projection frontierRankProjection
			var refusal *frontierCanvasRefusal
			for range b.N {
				projection = projectFrontierRanks(graph, nodes, frontierBenchmarkWidth)
				direction = chooseFrontierDirection(projection.candidates, frontierBenchmarkWidth, 30)
				refusal = refuseFrontierCandidate(projection.candidates[direction], len(nodes), len(graph.Ghosts()))
			}
			selected := projection.candidates[direction]
			if cells, ok := selected.projectedCells(); ok {
				b.ReportMetric(float64(cells), "projected_cells")
			}
			runtime.KeepAlive(projection)
			runtime.KeepAlive(refusal)
		})
	}
}

func BenchmarkFrontierFocusedRender(b *testing.B) {
	for _, n := range []int{50, 100, 200, 500} {
		b.Run("chain-two-incident/N="+strconv.Itoa(n), func(b *testing.B) {
			tickets, links := benchmarkFrontierFixture("chain", n)
			graph := model.BuildBlockingGraph(tickets, links, model.Capabilities{BlockingLinks: true})
			layout := layoutFrontier(graph, frontierNodes(graph, tickets, true), frontierLayoutOptions{
				innerWidth: frontierBenchmarkWidth,
				direction:  frontierRanksHorizontal,
			})
			focus := model.TicketID("B-" + strconv.Itoa(n/2))
			if got := len(layout.incident[focus]); got != 2 {
				b.Fatalf("focused route count = %d, want 2", got)
			}
			const height = 30
			rect := layout.nodeAt[focus]
			offsetX, offsetY := ensureNodeVisible(rect, 0, 0, frontierBenchmarkWidth, height)
			offsetX = clampFrontierOffset(offsetX, layout.width, frontierBenchmarkWidth)
			offsetY = clampFrontierOffset(offsetY, layout.height, height)
			styles := DefaultStyles(true)
			b.ReportAllocs()
			b.ResetTimer()
			var lines []string
			for range b.N {
				lines = renderFrontierCanvas(layout, focus, true, offsetX, offsetY,
					frontierBenchmarkWidth, height, styles)
			}
			runtime.KeepAlive(lines)
		})
	}
}

func BenchmarkFrontierFocusedHubOverlay(b *testing.B) {
	for _, n := range []int{50, 100, 200, 500} {
		b.Run("high-degree/N="+strconv.Itoa(n), func(b *testing.B) {
			tickets, links := benchmarkFrontierFixture("hub", n)
			graph := model.BuildBlockingGraph(tickets, links, model.Capabilities{BlockingLinks: true})
			layout := layoutFrontier(graph, frontierNodes(graph, tickets, true), frontierLayoutOptions{
				innerWidth: frontierBenchmarkWidth,
				direction:  frontierRanksHorizontal,
			})
			focus := model.TicketID("B-0")
			if got := len(layout.incident[focus]); got != n-1 {
				b.Fatalf("hub incident route count = %d, want %d", got, n-1)
			}
			const height = 30
			rect := layout.nodeAt[focus]
			offsetX, offsetY := ensureNodeVisible(rect, 0, 0, frontierBenchmarkWidth, height)
			offsetX = clampFrontierOffset(offsetX, layout.width, frontierBenchmarkWidth)
			offsetY = clampFrontierOffset(offsetY, layout.height, height)
			clip := frontierRect{X: offsetX, Y: offsetY, W: frontierBenchmarkWidth, H: height}
			visibleCards := len(visibleFrontierCardRects(layout, clip))
			b.ReportAllocs()
			b.ResetTimer()
			var overlay map[[2]int]frontierCell
			for range b.N {
				overlay = focusedFrontierOverlay(layout, focus, offsetX, offsetY,
					frontierBenchmarkWidth, height)
			}
			b.ReportMetric(float64(len(layout.incident[focus])), "incident_routes")
			b.ReportMetric(float64(visibleCards), "visible_cards")
			runtime.KeepAlive(overlay)
		})
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
