// Command machocmp checks that a signed macOS binary (or app bundle) made
// elsewhere contains exactly the code this repository builds, and nothing
// but an Apple code signature on top of it.
//
// The release's macOS files are built, signed and notarized in another
// repository, where the Apple credentials live (see .github/workflows/release.yml).
// Go builds are reproducible: the same source, Go version and flags give the
// same bytes on any host. So the release workflow rebuilds the unsigned
// binaries from the tagged commit and runs this command, which accepts a
// signed slice only if
//
//   - every byte before the code signature equals the rebuilt slice, apart
//     from the fields codesign rewrites: the LC_CODE_SIGNATURE load command
//     (and the header's command count and size it changes), and the size of
//     the __LINKEDIT segment that holds the signature;
//   - nothing follows the signature (but what the rebuild had there);
//   - the signature's code directories cover exactly that code, their page
//     hashes match it, it carries no entitlements, and (when asked) the
//     hardened runtime flag, the signing identifier and the Apple team ID
//     are the expected ones.
//
// Whether the signature itself is valid (the certificate chain to Apple and
// the notarization) is checked by codesign, spctl and stapler on a Mac; this
// command makes sure that what is signed is our code.
//
// Usage:
//
//	machocmp [flags] SIGNED REF [REF...]
//	    SIGNED is a universal or thin Mach-O; each REF a thin, unsigned (or
//	    ad-hoc signed) rebuild, one per architecture of SIGNED.
//	machocmp [flags] -app SIGNED.app REF.app
//	    every file of the rebuilt bundle must be in the signed one with the
//	    same bytes, the executables under Contents/MacOS compared as above;
//	    the signed bundle may add only its signature files.
//	machocmp -same DIR1 DIR2
//	    the two trees hold the same files with the same bytes (no symlinks).
//
// Flags: -team ID (Apple team identifier), -id IDENTIFIER (signing
// identifier), -runtime (require the hardened runtime).
package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"debug/macho"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type want struct {
	team    string
	ident   string
	runtime bool
}

func main() {
	var w want
	app := flag.Bool("app", false, "compare app bundles")
	same := flag.Bool("same", false, "compare two trees byte for byte")
	flag.StringVar(&w.team, "team", "", "required Apple team identifier")
	flag.StringVar(&w.ident, "id", "", "required signing identifier")
	flag.BoolVar(&w.runtime, "runtime", false, "require the hardened runtime")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: machocmp [-team ID] [-id IDENT] [-runtime] SIGNED REF...\n       machocmp [flags] -app SIGNED.app REF.app\n       machocmp -same DIR1 DIR2")
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	var err error
	switch {
	case *app && *same:
		err = errors.New("-app and -same are exclusive")
	case *app || *same:
		if len(args) != 2 {
			flag.Usage()
			os.Exit(2)
		}
		if *app {
			err = compareApps(args[0], args[1], w)
		} else {
			err = sameTrees(args[0], args[1])
		}
	default:
		if len(args) < 2 {
			flag.Usage()
			os.Exit(2)
		}
		err = compareFiles(args[0], args[1:], w)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "machocmp:", err)
		os.Exit(1)
	}
}

// compareFiles checks every slice of the signed file against the rebuilt
// slice of the same architecture.
func compareFiles(signedPath string, refPaths []string, w want) error {
	signed, err := os.ReadFile(signedPath)
	if err != nil {
		return err
	}
	refs := map[string][]byte{}
	for _, p := range refPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sl, err := slices(b)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		for k, v := range sl {
			if _, dup := refs[k]; dup {
				return fmt.Errorf("two rebuilt slices for %s", k)
			}
			refs[k] = v
		}
	}
	if err := compareSlices(signedPath, signed, refs, w); err != nil {
		return err
	}
	fmt.Printf("%s: same code as the rebuild (%s)\n", signedPath, strings.Join(keys(refs), ", "))
	return nil
}

