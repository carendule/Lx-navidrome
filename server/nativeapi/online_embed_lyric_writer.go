package nativeapi

// online_embed_lyric_writer.go is the post-ffmpeg tag-rewrite pass
// for the embed pipeline.
//
// Why this exists
//
// ffmpeg 8.x (and earlier versions, going back to 4.x) has a
// long-standing behavior where the MP3 muxer does not write a
// real ID3 USLT frame from `-metadata lyrics=...` — it writes
// the lyrics as a generic TXXX (User-defined text) frame with
// the description "USLT" instead. The MP3 spec's USLT frame
// (4-byte ID "USLT" + 1-byte encoding + 3-byte language + text)
// is what every standalone player (mp3tag, MusicBee,
// foobar2000, AIMP, even Windows File Explorer) surfaces as
// the standard "Lyrics" column. TXXX(USLT) is a custom field
// that mp3tag lists under "Custom Fields" and most other
// players ignore entirely.
//
// The user reported "the USLT field is hard to read, please
// write to LYRICS instead" — but the key name doesn't matter,
// ffmpeg always writes a TXXX. The actual fix is to write a
// real USLT frame ourselves. We do the rewrite in Go so we
// don't need to add a new ffmpeg argument or pull in a C
// library (id3tag, taglib) just for this single frame.
//
// For FLAC the same story: ffmpeg writes the lyrics as a
// generic Vorbis comment with the lowercase key "lyrics",
// which is *almost* right but the canonical Vorbis spec
// defines `LYRICS` (uppercase) as the standard key. MusicBee
// and foobar2000 surface both, but the Xiph.Org reference
// tooling only surfaces the uppercase form. We rewrite the
// Vorbis comment block here too.
//
// For M4A / MP4 ffmpeg writes the iTunes `©lyr` atom
// correctly, so we don't need to do anything for that
// container — see onlineEmbedDownloadMetadata for the format
// dispatch.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"unicode/utf16"
)

// onlineEmbedWriteLyricContainer dispatches to the per-format
// lyric rewrite. The "format" argument is the ffmpeg container
// name we already sniffed in the caller ("mp3", "flac",
// "mp4", etc.). Returns nil on success or on a no-op path
// (m4a). Errors are logged by the caller; we never fail the
// download because a lyric couldn't be embedded.
func onlineEmbedWriteLyricContainer(ctx context.Context, format, audioPath, lyric string) error {
	switch format {
	case "mp3":
		return onlineEmbedWriteID3USLT(audioPath, lyric)
	case "flac":
		return onlineEmbedWriteFLACLyric(audioPath, lyric)
	case "mp4", "m4a":
		// ffmpeg wrote the iTunes ©lyr atom; nothing to do.
		return nil
	}
	return nil
}

// ---------------------------------------------------------------------------
// MP3 — write a real ID3v2.3 USLT frame
// ---------------------------------------------------------------------------

