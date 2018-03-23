package hashgrouper

import (
	"bytes"
	"github.com/tobgu/qframe/internal/column"
	"github.com/tobgu/qframe/internal/index"
)

/*
This package implements a basic hash table used for GroupBy and Distinct operations.

Hashing is done using murmur 3, collisions are handled using linear probing.

When the table reaches a certain load factor it will be reallocated into a new, larger table.
*/

// An entry in the hash table. For group by operations a slice of all positions each group
// are stored. For distinct operations only the first position is stored to avoid some overhead.
type tableEntry struct {
	ix       index.Int
	hash     uint32
	firstPos uint32
	occupied bool
}

type table struct {
	entries       []tableEntry
	occupiedCount int
	comparables   []column.Comparable
	stats         GroupStats
	hashBuf       *bytes.Buffer
	groupCount    uint32
	collectIx     bool
}

func (t *table) loadFactor() float64 {
	return float64(t.groupCount) / float64(len(t.entries))
}

func (t *table) grow() {
	newLen := uint32(2 * len(t.entries))
	newEntries := make([]tableEntry, newLen)
	for _, e := range t.entries {
		for pos := e.hash % newLen; ; pos = (pos + 1) % newLen {
			if !newEntries[pos].occupied {
				newEntries[pos] = e
				break
			}
			t.stats.RelocationCollisions++
		}
	}

	t.stats.RelocationCount++
	t.entries = newEntries
}

func (t *table) hash(i uint32) uint32 {
	t.hashBuf.Reset()
	for _, c := range t.comparables {
		c.HashBytes(i, t.hashBuf)
	}

	return murmur32(t.hashBuf.Bytes())
}

const maxLoadFactor = 0.5

func (t *table) insertEntry(i uint32) {
	if t.loadFactor() > maxLoadFactor {
		t.grow()
	}

	hash := t.hash(i)
	entriesLen := uint64(len(t.entries))
	startPos := uint64(hash) % entriesLen
	var dstEntry *tableEntry
	for pos := startPos; dstEntry == nil; pos = (pos + 1) % entriesLen {
		e := &t.entries[pos]
		if !e.occupied || e.hash == hash && equals(t.comparables, i, e.firstPos) {
			dstEntry = e
		} else {
			t.stats.InsertCollisions++
		}
	}

	// Update entry
	if !dstEntry.occupied {
		// Eden entry
		dstEntry.hash = hash
		dstEntry.firstPos = i
		dstEntry.occupied = true
		t.groupCount++
	} else {
		// Existing entry
		if t.collectIx {
			// Small hack to reduce number of allocations under some circumstances. Delay
			// creation of index slice until there are at least two entries in the group
			// since we store the first position in a separate variable on the entry anyway.
			if dstEntry.ix == nil {
				dstEntry.ix = index.Int{dstEntry.firstPos, i}
			} else {
				dstEntry.ix = append(dstEntry.ix, i)
			}
		}
	}
}

func newTable(size int, comparables []column.Comparable, collectIx bool) *table {
	return &table{entries: make([]tableEntry, size), comparables: comparables, collectIx: collectIx, hashBuf: new(bytes.Buffer)}
}

func equals(comparables []column.Comparable, i, j uint32) bool {
	for _, c := range comparables {
		if c.Compare(i, j) != column.Equal {
			return false
		}
	}
	return true
}

type GroupStats struct {
	RelocationCount      int
	RelocationCollisions int
	InsertCollisions     int
	GroupCount           int
	LoadFactor           float64
}

func groupIndex(ix index.Int, comparables []column.Comparable, collectIx bool) ([]tableEntry, GroupStats) {
	// Initial size is picked fairly arbitrarily at the moment
	initialSize := (len(ix) / 4) + 10
	table := newTable(initialSize, comparables, collectIx)
	for _, i := range ix {
		table.insertEntry(i)
	}

	stats := table.stats
	stats.LoadFactor = table.loadFactor()
	stats.GroupCount = int(table.groupCount)
	return table.entries, stats
}

func GroupBy(ix index.Int, comparables []column.Comparable) ([]index.Int, GroupStats) {
	entries, stats := groupIndex(ix, comparables, true)
	result := make([]index.Int, 0, stats.GroupCount)
	for _, e := range entries {
		if e.occupied {
			if e.ix == nil {
				result = append(result, index.Int{e.firstPos})
			} else {
				result = append(result, e.ix)
			}
		}
	}

	return result, stats
}

func Distinct(ix index.Int, comparables []column.Comparable) index.Int {
	entries, stats := groupIndex(ix, comparables, false)
	result := make(index.Int, 0, stats.GroupCount)
	for _, e := range entries {
		if e.occupied {
			result = append(result, e.firstPos)
		}
	}

	return result
}
