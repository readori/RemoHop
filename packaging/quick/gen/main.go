// Command gen makes the packaging pieces of the double-click build (see
// packaging/quick/README.md): the Windows resource objects that give
// RemoHop.exe its icon and manifest, the icon files, and the RemoHop.app
// bundle for macOS. It needs nothing but Go, so the packages can be built
// on any system:
//
//	go run ./packaging/quick/gen -syso cmd/remohop-quick
//	go run ./packaging/quick/gen -ico dist/quick/RemoHop.ico -icns dist/quick/RemoHop.icns
//	go run ./packaging/quick/gen -app dist/quick/RemoHop.app -version v1.2.3 -binary arm64=PATH,amd64=PATH
//	go run ./packaging/quick/gen -verify dist/quick/RemoHop.exe
//	go run ./packaging/quick/gen -msix-assets DIR      (the Assets folder of the MSIX package)
package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	syso := flag.String("syso", "", "write rsrc_windows_{amd64,arm64,386}.syso into this directory (the main package's)")
	ico := flag.String("ico", "", "write the Windows icon to this file")
	icns := flag.String("icns", "", "write the macOS icon to this file")
	pngOut := flag.String("png", "", "write a 512-pixel PNG of the icon to this file")
	app := flag.String("app", "", "assemble the macOS bundle at this path (ends in .app)")
	binary := flag.String("binary", "", "with -app: the program, PATH or arm64=PATH,amd64=PATH for a universal binary")
	version := flag.String("version", "0.0.0", "with -app: the version (a leading v and anything after the numbers is dropped)")
	verify := flag.String("verify", "", "check that this Windows program carries the icon and the manifest")
	msix := flag.String("msix-assets", "", "write the MSIX package's logos (Store, tiles, app list) into this directory")
	tray := flag.String("tray", "", "write the desktop app's tray icons (tray.png, tray-template.png) into this directory")
	flag.Parse()
	if *tray != "" {
		if err := os.WriteFile(filepath.Join(*tray, "tray.png"), pngBytes(render(64, 0)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(*tray, "tray-template.png"), pngBytes(trayTemplate(64)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*syso, *ico, *icns, *pngOut, *app, *binary, *version, *verify, *msix); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(syso, ico, icns, pngOut, app, binary, version, verify, msix string) error {
	did := false
	write := func(path string, data []byte, mode os.FileMode) error {
		did = true
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, data, mode)
	}
	if syso != "" {
		for arch := range machines {
			data, err := sysoFile(arch, resources())
			if err != nil {
				return err
			}
			if err := write(filepath.Join(syso, "rsrc_windows_"+arch+".syso"), data, 0o644); err != nil {
				return err
			}
		}
	}
	if ico != "" {
		if err := write(ico, icoFile(windowsIcon()), 0o644); err != nil {
			return err
		}
	}
	if icns != "" {
		if err := write(icns, icnsFile(), 0o644); err != nil {
			return err
		}
	}
	if pngOut != "" {
		if err := write(pngOut, pngBytes(render(512, 0)), 0o644); err != nil {
			return err
		}
	}
	if app != "" {
		did = true
		if err := writeApp(app, binary, version); err != nil {
			return err
		}
	}
	if msix != "" {
		if err := writeMsixAssets(msix, func(p string, data []byte) error { return write(p, data, 0o644) }); err != nil {
			return err
		}
	}
	if verify != "" {
		did = true
		if err := verifyExe(verify); err != nil {
			return fmt.Errorf("%s: %w", verify, err)
		}
		fmt.Println(verify + ": icon and manifest present")
	}
	if !did {
		return errors.New("nothing to do; see -help")
	}
	return nil
}

// bundleVersion reduces a version like v1.2.3-4-gabcdef to the dotted
// numbers a bundle's version must be.
func bundleVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	end := 0
	for end < len(v) && (v[end] >= '0' && v[end] <= '9' || v[end] == '.') {
		end++
	}
	v = strings.Trim(v[:end], ".")
	if v == "" || len(strings.Split(v, ".")) > 3 {
		return "0.0.0"
	}
	return v
}

// infoPlist describes the bundle. LSUIElement keeps it out of the Dock and
// the app switcher: the program has no window of its own (its window is a
// browser page), and a Dock icon for a program that never opens one would
// bounce and then read as "not responding".
func infoPlist(version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleLocalizations</key>
	<array>
		<string>en</string>
		<string>zh_CN</string>
	</array>
	<key>CFBundleDisplayName</key>
	<string>RemoHop</string>
	<key>CFBundleExecutable</key>
	<string>RemoHop</string>
	<key>CFBundleIconFile</key>
	<string>RemoHop</string>
	<key>CFBundleIdentifier</key>
	<string>com.remohop.quick</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>RemoHop</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>` + version + `</string>
	<key>CFBundleVersion</key>
	<string>` + version + `</string>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.utilities</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
`
}

// writeApp assembles RemoHop.app:
//
//	Contents/Info.plist
//	Contents/PkgInfo
//	Contents/MacOS/RemoHop       the program (cmd/remohop-quick built for darwin)
//	Contents/Resources/RemoHop.icns
func writeApp(app, binary, version string) error {
	if !strings.HasSuffix(app, ".app") {
		return errors.New("-app must name a directory ending in .app")
	}
	if binary == "" {
		return errors.New("-app needs -binary")
	}
	program, err := loadProgram(binary)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(app); err != nil {
		return err
	}
	files := []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{"Contents/Info.plist", []byte(infoPlist(bundleVersion(version))), 0o644},
		{"Contents/PkgInfo", []byte("APPL????"), 0o644},
		{"Contents/MacOS/RemoHop", program, 0o755},
		{"Contents/Resources/RemoHop.icns", icnsFile(), 0o644},
	}
	for _, f := range files {
		p := filepath.Join(app, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			return err
		}
	}
	return nil
}

// loadProgram reads the program for the bundle: one Mach-O file, or
// "arm64=PATH,amd64=PATH" joined into a universal binary.
func loadProgram(spec string) ([]byte, error) {
	if !strings.Contains(spec, "=") {
		return os.ReadFile(spec)
	}
	var slices []machoSlice
	for _, part := range strings.Split(spec, ",") {
		arch, path, _ := strings.Cut(part, "=")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		s, err := sliceOf(arch, data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		slices = append(slices, s)
	}
	return universal(slices), nil
}

type machoSlice struct {
	cpu, sub uint32
	data     []byte
}

// Mach-O CPU types.
const (
	cpuAMD64 = 0x01000007
	cpuARM64 = 0x0100000c
)

func sliceOf(arch string, data []byte) (machoSlice, error) {
	var want uint32
	switch arch {
	case "amd64":
		want = cpuAMD64
	case "arm64":
		want = cpuARM64
	default:
		return machoSlice{}, fmt.Errorf("unknown architecture %q (amd64 or arm64)", arch)
	}
	// A 64-bit little-endian Mach-O starts with the magic, CPU type and subtype.
	if len(data) < 12 || binary.LittleEndian.Uint32(data) != 0xfeedfacf {
		return machoSlice{}, errors.New("not a 64-bit Mach-O program")
	}
	cpu, sub := binary.LittleEndian.Uint32(data[4:]), binary.LittleEndian.Uint32(data[8:])
	if cpu != want {
		return machoSlice{}, fmt.Errorf("built for CPU type %#x, not %s", cpu, arch)
	}
	return machoSlice{cpu, sub, data}, nil
}

// universal joins thin Mach-O programs into one "fat" file, as lipo does:
// a big-endian table of slices, each slice starting on a 16 KiB boundary.
func universal(slices []machoSlice) []byte {
	const align = 14
	var out bytes.Buffer
	be := func(v uint32) { binary.Write(&out, binary.BigEndian, v) }
	be(0xcafebabe)
	be(uint32(len(slices)))
	offset := uint32(8 + 20*len(slices))
	var offsets []uint32
	for _, s := range slices {
		offset = (offset + 1<<align - 1) &^ (1<<align - 1)
		offsets = append(offsets, offset)
		be(s.cpu)
		be(s.sub)
		be(offset)
		be(uint32(len(s.data)))
		be(align)
		offset += uint32(len(s.data))
	}
	for i, s := range slices {
		out.Write(make([]byte, int(offsets[i])-out.Len()))
		out.Write(s.data)
	}
	return out.Bytes()
}

// verifyExe checks a linked Windows program: it has a resource directory
// with an icon group, the icon images and the manifest, and it is a GUI
// program (no console window).
func verifyExe(path string) error {
	f, err := pe.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var dir pe.DataDirectory
	var subsystem uint16
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		dir, subsystem = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE], oh.Subsystem
	case *pe.OptionalHeader32:
		dir, subsystem = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE], oh.Subsystem
	default:
		return errors.New("not a linked Windows program")
	}
	if subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		return fmt.Errorf("subsystem %d: not a GUI program, a console window would appear (link with -H windowsgui)", subsystem)
	}
	if dir.VirtualAddress == 0 || dir.Size == 0 {
		return errors.New("no resources: the .syso file was not in the main package's directory when it was built")
	}
	for _, s := range f.Sections {
		if dir.VirtualAddress < s.VirtualAddress || dir.VirtualAddress >= s.VirtualAddress+s.VirtualSize {
			continue
		}
		data, err := s.Data()
		if err != nil {
			return err
		}
		start := dir.VirtualAddress - s.VirtualAddress
		leaves, err := walkResources(data[start:], func(rva uint32) int { return int(rva) - int(dir.VirtualAddress) })
		if err != nil {
			return err
		}
		return checkLeaves(leaves)
	}
	return errors.New("the resource directory is in no section")
}

type leaf struct {
	typ, id int
	data    []byte
}

// walkResources reads a resource tree. addr turns the address in a data
// entry into an offset in rsrc.
func walkResources(rsrc []byte, addr func(uint32) int) ([]leaf, error) {
	le := binary.LittleEndian
	bad := errors.New("malformed resource directory")
	entries := func(at int) ([][2]uint32, error) {
		if at < 0 || at+16 > len(rsrc) {
			return nil, bad
		}
		n := int(le.Uint16(rsrc[at+12:])) + int(le.Uint16(rsrc[at+14:]))
		if at+16+8*n > len(rsrc) {
			return nil, bad
		}
		out := make([][2]uint32, n)
		for i := range out {
			out[i] = [2]uint32{le.Uint32(rsrc[at+16+8*i:]), le.Uint32(rsrc[at+20+8*i:])}
		}
		return out, nil
	}
	const subdir = 0x80000000
	var leaves []leaf
	types, err := entries(0)
	if err != nil {
		return nil, err
	}
	for _, t := range types {
		ids, err := entries(int(t[1] &^ subdir))
		if err != nil || t[1]&subdir == 0 {
			return nil, bad
		}
		for _, id := range ids {
			langs, err := entries(int(id[1] &^ subdir))
			if err != nil || id[1]&subdir == 0 || len(langs) == 0 {
				return nil, bad
			}
			at := int(langs[0][1])
			if langs[0][1]&subdir != 0 || at+16 > len(rsrc) {
				return nil, bad
			}
			off, size := addr(le.Uint32(rsrc[at:])), int(le.Uint32(rsrc[at+4:]))
			if off < 0 || size < 0 || off+size > len(rsrc) {
				return nil, bad
			}
			leaves = append(leaves, leaf{int(t[0]), int(id[0]), rsrc[off : off+size]})
		}
	}
	return leaves, nil
}

func checkLeaves(leaves []leaf) error {
	icons := map[int][]byte{}
	var group, man []byte
	for _, l := range leaves {
		switch l.typ {
		case rtIcon:
			icons[l.id] = l.data
		case rtGroupIcon:
			if group == nil {
				group = l.data
			}
		case rtManifest:
			man = l.data
		}
	}
	if !bytes.Contains(man, []byte("requestedExecutionLevel")) {
		return errors.New("no application manifest")
	}
	if len(group) < 6 {
		return errors.New("no icon group")
	}
	n := int(binary.LittleEndian.Uint16(group[4:]))
	if n == 0 || len(group) < 6+14*n {
		return errors.New("empty icon group")
	}
	for i := 0; i < n; i++ {
		e := group[6+14*i:]
		id, size := int(binary.LittleEndian.Uint16(e[12:])), int(binary.LittleEndian.Uint32(e[8:]))
		if len(icons[id]) != size {
			return fmt.Errorf("icon %d of the group: image missing or of the wrong size", id)
		}
	}
	return nil
}
