package repository

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReplaceDestinationAtomicReaderVisibility(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "destination")
	oldValue := []byte("old-complete-content")
	newValue := []byte("new-complete-content")
	if err := os.WriteFile(destination, oldValue, 0600); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var stopOnce sync.Once
	readErrs := make(chan error, 1)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := os.ReadFile(destination)
				if err != nil {
					select {
					case readErrs <- err:
					default:
					}
					return
				}
				if !equalReplacementBytes(data, oldValue) && !equalReplacementBytes(data, newValue) {
					select {
					case readErrs <- errors.New("reader observed partial destination contents"):
					default:
					}
					return
				}
			}
		}()
	}
	t.Cleanup(func() {
		stopOnce.Do(func() { close(stop) })
		wg.Wait()
	})

	for i := 0; i < 200; i++ {
		value := oldValue
		if i%2 == 0 {
			value = newValue
		}
		temp, err := os.CreateTemp(dir, ".nando-get-test-*")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := temp.Write(value); err != nil {
			temp.Close()
			t.Fatal(err)
		}
		if err := temp.Sync(); err != nil {
			temp.Close()
			t.Fatal(err)
		}
		if err := temp.Close(); err != nil {
			t.Fatal(err)
		}
		if err := replaceDestination(temp.Name(), destination, true); err != nil {
			t.Fatalf("replace destination iteration %d: %v", i, err)
		}
	}
	stopOnce.Do(func() { close(stop) })
	wg.Wait()
	select {
	case err := <-readErrs:
		t.Fatal(err)
	default:
	}
}

func equalReplacementBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
