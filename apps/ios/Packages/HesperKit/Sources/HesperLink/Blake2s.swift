import Foundation

/// BLAKE2s (RFC 7693), the hash of the end-to-end channel's Noise suite.
public enum BLAKE2s {
    /// BLAKE2s-256 (RFC 7693), optional key (≤ 32 bytes), digest length 1…32.
    public static func hash(_ data: some DataProtocol, key: some DataProtocol = Data(), length: Int = 32) -> Data {
        precondition(key.count <= 32 && (1...32).contains(length), "BLAKE2s: key ≤ 32 bytes, length 1…32")
        var h = iv
        h[0] ^= 0x0101_0000 ^ (UInt32(key.count) << 8) ^ UInt32(length)
        var message = [UInt8]()
        if !key.isEmpty {
            message = Array(key) + [UInt8](repeating: 0, count: 64 - key.count)
        }
        message += Array(data)
        let blocks = max(1, (message.count + 63) / 64)
        for i in 0..<blocks {
            var block = [UInt8](repeating: 0, count: 64)
            let start = i * 64
            let end = min(start + 64, message.count)
            if start < end { block.replaceSubrange(0..<(end - start), with: message[start..<end]) }
            compress(&h, block, counter: UInt64(end), last: i == blocks - 1)
        }
        var out = Data(capacity: 32)
        for word in h { withUnsafeBytes(of: word.littleEndian) { out.append(contentsOf: $0) } }
        return out.prefix(length)
    }

    /// HMAC-BLAKE2s (RFC 2104, block 64), as Noise's HMAC-HASH.
    static func hmac(key: Data, _ data: Data) -> Data {
        var k = key.count > 64 ? hash(key) : key
        k.append(Data(count: 64 - k.count))
        let inner = hash(Data(k.map { $0 ^ 0x36 }) + data)
        return hash(Data(k.map { $0 ^ 0x5c }) + inner)
    }

    /// Noise's HKDF with two outputs.
    static func hkdf(chainingKey: Data, _ ikm: Data) -> (Data, Data) {
        let temp = hmac(key: chainingKey, ikm)
        let one = hmac(key: temp, Data([1]))
        return (one, hmac(key: temp, one + Data([2])))
    }

    /// Noise's HKDF with three outputs.
    static func hkdf3(chainingKey: Data, _ ikm: Data) -> (Data, Data, Data) {
        let temp = hmac(key: chainingKey, ikm)
        let one = hmac(key: temp, Data([1]))
        let two = hmac(key: temp, one + Data([2]))
        return (one, two, hmac(key: temp, two + Data([3])))
    }

    private static let iv: [UInt32] = [
        0x6A09_E667, 0xBB67_AE85, 0x3C6E_F372, 0xA54F_F53A, 0x510E_527F, 0x9B05_688C, 0x1F83_D9AB, 0x5BE0_CD19,
    ]

    private static let sigma: [[Int]] = [
        [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15],
        [14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3],
        [11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4],
        [7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8],
        [9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13],
        [2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9],
        [12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11],
        [13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10],
        [6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5],
        [10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0],
    ]

    private static func compress(_ h: inout [UInt32], _ block: [UInt8], counter: UInt64, last: Bool) {
        var m = [UInt32](repeating: 0, count: 16)
        for i in 0..<16 {
            m[i] = UInt32(block[4 * i]) | UInt32(block[4 * i + 1]) << 8 | UInt32(block[4 * i + 2]) << 16 | UInt32(block[4 * i + 3]) << 24
        }
        var v = h + iv
        v[12] ^= UInt32(truncatingIfNeeded: counter)
        v[13] ^= UInt32(truncatingIfNeeded: counter >> 32)
        if last { v[14] = ~v[14] }
        func g(_ a: Int, _ b: Int, _ c: Int, _ d: Int, _ x: UInt32, _ y: UInt32) {
            v[a] = v[a] &+ v[b] &+ x
            v[d] = (v[d] ^ v[a]).rotatedRight(16)
            v[c] = v[c] &+ v[d]
            v[b] = (v[b] ^ v[c]).rotatedRight(12)
            v[a] = v[a] &+ v[b] &+ y
            v[d] = (v[d] ^ v[a]).rotatedRight(8)
            v[c] = v[c] &+ v[d]
            v[b] = (v[b] ^ v[c]).rotatedRight(7)
        }
        for s in sigma {
            g(0, 4, 8, 12, m[s[0]], m[s[1]])
            g(1, 5, 9, 13, m[s[2]], m[s[3]])
            g(2, 6, 10, 14, m[s[4]], m[s[5]])
            g(3, 7, 11, 15, m[s[6]], m[s[7]])
            g(0, 5, 10, 15, m[s[8]], m[s[9]])
            g(1, 6, 11, 12, m[s[10]], m[s[11]])
            g(2, 7, 8, 13, m[s[12]], m[s[13]])
            g(3, 4, 9, 14, m[s[14]], m[s[15]])
        }
        for i in 0..<8 { h[i] ^= v[i] ^ v[i + 8] }
    }
}

extension UInt32 {
    fileprivate func rotatedRight(_ n: UInt32) -> UInt32 { (self >> n) | (self << (32 - n)) }
}
