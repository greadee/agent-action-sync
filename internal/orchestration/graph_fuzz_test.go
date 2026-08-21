package orchestration

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func FuzzGraphValidationAndReadiness(f *testing.F) {
	f.Add([]byte{1, 0})
	f.Add([]byte{4, 0x5a, 0xa5})
	f.Add([]byte{8, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) == 0 {
			raw = []byte{1}
		}
		nodeCount := int(raw[0]%8) + 1
		dependencies := make(map[string][]string, nodeCount)
		for index := 0; index < nodeCount; index++ {
			id := "wp-" + string(rune('a'+index))
			dependencies[id] = nil
			for predecessor := 0; predecessor < index; predecessor++ {
				bit := index*nodeCount + predecessor + 1
				if raw[bit%len(raw)]&(1<<uint(bit%8)) != 0 {
					dependencies[id] = append(dependencies[id], "wp-"+string(rune('a'+predecessor)))
				}
			}
		}
		graph := testGraph(t, dependencies, nil)
		if err := ValidateGraph(context.Background(), graph); err != nil {
			t.Fatalf("valid generated graph: %v", err)
		}
		events := createdEvents(graph, time.Unix(500, 0).UTC())
		first, err := ReduceReadiness(context.Background(), graph, events)
		if err != nil {
			t.Fatalf("first readiness: %v", err)
		}
		for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
			events[left], events[right] = events[right], events[left]
		}
		second, err := ReduceReadiness(context.Background(), graph, events)
		if err != nil {
			t.Fatalf("replayed readiness: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("readiness changed after event enumeration changed")
		}
	})
}