func compareSlices(name string, signed []byte, refs map[string][]byte, w want) error {
	ss, err := slices(signed)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if len(ss) != len(refs) {
		return fmt.Errorf("%s has %d architectures (%s), the rebuild %d (%s)",
			name, len(ss), strings.Join(keys(ss), ", "), len(refs), strings.Join(keys(refs), ", "))
	}
	for arch, s := range ss {
		r, ok := refs[arch]
		if !ok {
			return fmt.Errorf("%s: architecture %s was not rebuilt", name, arch)
		}
		if err := compareSlice(s, r, w); err != nil {
			return fmt.Errorf("%s (%s): %w", name, arch, err)
		}
	}
	return nil
}

func keys(m map[string][]byte) []string {
	var k []string
	for a := range m {
		k = append(k, a)
	}
	sort.Strings(k)
	return k
}

// slices splits a universal binary into its thin slices, keyed by CPU type
// and subtype; a thin binary is one slice.
func slices(b []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(b) >= 4 && binary.BigEndian.Uint32(b) == macho.MagicFat {
		n := binary.BigEndian.Uint32(b[4:])
		if n == 0 || n > 8 || len(b) < 8+int(n)*20 {
			return nil, errors.New("bad universal header")
		}
		for i := 0; i < int(n); i++ {
			e := b[8+i*20:]
			cpu, sub := binary.BigEndian.Uint32(e), binary.BigEndian.Uint32(e[4:])
			off, size := uint64(binary.BigEndian.Uint32(e[8:])), uint64(binary.BigEndian.Uint32(e[12:]))
			if off+size > uint64(len(b)) {
				return nil, errors.New("universal slice beyond the end of the file")
			}
			k := archKey(cpu, sub)
			if _, dup := out[k]; dup {
				return nil, fmt.Errorf("two slices for %s", k)
			}
			out[k] = b[off : off+size]
		}
		return out, nil
	}
	if len(b) < 32 || binary.LittleEndian.Uint32(b) != macho.Magic64 {
		return nil, errors.New("not a 64-bit Mach-O or universal binary")
	}
	out[archKey(binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint32(b[8:]))] = b
	return out, nil
}

func archKey(cpu, sub uint32) string {
	// The high bits of the subtype are capability flags (e.g. pointer
	// authentication), not part of the architecture.
	return fmt.Sprintf("%v/%d", macho.Cpu(cpu), sub&0x00ffffff)
}

const (
	lcSegment64     = 0x19
	lcCodeSignature = 0x1d
)

// layout is what compareSlice needs to know about one thin Mach-O.
type layout struct {
	sigCmd       int // offset of LC_CODE_SIGNATURE, or -1
	sigOff       int // dataoff of the signature
	sigSize      int
	linkeditCmd  int // offset of the __LINKEDIT segment command, or -1
	linkeditOff  uint64
	linkeditSize uint64
}

func parse(b []byte) (*layout, error) {
	le := binary.LittleEndian
	if len(b) < 32 || le.Uint32(b) != macho.Magic64 {
		return nil, errors.New("not a 64-bit little-endian Mach-O")
	}
	ncmds, sizeofcmds := int(le.Uint32(b[16:])), int(le.Uint32(b[20:]))
	if 32+sizeofcmds > len(b) {
		return nil, errors.New("load commands beyond the end of the file")
	}
	l := &layout{sigCmd: -1, linkeditCmd: -1}
	off := 32
	for i := 0; i < ncmds; i++ {
		if off+8 > 32+sizeofcmds {
			return nil, errors.New("truncated load commands")
		}
		cmd, size := le.Uint32(b[off:]), int(le.Uint32(b[off+4:]))
		if size < 8 || off+size > 32+sizeofcmds {
			return nil, errors.New("bad load command size")
		}
		switch cmd {
		case lcCodeSignature:
			if size != 16 || l.sigCmd >= 0 {
				return nil, errors.New("bad or repeated LC_CODE_SIGNATURE")
			}
			if i != ncmds-1 {
				return nil, errors.New("LC_CODE_SIGNATURE is not the last load command")
			}
			l.sigCmd = off
			l.sigOff, l.sigSize = int(le.Uint32(b[off+8:])), int(le.Uint32(b[off+12:]))
		case lcSegment64:
			if size < 72 {
				return nil, errors.New("short segment command")
			}
			if string(bytes.TrimRight(b[off+8:off+24], "\x00")) == "__LINKEDIT" {
				l.linkeditCmd = off
				l.linkeditOff, l.linkeditSize = le.Uint64(b[off+40:]), le.Uint64(b[off+48:])
			}
		}
		off += size
	}
	if off != 32+sizeofcmds {
		return nil, errors.New("load commands do not fill sizeofcmds")
	}
	if l.sigCmd >= 0 && (l.sigOff < off || l.sigOff+l.sigSize > len(b)) {
		return nil, errors.New("code signature outside the file")
	}
	return l, nil
}

