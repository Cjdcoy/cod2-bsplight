// bsplight scans CoD2 map IWDs read-only and reports the state of each map's
// light grid (BSP lumps 2 and 3), which is what lights models in game. Its fix
// command writes copies of IWDs whose maps have no light grid, with one injected.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// CoD2 d3dbsp: "IBSP", u32 version 4, then 39 lumps of {u32 length, u32 offset}.
const (
	lumpCount       = 39
	headerSize      = 8 + lumpCount*8
	lumpLightmaps   = 1
	lumpGridEntries = 2
	lumpGridColors  = 3
	lumpModels      = 35 // 48-byte records: float mins[3], float maxs[3], 6 x u32
	lumpEntities    = 37
	entrySize       = 8  // u32 key, u8 sun/flags, u8 trace mask, u16 colorsIndex
	colorSize       = 24 // u8 R[8], G[8], B[8]
	maxBSPSize      = 1 << 30
	emptyGrid       = "BROKEN empty light grid"
)

// medianColorRecord is the entry-weighted median color of the 333 maps with a grid (bsplight scan, 2026-10).
var medianColorRecord = []byte{
	81, 101, 102, 102, 94, 96, 91, 112, // R
	79, 100, 100, 100, 92, 94, 90, 114, // G
	86, 102, 105, 105, 95, 98, 93, 112, // B
}

var (
	le      = binary.LittleEndian
	keyPair = regexp.MustCompile(`"([^"]*)"\s+"([^"]*)"`)
)

type grid struct {
	iwd, name          string
	version            uint32
	entries, colors    []byte // copies, so the whole BSP buffer is not retained
	lightBytes         int
	mins, maxs         [3]float32
	keys               [][2]string // worldspawn keys mentioning sun, light, ambient or color
	maxIndex, badIndex int
	sat                float64
	verdict            string
}

