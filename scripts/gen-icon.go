//go:build ignore

// gen-icon.go 用 Go 标准库从同一份几何描述生成 Windows 图标资源，替代 rsrc 工具：
//   go run scripts/gen-icon.go        （在仓库根目录执行）
//
// 产物：
//   assets/icon.ico                 多尺寸 ICO（PNG 帧，Vista+ 支持）
//   rsrc_windows_amd64.syso        COFF .rsrc 对象，go build 自动链接
//   rsrc_windows_arm64.syso        同上（arm64 重定位类型不同）
//
// 资源 ID 约定与 internal/desktop 对齐：1 = manifest，2 = RT_GROUP_ICON（窗口 IconId / LoadImageW 用），
// 3..6 = RT_ICON 各尺寸。COFF 结构与 akavel/rsrc 产物逐字段一致（含 ADDR32NB 重定位），
// 不做任何网络下载、不引入新依赖。

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"sort"
)

var sizes = []int{16, 32, 48, 256}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-icon:", err)
		os.Exit(1)
	}
}

func run() error {
	for _, p := range []string{"assets", "scripts", "go.mod"} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("请在仓库根目录执行（缺少 %s）: %w", p, err)
		}
	}
	if err := os.MkdirAll("assets", 0o755); err != nil {
		return err
	}

	frames := make([][]byte, len(sizes))
	for i, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(s)); err != nil {
			return fmt.Errorf("编码 %dpx PNG 失败: %w", s, err)
		}
		frames[i] = buf.Bytes()
	}
	if err := os.WriteFile("assets/icon.ico", buildICO(frames), 0o644); err != nil {
		return err
	}

	manifest, err := os.ReadFile("assets/windows.manifest")
	if err != nil {
		return fmt.Errorf("读取 windows.manifest 失败: %w", err)
	}

	group := buildGroupIcon(frames)
	resources := []resource{
		{typ: 24, name: 1, lang: 1033, data: manifest}, // RT_MANIFEST
		{typ: 14, name: 2, lang: 1033, data: group},    // RT_GROUP_ICON
	}
	for i, data := range frames {
		resources = append(resources, resource{typ: 3, name: uint32(3 + i), lang: 1033, data: data})
	}

	for _, arch := range []struct {
		name    string
		machine uint16
		reloc   uint16
	}{
		{"amd64", 0x8664, 0x0003}, // IMAGE_REL_AMD64_ADDR32NB
		{"arm64", 0xaa64, 0x0002}, // IMAGE_REL_ARM64_ADDR32NB
	} {
		out, err := buildSyso(resources, arch.machine, arch.reloc)
		if err != nil {
			return err
		}
		path := "rsrc_windows_" + arch.name + ".syso"
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return err
		}
		fmt.Printf("  %s: %d bytes, %d resources\n", path, len(out), len(resources))
	}
	fmt.Printf("  assets/icon.ico: %d frames (16/32/48/256)\n", len(frames))
	return nil
}

/* ---------- 图标绘制：SDF + 逐层 alpha 合成，带 1px 边缘抗锯齿 ---------- */

