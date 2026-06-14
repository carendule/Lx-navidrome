package nativeapi

// online_lyric_charset.go provides GB18030 / GBK decoding for
// the kw lyric fetcher. The kw endpoint returns its LRC body
// in GB18030 after zlib inflation; without this conversion the
// text comes back as garbled CJK. We use the
// golang.org/x/text/encoding/simplifiedchinese package, which
// is already a transitive dependency of the project.
//
// The function is split into its own file because the
// x/text/encoding/simplifiedchinese import has a noticeable
// compile-time cost (the package carries a large conversion
// table); keeping it isolated lets the linker GC it when
// online_lyric.go is unused.

import (
	"bytes"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// onlineLyricGB18030ToUTF8 decodes GB18030 (or any GBK /
// GB2312 subset) bytes into UTF-8. GB18030 is a strict
// superset of GBK and GB2312, so this single function handles
// all three encodings — which is what lxserver-main does
// with iconv-lite's `gb18030` codec. The transformer doesn't
// surface mid-stream errors (it only signals end-of-input),
// so the function returns a single []byte rather than a
// (bytes, error) pair; callers that need to distinguish
// "empty input" from "decoder error" can check len(out).
func onlineLyricGB18030ToUTF8(b []byte) []byte {
	reader := transform.NewReader(bytes.NewReader(b), simplifiedchinese.GB18030.NewDecoder())
	var out bytes.Buffer
	_, _ = out.ReadFrom(reader)
	return out.Bytes()
}