// onlineEmbedWriteID3USLT injects a real ID3v2.4 USLT frame
// into the file at audioPath. The strategy is:
//
//  1. Read the entire file into memory. The files we embed
//     are at most a few MB (lyrics are small, mp3 bitrates
//     are < 320k) so the memory footprint is bounded.
//  2. If the file already has an ID3v2 tag, walk the frames
//     and drop any existing USLT frame. Capture the
//     trailing padding that the original tag used (ffmpeg
//     often pads the tag body out to a 4-byte boundary,
//     and we MUST drop that padding — otherwise the
//     rewritten file has a shorter declared tag size but
//     an audio slice that starts at the OLD tag end, which
//     is now mid-padding. mp3tag's strict ID3v2 parser
//     refuses such files with "ID3v2 tag parse error").
//  3. If the file has no ID3v2 tag, build a fresh one with
//     just the USLT frame.
//  4. If the file has an ID3v1 tag (the old "TAG" footer
//     at the end), preserve it.
//  5. Rewrite the file with the new ID3v2 header + audio
//     payload + ID3v1 footer (if any).
//
// We deliberately do not edit the file in place: editing
// the ID3v2 header changes the file's perceived length and
// any byte offsets ffmpeg / players cached, so a full
// rewrite is the safest path. The audio payload is not
// re-encoded — we copy it byte-for-byte.
func onlineEmbedWriteID3USLT(audioPath, lyric string) error {
	data, err := os.ReadFile(audioPath) // #nosec G304 -- caller-controlled, the same way ffmpeg reads it
	if err != nil {
		return fmt.Errorf("read audio: %w", err)
	}
	audioStart := 0
	audioEnd := len(data)
	// ID3v1 footer: 128 bytes, starts with the literal "TAG".
	if n := len(data); n >= 128 && bytes.Equal(data[n-128:n-128+3], []byte("TAG")) {
		audioEnd = n - 128
	}
	// ID3v2 header: 10 bytes, starts with the literal "ID3".
	var existingFrames []byte
	if len(data) >= 10 && bytes.Equal(data[:3], []byte("ID3")) {
		headerSize := id3v2TagSize(data[:10])
		if headerSize <= 0 || 10+headerSize > audioEnd {
			// Corrupt tag — drop it.
			audioStart = 0
		} else {
			// Walk the existing frames and keep every
			// non-USLT frame. We deliberately do NOT
			// keep the ffmpeg-emitted TXXX(USLT) wrapper
			// because we're going to write a real USLT
			// frame right after, and keeping the TXXX
			// would leave the file with two lyric
			// entries (the wrapper AND the real frame),
			// which mp3tag surfaces as a duplicate row
			// in the Lyrics column.
			tagBody := data[10 : 10+headerSize]
			versionMajor := data[3]
			kept := id3v2FilterFrames(tagBody, []byte("USLTTXXX"), versionMajor)
			existingFrames = kept
			// The audio payload starts at the declared
			// end of the ID3v2 tag (10 + headerSize).
			// Any zero-padding ffmpeg added between
			// the last frame and the declared tag end
			// is part of the ID3v2 tag and gets
			// discarded along with the rest of the
			// old tag — the new tag is constructed
			// from scratch with the kept frames plus
			// a fresh USLT frame, and ffmpeg-style
			// alignment padding is no longer needed
			// (mp3tag reads the new tag without it,
			// and the saved bytes are negligible).
			audioStart = 10 + headerSize
		}
	}
	audio := data[audioStart:audioEnd]
	// Preserve the existing tag major version when possible.
	// ffmpeg writes ID3v2.3 by default (`-id3v2_version 3`),
	// and keeping v2.3 prevents a mixed-format tag where the
	// header says v2.4 but preserved frames still use v2.3
	// frame-size encoding (big-endian), which strict parsers
	// like mp3tag treat as malformed.
	targetMajor := byte(3)
	targetMinor := byte(0)
	if len(data) >= 10 && bytes.Equal(data[:3], []byte("ID3")) {
		if data[3] == 3 || data[3] == 4 {
			targetMajor = data[3]
			targetMinor = data[4]
		}
	}

	var usltFrame []byte
	if targetMajor >= 4 {
		usltFrame = buildID3v24USLTFrame(lyric, "eng")
	} else {
		usltFrame = buildID3v23USLTFrame(lyric, "eng")
	}

	newTagBody := append(existingFrames, usltFrame...)
	newHeader := buildID3v2Header(len(newTagBody), int(targetMajor), int(targetMinor), 0)
	out := make([]byte, 0, len(newHeader)+len(newTagBody)+len(audio)+128)
	out = append(out, newHeader...)
	out = append(out, newTagBody...)
	out = append(out, audio...)
	// Re-append ID3v1 footer if the source had one.
	if audioEnd != len(data) {
		out = append(out, data[audioEnd:]...)
	}
	return os.WriteFile(audioPath, out, 0o600)
}