func main() {
	var lines []string
	var err error
	switch {
	case len(os.Args) == 3 && os.Args[1] == "scan":
		lines, err = scan(os.Args[2])
	case len(os.Args) == 3 && os.Args[1] == "dump":
		lines = dump(os.Args[2])
	case len(os.Args) == 4 && os.Args[1] == "fix":
		lines, err = fix(os.Args[2], os.Args[3])
	default:
		fmt.Fprintln(os.Stderr, "usage: bsplight scan <dir-with-iwds> | dump <iwd> | fix <srcdir> <outdir>")
		os.Exit(2)
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func scan(dir string) ([]string, error) {
	iwds, err := filepath.Glob(filepath.Join(dir, "*.iwd"))
	if err != nil {
		return nil, err
	}
	var grids []grid
	for _, p := range iwds {
		grids = append(grids, scanIWD(p)...)
	}
	sort.Slice(grids, func(i, j int) bool {
		if grids[i].name != grids[j].name {
			return grids[i].name < grids[j].name
		}
		return grids[i].iwd < grids[j].iwd
	})
	var lines []string
	for _, g := range grids {
		lines = append(lines, g.line())
	}
	return append(lines, medianColor(grids)...), nil
}

// dump prints one IWD's maps with every grid entry and color record.
func dump(p string) []string {
	var lines []string
	for _, g := range scanIWD(p) {
		lines = append(lines, g.line())
		for _, kv := range g.keys {
			lines = append(lines, fmt.Sprintf("  key %q %q", kv[0], kv[1]))
		}
		for i := 0; i+entrySize <= len(g.entries); i += entrySize {
			e := g.entries[i : i+entrySize]
			lines = append(lines, fmt.Sprintf("  e%d % x | %d %d %d %d | b4=%d b5=%d", i/entrySize, e,
				le.Uint16(e), le.Uint16(e[2:]), le.Uint16(e[4:]), le.Uint16(e[6:]), e[4], e[5]))
		}
		for i := 0; i+colorSize <= len(g.colors); i += colorSize {
			lines = append(lines, fmt.Sprintf("  c%d % x", i/colorSize, g.colors[i:i+colorSize]))
		}
	}
	return lines
}

// medianColor returns the per-byte median color record over OK grids, weighted by entry references.
func medianColor(grids []grid) []string {
	var hist [colorSize][256]int
	maps, total := 0, 0
	for _, g := range grids {
		if g.verdict != "OK" {
			continue
		}
		maps++
		for i := 0; i+entrySize <= len(g.entries); i += entrySize {
			c := g.colors[int(le.Uint16(g.entries[i+6:]))*colorSize:]
			for j := range hist {
				hist[j][c[j]]++
			}
			total++
		}
	}
	if total == 0 {
		return nil
	}
	med := make([]byte, colorSize)
	for j := range hist {
		for v, sum := 0, 0; v < 256; v++ {
			if sum += hist[j][v]; 2*sum >= total {
				med[j] = byte(v)
				break
			}
		}
	}
	return []string{fmt.Sprintf("# median color (%d maps, %d entries): %v planar R=%v G=%v B=%v",
		maps, total, med, med[:8], med[8:16], med[16:])}
}

func (g grid) line() string {
	var names []string
	for _, kv := range g.keys {
		names = append(names, kv[0])
	}
	keys := strings.Join(names, ",")
	if keys == "" {
		keys = "-"
	}
	return fmt.Sprintf("%s %s v%d entries=%dB/%d colors=%dB/%d maxidx=%d badidx=%d sat=%.3f light=%d mins=%g,%g,%g maxs=%g,%g,%g keys=%s %s",
		g.iwd, g.name, g.version, len(g.entries), len(g.entries)/entrySize, len(g.colors), len(g.colors)/colorSize,
		g.maxIndex, g.badIndex, g.sat, g.lightBytes, g.mins[0], g.mins[1], g.mins[2], g.maxs[0], g.maxs[1], g.maxs[2],
		keys, g.verdict)
}

func isBSP(name string) bool {
	dir, file := path.Split(name)
	return strings.EqualFold(dir, "maps/mp/") && strings.HasSuffix(strings.ToLower(file), ".d3dbsp")
}

func scanIWD(p string) []grid {
	iwd := filepath.Base(p)
	r, err := zip.OpenReader(p)
	if err != nil {
		return []grid{{iwd: iwd, name: "-", verdict: "ERROR " + err.Error()}}
	}
	defer r.Close()
	var out []grid
	for _, f := range r.File {
		if isBSP(f.Name) {
			g, _ := inspect(iwd, f)
			out = append(out, g)
		}
	}
	return out
}

// lumpRange returns lump i's offset and length from a BSP header; the caller checks bounds.
func lumpRange(b []byte, i int) (off, n uint64) {
	return uint64(le.Uint32(b[12+i*8:])), uint64(le.Uint32(b[8+i*8:]))
}

// inspect reads one d3dbsp and also returns its bytes when the header and used lumps are valid.
func inspect(iwd string, f *zip.File) (grid, []byte) {
	g := grid{iwd: iwd, name: strings.TrimSuffix(path.Base(f.Name), path.Ext(f.Name)), maxIndex: -1}
	if f.UncompressedSize64 < headerSize || f.UncompressedSize64 > maxBSPSize {
		g.verdict = fmt.Sprintf("BROKEN implausible size %d", f.UncompressedSize64)
		return g, nil
	}
	rc, err := f.Open()
	if err != nil {
		g.verdict = "ERROR " + err.Error()
		return g, nil
	}
	defer rc.Close()
	b := make([]byte, f.UncompressedSize64)
	if _, err := io.ReadFull(rc, b); err != nil {
		g.verdict = "ERROR " + err.Error()
		return g, nil
	}
	g.version = le.Uint32(b[4:])
	if string(b[:4]) != "IBSP" || g.version != 4 {
		g.verdict = fmt.Sprintf("BROKEN not IBSP v4 (%q)", b[:4])
		return g, nil
	}
	for _, i := range []int{lumpLightmaps, lumpGridEntries, lumpGridColors, lumpModels, lumpEntities} {
		if off, n := lumpRange(b, i); off+n > uint64(len(b)) {
			g.verdict = fmt.Sprintf("BROKEN lump %d past end of file", i)
			return g, nil
		}
	}
	lump := func(i int) []byte {
		off, n := lumpRange(b, i)
		return b[off : off+n]
	}
	g.lightBytes = len(lump(lumpLightmaps))
	if m := lump(lumpModels); len(m) >= 24 {
		for i := 0; i < 3; i++ {
			g.mins[i] = math.Float32frombits(le.Uint32(m[i*4:]))
			g.maxs[i] = math.Float32frombits(le.Uint32(m[12+i*4:]))
		}
	}
	worldspawn, _, _ := strings.Cut(string(lump(lumpEntities)), "}")
	for _, kv := range keyPair.FindAllStringSubmatch(worldspawn, -1) {
		k := strings.ToLower(kv[1])
		if strings.Contains(k, "sun") || strings.Contains(k, "light") || strings.Contains(k, "ambient") || strings.Contains(k, "color") {
			g.keys = append(g.keys, [2]string{kv[1], kv[2]})
		}
	}
	g.entries, g.colors = bytes.Clone(lump(lumpGridEntries)), bytes.Clone(lump(lumpGridColors))

	nColors := len(g.colors) / colorSize
	for i := 0; i+entrySize <= len(g.entries); i += entrySize {
		idx := int(le.Uint16(g.entries[i+6:]))
		g.maxIndex = max(g.maxIndex, idx)
		if idx >= nColors {
			g.badIndex++
		}
	}
	// sat is informational only: no labeled map ties saturation to the bug.
	for k := 0; k < nColors; k++ {
		c := g.colors[k*colorSize:]
		for d := 0; d < 8; d++ {
			if hi := max(c[d], c[8+d], c[16+d]); hi > 0 {
				g.sat += float64(hi-min(c[d], c[8+d], c[16+d])) / float64(hi)
			}
		}
	}
	if nColors > 0 {
		g.sat /= float64(nColors * 8)
	}

	switch {
	case len(g.entries) == 0 || len(g.colors) == 0:
		g.verdict = emptyGrid
	case len(g.entries)%entrySize != 0 || len(g.colors)%colorSize != 0:
		g.verdict = "BROKEN lump length not a multiple of record size"
	case g.badIndex > 0:
		g.verdict = fmt.Sprintf("BROKEN %d entries index past %d colors", g.badIndex, nColors)
	default:
		g.verdict = "OK"
	}
	return g, b
}

// fix writes <out>/<name>.iwd for every IWD in src holding a map with both grid lumps empty.
func fix(src, out string) ([]string, error) {
	src, err := filepath.EvalSymlinks(src)
	if err == nil {
		src, err = filepath.Abs(src)
	}
	if err != nil {
		return nil, err
	}
	out, err = resolve(out)
	if err != nil {
		return nil, err
	}
	if rel, err := filepath.Rel(src, out); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("refusing outdir %s: it is srcdir or inside it", out)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	iwds, err := filepath.Glob(filepath.Join(src, "*.iwd"))
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, p := range iwds {
		lines = append(lines, fixIWD(p, out)...)
	}
	return lines, nil
}

// resolve returns p absolute with symlinks evaluated, even when its tail does not exist yet.
func resolve(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, nil
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p, nil
	}
	r, err := resolve(parent)
	return filepath.Join(r, filepath.Base(p)), err
}

func fixIWD(p, outDir string) []string {
	iwd := filepath.Base(p)
	r, err := zip.OpenReader(p)
	if err != nil {
		return []string{fmt.Sprintf("skip %s: %v", iwd, err)}
	}
	defer r.Close()
	var lines, done []string
	fixed := map[string][]byte{}
	hashes := map[string][lumpCount][32]byte{}
	for _, f := range r.File {
		if !isBSP(f.Name) {
			continue
		}
		g, b := inspect(iwd, f)
		switch {
		case g.verdict != emptyGrid || len(g.entries) != 0 || len(g.colors) != 0:
			lines = append(lines, fmt.Sprintf("skip %s %s: verdict %s", iwd, g.name, g.verdict))
		case !canonical(b):
			lines = append(lines, fmt.Sprintf("skip %s %s: non-canonical lump layout", iwd, g.name))
		default:
			sun := false
			for _, kv := range g.keys {
				sun = sun || strings.HasPrefix(strings.ToLower(kv[0]), "sun")
			}
			fixed[f.Name] = injectGrid(b, sun)
			hashes[f.Name], _ = lumpHashes(b)
			done = append(done, fmt.Sprintf("fixed %s %s sun=%v size=%d->%d", iwd, g.name, sun, len(b), len(fixed[f.Name])))
		}
	}
	if len(fixed) == 0 {
		return lines
	}
	if err := writeIWD(r, fixed, hashes, filepath.Join(outDir, iwd)); err != nil {
		return append(lines, fmt.Sprintf("FAILED %s: %v (no output written)", iwd, err))
	}
	return append(lines, done...)
}

// canonical reports whether lumps 0-37 sit back to back from the header, each padded to 4 bytes,
// up to the end of the file. The compiler leaves the last header entry (lump 38) as {0, 0}.
func canonical(b []byte) bool {
	off := uint64(headerSize)
	for i := 0; i < lumpCount-1; i++ {
		o, n := lumpRange(b, i)
		if o != off {
			return false
		}
		off += (n + 3) &^ 3
	}
	o, n := lumpRange(b, lumpCount-1)
	return o == 0 && n == 0 && off == uint64(len(b))
}

// injectGrid inserts a 3-entry grid and one color into a canonical BSP whose lumps 2 and 3 are empty.
// Keys 0, 2^31 and 2^32-1 keep the engine's nearest-entry search inside 0..2 for any key.
func injectGrid(b []byte, sun bool) []byte {
	s := byte(0)
	if sun {
		s = 1
	}
	ins := []byte{
		0x00, 0x00, 0x00, 0x00, s, 0, 0, 0,
		0x00, 0x00, 0x00, 0x80, s, 0, 0, 0,
		0xff, 0xff, 0xff, 0xff, 0xfc | s, 0, 0, 0,
	}
	ins = append(ins, medianColorRecord...)
	at, _ := lumpRange(b, lumpGridEntries)
	out := make([]byte, 0, len(b)+len(ins))
	out = append(append(append(out, b[:at]...), ins...), b[at:]...)
	le.PutUint32(out[8+lumpGridEntries*8:], 3*entrySize)
	le.PutUint32(out[8+lumpGridColors*8:], colorSize)
	le.PutUint32(out[12+lumpGridColors*8:], uint32(at)+3*entrySize)
	for i := lumpGridColors + 1; i < lumpCount-1; i++ {
		le.PutUint32(out[12+i*8:], le.Uint32(out[12+i*8:])+uint32(len(ins)))
	}
	return out
}

func lumpHashes(b []byte) (h [lumpCount][32]byte, ok bool) {
	for i := range h {
		off, n := lumpRange(b, i)
		if off+n > uint64(len(b)) {
			return h, false
		}
		h[i] = sha256.Sum256(b[off : off+n])
	}
	return h, true
}

// writeIWD writes dst via a temp file in its dir: fixed entries deflated, all others copied raw, then self-checked.
func writeIWD(r *zip.ReadCloser, fixed map[string][]byte, hashes map[string][lumpCount][32]byte, dst string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".bsplight-*.iwd")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // removes the output on any failure; a no-op after the rename
	err = writeZip(tmp, r, fixed)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = verify(tmp.Name(), hashes)
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func writeZip(w io.Writer, r *zip.ReadCloser, fixed map[string][]byte) error {
	zw := zip.NewWriter(w)
	for _, f := range r.File {
		b, ok := fixed[f.Name]
		if !ok {
			if err := zw.Copy(f); err != nil {
				return err
			}
			continue
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: f.Modified})
		if err != nil {
			return err
		}
		if _, err := fw.Write(b); err != nil {
			return err
		}
	}
	return zw.Close()
}

// verify re-reads a written IWD: each fixed map must scan OK with every lump but 2 and 3 unchanged.
func verify(p string, want map[string][lumpCount][32]byte) error {
	r, err := zip.OpenReader(p)
	if err != nil {
		return err
	}
	defer r.Close()
	seen := 0
	for _, f := range r.File {
		h, ok := want[f.Name]
		if !ok {
			continue
		}
		seen++
		g, b := inspect("", f)
		if g.verdict != "OK" {
			return fmt.Errorf("self-check %s: verdict %s", f.Name, g.verdict)
		}
		got, ok := lumpHashes(b)
		if !ok {
			return fmt.Errorf("self-check %s: lump past end of file", f.Name)
		}
		for i := range got {
			if i != lumpGridEntries && i != lumpGridColors && got[i] != h[i] {
				return fmt.Errorf("self-check %s: lump %d changed", f.Name, i)
			}
		}
	}
	if seen != len(want) {
		return fmt.Errorf("self-check found %d of %d fixed maps", seen, len(want))
	}
	return nil
}
