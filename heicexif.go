package main

// EXIF out of a HEIC/HEIF file, in pure Go.
//
// HEIC doesn't carry EXIF the way a JPEG does: there is no APP1 segment to
// scan for, so goexif can't read one directly and every iPhone photo fell back
// to its filesystem timestamp — sorted by when it was copied rather than when
// it was taken, and missing from the map entirely.
//
// The EXIF is there, as an item inside the ISO base media file format's `meta`
// box. Finding it means walking the box tree: `iinf` lists the items and says
// which one has type "Exif", and `iloc` says where that item's bytes live in
// the file. The payload is a 4-byte offset followed by an ordinary TIFF block,
// which is what goexif wants.
//
// Done here rather than by shelling out to exiftool/ffprobe because fileDate
// runs for every file in a library walk — a subprocess per photo would make
// scanning a 12,000-photo library unusable — and because the Windows desktop
// build has neither tool available.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/rwcarlsen/goexif/exif"
)

// Bounds. A HEIC's metadata sits near the front and an EXIF block is a few KB;
// these caps mean a corrupt or hostile file can't make us allocate a library's
// worth of memory from a size field we haven't validated.
const (
	maxMetaBox  = 8 << 20 // the whole meta box
	maxExifItem = 4 << 20 // one item's payload
)

var errNoExif = errors.New("no exif item in heif")

// heicExif returns the parsed EXIF of a HEIC/HEIF file.
func heicExif(path string) (*exif.Exif, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := heicExifBlock(f)
	if err != nil {
		return nil, err
	}
	return exif.Decode(bytes.NewReader(raw))
}

// heicExifBlock finds the TIFF block. Split from heicExif so tests can check
// the container parsing without also depending on what goexif makes of it.
func heicExifBlock(r io.ReaderAt) ([]byte, error) {
	meta, metaPos, err := findTopLevelBox(r, "meta")
	if err != nil {
		return nil, err
	}
	// meta is a FullBox: one version byte then three flag bytes before the
	// children start. Skipping this is the classic way to end up parsing the
	// version field as a box size.
	if len(meta) < 4 {
		return nil, errors.New("meta box too short")
	}
	children := meta[4:]
	childBase := metaPos + 4

	itemID, err := exifItemID(children)
	if err != nil {
		return nil, err
	}
	off, length, err := itemLocation(children, childBase, itemID)
	if err != nil {
		return nil, err
	}
	if length < 4 || length > maxExifItem {
		return nil, fmt.Errorf("exif item length %d out of range", length)
	}

	item := make([]byte, length)
	if _, err := r.ReadAt(item, int64(off)); err != nil {
		return nil, fmt.Errorf("reading exif item: %w", err)
	}
	// The item starts with a 4-byte big-endian offset to the TIFF header,
	// because the block may be preceded by padding. It is almost always 0,
	// which is exactly why a file where it isn't would break a parser that
	// assumed so.
	skip := binary.BigEndian.Uint32(item[:4])
	if uint64(skip)+4 > uint64(len(item)) {
		return nil, errors.New("exif tiff offset past end of item")
	}
	return item[4+skip:], nil
}

// ── ISO-BMFF box walking ─────────────────────────────────────────────────────

// readBoxHeader reads one box header at pos, returning its type, the offset of
// its payload, and the payload's size.
//
// Sizes need care: 1 means the real size is a 64-bit field after the type, and
// 0 means "to the end of the file". Treating either as a literal length is how
// a parser walks off into the middle of image data.
func readBoxHeader(r io.ReaderAt, pos, limit int64) (typ string, bodyPos int64, bodySize int64, err error) {
	var hdr [8]byte
	if _, err := r.ReadAt(hdr[:], pos); err != nil {
		return "", 0, 0, err
	}
	size := int64(binary.BigEndian.Uint32(hdr[:4]))
	typ = string(hdr[4:8])
	bodyPos = pos + 8

	switch size {
	case 1:
		var big [8]byte
		if _, err := r.ReadAt(big[:], pos+8); err != nil {
			return "", 0, 0, err
		}
		u := binary.BigEndian.Uint64(big[:])
		if u > uint64(1<<62) {
			return "", 0, 0, errors.New("absurd 64-bit box size")
		}
		size = int64(u)
		bodyPos = pos + 16
	case 0:
		size = limit - pos
	}
	if size < bodyPos-pos || pos+size > limit {
		return "", 0, 0, fmt.Errorf("box %q size %d overruns its container", typ, size)
	}
	return typ, bodyPos, size - (bodyPos - pos), nil
}

// findTopLevelBox returns the payload of the first top-level box of the given
// type, along with the file offset that payload starts at (iloc offsets are
// absolute file offsets, so that position has to be carried along).
func findTopLevelBox(r io.ReaderAt, want string) ([]byte, int64, error) {
	size, err := readerSize(r)
	if err != nil {
		return nil, 0, err
	}
	for pos := int64(0); pos < size; {
		typ, bodyPos, bodySize, err := readBoxHeader(r, pos, size)
		if err != nil {
			return nil, 0, err
		}
		if typ == want {
			if bodySize > maxMetaBox {
				return nil, 0, fmt.Errorf("%s box is %d bytes", want, bodySize)
			}
			buf := make([]byte, bodySize)
			if _, err := r.ReadAt(buf, bodyPos); err != nil {
				return nil, 0, err
			}
			return buf, bodyPos, nil
		}
		next := bodyPos + bodySize
		if next <= pos { // a zero-length box would spin here forever
			return nil, 0, errors.New("box made no progress")
		}
		pos = next
	}
	return nil, 0, fmt.Errorf("no %s box", want)
}

// childBox finds a box of the given type among a parsed box's children,
// returning its payload and that payload's offset relative to the parent's
// own start.
func childBox(buf []byte, want string) ([]byte, int64, bool) {
	for pos := 0; pos+8 <= len(buf); {
		size := int64(binary.BigEndian.Uint32(buf[pos : pos+4]))
		typ := string(buf[pos+4 : pos+8])
		body := pos + 8
		if size == 1 {
			if pos+16 > len(buf) {
				return nil, 0, false
			}
			size = int64(binary.BigEndian.Uint64(buf[pos+8 : pos+16]))
			body = pos + 16
		} else if size == 0 {
			size = int64(len(buf) - pos)
		}
		if size < int64(body-pos) || int64(pos)+size > int64(len(buf)) {
			return nil, 0, false
		}
		end := int64(pos) + size
		if typ == want {
			return buf[body:end], int64(body), true
		}
		if end <= int64(pos) {
			return nil, 0, false
		}
		pos = int(end)
	}
	return nil, 0, false
}

// ── iinf: which item is the EXIF one ─────────────────────────────────────────

// exifItemID walks the item info box for an entry whose type is "Exif".
func exifItemID(meta []byte) (uint32, error) {
	iinf, _, ok := childBox(meta, "iinf")
	if !ok {
		return 0, errNoExif
	}
	if len(iinf) < 4 {
		return 0, errors.New("iinf too short")
	}
	version := iinf[0]
	p := 4
	// Entry count widened in version 1, which is the sort of detail that
	// silently shifts every subsequent read by two bytes if ignored.
	var count int
	if version == 0 {
		if len(iinf) < 6 {
			return 0, errors.New("iinf too short")
		}
		count = int(binary.BigEndian.Uint16(iinf[4:6]))
		p = 6
	} else {
		if len(iinf) < 8 {
			return 0, errors.New("iinf too short")
		}
		count = int(binary.BigEndian.Uint32(iinf[4:8]))
		p = 8
	}

	for i := 0; i < count && p+8 <= len(iinf); i++ {
		size := int(binary.BigEndian.Uint32(iinf[p : p+4]))
		typ := string(iinf[p+4 : p+8])
		if size < 8 || p+size > len(iinf) {
			return 0, errors.New("infe box overruns iinf")
		}
		if typ == "infe" {
			if id, ok := infeExifID(iinf[p+8 : p+size]); ok {
				return id, nil
			}
		}
		p += size
	}
	return 0, errNoExif
}

// infeExifID reads one item info entry, returning its id if its type is "Exif".
//
// Only versions 2 and 3 carry a four-character item_type; 0 and 1 identify
// items by name instead and are not used for EXIF in practice.
func infeExifID(b []byte) (uint32, bool) {
	if len(b) < 4 {
		return 0, false
	}
	version := b[0]
	p := 4
	var id uint32
	switch version {
	case 2:
		if p+4 > len(b) {
			return 0, false
		}
		id = uint32(binary.BigEndian.Uint16(b[p : p+2]))
		p += 4 // item_ID (2) + protection_index (2)
	case 3:
		if p+6 > len(b) {
			return 0, false
		}
		id = binary.BigEndian.Uint32(b[p : p+4])
		p += 6 // item_ID (4) + protection_index (2)
	default:
		return 0, false
	}
	if p+4 > len(b) {
		return 0, false
	}
	if string(b[p:p+4]) != "Exif" {
		return 0, false
	}
	return id, true
}

// ── iloc: where that item's bytes are ────────────────────────────────────────

// itemLocation returns the absolute file offset and length of an item's data.
//
// The item location box packs its field widths into nibbles, so the same
// structure can be laid out four different ways; each has to be honoured or
// the offsets come out as nonsense that happens to point inside the file.
func itemLocation(meta []byte, metaBase int64, wantID uint32) (offset, length uint64, err error) {
	iloc, _, ok := childBox(meta, "iloc")
	if !ok {
		return 0, 0, errors.New("no iloc box")
	}
	if len(iloc) < 6 {
		return 0, 0, errors.New("iloc too short")
	}
	version := iloc[0]
	offsetSize := int(iloc[4] >> 4)
	lengthSize := int(iloc[4] & 0x0f)
	baseOffsetSize := int(iloc[5] >> 4)
	indexSize := 0
	if version == 1 || version == 2 {
		indexSize = int(iloc[5] & 0x0f)
	}
	for _, n := range []int{offsetSize, lengthSize, baseOffsetSize, indexSize} {
		if n != 0 && n != 4 && n != 8 {
			return 0, 0, fmt.Errorf("iloc field size %d is not 0, 4 or 8", n)
		}
	}

	p := 6
	var count int
	if version < 2 {
		if p+2 > len(iloc) {
			return 0, 0, errors.New("iloc truncated")
		}
		count = int(binary.BigEndian.Uint16(iloc[p : p+2]))
		p += 2
	} else {
		if p+4 > len(iloc) {
			return 0, 0, errors.New("iloc truncated")
		}
		count = int(binary.BigEndian.Uint32(iloc[p : p+4]))
		p += 4
	}

	readN := func(n int) (uint64, bool) {
		if n == 0 {
			return 0, true
		}
		if p+n > len(iloc) {
			return 0, false
		}
		var v uint64
		if n == 4 {
			v = uint64(binary.BigEndian.Uint32(iloc[p : p+4]))
		} else {
			v = binary.BigEndian.Uint64(iloc[p : p+8])
		}
		p += n
		return v, true
	}

	for i := 0; i < count; i++ {
		var id uint32
		if version < 2 {
			if p+2 > len(iloc) {
				return 0, 0, errors.New("iloc truncated")
			}
			id = uint32(binary.BigEndian.Uint16(iloc[p : p+2]))
			p += 2
		} else {
			if p+4 > len(iloc) {
				return 0, 0, errors.New("iloc truncated")
			}
			id = binary.BigEndian.Uint32(iloc[p : p+4])
			p += 4
		}
		var constructionMethod uint16
		if version == 1 || version == 2 {
			if p+2 > len(iloc) {
				return 0, 0, errors.New("iloc truncated")
			}
			constructionMethod = binary.BigEndian.Uint16(iloc[p:p+2]) & 0x0f
			p += 2
		}
		p += 2 // data_reference_index
		if p > len(iloc) {
			return 0, 0, errors.New("iloc truncated")
		}
		baseOffset, ok := readN(baseOffsetSize)
		if !ok {
			return 0, 0, errors.New("iloc truncated")
		}
		if p+2 > len(iloc) {
			return 0, 0, errors.New("iloc truncated")
		}
		extents := int(binary.BigEndian.Uint16(iloc[p : p+2]))
		p += 2

		for e := 0; e < extents; e++ {
			if _, ok := readN(indexSize); !ok {
				return 0, 0, errors.New("iloc truncated")
			}
			extOff, ok1 := readN(offsetSize)
			extLen, ok2 := readN(lengthSize)
			if !ok1 || !ok2 {
				return 0, 0, errors.New("iloc truncated")
			}
			// Only the first extent is used: EXIF is written as one contiguous
			// run, and stitching fragments would be guesswork for no gain.
			if id == wantID && e == 0 {
				// construction_method 1 means the offset is relative to the
				// idat box rather than the file. Refuse rather than return a
				// file offset that is really an idat offset — that would read
				// arbitrary bytes and hand them to the EXIF parser.
				if constructionMethod != 0 {
					return 0, 0, fmt.Errorf("unsupported iloc construction method %d", constructionMethod)
				}
				return baseOffset + extOff, extLen, nil
			}
		}
	}
	return 0, 0, errNoExif
}

// readerSize reports the length of the underlying file or buffer.
func readerSize(r io.ReaderAt) (int64, error) {
	switch v := r.(type) {
	case *os.File:
		fi, err := v.Stat()
		if err != nil {
			return 0, err
		}
		return fi.Size(), nil
	case *bytes.Reader:
		return v.Size(), nil
	case interface{ Size() int64 }:
		return v.Size(), nil
	}
	return 0, errors.New("cannot determine size")
}