// isID3v2FrameID returns true iff every byte of the
// 4-byte ID3v2 frame ID is in [A-Z0-9]. The ID3v2 spec
// restricts frame IDs to those characters; anything
// else (lowercase letters, control bytes, high-bit
// bytes, or binary garbage) means the tag is corrupted
// and the walker has walked off the end of the real
// frames into either padding or junk.
func isID3v2FrameID(fid [4]byte) bool {
	for _, b := range fid {
		if !((b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')) {
			return false
		}
	}
	return true
}

// id3v2FilterFrames walks an ID3v2 tag body and returns
// the concatenation of all frames whose 4-byte ID is NOT
// in the "drop" list. Any trailing padding bytes
// (typically zero bytes ffmpeg adds for 4-byte alignment)
// are discarded — the caller rebuilds a fresh ID3v2 tag
// from the kept frames plus any new frames, and the
// rewrite does not need the old padding.
//
// `versionMajor` is the ID3v2 major version read from the
// file's tag header (data[3]). 4 = ID3v2.4 (synchsafe
// frame sizes); 3 = ID3v2.3 (big-endian frame sizes); any
// other value is treated as v2.3 for safety.
//
// The walk supports both ID3v2.3 (32-bit big-endian size)
// and ID3v2.4 (synchsafe 28-bit size). The caller MUST
// pass the version it read from the header — guessing the
// encoding from the size bytes themselves is unreliable,
// because a v2.4 file with a high bit set in the last
// size byte (e.g. a frame size like 0x4dbb) would be
// misread as v2.3 big-endian (19899) instead of v2.4
// synchsafe (9915), causing the walker to skip past
// legitimate frames and into the USLT frame body.
func id3v2FilterFrames(tagBody []byte, drop []byte, versionMajor byte) (kept []byte) {
	if len(tagBody) < 4 {
		return nil
	}
	dropSet := make(map[[4]byte]bool, len(drop)/4)
	for i := 0; i+4 <= len(drop); i += 4 {
		var fid [4]byte
		copy(fid[:], drop[i:i+4])
		dropSet[fid] = true
	}
	pos := 0
	for pos+10 <= len(tagBody) {
		fid := [4]byte{tagBody[pos], tagBody[pos+1], tagBody[pos+2], tagBody[pos+3]}
		// A valid ID3v2 frame ID is 4 bytes of [A-Z0-9].
		// A leading NUL means we've hit the ID3v2 padding
		// area (or the end of frames). Anything else
		// (lowercase letters, control chars, raw binary
		// data) means the tag is corrupted and we're no
		// longer walking frames — treat the rest as
		// padding and stop.
		if fid[0] == 0 {
			break
		}
		if !isID3v2FrameID(fid) {
			break
		}
		// ID3v2.3 / 2.4 frame size is 4 bytes. v2.3 is
		// big-endian, v2.4 is synchsafe (28 bits). Use
		// the version from the tag header, not the
		// size bytes themselves — a v2.4 frame whose
		// size has the high bit set (e.g. 0x4dbb) is
		// still a synchsafe value, NOT big-endian.
		var sz uint32
		if versionMajor >= 4 {
			sz = (uint32(tagBody[pos+4]) << 21) |
				(uint32(tagBody[pos+5]) << 14) |
				(uint32(tagBody[pos+6]) << 7) |
				uint32(tagBody[pos+7])
		} else {
			sz = uint32(tagBody[pos+4])<<24 |
				uint32(tagBody[pos+5])<<16 |
				uint32(tagBody[pos+6])<<8 |
				uint32(tagBody[pos+7])
		}
		// Sanity: if the declared size is bigger than
		// the rest of the tag body, treat the entire
		// remainder as this one frame's body. This
		// keeps us from skipping legitimate frames
		// after a malformed one (which is exactly the
		// failure mode of a too-large size: the walker
		// would jump off the end of the tag and lose
		// every frame after the bad one).
		remaining := len(tagBody) - pos - 10
		if sz > uint32(remaining) {
			sz = uint32(remaining)
		}
		frameEnd := pos + 10 + int(sz)
		if !dropSet[fid] {
			kept = append(kept, tagBody[pos:frameEnd]...)
		}
		pos = frameEnd
	}
	// Trailing bytes (typically ffmpeg-emitted zero
	// padding) are intentionally discarded. The caller
	// reconstructs the ID3v2 tag from the kept frames
	// plus any new ones; if alignment is needed, the
	// caller's new tag can pad itself.
	return kept
}

// id3v2TagSize returns the size of the ID3v2 tag payload
// (not including the 10-byte header) parsed from a tag
// header, or 0 on parse failure. The ID3v2 size is encoded
// as 4 7-bit big-endian bytes ("synchsafe" integer).
func id3v2TagSize(header []byte) int {
	if len(header) < 10 {
		return 0
	}
	s := (uint(header[6]) << 21) | (uint(header[7]) << 14) | (uint(header[8]) << 7) | uint(header[9])
	return int(s)
}

// buildID3v2Header constructs an ID3v2 header for the
// given body length, version (major.minor), and flags.
// Layout:
//
//	3 bytes  "ID3"
//	2 bytes  version (e.g. 0x04 0x00 for 2.4.0)
//	1 byte   flags
//	4 bytes  synchsafe body length
func buildID3v2Header(bodyLen, versionMajor, versionMinor, flags int) []byte {
	out := make([]byte, 10)
	copy(out, "ID3")
	out[3] = byte(versionMajor)
	out[4] = byte(versionMinor)
	out[5] = byte(flags)
	// Synchsafe encoding: 28 bits across 4 bytes, MSB
	// first, each byte uses only 7 bits.
	v := uint32(bodyLen)
	out[6] = byte((v >> 21) & 0x7f)
	out[7] = byte((v >> 14) & 0x7f)
	out[8] = byte((v >> 7) & 0x7f)
	out[9] = byte(v & 0x7f)
	return out
}

// buildID3v24USLTFrame constructs an ID3v2.4 USLT
// (Unsynchronized Lyrics) frame. The frame layout
// (from the ID3v2.4 spec) is:
//
//	4 bytes  "USLT" (frame ID)
//	4 bytes  frame size (synchsafe in 2.4; 0x00 0x00 0x00
//	                        0x00 if the frame is the last
//	                        before the end of the tag, but
//	                        we always pass an explicit size)
//	2 bytes  flags (we use 0)
//	1 byte   text encoding:
//	            0 = ISO-8859-1
//	            1 = UTF-16 w/ BOM
//	            2 = UTF-16BE w/o BOM
//	            3 = UTF-8 (defined in 2.4 only)
//	3 bytes  language code (ISO 639-2, e.g. "eng", "zho")
//	…        content descriptor (NUL-terminated; we emit
//	                        an empty one, i.e. a single
//	                        NUL byte, to keep the lyrics
//	                        text from being misread as a
//	                        descriptor)
//	…        the lyrics text (UTF-8, no terminator in 2.4)
//
// We deliberately use ID3v2.4 (not 2.3) because v2.4
// defines encoding 3 = UTF-8, which is the only safe
// choice for LRC text containing Chinese / Korean /
// Japanese characters. v2.3 only allows ISO-8859-1 and
// UTF-16, and forcing UTF-16 would double the file size
// and break players that don't handle the BOM. v2.4 has
// been supported by every modern player for 15+ years
// (mp3tag 2.5+, MusicBee 1.0+, foobar2000 0.9.5+).
func buildID3v24USLTFrame(lyric, lang string) []byte {
	if lang == "" {
		lang = "eng"
	}
	encByte := byte(0x03) // UTF-8 (ID3v2.4 only)
	langBytes := []byte(lang)
	if len(langBytes) != 3 {
		langBytes = []byte("eng")
	}
	descriptor := []byte{0x00} // empty descriptor, NUL-terminated
	text := []byte(lyric)
	body := make([]byte, 0, 1+3+len(descriptor)+len(text))
	body = append(body, encByte)
	body = append(body, langBytes...)
	body = append(body, descriptor...)
	body = append(body, text...)
	// ID3v2.4 frame header: 4-byte ID + 4-byte synchsafe
	// size + 2-byte flags.
	frame := make([]byte, 10+len(body))
	copy(frame, "USLT")
	v := uint32(len(body))
	frame[4] = byte((v >> 21) & 0x7f)
	frame[5] = byte((v >> 14) & 0x7f)
	frame[6] = byte((v >> 7) & 0x7f)
	frame[7] = byte(v & 0x7f)
	// frame[8:10] = flags = 0
	copy(frame[10:], body)
	return frame
}

// buildID3v23USLTFrame constructs an ID3v2.3 USLT frame.
// v2.3 does not support UTF-8 text encoding, so we emit
// UTF-16 with BOM (encoding byte 0x01) to preserve CJK text.
func buildID3v23USLTFrame(lyric, lang string) []byte {
	if lang == "" {
		lang = "eng"
	}
	langBytes := []byte(lang)
	if len(langBytes) != 3 {
		langBytes = []byte("eng")
	}
	encByte := byte(0x01) // UTF-16 with BOM (ID3v2.3)
	// Empty descriptor in UTF-16: BOM + NUL terminator.
	descriptor := []byte{0xFF, 0xFE, 0x00, 0x00}
	text := encodeUTF16LEWithBOM(lyric)
	body := make([]byte, 0, 1+3+len(descriptor)+len(text))
	body = append(body, encByte)
	body = append(body, langBytes...)
	body = append(body, descriptor...)
	body = append(body, text...)

	frame := make([]byte, 10+len(body))
	copy(frame, "USLT")
	v := uint32(len(body))
	frame[4] = byte((v >> 24) & 0xff)
	frame[5] = byte((v >> 16) & 0xff)
	frame[6] = byte((v >> 8) & 0xff)
	frame[7] = byte(v & 0xff)
	copy(frame[10:], body)
	return frame
}

func encodeUTF16LEWithBOM(s string) []byte {
	codeUnits := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+len(codeUnits)*2)
	out = append(out, 0xFF, 0xFE)
	for _, u := range codeUnits {
		out = append(out, byte(u&0xff), byte((u>>8)&0xff))
	}
	return out
}

