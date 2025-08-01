// Copyright 2017 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package index

import (
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	tsdb_enc "github.com/prometheus/prometheus/tsdb/encoding"
	"github.com/prometheus/prometheus/util/testutil"

	"github.com/grafana/loki/v3/pkg/util/encoding"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type series struct {
	l      labels.Labels
	chunks []ChunkMeta
}

type mockIndex struct {
	series   map[storage.SeriesRef]series
	postings map[labels.Label][]storage.SeriesRef
	symbols  map[string]struct{}
}

func newMockIndex() mockIndex {
	ix := mockIndex{
		series:   make(map[storage.SeriesRef]series),
		postings: make(map[labels.Label][]storage.SeriesRef),
		symbols:  make(map[string]struct{}),
	}
	ix.postings[allPostingsKey] = []storage.SeriesRef{}
	return ix
}

func (m mockIndex) Symbols() (map[string]struct{}, error) {
	return m.symbols, nil
}

func (m mockIndex) AddSeries(ref storage.SeriesRef, l labels.Labels, chunks ...ChunkMeta) error {
	if _, ok := m.series[ref]; ok {
		return errors.Errorf("series with reference %d already added", ref)
	}
	l.Range(func(lbl labels.Label) {
		m.symbols[lbl.Name] = struct{}{}
		m.symbols[lbl.Value] = struct{}{}
		if _, ok := m.postings[lbl]; !ok {
			m.postings[lbl] = []storage.SeriesRef{}
		}
		m.postings[lbl] = append(m.postings[lbl], ref)
	})
	m.postings[allPostingsKey] = append(m.postings[allPostingsKey], ref)

	s := series{l: l}
	// Actual chunk data is not stored in the index.
	s.chunks = append(s.chunks, chunks...)
	m.series[ref] = s

	return nil
}

func (m mockIndex) Close() error {
	return nil
}

func (m mockIndex) LabelValues(name string) ([]string, error) {
	values := []string{}
	for l := range m.postings {
		if l.Name == name {
			values = append(values, l.Value)
		}
	}
	return values, nil
}

func (m mockIndex) Postings(name string, values ...string) (Postings, error) {
	p := []Postings{}
	for _, value := range values {
		l := labels.Label{Name: name, Value: value}
		p = append(p, NewListPostings(m.postings[l]))
	}
	return Merge(p...), nil
}

func (m mockIndex) Series(ref storage.SeriesRef, lset *labels.Labels, chks *[]ChunkMeta) error {
	s, ok := m.series[ref]
	if !ok {
		return errors.New("not found")
	}

	lset.CopyFrom(s.l)
	*chks = append((*chks)[:0], s.chunks...)

	return nil
}

func TestIndexRW_Create_Open(t *testing.T) {
	dir := t.TempDir()

	fn := filepath.Join(dir, IndexFilename)

	// An empty index must still result in a readable file.
	iw, err := NewWriter(context.Background(), FormatV3, fn)
	require.NoError(t, err)
	_, err = iw.Close(false)
	require.NoError(t, err)

	ir, err := NewFileReader(fn)
	require.NoError(t, err)
	require.NoError(t, ir.Close())

	// Modify magic header must cause open to fail.
	f, err := os.OpenFile(fn, os.O_WRONLY, 0o666)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte{0, 0}, 0)
	require.NoError(t, err)
	f.Close()

	_, err = NewFileReader(dir)
	require.Error(t, err)
}