func render(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	sc := s / 1024.0
	white := color.RGBA{255, 255, 255, 255}
	outer := color.RGBA{21, 26, 36, 255}
	from := color.RGBA{91, 124, 255, 255}
	to := color.RGBA{52, 201, 163, 255}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			c := color.RGBA{}

			// 深色外框（圆角方块）
			dOuter := sdRoundBox(px, py, 512*sc, 512*sc, 440*sc, 440*sc, 208*sc)
			c = over(c, outer, cover(dOuter))

			// 渐变内块 + 顶部高光
			dInner := sdRoundBox(px, py, 512*sc, 512*sc, 400*sc, 400*sc, 176*sc)
			aInner := cover(dInner)
			if aInner > 0 {
				t := clamp((px/s+py/s)/2, 0, 1)
				c = over(c, lerp(from, to, t), aInner)
				sheen := 0.18 * clamp(1-(py/s)/0.55, 0, 1)
				c = over(c, white, sheen*aInner)
			}

			// 内描边
			dStroke := math.Abs(sdRoundBox(px, py, 512*sc, 512*sc, 396*sc, 396*sc, 172*sc)) - 4*sc
			c = over(c, white, cover(dStroke)*0.16)

			// 树形主干与分支（capsule 并集）
			segments := [][4]float64{
				{512, 716, 512, 456},
				{512, 556, 316, 556},
				{316, 556, 316, 392},
				{512, 556, 708, 556},
				{708, 556, 708, 392},
			}
			dLine := math.Inf(1)
			for _, seg := range segments {
				d := sdSegment(px, py, seg[0]*sc, seg[1]*sc, seg[2]*sc, seg[3]*sc) - 28*sc
				if d < dLine {
					dLine = d
				}
			}
			c = over(c, white, cover(dLine))

			// 三个节点
			for _, node := range [][3]float64{{512, 732, 76}, {316, 364, 76}, {708, 364, 76}} {
				d := math.Hypot(px-node[0]*sc, py-node[1]*sc) - node[2]*sc
				c = over(c, white, cover(d))
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func cover(d float64) float64 { return clamp(0.5-d, 0, 1) }

func lerp(a, b color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		R: uint8(clamp(float64(a.R)+(float64(b.R)-float64(a.R))*t, 0, 255)),
		G: uint8(clamp(float64(a.G)+(float64(b.G)-float64(a.G))*t, 0, 255)),
		B: uint8(clamp(float64(a.B)+(float64(b.B)-float64(a.B))*t, 0, 255)),
		A: 255,
	}
}

func over(dst, src color.RGBA, a float64) color.RGBA {
	sa := float64(src.A) / 255 * clamp(a, 0, 1)
	da := float64(dst.A) / 255
	oa := sa + da*(1-sa)
	if oa <= 0 {
		return color.RGBA{}
	}
	mix := func(d, s uint8) uint8 {
		return uint8(clamp((float64(s)*sa+float64(d)*da*(1-sa))/oa, 0, 255))
	}
	return color.RGBA{R: mix(dst.R, src.R), G: mix(dst.G, src.G), B: mix(dst.B, src.B), A: uint8(clamp(oa*255, 0, 255))}
}

func sdRoundBox(px, py, cx, cy, hx, hy, r float64) float64 {
	qx := math.Abs(px-cx) - (hx - r)
	qy := math.Abs(py-cy) - (hy - r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func sdSegment(px, py, ax, ay, bx, by float64) float64 {
	pax, pay := px-ax, py-ay
	bax, bay := bx-ax, by-ay
	h := clamp((pax*bax+pay*bay)/(bax*bax+bay*bay), 0, 1)
	return math.Hypot(pax-bax*h, pay-bay*h)
}

/* ---------- ICO ---------- */

func buildICO(frames [][]byte) []byte {
	header := 6 + 16*len(frames)
	buf := make([]byte, header)
	le16(buf[0:], 0)
	le16(buf[2:], 1)
	le16(buf[4:], uint16(len(frames)))
	off := header
	for i, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0
		}
		e := buf[6+16*i : 6+16*(i+1)]
		e[0], e[1], e[2], e[3] = w, w, 0, 0
		le16(e[4:], 1)
		le16(e[6:], 32)
		le32(e[8:], uint32(len(frames[i])))
		le32(e[12:], uint32(off))
		off += len(frames[i])
	}
	for _, f := range frames {
		buf = append(buf, f...)
	}
	return buf
}

// buildGroupIcon 生成 GRPICONDIR：窗口 IconId=2 与托盘 LoadImageW(2) 读的就是它。
func buildGroupIcon(frames [][]byte) []byte {
	buf := make([]byte, 6+14*len(frames))
	le16(buf[0:], 0)
	le16(buf[2:], 1)
	le16(buf[4:], uint16(len(frames)))
	for i, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0
		}
		e := buf[6+14*i : 6+14*(i+1)]
		e[0], e[1], e[2], e[3] = w, w, 0, 0
		le16(e[4:], 1)
		le16(e[6:], 32)
		le32(e[8:], uint32(len(frames[i])))
		le16(e[12:], uint16(3+i)) // 对应 RT_ICON 资源 ID
	}
	return buf
}

/* ---------- COFF .rsrc 对象 ---------- */

type resource struct {
	typ  uint32
	name uint32
	lang uint32
	data []byte
}

type resKey struct{ typ, name, lang uint32 }

