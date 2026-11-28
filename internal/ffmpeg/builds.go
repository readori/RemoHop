package ffmpeg

// Build is one ffmpeg build RemoHop may download: an archive at a fixed,
// versioned address whose SHA-256 is pinned here. The archive is checked
// against the pin before anything in it is read, and only the ffmpeg
// program is taken from it.
type Build struct {
	// Version is ffmpeg's version, as people read it.
	Version string
	// Source names who builds it, for people (the status and the UI).
	Source string
	// License is the build's licence: all of these are GPL builds, as
	// they include libx264 (see the package comment).
	License string
	// URLs are mirrors of the same file, tried in order; each must be
	// HTTPS.
	URLs []string
	// SHA256 is the archive's hash, in hex.
	SHA256 string
	// Size is the archive's size in bytes.
	Size int64
	// Member is the path of the program in the archive (a zip file).
	Member string
	// MemberSize is the program's size in bytes once extracted.
	MemberSize int64
}

// SizeMB is the download's size in megabytes (10^6 bytes), rounded.
func (b Build) SizeMB() int { return int((b.Size + 500_000) / 1_000_000) }

// builds are the pinned builds, by GOOS/GOARCH. Linux has none: its
// distributions package ffmpeg.
//
// To move to a newer build: pick a release (never a "latest" link: the file
// behind it changes), download it, check its hash against the one its
// publisher lists, put both here, and run the real-download test
// (REMOHOP_FFMPEG_DOWNLOAD_TEST=1 go test -run TestPinnedBuilds
// ./internal/ffmpeg), which checks every entry. Martin Riedl keeps every
// release build (back to 2022) and Gyan's release builds stay on GitHub;
// BtbN keeps a month's last build for about two years, so the Windows arm64
// entry needs moving within that time.
var builds = map[string]Build{
	// Gyan Doshi's release "essentials" build (gyan.dev), mirrored on
	// GitHub by its author. libx264, libaom, libopus and every hardware
	// encoder ffmpeg has for Windows (NVENC, AMF, Quick Sync through
	// libvpl, Media Foundation); no SVT-AV1 (libaom does software AV1).
	"windows/amd64": {
		Version: "9.1",
		Source:  "gyan.dev (release essentials build)",
		License: "GPL-3.0",
		URLs: []string{
			"https://github.com/GyanD/codexffmpeg/releases/download/9.1/ffmpeg-9.1-essentials_build.zip",
			"https://www.gyan.dev/ffmpeg/builds/packages/ffmpeg-9.1-essentials_build.zip",
		},
		SHA256:     "b9bbbb3acece000cbeec994f856649354e52c773807841e78908374b24d658fd",
		Size:       119467219,
		Member:     "ffmpeg-9.1-essentials_build/bin/ffmpeg.exe",
		MemberSize: 110590976,
	},
	// BtbN's FFmpeg-Builds, the 9.0 release branch from the dated build of
	// 31 October 2026: libx264, SVT-AV1, libopus, OpenH264, AMF and Media
	// Foundation. The only well-known Windows arm64 build.
	"windows/arm64": {
		Version: "9.0.2",
		Source:  "BtbN/FFmpeg-Builds (autobuild-2026-10-31-12-59)",
		License: "GPL-3.0",
		URLs: []string{
			"https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-10-31-12-59/ffmpeg-n9.0.2-39-g7adfbe40dc-winarm64-gpl-9.0.zip",
		},
		SHA256:     "8b3c394762f26716ccef03ac1b7ac76430759080e1255aa9bc01d74db590bcb5",
		Size:       136603040,
		Member:     "ffmpeg-n9.0.2-39-g7adfbe40dc-winarm64-gpl-9.0/bin/ffmpeg.exe",
		MemberSize: 95170560,
	},
	// Martin Riedl's release builds (ffmpeg.martin-riedl.de): static,
	// signed with an Apple Developer ID, macOS 12 or newer. VideoToolbox,
	// libx264, SVT-AV1, libaom and libopus.
	"darwin/arm64": {
		Version: "9.1",
		Source:  "ffmpeg.martin-riedl.de (release build)",
		License: "GPL-3.0",
		URLs: []string{
			"https://ffmpeg.martin-riedl.de/download/macos/arm64/1795207104_9.1/ffmpeg.zip",
		},
		SHA256:     "1f66e9a690a7f1c9e60b012379f8869c11818d1797f45b21dbb2f2564ba61ae1",
		Size:       28623086,
		Member:     "ffmpeg",
		MemberSize: 66848272,
	},
	"darwin/amd64": {
		Version: "9.1",
		Source:  "ffmpeg.martin-riedl.de (release build)",
		License: "GPL-3.0",
		URLs: []string{
			"https://ffmpeg.martin-riedl.de/download/macos/amd64/1795212433_9.1/ffmpeg.zip",
		},
		SHA256:     "35b3d82cccc2c1d644d49d15f5e57eb913a08ef68f798042c90eecec62c51f1e",
		Size:       34064287,
		Member:     "ffmpeg",
		MemberSize: 95329264,
	},
}

// BuildFor is the pinned build for goos/goarch, if there is one.
func BuildFor(goos, goarch string) (Build, bool) {
	b, ok := builds[goos+"/"+goarch]
	return b, ok
}
