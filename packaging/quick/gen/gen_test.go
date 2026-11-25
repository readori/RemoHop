package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIconDrawing(t *testing.T) {
	img := render(64, 0)
	// Corners are transparent, the background cobalt, the mark paper.
	if a := img.NRGBAAt(0, 0).A; a != 0 {
		t.Errorf("corner alpha %d", a)
	}
	if c := img.NRGBAAt(6, 32); c != blue {
		t.Errorf("background %v", c)
	}
	if c := img.NRGBAAt(13, 24); c != paper {
		t.Errorf("upper-left corner stroke %v", c)
	}
	if c := img.NRGBAAt(41, 31); c != paper {
		t.Errorf("arrow tip %v", c)
	}
	if c := img.NRGBAAt(20, 24); c != blue {
		t.Errorf("inside the corners %v", c)
	}
	// A macOS icon leaves a margin.
	if a := render(64, 0.1).NRGBAAt(3, 32).A; a != 0 {
		t.Errorf("margin alpha %d", a)
	}
}

func TestIcoFile(t *testing.T) {
	images := windowsIcon()
	ico := icoFile(images)
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || int(binary.LittleEndian.Uint16(ico[4:])) != len(images) {
		t.Fatal("bad ICO header")
	}
	for i, im := range images {
		e := ico[6+16*i:]
		size, off := int(binary.LittleEndian.Uint32(e[8:])), int(binary.LittleEndian.Uint32(e[12:]))
		if size != len(im.data) || !bytes.Equal(ico[off:off+size], im.data) {
			t.Fatalf("image %d: directory does not match data", i)
		}
		if im.size == 256 {
			if e[0] != 0 {
				t.Errorf("256 is written as 0 in the directory, got %d", e[0])
			}
			if cfg, err := png.DecodeConfig(bytes.NewReader(im.data)); err != nil || cfg.Width != 256 {
				t.Errorf("256-pixel image is not a PNG: %v", err)
			}
			continue
		}
		// DIB: header 40, height doubled, 32 bits, colour rows then mask rows.
		if int(e[0]) != im.size || binary.LittleEndian.Uint32(im.data) != 40 ||
			int(int32(binary.LittleEndian.Uint32(im.data[8:]))) != 2*im.size || binary.LittleEndian.Uint16(im.data[14:]) != 32 {
			t.Errorf("image %d (%dpx): bad bitmap header", i, im.size)
		}
		if want := 40 + 4*im.size*im.size + (im.size+31)/32*4*im.size; len(im.data) != want {
			t.Errorf("image %d (%dpx): %d bytes, want %d", i, im.size, len(im.data), want)
		}
	}
}

func TestIcnsFile(t *testing.T) {
	b := icnsFile()
	if string(b[:4]) != "icns" || int(binary.BigEndian.Uint32(b[4:])) != len(b) {
		t.Fatal("bad icns header")
	}
	seen := map[string]int{}
	for at := 8; at < len(b); {
		typ, n := string(b[at:at+4]), int(binary.BigEndian.Uint32(b[at+4:]))
		cfg, err := png.DecodeConfig(bytes.NewReader(b[at+8 : at+n]))
		if err != nil {
			t.Fatalf("%s is not a PNG: %v", typ, err)
		}
		seen[typ] = cfg.Width
		at += n
	}
	for typ, size := range map[string]int{"ic07": 128, "ic08": 256, "ic09": 512, "ic10": 1024, "ic11": 32, "ic12": 64, "ic13": 256, "ic14": 512} {
		if seen[typ] != size {
			t.Errorf("%s: %d pixels, want %d", typ, seen[typ], size)
		}
	}
}

// The object must be something a linker accepts: one .rsrc section whose
// data entries are relocated against the section, and a tree that leads to
// the icon and the manifest.
func TestSysoFile(t *testing.T) {
	for arch, m := range machines {
		data, err := sysoFile(arch, resources())
		if err != nil {
			t.Fatal(err)
		}
		f, err := pe.NewFile(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: not a COFF object: %v", arch, err)
		}
		if f.Machine != m.machine || len(f.Sections) != 1 || f.Sections[0].Name != ".rsrc" || f.OptionalHeader != nil {
			t.Fatalf("%s: machine %#x, %d sections", arch, f.Machine, len(f.Sections))
		}
		s := f.Sections[0]
		if s.Characteristics != 0x40000040 {
			t.Errorf("%s: section characteristics %#x", arch, s.Characteristics)
		}
		if len(f.Symbols) != 1 || f.Symbols[0].Name != ".rsrc" || f.Symbols[0].SectionNumber != 1 || f.Symbols[0].StorageClass != 3 {
			t.Fatalf("%s: symbols %+v", arch, f.Symbols)
		}
		rsrc, err := s.Data()
		if err != nil {
			t.Fatal(err)
		}
		// In the object, a data entry holds the data's offset in the
		// section; each such field has a relocation.
		leaves, err := walkResources(rsrc, func(off uint32) int { return int(off) })
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		if err := checkLeaves(leaves); err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		if len(s.Relocs) != len(leaves) {
			t.Fatalf("%s: %d relocations for %d resources", arch, len(s.Relocs), len(leaves))
		}
		for _, r := range s.Relocs {
			if r.Type != m.reloc || r.SymbolTableIndex != 0 || int(r.VirtualAddress)+4 > len(rsrc) {
				t.Errorf("%s: relocation %+v", arch, r)
			}
		}
		again, _ := sysoFile(arch, resources())
		if !bytes.Equal(data, again) {
			t.Errorf("%s: output is not reproducible", arch)
		}
	}
	if _, err := sysoFile("riscv64", nil); err == nil {
		t.Error("an object for an unsupported architecture was written")
	}
}