func buildSyso(resources []resource, machine, relocType uint16) ([]byte, error) {
	// 按 type / name / lang 升序排列：PE 资源目录要求 ID 项有序。
	sort.Slice(resources, func(i, j int) bool {
		a, b := resources[i], resources[j]
		if a.typ != b.typ {
			return a.typ < b.typ
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.lang < b.lang
	})

	typeIDs := distinctTypes(resources)
	typeOffset := map[uint32]uint32{}
	nameOffset := map[resKey]uint32{}
	dataOffset := map[resKey]uint32{}
	rawOffset := map[resKey]uint32{}

	cursor := uint32(16 + 8*len(typeIDs)) // 根目录
	for _, t := range typeIDs {
		typeOffset[t] = cursor
		cursor += uint32(16 + 8*countNames(resources, t))
	}
	for _, r := range resources {
		k := resKey{r.typ, r.name, r.lang}
		nameOffset[k] = cursor
		cursor += 16 + 8 // 一个语言项
	}
	for _, r := range resources {
		k := resKey{r.typ, r.name, r.lang}
		dataOffset[k] = cursor
		cursor += 16 // IMAGE_RESOURCE_DATA_ENTRY
	}
	cursor = align4(cursor)
	for _, r := range resources {
		k := resKey{r.typ, r.name, r.lang}
		rawOffset[k] = cursor
		cursor += align4(uint32(len(r.data)))
	}
	total := cursor

	res := make([]byte, total)
	// 根目录
	putDirHeader(res, 0, uint16(len(typeIDs)))
	for i, t := range typeIDs {
		putDirEntry(res, uint32(16+8*i), t, typeOffset[t]|0x80000000)
	}
	// 类型目录
	for _, t := range typeIDs {
		base := typeOffset[t]
		names := namesOf(resources, t)
		putDirHeader(res, base, uint16(len(names)))
		for i, n := range names {
			k := resKey{t, n, 1033}
			putDirEntry(res, base+uint32(16+8*i), n, nameOffset[k]|0x80000000)
		}
	}
	// 名称目录 → 数据项
	for _, r := range resources {
		k := resKey{r.typ, r.name, r.lang}
		base := nameOffset[k]
		putDirHeader(res, base, 1)
		putDirEntry(res, base+16, r.lang, dataOffset[k])
	}
	// 数据项（OffsetToData 需要 ADDR32NB 重定位）
	for _, r := range resources {
		k := resKey{r.typ, r.name, r.lang}
		le32(res[dataOffset[k]:], rawOffset[k])
		le32(res[dataOffset[k]+4:], uint32(len(r.data)))
		le32(res[dataOffset[k]+8:], 0) // CodePage
		le32(res[dataOffset[k]+12:], 0)
		copy(res[rawOffset[k]:], r.data)
	}

	const headerSize = 60 // 20 (COFF) + 40 (section header)
	ptrReloc := headerSize + total
	ptrSym := ptrReloc + uint32(10*len(resources))

	out := make([]byte, headerSize)
	le16(out[0:], machine)
	le16(out[2:], 1) // NumberOfSections
	le32(out[8:], ptrSym)
	le32(out[12:], 1)      // NumberOfSymbols
	le16(out[16:], 0)      // SizeOfOptionalHeader
	le16(out[18:], 0x0104) // Characteristics（与 rsrc 产物一致）
	copy(out[20:28], ".rsrc")
	le32(out[36:], total) // SizeOfRawData
	le32(out[40:], headerSize)
	le32(out[44:], ptrReloc)
	le16(out[52:], uint16(len(resources)))
	le16(out[54:], 0)
	le32(out[56:], 0x40000040) // CNT_INITIALIZED_DATA | MEM_READ

	out = append(out, res...)
	// 重定位：每条指向一个 IMAGE_RESOURCE_DATA_ENTRY.OffsetToData
	for _, r := range resources {
		k := resKey{r.typ, r.name, r.lang}
		rel := make([]byte, 10)
		le32(rel[0:], dataOffset[k])
		le32(rel[4:], 0) // SymbolTableIndex
		le16(rel[8:], relocType)
		out = append(out, rel...)
	}
	// 符号表：短名 ".rsrc"、SectionNumber=1、STATIC
	sym := make([]byte, 18)
	copy(sym[0:8], ".rsrc")
	le16(sym[12:], 1)
	sym[16] = 3 // IMAGE_SYM_CLASS_STATIC
	out = append(out, sym...)
	strtab := make([]byte, 4)
	le32(strtab, 4) // 仅有长度字段
	out = append(out, strtab...)
	return out, nil
}

func putDirHeader(buf []byte, off uint32, idEntries uint16) {
	le32(buf[off:], 0)   // Characteristics
	le32(buf[off+4:], 0) // TimeDateStamp
	le16(buf[off+8:], 0) // MajorVersion
	le16(buf[off+10:], 0)
	le16(buf[off+12:], 0) // NumberOfNamedEntries
	le16(buf[off+14:], idEntries)
}

func putDirEntry(buf []byte, off, id, data uint32) {
	le32(buf[off:], id)
	le32(buf[off+4:], data)
}

func distinctTypes(rs []resource) []uint32 {
	seen := map[uint32]bool{}
	var out []uint32
	for _, r := range rs {
		if !seen[r.typ] {
			seen[r.typ] = true
			out = append(out, r.typ)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func countNames(rs []resource, typ uint32) int { return len(namesOf(rs, typ)) }

func namesOf(rs []resource, typ uint32) []uint32 {
	seen := map[uint32]bool{}
	var out []uint32
	for _, r := range rs {
		if r.typ == typ && !seen[r.name] {
			seen[r.name] = true
			out = append(out, r.name)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func align4(v uint32) uint32 { return (v + 3) &^ 3 }

func le16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func le32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }
