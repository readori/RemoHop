package main

import (
	"bytes"
	"crypto/sha256"
	"debug/macho"
	"encoding/binary"
	"strings"
	"testing"
)

// unsigned builds a minimal thin arm64 Mach-O: a header, a __TEXT and a
// __LINKEDIT segment command, room for one more command, and code bytes.
func unsigned(code []byte) []byte {
	le := binary.LittleEndian
	b := make([]byte, 0x200)
	le.PutUint32(b[0:], macho.Magic64)
	le.PutUint32(b[4:], uint32(macho.CpuArm64))
	le.PutUint32(b[12:], uint32(macho.TypeExec))
	le.PutUint32(b[16:], 2)    // ncmds
	le.PutUint32(b[20:], 72*2) // sizeofcmds
	seg := func(off int, name string, fileoff, size uint64) {
		le.PutUint32(b[off:], lcSegment64)
		le.PutUint32(b[off+4:], 72)
		copy(b[off+8:off+24], name)
		le.PutUint64(b[off+32:], size) // vmsize
		le.PutUint64(b[off+40:], fileoff)
		le.PutUint64(b[off+48:], size) // filesize
	}
	seg(32, "__TEXT", 0, 0x200)
	seg(32+72, "__LINKEDIT", 0x200, uint64(len(code)))
	return append(b, code...)
}

type signOpts struct {
	team         string
	ident        string
	flags        uint32
	entitlements bool
	trailer      []byte
	corruptHash  bool
}

// sign adds what codesign adds: LC_CODE_SIGNATURE, a superblob with one
// SHA-256 code directory at the end of __LINKEDIT, and the new sizes.
func sign(ref []byte, o signOpts) []byte {
	le, be := binary.LittleEndian, binary.BigEndian
	b := bytes.Clone(ref)
	codeEnd := len(b)
	off := 32 + int(le.Uint32(b[20:]))
	le.PutUint32(b[off:], lcCodeSignature)
	le.PutUint32(b[off+4:], 16)
	le.PutUint32(b[16:], le.Uint32(b[16:])+1)
	le.PutUint32(b[20:], le.Uint32(b[20:])+16)

	const page = 1 << 12
	n := (codeEnd + page - 1) / page
	ident, team := o.ident+"\x00", o.team+"\x00"
	hdr := 52
	identOff, teamOff := hdr, hdr+len(ident)
	hashOff := teamOff + len(team) + 7*32 // seven special slots
	cd := make([]byte, hashOff+n*32)
	be.PutUint32(cd[0:], csMagicCodeDirectory)
	be.PutUint32(cd[4:], uint32(len(cd)))
	be.PutUint32(cd[8:], 0x20200)
	be.PutUint32(cd[12:], o.flags)
	be.PutUint32(cd[16:], uint32(hashOff))
	be.PutUint32(cd[20:], uint32(identOff))
	be.PutUint32(cd[24:], 7)
	be.PutUint32(cd[28:], uint32(n))
	be.PutUint32(cd[32:], uint32(codeEnd))
	cd[36], cd[37], cd[39] = 32, 2, 12
	be.PutUint32(cd[48:], uint32(teamOff))
	copy(cd[identOff:], ident)
	copy(cd[teamOff:], team)
	nblobs, entLen := 1, 0
	if o.entitlements {
		nblobs, entLen = 2, 16
	}
	sigLen := 12 + 8*nblobs + len(cd) + entLen + len(o.trailer)
	le.PutUint32(b[off+8:], uint32(codeEnd))
	le.PutUint32(b[off+12:], uint32(sigLen))
	// __LINKEDIT reaches to the end of the signature.
	ls := uint64(codeEnd + sigLen - 0x200)
	le.PutUint64(b[32+72+32:], ls)
	le.PutUint64(b[32+72+48:], ls)
	if o.entitlements {
		cd[hashOff-5*32] = 1
	}
	for i := 0; i < n; i++ {
		s := sha256.Sum256(b[i*page : min((i+1)*page, codeEnd)])
		copy(cd[hashOff+i*32:], s[:])
	}
	if o.corruptHash {
		cd[hashOff] ^= 1
	}
	blobs := [][2]any{{uint32(csSlotCodeDirectory), cd}}
	if o.entitlements {
		ent := make([]byte, 16)
		be.PutUint32(ent[0:], 0xfade7171)
		be.PutUint32(ent[4:], 16)
		blobs = append(blobs, [2]any{uint32(csSlotEntitlements), ent})
	}
	sb := make([]byte, 12+8*len(blobs))
	be.PutUint32(sb[0:], csMagicEmbeddedSignature)
	be.PutUint32(sb[8:], uint32(len(blobs)))
	for i, bl := range blobs {
		be.PutUint32(sb[12+i*8:], bl[0].(uint32))
		be.PutUint32(sb[16+i*8:], uint32(len(sb)))
		sb = append(sb, bl[1].([]byte)...)
	}
	be.PutUint32(sb[4:], uint32(len(sb)))
	return append(b, append(sb, o.trailer...)...)
}