// canonical returns the bytes of a slice that codesign leaves alone: the
// file up to its signature, with the signature's load command removed (as
// if never added) and __LINKEDIT's sizes zeroed.
func canonical(b []byte, l *layout) []byte {
	le := binary.LittleEndian
	end := len(b)
	if l.sigCmd >= 0 {
		end = l.sigOff
	}
	c := bytes.Clone(b[:end])
	if l.sigCmd >= 0 {
		clear(c[l.sigCmd : l.sigCmd+16])
		le.PutUint32(c[16:], le.Uint32(c[16:])-1)
		le.PutUint32(c[20:], le.Uint32(c[20:])-16)
	}
	if l.linkeditCmd >= 0 {
		clear(c[l.linkeditCmd+32 : l.linkeditCmd+40]) // vmsize
		clear(c[l.linkeditCmd+48 : l.linkeditCmd+56]) // filesize
	}
	return c
}

func compareSlice(signed, ref []byte, w want) error {
	ls, err := parse(signed)
	if err != nil {
		return fmt.Errorf("signed: %w", err)
	}
	lr, err := parse(ref)
	if err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}
	if ls.sigCmd < 0 {
		return errors.New("the signed binary has no code signature")
	}
	if ls.linkeditCmd < 0 || ls.linkeditOff+ls.linkeditSize < uint64(ls.sigOff+ls.sigSize) {
		return errors.New("the code signature is not inside __LINKEDIT")
	}
	cs, cr := canonical(signed, ls), canonical(ref, lr)
	if len(cs) != len(cr) {
		return fmt.Errorf("the code is %d bytes, the rebuild's %d", len(cs), len(cr))
	}
	if i := firstDiff(cs, cr); i >= 0 {
		return fmt.Errorf("differs from the rebuild at offset %#x", i)
	}
	sig := signed[ls.sigOff : ls.sigOff+ls.sigSize]
	blobLen, err := checkSignature(sig, signed[:ls.sigOff], w)
	if err != nil {
		return err
	}
	// codesign does not clear the slack after a signature that is shorter
	// than the one it replaced: there the bytes of the rebuild's own
	// (linker-made, ad-hoc) signature remain. Anything else is extra data.
	for i := ls.sigOff + blobLen; i < len(signed); i++ {
		if c := signed[i]; c != 0 && (i >= len(ref) || ref[i] != c) {
			return fmt.Errorf("data after the code signature (offset %#x)", i)
		}
	}
	return nil
}

