package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// bsp builds a canonical CoD2 d3dbsp: lumps 0-37 back to back, 4-byte padded, lump 38 left {0, 0}.
func bsp(lumps map[int][]byte) []byte {
	b := make([]byte, headerSize)
	copy(b, "IBSP")
	binary.LittleEndian.PutUint32(b[4:], 4)
	for i := 0; i < lumpCount-1; i++ {
		binary.LittleEndian.PutUint32(b[8+i*8:], uint32(len(lumps[i])))
		binary.LittleEndian.PutUint32(b[12+i*8:], uint32(len(b)))
		b = append(b, lumps[i]...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}
	return b
}

// testLumps returns a grid with the given color indices over nColors grey colors, one model and entities.
func testLumps(entryIdx []uint16, nColors int, worldspawn string) map[int][]byte {
	entries := make([]byte, len(entryIdx)*entrySize)
	for i, idx := range entryIdx {
		binary.LittleEndian.PutUint16(entries[i*entrySize+6:], idx)
	}
	model := make([]byte, 48)
	for i, v := range []float32{-1, -2, -3, 4, 5, 6} {
		binary.LittleEndian.PutUint32(model[i*4:], math.Float32bits(v))
	}
	return map[int][]byte{
		lumpGridEntries: entries,
		lumpGridColors:  bytes.Repeat([]byte{100}, nColors*colorSize), // grey: zero saturation
		lumpModels:      model,
		lumpEntities:    []byte("{\n\"classname\" \"worldspawn\"\n" + worldspawn + "}\n{\n\"classname\" \"light\"\n\"light\" \"300\"\n}\n\x00"),
	}
}

const ws = "\"ambient\" \".2\"\n\"_color\" \"1 1 1\"\n"

func writeIWDFile(t *testing.T, p string, names []string, data map[string][]byte) {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data[name])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestScanAndDump(t *testing.T) {
	dir := t.TempDir()
	iwd := filepath.Join(dir, "test.iwd")
	data := map[string][]byte{
		"maps/mp/good.d3dbsp":  bsp(testLumps([]uint16{0, 1, 1}, 2, ws)),
		"maps/mp/empty.d3dbsp": bsp(testLumps(nil, 0, ws)),
		"maps/mp/past.d3dbsp":  bsp(testLumps([]uint16{0, 2}, 2, ws)),
		"maps/mp/good.csv":     []byte("ignored"),
	}
	writeIWDFile(t, iwd, []string{"maps/mp/good.d3dbsp", "maps/mp/empty.d3dbsp", "maps/mp/past.d3dbsp", "maps/mp/good.csv"}, data)

	lines, err := scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	const tail = " light=0 mins=-1,-2,-3 maxs=4,5,6 keys=ambient,_color "
	want := []string{
		"test.iwd empty v4 entries=0B/0 colors=0B/0 maxidx=-1 badidx=0 sat=0.000" + tail + "BROKEN empty light grid",
		"test.iwd good v4 entries=24B/3 colors=48B/2 maxidx=1 badidx=0 sat=0.000" + tail + "OK",
		"test.iwd past v4 entries=16B/2 colors=48B/2 maxidx=2 badidx=1 sat=0.000" + tail + "BROKEN 1 entries index past 2 colors",
		"# median color (1 maps, 3 entries): [100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100 100]" +
			" planar R=[100 100 100 100 100 100 100 100] G=[100 100 100 100 100 100 100 100] B=[100 100 100 100 100 100 100 100]",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("scan got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	d := strings.Join(dump(iwd), "\n")
	for _, s := range []string{
		`  key "ambient" ".2"`,
		"  e2 00 00 00 00 00 00 01 00 | 0 0 0 1 | b4=0 b5=0",
		"  c1 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64 64",
	} {
		if !strings.Contains(d, s) {
			t.Errorf("dump missing %q in:\n%s", s, d)
		}
	}
}

func TestFix(t *testing.T) {
	src, out := filepath.Join(t.TempDir(), "src"), filepath.Join(t.TempDir(), "out")
	os.Mkdir(src, 0o755)
	lumps := testLumps(nil, 0, "\"SunLight\" \"1\"\n")
	lumps[lumpLightmaps] = []byte{1, 2, 3, 4, 5} // exercises padding
	lumps[4] = []byte{9, 9, 9}
	in := bsp(lumps)
	names := []string{"maps/mp/fixme.d3dbsp", "readme.txt", "maps/mp/good.d3dbsp"}
	data := map[string][]byte{
		names[0]: in,
		names[1]: []byte("copied raw"),
		names[2]: bsp(testLumps([]uint16{0}, 1, ws)),
	}
	writeIWDFile(t, filepath.Join(src, "test.iwd"), names, data)
	writeIWDFile(t, filepath.Join(src, "bad.iwd"), []string{"maps/mp/bad.d3dbsp"},
		map[string][]byte{"maps/mp/bad.d3dbsp": append(bsp(testLumps(nil, 0, ws)), 0, 0, 0, 0)})

	for _, o := range []string{src, filepath.Join(src, "sub")} {
		if _, err := fix(src, o); err == nil {
			t.Errorf("fix(%s) was not refused", o)
		}
	}
	lines, err := fix(src, out)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []string{
		"skip bad.iwd bad: non-canonical lump layout",
		"skip test.iwd good: verdict OK",
		"fixed test.iwd fixme sun=true size=" + strconv.Itoa(len(in)) + "->" + strconv.Itoa(len(in)+48),
	}
	if strings.Join(lines, "\n") != strings.Join(wantLines, "\n") {
		t.Fatalf("fix got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(wantLines, "\n"))
	}
	if _, err := os.Stat(filepath.Join(out, "bad.iwd")); err == nil {
		t.Error("bad.iwd was written")
	}

	r, err := zip.OpenReader(filepath.Join(out, "test.iwd"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := map[string][]byte{}
	for i, f := range r.File {
		if f.Name != names[i] {
			t.Fatalf("entry %d = %s, want %s", i, f.Name, names[i])
		}
		rc, _ := f.Open()
		got[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	if !bytes.Equal(got[names[1]], data[names[1]]) || !bytes.Equal(got[names[2]], data[names[2]]) {
		t.Error("extra entries not copied unchanged")
	}
	b := got[names[0]]
	lump := func(b []byte, i int) []byte { off, n := lumpRange(b, i); return b[off : off+n] }
	wantGrid := []byte{0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0x80, 1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xfd, 0, 0, 0}
	if !bytes.Equal(lump(b, lumpGridEntries), wantGrid) || !bytes.Equal(lump(b, lumpGridColors), medianColorRecord) {
		t.Errorf("grid lumps = % x / % x", lump(b, lumpGridEntries), lump(b, lumpGridColors))
	}
	for i := 0; i < lumpCount; i++ {
		if i == lumpGridEntries || i == lumpGridColors {
			continue
		}
		if !bytes.Equal(lump(b, i), lump(in, i)) {
			t.Errorf("lump %d changed", i)
		}
		want, _ := lumpRange(in, i)
		if i > lumpGridColors && i < lumpCount-1 {
			want += 48
		}
		if off, _ := lumpRange(b, i); off != want {
			t.Errorf("lump %d offset %d, want %d", i, off, want)
		}
	}
	if !canonical(b) {
		t.Error("output is not canonical")
	}
}

// nearest ports the client's nearest-entry binary search over the grid keys.
func nearest(keys []uint32, k uint32) int {
	lo, hi := 0, len(keys)-1
	for lo <= hi {
		mid := (lo + hi) >> 1
		d := int32(keys[mid] - k)
		if d == 0 {
			return mid
		}
		if d >= 0 {
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	return hi
}

func TestInjectedKeysStayInRange(t *testing.T) {
	b := injectGrid(bsp(testLumps(nil, 0, ws)), false)
	off, n := lumpRange(b, lumpGridEntries)
	var keys []uint32
	for i := off; i < off+n; i += entrySize {
		keys = append(keys, binary.LittleEndian.Uint32(b[i:]))
	}
	probe := []uint32{0, 1, 0x7FFFFFFF, 0x80000000, 0x80000001, 0xFFFFFFFF}
	for k, i := uint32(12345), 0; i < 5000; i++ {
		k = k*1664525 + 1013904223
		probe = append(probe, k)
	}
	for _, k := range probe {
		if got := nearest(keys, k); got < 0 || got >= len(keys) {
			t.Fatalf("nearest(%#x) = %d over keys % x", k, got, keys)
		}
	}
}
