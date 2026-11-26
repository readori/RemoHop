import Foundation

/// VideoToolbox writes H.264 as length-prefixed NAL units (AVCC, 4-byte
/// big-endian lengths); the core's provided stream takes Annex-B (start
/// codes). With 4-byte lengths the two have the same size, so the encoder's
/// output is converted in place, without copying the frame.
enum AnnexB {
    static let startCode: [UInt8] = [0, 0, 0, 1]

    /// Replaces every 4-byte length prefix in `buf` by a start code. Returns
    /// false (leaving the rest unconverted) if a length runs past the end.
    @discardableResult
    static func convertInPlace(_ buf: UnsafeMutableRawBufferPointer) -> Bool {
        var i = 0
        let n = buf.count
        while i < n {
            guard n - i >= 4 else { return false }
            let len = Int(buf[i]) << 24 | Int(buf[i + 1]) << 16 | Int(buf[i + 2]) << 8 | Int(buf[i + 3])
            guard len > 0, len <= n - i - 4 else { return false }
            buf[i] = 0; buf[i + 1] = 0; buf[i + 2] = 0; buf[i + 3] = 1
            i += 4 + len
        }
        return true
    }

    /// Start code + SPS + start code + PPS, the head of a keyframe.
    static func parameterSets(sps: Data, pps: Data) -> Data {
        var d = Data(capacity: 8 + sps.count + pps.count)
        d.append(contentsOf: startCode)
        d.append(sps)
        d.append(contentsOf: startCode)
        d.append(pps)
        return d
    }

    /// The NAL unit types in an Annex-B buffer with 4-byte start codes (for tests and logs).
    static func nalTypes(_ d: Data) -> [Int] {
        var out: [Int] = []
        let b = [UInt8](d)
        var i = 0
        while i + 4 < b.count {
            if b[i] == 0, b[i + 1] == 0, b[i + 2] == 0, b[i + 3] == 1 {
                out.append(Int(b[i + 4] & 0x1f))
                i += 5
            } else {
                i += 1
            }
        }
        return out
    }
}
