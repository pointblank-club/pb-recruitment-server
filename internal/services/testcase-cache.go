package services

import (
	"context"
	"path"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

func testcaseAnswersKey(testcasesKey string) string {
	return path.Join(path.Dir(testcasesKey), "answers.json")
}

type testcaseEntry struct {
	inputs, outputs []string
	loaded          time.Time
	bytes           int
}

type testcaseCache struct {
	mu      sync.Mutex
	entries map[string]testcaseEntry
	bytes   int
	loads   singleflight.Group
}

const testcaseCacheBytes = 32 << 20

func (c *testcaseCache) load(ctx context.Context, key string, fetch func(context.Context) ([]string, []string, error)) ([]string, []string, error) {
	result := c.loads.DoChan(key, func() (any, error) {
		c.mu.Lock()
		entry, ok := c.entries[key]
		c.mu.Unlock()
		if ok && time.Since(entry.loaded) < 5*time.Minute {
			return entry, nil
		}
		inputs, outputs, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		entry = testcaseEntry{inputs: inputs, outputs: outputs, loaded: time.Now(), bytes: len(key) + len(inputs)*32}
		for i := range inputs {
			entry.bytes += len(inputs[i]) + len(outputs[i])
		}
		if entry.bytes <= testcaseCacheBytes {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.entries == nil {
				c.entries = make(map[string]testcaseEntry)
			}
			if old, ok := c.entries[key]; ok {
				c.bytes -= old.bytes
				delete(c.entries, key)
			}
			for len(c.entries) >= 64 || c.bytes+entry.bytes > testcaseCacheBytes {
				var oldest string
				for k, value := range c.entries {
					if oldest == "" || value.loaded.Before(c.entries[oldest].loaded) {
						oldest = k
					}
				}
				c.bytes -= c.entries[oldest].bytes
				delete(c.entries, oldest)
			}
			c.entries[key], c.bytes = entry, c.bytes+entry.bytes
		}
		return entry, nil
	})
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, nil, r.Err
		}
		entry := r.Val.(testcaseEntry)
		return entry.inputs, entry.outputs, nil
	}
}