func TestCompareSlice(t *testing.T) {
	code := bytes.Repeat([]byte("remohop code "), 900) // more than two pages
	ref := unsigned(code)
	good := signOpts{team: "ABCDE12345", ident: "com.remohop.cli", flags: csRuntime}
	w := want{team: "ABCDE12345", ident: "com.remohop.cli", runtime: true}
	if err := compareSlice(sign(ref, good), ref, w); err != nil {
		t.Fatalf("a correctly signed rebuild is refused: %v", err)
	}

	tampered := bytes.Clone(ref)
	tampered[0x200+100] ^= 0xff
	cases := map[string]struct {
		signed []byte
		w      want
		err    string
	}{
		"other code":       {sign(tampered, good), w, "differs from the rebuild"},
		"other team":       {sign(ref, signOpts{team: "ZZZZZ99999", ident: good.ident, flags: csRuntime}), w, "team identifier"},
		"other identifier": {sign(ref, signOpts{team: good.team, ident: "com.example", flags: csRuntime}), w, "signing identifier"},
		"no runtime":       {sign(ref, signOpts{team: good.team, ident: good.ident}), w, "hardened runtime"},
		"entitlements":     {sign(ref, signOpts{team: good.team, ident: good.ident, flags: csRuntime, entitlements: true}), w, "entitlements"},
		"trailing data":    {sign(ref, signOpts{team: good.team, ident: good.ident, flags: csRuntime, trailer: []byte("payload")}), w, "data after the code signature"},
		"bad page hash":    {sign(ref, signOpts{team: good.team, ident: good.ident, flags: csRuntime, corruptHash: true}), w, "does not match its hash"},
		"unsigned":         {ref, w, "no code signature"},
	}
	for name, c := range cases {
		err := compareSlice(c.signed, ref, c.w)
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, c.err)
		}
	}

	// Zero padding after the blob is what codesign leaves when it rounds up.
	padded := sign(ref, signOpts{team: good.team, ident: good.ident, flags: csRuntime, trailer: make([]byte, 16)})
	if err := compareSlice(padded, ref, w); err != nil {
		t.Errorf("zero padding after the signature is refused: %v", err)
	}
}

func TestCompareSliceReplacesAdhocSignature(t *testing.T) {
	// The Go linker signs darwin/arm64 binaries ad hoc. codesign replaces
	// that signature in place and leaves the rest of the old one behind
	// when the new one is shorter.
	code := bytes.Repeat([]byte{0xa5}, 5000)
	ref := sign(unsigned(code), signOpts{ident: "a.out", trailer: bytes.Repeat([]byte{0x77}, 600)})
	refLayout, _ := parse(ref)
	plain := ref[:refLayout.sigOff]
	// Re-sign the unsigned part as codesign would, keeping the ad-hoc
	// command in place: build from the version without it, then splice the
	// old leftovers after the new, shorter blob.
	signed := sign(unsigned(code), signOpts{team: "ABCDE12345", ident: "com.remohop.cli", flags: csRuntime})
	if !bytes.Equal(signed[0x200:0x200+len(code)], plain[0x200:]) {
		t.Fatal("test setup: code differs")
	}
	w := want{team: "ABCDE12345", runtime: true}
	if err := compareSlice(signed, ref, w); err != nil {
		t.Fatalf("re-signed ad-hoc binary refused: %v", err)
	}
	withLeftover := append(bytes.Clone(signed), ref[len(signed):]...)
	if err := compareSlice(withLeftover, ref, w); err != nil {
		t.Fatalf("leftover bytes of the old signature refused: %v", err)
	}
	withLeftover[len(withLeftover)-1] ^= 1
	if err := compareSlice(withLeftover, ref, w); err == nil {
		t.Fatal("changed leftover bytes accepted")
	}
}

func TestSlicesUniversal(t *testing.T) {
	a := unsigned([]byte("arm"))
	x := unsigned([]byte("x86"))
	binary.LittleEndian.PutUint32(x[4:], uint32(macho.CpuAmd64))
	fat := make([]byte, 0x1000)
	be := binary.BigEndian
	be.PutUint32(fat[0:], macho.MagicFat)
	be.PutUint32(fat[4:], 2)
	put := func(i int, cpu macho.Cpu, off int, b []byte) {
		e := fat[8+i*20:]
		be.PutUint32(e[0:], uint32(cpu))
		be.PutUint32(e[8:], uint32(off))
		be.PutUint32(e[12:], uint32(len(b)))
	}
	put(0, macho.CpuArm64, len(fat), a)
	fat = append(fat, a...)
	put(1, macho.CpuAmd64, len(fat), x)
	fat = append(fat, x...)
	s, err := slices(fat)
	if err != nil || len(s) != 2 || !bytes.Equal(s[archKey(uint32(macho.CpuAmd64), 0)], x) {
		t.Fatalf("slices: %v %v", err, keys(s))
	}
}