func firstDiff(a, b []byte) int {
	for i := range a {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// Code signing blob magics and slot types (xnu osfmk/kern/cs_blobs.h).
const (
	csMagicEmbeddedSignature = 0xfade0cc0
	csMagicCodeDirectory     = 0xfade0c02
	csSlotCodeDirectory      = 0
	csSlotEntitlements       = 5
	csSlotDEREntitlements    = 7
	csSlotAlternateFirst     = 0x1000
	csSlotAlternateLast      = 0x1004
	csRuntime                = 0x10000
)

// checkSignature validates the embedded signature's structure against the
// code it covers and returns the length of the signature blob.
func checkSignature(sig, code []byte, w want) (int, error) {
	be := binary.BigEndian
	if len(sig) < 12 || be.Uint32(sig) != csMagicEmbeddedSignature {
		return 0, errors.New("the code signature is not an embedded signature")
	}
	length, count := int(be.Uint32(sig[4:])), int(be.Uint32(sig[8:]))
	if length > len(sig) || 12+count*8 > length {
		return 0, errors.New("malformed code signature")
	}
	sig = sig[:length]
	cds := 0
	for i := 0; i < count; i++ {
		typ, off := be.Uint32(sig[12+i*8:]), int(be.Uint32(sig[16+i*8:]))
		if off+8 > length {
			return 0, errors.New("signature blob outside the signature")
		}
		blen := int(be.Uint32(sig[off+4:]))
		if blen < 8 || off+blen > length {
			return 0, errors.New("signature blob outside the signature")
		}
		blob := sig[off : off+blen]
		switch {
		case typ == csSlotEntitlements || typ == csSlotDEREntitlements:
			return 0, errors.New("the signature carries entitlements; the release is signed without any")
		case typ == csSlotCodeDirectory || (typ >= csSlotAlternateFirst && typ <= csSlotAlternateLast):
			if err := checkCodeDirectory(blob, code, w); err != nil {
				return 0, err
			}
			cds++
		}
	}
	if cds == 0 {
		return 0, errors.New("the signature has no code directory")
	}
	return length, nil
}

func checkCodeDirectory(cd, code []byte, w want) error {
	be := binary.BigEndian
	if len(cd) < 44 || be.Uint32(cd) != csMagicCodeDirectory {
		return errors.New("bad code directory")
	}
	version, flags := be.Uint32(cd[8:]), be.Uint32(cd[12:])
	hashOff, identOff := int(be.Uint32(cd[16:])), int(be.Uint32(cd[20:]))
	nSpecial, nCode := int(be.Uint32(cd[24:])), int(be.Uint32(cd[28:]))
	codeLimit := uint64(be.Uint32(cd[32:]))
	hashSize, hashType, pageShift := int(cd[36]), cd[37], cd[39]
	if version >= 0x20300 && len(cd) >= 64 {
		if l64 := be.Uint64(cd[56:]); l64 != 0 {
			codeLimit = l64
		}
	}
	if codeLimit != uint64(len(code)) {
		return fmt.Errorf("the signature covers %d bytes, the code is %d", codeLimit, len(code))
	}
	if w.runtime && flags&csRuntime == 0 {
		return errors.New("the signature lacks the hardened runtime flag")
	}
	if w.ident != "" {
		if got := cString(cd, identOff); got != w.ident {
			return fmt.Errorf("signing identifier %q, want %q", got, w.ident)
		}
	}
	if w.team != "" {
		team := ""
		if version >= 0x20200 && len(cd) >= 52 {
			if off := int(be.Uint32(cd[48:])); off != 0 {
				team = cString(cd, off)
			}
		}
		if team != w.team {
			return fmt.Errorf("team identifier %q, want %q", team, w.team)
		}
	}
	var h func() hash.Hash
	switch hashType {
	case 1:
		h = sha1.New
	case 2, 3:
		h = sha256.New
	case 4:
		h = sha512.New384
	default:
		return fmt.Errorf("unknown hash type %d", hashType)
	}
	if pageShift == 0 || pageShift > 20 {
		return errors.New("unsupported code directory page size")
	}
	page := 1 << pageShift
	if want := (len(code) + page - 1) / page; nCode != want {
		return fmt.Errorf("the signature hashes %d pages, the code has %d", nCode, want)
	}
	if hashSize == 0 || hashSize > h().Size() || hashOff < nSpecial*hashSize || hashOff+nCode*hashSize > len(cd) {
		return errors.New("bad code directory hash table")
	}
	// Entitlement slots must be empty as well as their blobs absent.
	for _, slot := range []int{csSlotEntitlements, csSlotDEREntitlements} {
		if slot <= nSpecial && !allZero(cd[hashOff-slot*hashSize:hashOff-(slot-1)*hashSize]) {
			return errors.New("the signature refers to entitlements")
		}
	}
	for i := 0; i < nCode; i++ {
		p := code[i*page : min((i+1)*page, len(code))]
		d := h()
		d.Write(p)
		if !bytes.Equal(d.Sum(nil)[:hashSize], cd[hashOff+i*hashSize:hashOff+(i+1)*hashSize]) {
			return fmt.Errorf("page %d does not match its hash in the signature", i)
		}
	}
	return nil
}

func cString(b []byte, off int) string {
	if off <= 0 || off >= len(b) {
		return ""
	}
	if i := bytes.IndexByte(b[off:], 0); i >= 0 {
		return string(b[off : off+i])
	}
	return ""
}

// Files a signed (and stapled) bundle adds to the one that was signed.
var signatureFiles = map[string]bool{
	"Contents/_CodeSignature/CodeResources": true,
	"Contents/CodeResources":                true, // the stapled notarization ticket
}

type entry struct {
	dir  bool
	path string
}

// tree lists the files and directories under root by relative slash path;
// any other kind of entry (a symlink above all) is an error.
func tree(root string) (map[string]entry, error) {
	out := map[string]entry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			out[rel] = entry{dir: true, path: p}
		case d.Type().IsRegular():
			out[rel] = entry{path: p}
		default:
			return fmt.Errorf("%s: unexpected %v in the bundle", p, d.Type())
		}
		return nil
	})
	return out, err
}