func TestIndexRW_Postings(t *testing.T) {
	dir := t.TempDir()

	fn := filepath.Join(dir, IndexFilename)

	iw, err := NewWriter(context.Background(), FormatV3, fn)
	require.NoError(t, err)

	series := []labels.Labels{
		labels.FromStrings("a", "1", "b", "1"),
		labels.FromStrings("a", "1", "b", "2"),
		labels.FromStrings("a", "1", "b", "3"),
		labels.FromStrings("a", "1", "b", "4"),
	}

	require.NoError(t, iw.AddSymbol("1"))
	require.NoError(t, iw.AddSymbol("2"))
	require.NoError(t, iw.AddSymbol("3"))
	require.NoError(t, iw.AddSymbol("4"))
	require.NoError(t, iw.AddSymbol("a"))
	require.NoError(t, iw.AddSymbol("b"))

	// Postings lists are only written if a series with the respective
	// reference was added before.
	require.NoError(t, iw.AddSeries(1, series[0], model.Fingerprint(labels.StableHash(series[0]))))
	require.NoError(t, iw.AddSeries(2, series[1], model.Fingerprint(labels.StableHash(series[1]))))
	require.NoError(t, iw.AddSeries(3, series[2], model.Fingerprint(labels.StableHash(series[2]))))
	require.NoError(t, iw.AddSeries(4, series[3], model.Fingerprint(labels.StableHash(series[3]))))

	_, err = iw.Close(false)
	require.NoError(t, err)

	ir, err := NewFileReader(fn)
	require.NoError(t, err)

	p, err := ir.Postings("a", nil, "1")
	require.NoError(t, err)

	var l labels.Labels
	var c []ChunkMeta

	for i := 0; p.Next(); i++ {
		_, err := ir.Series(p.At(), 0, math.MaxInt64, &l, &c)

		require.NoError(t, err)
		require.Equal(t, 0, len(c))
		require.Equal(t, series[i], l)
	}
	require.NoError(t, p.Err())

	// The label indices are no longer used, so test them by hand here.
	labelValuesOffsets := map[string]uint64{}
	d := tsdb_enc.NewDecbufAt(ir.b, int(ir.toc.LabelIndicesTable), castagnoliTable)
	cnt := d.Be32()

	for d.Err() == nil && d.Len() > 0 && cnt > 0 {
		require.Equal(t, 1, d.Uvarint(), "Unexpected number of keys for label indices table")
		lbl := d.UvarintStr()
		off := d.Uvarint64()
		labelValuesOffsets[lbl] = off
		cnt--
	}
	require.NoError(t, d.Err())

	labelIndices := map[string][]string{}
	for lbl, off := range labelValuesOffsets {
		d := tsdb_enc.NewDecbufAt(ir.b, int(off), castagnoliTable)
		require.Equal(t, 1, d.Be32int(), "Unexpected number of label indices table names")
		for i := d.Be32(); i > 0 && d.Err() == nil; i-- {
			v, err := ir.lookupSymbol(d.Be32())
			require.NoError(t, err)
			labelIndices[lbl] = append(labelIndices[lbl], v)
		}
		require.NoError(t, d.Err())
	}
	require.Equal(t, map[string][]string{
		"a": {"1"},
		"b": {"1", "2", "3", "4"},
	}, labelIndices)

	require.NoError(t, ir.Close())
}