func TestBundle(t *testing.T) {
	dir := t.TempDir()
	thin := func(cpu uint32, fill byte) string {
		b := make([]byte, 5000)
		binary.LittleEndian.PutUint32(b, 0xfeedfacf)
		binary.LittleEndian.PutUint32(b[4:], cpu)
		binary.LittleEndian.PutUint32(b[8:], 3)
		b[4999] = fill
		p := filepath.Join(dir, "bin-"+string('a'+rune(fill)))
		os.WriteFile(p, b, 0o755)
		return p
	}
	arm, amd := thin(cpuARM64, 1), thin(cpuAMD64, 2)
	app := filepath.Join(dir, "RemoHop.app")
	if err := writeApp(app, "arm64="+arm+",amd64="+amd, "v1.2.3-4-gabcdef-dirty", appKinds["app"]); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>1.2.3</string>", "<key>LSUIElement</key>\n\t<true/>", "com.remohop.quick", "<key>CFBundleExecutable</key>\n\t<string>RemoHop</string>",
		"<key>CFBundleIconFile</key>\n\t<string>RemoHop</string>"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("Info.plist lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "Resources", "RemoHop.icns")); err != nil {
		t.Error(err)
	}
	st, err := os.Stat(filepath.Join(app, "Contents", "MacOS", "RemoHop"))
	// Windows has no execute bits; the bundle's mode matters where it is unpacked (macOS).
	if err != nil || runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("program: %v, mode %v", err, st.Mode())
	}
	fat, _ := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "RemoHop"))
	be := binary.BigEndian
	if be.Uint32(fat) != 0xcafebabe || be.Uint32(fat[4:]) != 2 {
		t.Fatal("not a universal binary with two slices")
	}
	for i, want := range []struct {
		cpu  uint32
		fill byte
	}{{cpuARM64, 1}, {cpuAMD64, 2}} {
		e := fat[8+20*i:]
		off, size := int(be.Uint32(e[8:])), int(be.Uint32(e[12:]))
		if be.Uint32(e) != want.cpu || off%(1<<14) != 0 || size != 5000 || fat[off+size-1] != want.fill || be.Uint32(e[16:]) != 14 {
			t.Errorf("slice %d: cpu %#x offset %d size %d", i, be.Uint32(e), off, size)
		}
	}
	// One architecture given under the other's name is refused.
	if err := writeApp(app, "arm64="+amd, "1", appKinds["app"]); err == nil {
		t.Error("an x86-64 program was accepted as the arm64 slice")
	}
	// Quick support: its own name, executable and identifier.
	qs := filepath.Join(dir, "RemoHop Quick Support.app")
	if err := writeApp(qs, "arm64="+arm+",amd64="+amd, "1.2.3", appKinds["quick"]); err != nil {
		t.Fatal(err)
	}
	plist, _ = os.ReadFile(filepath.Join(qs, "Contents", "Info.plist"))
	for _, want := range []string{"<string>com.remohop.quicksupport</string>", "<key>CFBundleExecutable</key>\n\t<string>RemoHop-QuickSupport</string>",
		"<key>CFBundleName</key>\n\t<string>RemoHop Quick Support</string>"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("quick support Info.plist lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(qs, "Contents", "MacOS", "RemoHop-QuickSupport")); err != nil {
		t.Error(err)
	}
	for in, want := range map[string]string{"v1.2.3": "1.2.3", "1.2": "1.2", "dev": "0.0.0", "v0.9.0-rc.1": "0.9.0", "": "0.0.0", "1.2.3.4": "0.0.0"} {
		if got := bundleVersion(in); got != want {
			t.Errorf("bundleVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMsixAssets(t *testing.T) {
	dir := t.TempDir()
	if err := run("", "", "", "", "", "", "", "", dir, appKinds["app"]); err != nil {
		t.Fatal(err)
	}
	// The sizes the manifest's logos need at 100% (Partner Center checks these),
	// and the 200% versions.
	want := map[string][2]int{
		"Square44x44Logo.scale-100.png": {44, 44}, "Square150x150Logo.scale-100.png": {150, 150},
		"Wide310x150Logo.scale-100.png": {310, 150}, "Square310x310Logo.scale-100.png": {310, 310},
		"Square71x71Logo.scale-100.png": {71, 71}, "StoreLogo.scale-100.png": {50, 50},
		"Wide310x150Logo.scale-200.png": {620, 300}, "StoreLogo.scale-200.png": {100, 100},
		"Square44x44Logo.targetsize-256_altform-unplated.png": {256, 256},
	}
	for name, size := range want {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Error(err)
			continue
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		b := img.Bounds()
		if b.Dx() != size[0] || b.Dy() != size[1] {
			t.Errorf("%s is %dx%d, want %dx%d", name, b.Dx(), b.Dy(), size[0], size[1])
		}
		// Transparent corners, the cobalt tile in the middle.
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("%s: corner is not transparent", name)
		}
		if _, _, _, a := img.At(b.Dx()/2, b.Dy()/2).RGBA(); a == 0 {
			t.Errorf("%s: nothing drawn in the middle", name)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != len(msixAssets()) {
		t.Errorf("%d files written, want %d", len(entries), len(msixAssets()))
	}
}
