package nativeapi

// online_lyric_helpers.go holds the small, dependency-bound
// helpers the per-source lyric fetchers in online_lyric.go need
// but that don't belong in the per-source file itself. The
// split keeps the main file focused on the per-source flow
// while keeping the imports and test surface obvious.
//
// We deliberately don't put these in the package's other HTTP
// helper files: they're lyric-API-specific (GB18030 for kw,
// PEM parsing for wy's RSA key, MD5 for the eapi digest) and
// pulling them in elsewhere would clutter the public surface.

import (
	"crypto/md5"
	"net/url"
)

// onlineLyricDecodeGB18030 implements GB18030 -> UTF-8
// conversion. The kw lyric endpoint serves its body in GB18030
// after zlib inflation; without this conversion the LRC text
// comes back as garbled CJK. GB18030 is a strict superset of
// GBK and GB2312, so a single decoder handles all three
// encodings — which is what lxserver-main does with
// iconv-lite's `gb18030` codec.
func onlineLyricDecodeGB18030(b []byte) []byte {
	return onlineLyricGB18030ToUTF8(b)
}

// onlineLyricMD5Sum returns the lowercase hex MD5 digest of b.
// It exists as a small indirection so the lyric file can call a
// stable function name without the rest of the package having
// to know about the import. MD5 is used only for the eapi
// digest (a non-cryptographic auth tag, not a security
// primitive) and for kg's krctype/contenttype flags.
func onlineLyricMD5Sum(b []byte) string {
	sum := md5.Sum(b)
	hex := make([]byte, 32)
	const hexDigits = "0123456789abcdef"
	for i, n := range sum {
		hex[i*2] = hexDigits[n>>4]
		hex[i*2+1] = hexDigits[n&0x0f]
	}
	return string(hex)
}

// onlineLyricURLEncodeImpl percent-encodes a string the way
// the lyric endpoints expect. The Go stdlib's `url.QueryEscape`
// encodes spaces as `+`, but kw / kg / wy / tx all expect
// spaces as `%20`. We use `url.PathEscape` (RFC 3986 path
// segment encoding) which produces `%20` and matches every
// endpoint we care about. The narrower `url.QueryEscape`
// would also be wrong because it escapes too many characters
// (e.g. `:` becomes `%3A`).
func onlineLyricURLEncodeImpl(s string) string {
	return url.PathEscape(s)
}