func TestPostingsMany(t *testing.T) {
	dir := t.TempDir()

	fn := filepath.Join(dir, IndexFilename)

	iw, err := NewWriter(context.Background(), FormatV3, fn)
	require.NoError(t, err)

	// Create a label in the index which has 999 values.
	symbols := map[string]struct{}{}
	series := []labels.Labels{}
	for i := 1; i < 1000; i++ {
		v := fmt.Sprintf("%03d", i)
		series = append(series, labels.FromStrings("i", v, "foo", "bar"))
		symbols[v] = struct{}{}
	}
	symbols["i"] = struct{}{}
	symbols["foo"] = struct{}{}
	symbols["bar"] = struct{}{}
	syms := []string{}
	for s := range symbols {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, s := range syms {
		require.NoError(t, iw.AddSymbol(s))
	}

	sort.Slice(series, func(i, j int) bool {
		return labels.StableHash(series[i]) < labels.StableHash(series[j])
	})

	for i, s := range series {
		require.NoError(t, iw.AddSeries(storage.SeriesRef(i), s, model.Fingerprint(labels.StableHash(s))))
	}
	_, err = iw.Close(false)
	require.NoError(t, err)

	ir, err := NewFileReader(fn)
	require.NoError(t, err)
	defer func() { require.NoError(t, ir.Close()) }()

	cases := []struct {
		in []string
	}{
		// Simple cases, everything is present.
		{in: []string{"002"}},
		{in: []string{"031", "032", "033"}},
		{in: []string{"032", "033"}},
		{in: []string{"127", "128"}},
		{in: []string{"127", "128", "129"}},
		{in: []string{"127", "129"}},
		{in: []string{"128", "129"}},
		{in: []string{"998", "999"}},
		{in: []string{"999"}},
		// Before actual values.
		{in: []string{"000"}},
		{in: []string{"000", "001"}},
		{in: []string{"000", "002"}},
		// After actual values.
		{in: []string{"999a"}},
		{in: []string{"999", "999a"}},
		{in: []string{"998", "999", "999a"}},
		// In the middle of actual values.
		{in: []string{"126a", "127", "128"}},
		{in: []string{"127", "127a", "128"}},
		{in: []string{"127", "127a", "128", "128a", "129"}},
		{in: []string{"127", "128a", "129"}},
		{in: []string{"128", "128a", "129"}},
		{in: []string{"128", "129", "129a"}},
		{in: []string{"126a", "126b", "127", "127a", "127b", "128", "128a", "128b", "129", "129a", "129b"}},
	}

	for _, c := range cases {
		it, err := ir.Postings("i", nil, c.in...)
		require.NoError(t, err)

		got := []string{}
		var lbls labels.Labels
		var metas []ChunkMeta
		for it.Next() {
			_, err := ir.Series(it.At(), 0, math.MaxInt64, &lbls, &metas)
			require.NoError(t, err)
			got = append(got, lbls.Get("i"))
		}
		require.NoError(t, it.Err())
		exp := []string{}
		for _, e := range c.in {
			if _, ok := symbols[e]; ok && e != "l" {
				exp = append(exp, e)
			}
		}

		// sort expected values by label hash instead of lexicographically by labelset
		sort.Slice(exp, func(i, j int) bool {
			return labels.StableHash(labels.FromStrings("i", exp[i], "foo", "bar")) < labels.StableHash(labels.FromStrings("i", exp[j], "foo", "bar"))
		})

		require.Equal(t, exp, got, fmt.Sprintf("input: %v", c.in))
	}
}

func TestPersistence_index_e2e(t *testing.T) {
	dir := t.TempDir()

	lbls, err := labels.ReadLabels(filepath.Join("..", "testdata", "20kseries.json"), 20000)
	require.NoError(t, err)

	// Sort labels as the index writer expects series in sorted order by fingerprint.
	sort.Slice(lbls, func(i, j int) bool {
		return labels.StableHash(lbls[i]) < labels.StableHash(lbls[j])
	})

	symbols := map[string]struct{}{}
	for _, lset := range lbls {
		lset.Range(func(l labels.Label) {
			symbols[l.Name] = struct{}{}
			symbols[l.Value] = struct{}{}
		})
	}

	var input indexWriterSeriesSlice

	// Generate ChunkMetas for every label set.
	for i, lset := range lbls {
		var metas []ChunkMeta

		for j := 0; j <= (i % 20); j++ {
			metas = append(metas, ChunkMeta{
				MinTime:  int64(j * 10000),
				MaxTime:  int64((j + 1) * 10000),
				Checksum: rand.Uint32(),
			})
		}
		input = append(input, &indexWriterSeries{
			labels: lset,
			chunks: metas,
		})
	}

	iw, err := NewWriter(context.Background(), FormatV3, filepath.Join(dir, IndexFilename))
	require.NoError(t, err)

	syms := []string{}
	for s := range symbols {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, s := range syms {
		require.NoError(t, iw.AddSymbol(s))
	}

	// Population procedure as done by compaction.
	var (
		postings = NewMemPostings()
		values   = map[string]map[string]struct{}{}
	)

	mi := newMockIndex()

	for i, s := range input {
		err = iw.AddSeries(storage.SeriesRef(i), s.labels, model.Fingerprint(labels.StableHash(s.labels)), s.chunks...)
		require.NoError(t, err)
		require.NoError(t, mi.AddSeries(storage.SeriesRef(i), s.labels, s.chunks...))

		s.labels.Range(func(l labels.Label) {
			valset, ok := values[l.Name]
			if !ok {
				valset = map[string]struct{}{}
				values[l.Name] = valset
			}
			valset[l.Value] = struct{}{}
		})
		postings.Add(storage.SeriesRef(i), s.labels)
	}

	_, err = iw.Close(false)
	require.NoError(t, err)

	ir, err := NewFileReader(filepath.Join(dir, IndexFilename))
	require.NoError(t, err)

	for p := range mi.postings {
		gotp, err := ir.Postings(p.Name, nil, p.Value)
		require.NoError(t, err)

		expp, err := mi.Postings(p.Name, p.Value)
		require.NoError(t, err)

		var lset, explset labels.Labels
		var chks, expchks []ChunkMeta

		for gotp.Next() {
			require.True(t, expp.Next())

			ref := gotp.At()

			_, err := ir.Series(ref, 0, math.MaxInt64, &lset, &chks)
			require.NoError(t, err)

			err = mi.Series(expp.At(), &explset, &expchks)
			require.NoError(t, err)
			require.Equal(t, explset, lset)
			require.Equal(t, expchks, chks)
		}
		require.False(t, expp.Next(), "Expected no more postings for %q=%q", p.Name, p.Value)
		require.NoError(t, gotp.Err())
	}

	labelPairs := map[string][]string{}
	for l := range mi.postings {
		labelPairs[l.Name] = append(labelPairs[l.Name], l.Value)
	}
	for k, v := range labelPairs {
		sort.Strings(v)

		res, err := ir.LabelValues(k)
		require.NoError(t, err)

		sort.Strings(res)
		require.Equal(t, len(v), len(res))
		for i := 0; i < len(v); i++ {
			require.Equal(t, v[i], res[i])
		}
	}

	gotSymbols := []string{}
	it := ir.Symbols()
	for it.Next() {
		gotSymbols = append(gotSymbols, it.At())
	}
	require.NoError(t, it.Err())
	expSymbols := []string{}
	for s := range mi.symbols {
		expSymbols = append(expSymbols, s)
	}
	sort.Strings(expSymbols)
	require.Equal(t, expSymbols, gotSymbols)

	require.NoError(t, ir.Close())
}

func TestDecbufUvarintWithInvalidBuffer(t *testing.T) {
	b := RealByteSlice([]byte{0x81, 0x81, 0x81, 0x81, 0x81, 0x81})

	db := tsdb_enc.NewDecbufUvarintAt(b, 0, castagnoliTable)
	require.Error(t, db.Err())
}

func TestReaderWithInvalidBuffer(t *testing.T) {
	b := RealByteSlice([]byte{0x81, 0x81, 0x81, 0x81, 0x81, 0x81})

	_, err := NewReader(b)
	require.Error(t, err)
}

// TestNewFileReaderErrorNoOpenFiles ensures that in case of an error no file remains open.
func TestNewFileReaderErrorNoOpenFiles(t *testing.T) {
	dir := testutil.NewTemporaryDirectory("block", t)

	idxName := filepath.Join(dir.Path(), "index")
	err := os.WriteFile(idxName, []byte("corrupted contents"), 0o666)
	require.NoError(t, err)

	_, err = NewFileReader(idxName)
	require.Error(t, err)

	// dir.Close will fail on Win if idxName fd is not closed on error path.
	dir.Close()
}

func TestSymbols(t *testing.T) {
	buf := encoding.Encbuf{}

	// Add prefix to the buffer to simulate symbols as part of larger buffer.
	buf.PutUvarintStr("something")

	symbolsStart := buf.Len()
	buf.PutBE32int(204) // Length of symbols table.
	buf.PutBE32int(100) // Number of symbols.
	for i := 0; i < 100; i++ {
		// i represents index in unicode characters table.
		buf.PutUvarintStr(string(rune(i))) // Symbol.
	}
	checksum := crc32.Checksum(buf.Get()[symbolsStart+4:], castagnoliTable)
	buf.PutBE32(checksum) // Check sum at the end.

	s, err := NewSymbols(RealByteSlice(buf.Get()), FormatV2, symbolsStart)
	require.NoError(t, err)

	// We store only 4 offsets to symbols.
	require.Equal(t, 32, s.Size())

	for i := 99; i >= 0; i-- {
		s, err := s.Lookup(uint32(i))
		require.NoError(t, err)
		require.Equal(t, string(rune(i)), s)
	}
	_, err = s.Lookup(100)
	require.Error(t, err)

	for i := 99; i >= 0; i-- {
		r, err := s.ReverseLookup(string(rune(i)))
		require.NoError(t, err)
		require.Equal(t, uint32(i), r)
	}
	_, err = s.ReverseLookup(string(rune(100)))
	require.Error(t, err)

	iter := s.Iter()
	i := 0
	for iter.Next() {
		require.Equal(t, string(rune(i)), iter.At())
		i++
	}
	require.NoError(t, iter.Err())
}

func TestDecoder_Postings_WrongInput(t *testing.T) {
	_, _, err := (&Decoder{}).Postings([]byte("the cake is a lie"))
	require.Error(t, err)
}

func TestDecoder_ChunkSamples(t *testing.T) {
	dir := t.TempDir()

	lbls := []labels.Labels{
		labels.New(labels.Label{Name: "fizz", Value: "buzz"}),
		labels.New(labels.Label{Name: "ping", Value: "pong"}),
	}

	symbols := map[string]struct{}{}
	for _, lset := range lbls {
		lset.Range(func(l labels.Label) {
			symbols[l.Name] = struct{}{}
			symbols[l.Value] = struct{}{}
		})
	}

	now := model.Now()

	for name, tc := range map[string]struct {
		chunkMetas           []ChunkMeta
		expectedChunkSamples []chunkSample
	}{
		"no overlapping chunks": {
			chunkMetas: []ChunkMeta{
				{
					MinTime: int64(now),
					MaxTime: int64(now.Add(30 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(40 * time.Minute)),
					MaxTime: int64(now.Add(80 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(90 * time.Minute)),
					MaxTime: int64(now.Add(120 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(130 * time.Minute)),
					MaxTime: int64(now.Add(150 * time.Minute)),
				},
			},
			expectedChunkSamples: []chunkSample{
				{
					largestMaxt:   int64(now.Add(30 * time.Minute)),
					idx:           0,
					prevChunkMaxt: 0,
				},
				{
					largestMaxt:   int64(now.Add(120 * time.Minute)),
					idx:           2,
					prevChunkMaxt: int64(now.Add(80 * time.Minute)),
				},
				{
					largestMaxt:   int64(now.Add(150 * time.Minute)),
					idx:           3,
					prevChunkMaxt: int64(now.Add(120 * time.Minute)),
				},
			},
		},
		"overlapping chunks": {
			chunkMetas: []ChunkMeta{
				{
					MinTime: int64(now),
					MaxTime: int64(now.Add(30 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(20 * time.Minute)),
					MaxTime: int64(now.Add(80 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(70 * time.Minute)),
					MaxTime: int64(now.Add(120 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(100 * time.Minute)),
					MaxTime: int64(now.Add(110 * time.Minute)),
				},
			},
			expectedChunkSamples: []chunkSample{
				{
					largestMaxt:   int64(now.Add(30 * time.Minute)),
					idx:           0,
					prevChunkMaxt: 0,
				},
				{
					largestMaxt:   int64(now.Add(120 * time.Minute)),
					idx:           2,
					prevChunkMaxt: int64(now.Add(80 * time.Minute)),
				},
				{
					largestMaxt:   int64(now.Add(120 * time.Minute)),
					idx:           3,
					prevChunkMaxt: int64(now.Add(120 * time.Minute)),
				},
			},
		},
		"first chunk overlapping all chunks": {
			chunkMetas: []ChunkMeta{
				{
					MinTime: int64(now),
					MaxTime: int64(now.Add(180 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(20 * time.Minute)),
					MaxTime: int64(now.Add(80 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(70 * time.Minute)),
					MaxTime: int64(now.Add(120 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(110 * time.Minute)),
					MaxTime: int64(now.Add(150 * time.Minute)),
				},
			},
			expectedChunkSamples: []chunkSample{
				{
					largestMaxt:   int64(now.Add(180 * time.Minute)),
					idx:           0,
					prevChunkMaxt: 0,
				},
				{
					largestMaxt:   int64(now.Add(180 * time.Minute)),
					idx:           3,
					prevChunkMaxt: int64(now.Add(120 * time.Minute)),
				},
			},
		},
		"large gaps between chunks": {
			chunkMetas: []ChunkMeta{
				{
					MinTime: int64(now),
					MaxTime: int64(now.Add(30 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(200 * time.Minute)),
					MaxTime: int64(now.Add(280 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(500 * time.Minute)),
					MaxTime: int64(now.Add(520 * time.Minute)),
				},
				{
					MinTime: int64(now.Add(800 * time.Minute)),
					MaxTime: int64(now.Add(835 * time.Minute)),
				},
			},
			expectedChunkSamples: []chunkSample{
				{
					largestMaxt:   int64(now.Add(30 * time.Minute)),
					idx:           0,
					prevChunkMaxt: 0,
				},
				{
					largestMaxt:   int64(now.Add(280 * time.Minute)),
					idx:           1,
					prevChunkMaxt: int64(now.Add(30 * time.Minute)),
				},
				{
					largestMaxt:   int64(now.Add(520 * time.Minute)),
					idx:           2,
					prevChunkMaxt: int64(now.Add(280 * time.Minute)),
				},
				{
					largestMaxt:   int64(now.Add(835 * time.Minute)),
					idx:           3,
					prevChunkMaxt: int64(now.Add(520 * time.Minute)),
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			iw, err := NewFileWriterWithVersion(context.Background(), FormatV2, filepath.Join(dir, name))
			require.NoError(t, err)

			syms := []string{}
			for s := range symbols {
				syms = append(syms, s)
			}
			sort.Strings(syms)
			for _, s := range syms {
				require.NoError(t, iw.AddSymbol(s))
			}

			for i, l := range lbls {
				err = iw.AddSeries(storage.SeriesRef(i), l, model.Fingerprint(labels.StableHash(l)), tc.chunkMetas...)
				require.NoError(t, err)
			}

			_, err = iw.Close(false)
			require.NoError(t, err)

			ir, err := NewFileReader(filepath.Join(dir, name))
			require.NoError(t, err)

			postings, err := ir.Postings("fizz", nil, "buzz")
			require.NoError(t, err)

			require.True(t, postings.Next())
			var lset labels.Labels
			var chks []ChunkMeta

			// there should be no chunk samples
			require.Nil(t, ir.dec.chunksSample[postings.At()])

			// read series so that chunk samples get built
			_, err = ir.Series(postings.At(), 0, math.MaxInt64, &lset, &chks)
			require.NoError(t, err)

			require.Equal(t, tc.chunkMetas, chks)
			require.Equal(t, lset, lbls[0])

			// there should be chunk samples for only the series we read
			require.Len(t, ir.dec.chunksSample, 1)
			require.NotNil(t, ir.dec.chunksSample[postings.At()])
			require.Len(t, ir.dec.chunksSample[postings.At()].chunks, len(tc.expectedChunkSamples))

			// build decoder for the series we read to verify the samples
			offset := postings.At() * 16
			d := encoding.DecWrap(tsdb_enc.NewDecbufUvarintAt(ir.b, int(offset), castagnoliTable))
			require.NoError(t, d.Err())

			// read chunk metadata to positing the decoder at the beginning of first chunk
			d.Be64()
			k := d.Uvarint()

			for i := 0; i < k; i++ {
				d.Uvarint()
				d.Uvarint()
			}
			require.Equal(t, len(tc.chunkMetas), d.Uvarint())
			for i, cs := range ir.dec.chunksSample[postings.At()].chunks {
				require.Equal(t, tc.expectedChunkSamples[i].idx, cs.idx)
				require.Equal(t, tc.expectedChunkSamples[i].largestMaxt, cs.largestMaxt)
				require.Equal(t, tc.expectedChunkSamples[i].prevChunkMaxt, cs.prevChunkMaxt)

				dw := encoding.DecWrap(tsdb_enc.Decbuf{B: d.Get()})
				dw.Skip(cs.offset)
				chunkMeta := ChunkMeta{}
				require.NoError(t, readChunkMeta(&dw, cs.prevChunkMaxt, &chunkMeta))
				require.Equal(t, tc.chunkMetas[tc.expectedChunkSamples[i].idx], chunkMeta)
			}

			require.NoError(t, ir.Close())
		})
	}
}

func TestChunkSamples_getChunkSampleForQueryStarting(t *testing.T) {
	for name, tc := range map[string]struct {
		chunkSamples           *chunkSamples
		queryMint              int64
		expectedChunkSampleIdx int
	}{
		"mint greater than largestMaxt": {
			chunkSamples: &chunkSamples{
				chunks: []chunkSample{
					{
						largestMaxt:   100,
						idx:           0,
						offset:        0,
						prevChunkMaxt: 0,
					},
					{
						largestMaxt:   200,
						idx:           5,
						offset:        5,
						prevChunkMaxt: 50,
					},
				},
			},
			queryMint:              250,
			expectedChunkSampleIdx: -1,
		},
		"mint smaller than first largestMaxt": {
			chunkSamples: &chunkSamples{
				chunks: []chunkSample{
					{
						largestMaxt:   100,
						idx:           0,
						offset:        0,
						prevChunkMaxt: 0,
					},
					{
						largestMaxt:   200,
						idx:           5,
						offset:        5,
						prevChunkMaxt: 50,
					},
				},
			},
			queryMint:              50,
			expectedChunkSampleIdx: 0,
		},
		"intermediate chunk sample": {
			chunkSamples: &chunkSamples{
				chunks: []chunkSample{
					{
						largestMaxt:   100,
						idx:           0,
						offset:        0,
						prevChunkMaxt: 0,
					},
					{
						largestMaxt:   200,
						idx:           5,
						offset:        5,
						prevChunkMaxt: 50,
					},
					{
						largestMaxt:   350,
						idx:           7,
						offset:        7,
						prevChunkMaxt: 150,
					},
					{
						largestMaxt:   500,
						idx:           9,
						offset:        9,
						prevChunkMaxt: 250,
					},
				},
			},
			queryMint:              250,
			expectedChunkSampleIdx: 1,
		},
		"mint matching samples largestMaxt": {
			chunkSamples: &chunkSamples{
				chunks: []chunkSample{
					{
						largestMaxt:   100,
						idx:           0,
						offset:        0,
						prevChunkMaxt: 0,
					},
					{
						largestMaxt:   200,
						idx:           5,
						offset:        5,
						prevChunkMaxt: 50,
					},
					{
						largestMaxt:   350,
						idx:           7,
						offset:        7,
						prevChunkMaxt: 150,
					},
					{
						largestMaxt:   500,
						idx:           9,
						offset:        9,
						prevChunkMaxt: 250,
					},
				},
			},
			queryMint:              350,
			expectedChunkSampleIdx: 1,
		},
		"same chunk sampled": {
			chunkSamples: &chunkSamples{
				chunks: []chunkSample{
					{
						largestMaxt:   100,
						idx:           0,
						offset:        0,
						prevChunkMaxt: 0,
					},
					{
						largestMaxt:   100,
						idx:           0,
						offset:        0,
						prevChunkMaxt: 0,
					},
				},
			},
			queryMint:              50,
			expectedChunkSampleIdx: 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			chunkSample := tc.chunkSamples.getChunkSampleForQueryStarting(tc.queryMint)
			if tc.expectedChunkSampleIdx == -1 {
				require.Nil(t, chunkSample)
				return
			}

			require.NotNil(t, chunkSample)
			require.Equal(t, tc.chunkSamples.chunks[tc.expectedChunkSampleIdx], *chunkSample)
		})
	}
}

func BenchmarkInitReader_ReadOffsetTable(b *testing.B) {
	dir := b.TempDir()
	idxFile := filepath.Join(dir, IndexFilename)

	lbls, err := labels.ReadLabels(filepath.Join("..", "testdata", "20kseries.json"), 1000)
	require.NoError(b, err)

	// Sort labels as the index writer expects series in sorted order by fingerprint.
	sort.Slice(lbls, func(i, j int) bool {
		return labels.StableHash(lbls[i]) < labels.StableHash(lbls[j])
	})

	symbols := map[string]struct{}{}
	for _, lset := range lbls {
		lset.Range(func(l labels.Label) {
			symbols[l.Name] = struct{}{}
			symbols[l.Value] = struct{}{}
		})
	}

	var input indexWriterSeriesSlice

	// Generate ChunkMetas for every label set.
	for _, lset := range lbls {
		input = append(input, &indexWriterSeries{
			labels: lset,
			chunks: []ChunkMeta{
				{
					MinTime:  0,
					MaxTime:  1,
					Checksum: rand.Uint32(),
				},
			},
		})
	}

	iw, err := NewWriter(context.Background(), FormatV3, idxFile)
	require.NoError(b, err)

	var syms []string
	for s := range symbols {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, s := range syms {
		require.NoError(b, iw.AddSymbol(s))
	}

	for i, s := range input {
		err = iw.AddSeries(storage.SeriesRef(i), s.labels, model.Fingerprint(labels.StableHash(s.labels)), s.chunks...)
		require.NoError(b, err)
	}

	_, err = iw.Close(false)
	require.NoError(b, err)

	bs, err := os.ReadFile(idxFile)
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r, err := newReader(RealByteSlice(bs), io.NopCloser(nil))
		require.NoError(b, err)
		require.NoError(b, r.Close())
	}
}