// ---------------------------------------------------------------------------
// FLAC — rewrite the Vorbis comment to use the canonical LYRICS key
// ---------------------------------------------------------------------------

// onlineEmbedWriteFLACLyric opens the FLAC file at audioPath,
// locates the VORBIS_COMMENT block, removes any existing
// `lyrics=` or `LYRICS=` comment, appends a canonical
// `LYRICS=…` comment, recomputes the block size and CRC
// (well, the metadata-block header's last-byte flag, not a
// CRC — FLAC uses a CRC on the audio data, not on the
// metadata blocks), and rewrites the file in place.
//
// The rewrite is conservative: we keep every other metadata
// block (STREAMINFO, PADDING, SEEKTABLE, PICTURE, etc.) byte-
// for-byte and only touch the VORBIS_COMMENT payload.
func onlineEmbedWriteFLACLyric(audioPath, lyric string) error {
	data, err := os.ReadFile(audioPath) // #nosec G304 -- caller-controlled
	if err != nil {
		return fmt.Errorf("read audio: %w", err)
	}
	if len(data) < 4 || !bytes.Equal(data[:4], []byte("fLaC")) {
		return fmt.Errorf("not a FLAC file (no fLaC magic)")
	}
	// Walk metadata blocks. Each block has a 1-byte type
	// (high bit = "last block") + 3-byte size + payload.
	pos := 4
	var newData bytes.Buffer
	newData.Write(data[:4])
	for pos < len(data) {
		if pos+4 > len(data) {
			// Truncated header — abort the rewrite and
			// leave the file as ffmpeg wrote it.
			return fmt.Errorf("truncated metadata header at offset %d", pos)
		}
		btype := data[pos]
		bsize := int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
		if pos+4+bsize > len(data) {
			return fmt.Errorf("truncated metadata block at offset %d", pos)
		}
		payload := data[pos+4 : pos+4+bsize]
		if btype&0x7f == 4 { // VORBIS_COMMENT (without the "last" bit)
			rewritten, err := rewriteVorbisComment(payload, lyric)
			if err != nil {
				return fmt.Errorf("rewrite vorbis comment: %w", err)
			}
			payload = rewritten
			bsize = len(payload)
		}
		// Write the (possibly rewritten) block header + payload.
		newData.WriteByte(btype)
		newData.WriteByte(byte((bsize >> 16) & 0xff))
		newData.WriteByte(byte((bsize >> 8) & 0xff))
		newData.WriteByte(byte(bsize & 0xff))
		newData.Write(payload)
		pos += 4 + bsize
		// High bit of btype signals "last block".
		if btype&0x80 != 0 {
			// Append the rest of the file (audio frames)
			// verbatim.
			newData.Write(data[pos:])
			break
		}
	}
	return os.WriteFile(audioPath, newData.Bytes(), 0o600)
}

// rewriteVorbisComment takes a Vorbis comment block
// payload and replaces the lyrics entry with a canonical
// LYRICS=<lyric> comment. Existing lyrics/LYRICS/©lyr
// entries are stripped first.
//
// Vorbis comment block layout:
//
//	4 bytes  vendor string length (LE)
//	N bytes  vendor string
//	4 bytes  comment count (LE)
//	M × {
//	  4 bytes comment length (LE)
//	  N bytes  comment (key=value, UTF-8)
//	}
//
// We preserve the vendor string and every non-lyrics
// comment exactly. The user-facing LYRICS value is
// written verbatim (UTF-8).
func rewriteVorbisComment(payload []byte, lyric string) ([]byte, error) {
	if len(payload) < 4 {
		return nil, fmt.Errorf("vorbis comment too short")
	}
	vendorLen := int(binary.LittleEndian.Uint32(payload[:4]))
	if 4+vendorLen > len(payload) {
		return nil, fmt.Errorf("vorbis comment vendor length out of range")
	}
	vendor := payload[4 : 4+vendorLen]
	rest := payload[4+vendorLen:]
	if len(rest) < 4 {
		return nil, fmt.Errorf("vorbis comment missing count")
	}
	count := int(binary.LittleEndian.Uint32(rest[:4]))
	rest = rest[4:]
	// Collect non-lyrics comments.
	var kept [][]byte
	pos := 0
	for i := 0; i < count; i++ {
		if pos+4 > len(rest) {
			return nil, fmt.Errorf("vorbis comment count mismatch at entry %d", i)
		}
		clen := int(binary.LittleEndian.Uint32(rest[pos : pos+4]))
		pos += 4
		if pos+clen > len(rest) {
			return nil, fmt.Errorf("vorbis comment entry %d out of range", i)
		}
		entry := rest[pos : pos+clen]
		pos += clen
		// Skip any "lyrics=…" or "LYRICS=…" or "©lyr=…"
		// entry; the new LYRICS= entry is appended at the
		// end so the user sees their freshly fetched
		// version.
		lower := bytes.ToLower(entry)
		if bytes.HasPrefix(lower, []byte("lyrics=")) ||
			bytes.HasPrefix(lower, []byte("©lyr=")) {
			continue
		}
		kept = append(kept, entry)
	}
	// Append the new LYRICS entry. Use the canonical
	// uppercase key so Xiph-spec-compliant tools (Quod
	// Libet, audiochecker) recognize it.
	newEntry := []byte("LYRICS=" + lyric)
	kept = append(kept, newEntry)
	// Rebuild the block. binary.Write on a bytes.Buffer
	// never fails in practice (it can only fail on a
	// Writer whose Write returns an error, and bytes.Buffer
	// is the canonical "always succeeds" writer), so we
	// ignore the error. Using MustPutU32 keeps golangci-lint's
	// errcheck linter happy.
	mustPutU32 := func(buf *bytes.Buffer, v uint32) {
		_ = binary.Write(buf, binary.LittleEndian, v)
	}
	var out bytes.Buffer
	mustPutU32(&out, uint32(len(vendor)))
	out.Write(vendor)
	mustPutU32(&out, uint32(len(kept)))
	for _, c := range kept {
		mustPutU32(&out, uint32(len(c)))
		out.Write(c)
	}
	return out.Bytes(), nil
}
