package local

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPublishNoReplaceConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first")
	secondPath := filepath.Join(dir, "second")
	destination := filepath.Join(dir, "published")
	if err := os.WriteFile(firstPath, []byte("first bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second bytes"), 0600); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	type result struct {
		published bool
		err       error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, source := range []string{firstPath, secondPath} {
		wg.Add(1)
		go func(source string) {
			defer wg.Done()
			<-start
			published, err := publishNoReplace(source, destination)
			results <- result{published: published, err: err}
		}(source)
	}
	close(start)
	wg.Wait()
	close(results)

	publishedCount := 0
	for res := range results {
		if res.err != nil {
			t.Fatalf("publish failed: %v", res.err)
		}
		if res.published {
			publishedCount++
		}
	}
	if publishedCount != 1 {
		t.Fatalf("exactly one writer should publish without replacement, got %d", publishedCount)
	}

	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("published destination is missing: %v", err)
	}
	if string(data) != "first bytes" && string(data) != "second bytes" {
		t.Fatalf("destination contains unexpected bytes: %q", data)
	}
}