func compareApps(signedApp, refApp string, w want) error {
	ts, err := tree(signedApp)
	if err != nil {
		return err
	}
	tr, err := tree(refApp)
	if err != nil {
		return err
	}
	var names []string
	for rel := range ts {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		s := ts[rel]
		r, ok := tr[rel]
		switch {
		case !ok && s.dir && (rel == "Contents/_CodeSignature"):
			continue
		case !ok && signatureFiles[rel]:
			continue
		case !ok:
			return fmt.Errorf("%s: not in the rebuilt bundle", rel)
		case s.dir != r.dir:
			return fmt.Errorf("%s: a file in one bundle, a directory in the other", rel)
		}
	}
	for rel, r := range tr {
		s, ok := ts[rel]
		if !ok {
			return fmt.Errorf("%s: missing from the signed bundle", rel)
		}
		if r.dir {
			continue
		}
		bs, err := os.ReadFile(s.path)
		if err != nil {
			return err
		}
		br, err := os.ReadFile(r.path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "Contents/MacOS/") {
			sl, err := slices(br)
			if err != nil {
				return fmt.Errorf("rebuilt %s: %w", rel, err)
			}
			if err := compareSlices(rel, bs, sl, w); err != nil {
				return err
			}
			continue
		}
		if !bytes.Equal(bs, br) {
			return fmt.Errorf("%s differs from the rebuild", rel)
		}
	}
	for rel := range signatureFiles {
		if _, ok := ts[rel]; !ok {
			return fmt.Errorf("%s: missing from the signed bundle (not signed and stapled?)", rel)
		}
	}
	fmt.Printf("%s: same files and code as the rebuild\n", signedApp)
	return nil
}

func sameTrees(a, b string) error {
	ta, err := tree(a)
	if err != nil {
		return err
	}
	tb, err := tree(b)
	if err != nil {
		return err
	}
	if len(ta) != len(tb) {
		return fmt.Errorf("%s has %d entries, %s %d", a, len(ta), b, len(tb))
	}
	for rel, ea := range ta {
		eb, ok := tb[rel]
		if !ok || ea.dir != eb.dir {
			return fmt.Errorf("%s: only in %s, or of another kind", rel, a)
		}
		if ea.dir {
			continue
		}
		x, err := os.ReadFile(ea.path)
		if err != nil {
			return err
		}
		y, err := os.ReadFile(eb.path)
		if err != nil {
			return err
		}
		if !bytes.Equal(x, y) {
			return fmt.Errorf("%s differs", rel)
		}
	}
	fmt.Printf("%s and %s are identical\n", a, b)
	return nil
}
