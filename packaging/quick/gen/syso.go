package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// A .syso file is an object file the Go linker adds to a program when it
// sits in the main package's directory and its name ends in
// _GOOS_GOARCH.syso. For Windows it is a COFF object whose one section,
// .rsrc, holds the resource tree: here the icon Explorer shows for the
// program and its application manifest. Writing it takes no tool beyond
// this file, so the build stays pure Go and works from any system.

// Resource types.
const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtManifest  = 24
)

// COFF machine types and their "32-bit address without image base"
// relocation, which is what a resource data entry's address needs.
var machines = map[string]struct{ machine, reloc uint16 }{
	"amd64": {0x8664, 3}, // IMAGE_REL_AMD64_ADDR32NB
	"arm64": {0xaa64, 2}, // IMAGE_REL_ARM64_ADDR32NB
	"386":   {0x014c, 7}, // IMAGE_REL_I386_DIR32NB
}

// The manifest: run as the invoking user (no elevation prompt), declare
// the Windows versions the program is written for, and use the current
// visual style for the message boxes it shows. It says nothing about DPI
// on purpose: the capture and input code sets the process's DPI awareness
// itself (SetProcessDPIAware), and a manifest setting would override the
// mode that code was written and tested for.
const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="RemoHop.QuickSupport" version="1.0.0.0" processorArchitecture="*"/>
  <description>RemoHop Quick Support</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
</assembly>
`

// resource is one leaf of the tree: type / id / language 0x409 (en-US).
type resource struct {
	typ, id int
	data    []byte
}

// resources is what RemoHop.exe carries.
func resources() []resource {
	images := windowsIcon()
	var out []resource
	for i, im := range images {
		out = append(out, resource{rtIcon, i + 1, im.data})
	}
	// Explorer shows the group icon with the lowest ID.
	out = append(out, resource{rtGroupIcon, 1, groupIcon(images, 1)})
	out = append(out, resource{rtManifest, 1, []byte(manifest)})
	return out
}

// rsrcSection lays out the resource tree: type directory, one ID directory
// per type, one language directory per resource, the data entries, then
// the data itself. It returns the section and the offsets of the fields
// that hold an address and need a relocation.
func rsrcSection(res []resource) (section []byte, relocs []uint32) {
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].typ != res[j].typ {
			return res[i].typ < res[j].typ
		}
		return res[i].id < res[j].id
	})
	var types []int
	byType := map[int][]int{} // type -> indexes into res
	for i, r := range res {
		if _, ok := byType[r.typ]; !ok {
			types = append(types, r.typ)
		}
		byType[r.typ] = append(byType[r.typ], i)
	}
	const dirSize, entrySize, dataEntrySize = 16, 8, 16
	// Offsets of each level.
	off := dirSize + entrySize*len(types)
	idDir := map[int]int{}
	for _, t := range types {
		idDir[t] = off
		off += dirSize + entrySize*len(byType[t])
	}
	langDir := make([]int, len(res))
	for i := range res {
		langDir[i] = off
		off += dirSize + entrySize
	}
	dataEntry := make([]int, len(res))
	for i := range res {
		dataEntry[i] = off
		off += dataEntrySize
	}
	dataOff := make([]int, len(res))
	for i, r := range res {
		dataOff[i] = off
		off += (len(r.data) + 7) &^ 7
	}

	b := make([]byte, off)
	le := binary.LittleEndian
	dir := func(at, ids int) { le.PutUint16(b[at+14:], uint16(ids)) }
	entry := func(at, id int, target uint32) {
		le.PutUint32(b[at:], uint32(id))
		le.PutUint32(b[at+4:], target)
	}
	const subdir = 0x80000000
	dir(0, len(types))
	for i, t := range types {
		entry(dirSize+entrySize*i, t, subdir|uint32(idDir[t]))
		dir(idDir[t], len(byType[t]))
		for j, ri := range byType[t] {
			entry(idDir[t]+dirSize+entrySize*j, res[ri].id, subdir|uint32(langDir[ri]))
		}
	}
	for i, r := range res {
		dir(langDir[i], 1)
		entry(langDir[i]+dirSize, 0x409, uint32(dataEntry[i]))
		// The linker adds the section's address to this offset.
		le.PutUint32(b[dataEntry[i]:], uint32(dataOff[i]))
		le.PutUint32(b[dataEntry[i]+4:], uint32(len(r.data)))
		relocs = append(relocs, uint32(dataEntry[i]))
		copy(b[dataOff[i]:], r.data)
	}
	return b, relocs
}

// sysoFile is the COFF object for goarch.
func sysoFile(goarch string, res []resource) ([]byte, error) {
	m, ok := machines[goarch]
	if !ok {
		return nil, fmt.Errorf("no Windows resource object for GOARCH %s", goarch)
	}
	section, relocs := rsrcSection(res)
	const fileHeader, sectionHeader, relocSize, symbolSize = 20, 40, 10, 18
	dataAt := fileHeader + sectionHeader
	relocAt := dataAt + len(section)
	symAt := relocAt + relocSize*len(relocs)

	var buf bytes.Buffer
	w := func(v any) { binary.Write(&buf, binary.LittleEndian, v) }
	// File header.
	w(m.machine)
	w(uint16(1))     // sections
	w(uint32(0))     // timestamp: none, for reproducible builds
	w(uint32(symAt)) // symbol table
	w(uint32(1))     // symbols
	w(uint16(0))     // no optional header
	w(uint16(0))     // characteristics
	// Section header.
	buf.Write([]byte(".rsrc\x00\x00\x00"))
	w(uint32(0)) // virtual size
	w(uint32(0)) // virtual address
	w(uint32(len(section)))
	w(uint32(dataAt))
	w(uint32(relocAt))
	w(uint32(0)) // line numbers
	w(uint16(len(relocs)))
	w(uint16(0))
	w(uint32(0x40000040)) // initialised data, readable
	buf.Write(section)
	for _, at := range relocs {
		w(at)
		w(uint32(0)) // symbol 0: the section
		w(m.reloc)
	}
	// The symbol the relocations refer to: the .rsrc section itself.
	buf.Write([]byte(".rsrc\x00\x00\x00"))
	w(uint32(0)) // value
	w(uint16(1)) // section number
	w(uint16(0)) // type
	w(uint8(3))  // static
	w(uint8(0))  // no auxiliary records
	w(uint32(4)) // empty string table: just its own length
	return buf.Bytes(), nil
}
