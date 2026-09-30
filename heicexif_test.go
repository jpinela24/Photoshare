package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// ── Fixtures ─────────────────────────────────────────────────────────────────

// tiffEXIF builds a real big-endian TIFF/EXIF block carrying a capture date
// and GPS coordinates, so the tests exercise what goexif actually parses
// rather than a stand-in.
//
// Layout is fixed, with every offset computed from it:
//
//	0    TIFF header ("MM", 42, offset of IFD0)
//	8    IFD0     — DateTime, GPSInfoIFDPointer
//	38   GPS IFD  — lat/long and their hemisphere refs
//	92   the date string
//	112  latitude  as three rationals
//	136  longitude as three rationals
func tiffEXIF() []byte {
	const (
		ifd0Off = 8
		exifOff = 50
		gpsOff  = 68
		dtOff   = 122 // DateTime        (IFD0)
		dtoOff  = 142 // DateTimeOriginal (Exif IFD)
		latOff  = 162
		lonOff  = 186
		total   = 210
	)
	b := make([]byte, total)
	be := binary.BigEndian

	copy(b[0:2], "MM")
	be.PutUint16(b[2:4], 42)
	be.PutUint32(b[4:8], ifd0Off)

	entry := func(at int, tag, typ uint16, count uint32, val func([]byte)) {
		be.PutUint16(b[at:], tag)
		be.PutUint16(b[at+2:], typ)
		be.PutUint32(b[at+4:], count)
		val(b[at+8 : at+12])
	}

	// IFD0 — DateTime, plus pointers to the Exif and GPS sub-IFDs.
	be.PutUint16(b[ifd0Off:], 3)
	entry(ifd0Off+2, 0x0132, 2, 20, func(v []byte) { be.PutUint32(v, dtOff) })
	entry(ifd0Off+14, 0x8769, 4, 1, func(v []byte) { be.PutUint32(v, exifOff) })
	entry(ifd0Off+26, 0x8825, 4, 1, func(v []byte) { be.PutUint32(v, gpsOff) })
	be.PutUint32(b[ifd0Off+38:], 0)

	// Exif sub-IFD — DateTimeOriginal. This is where a camera records when the
	// shot was taken; IFD0's DateTime is often just when the file was last
	// written. They are deliberately different here so the tests can tell
	// which one the code actually reads.
	be.PutUint16(b[exifOff:], 1)
	entry(exifOff+2, 0x9003, 2, 20, func(v []byte) { be.PutUint32(v, dtoOff) })
	be.PutUint32(b[exifOff+14:], 0)

	// GPS sub-IFD
	be.PutUint16(b[gpsOff:], 4)
	entry(gpsOff+2, 0x0001, 2, 2, func(v []byte) { copy(v, "N\x00") })
	entry(gpsOff+14, 0x0002, 5, 3, func(v []byte) { be.PutUint32(v, latOff) })
	entry(gpsOff+26, 0x0003, 2, 2, func(v []byte) { copy(v, "W\x00") })
	entry(gpsOff+38, 0x0004, 5, 3, func(v []byte) { be.PutUint32(v, lonOff) })
	be.PutUint32(b[gpsOff+50:], 0)

	copy(b[dtOff:], "2019:06:01 12:00:00\x00")
	copy(b[dtoOff:], "2016:03:04 09:30:15\x00")

	// 25 deg 46' 30" N, 80 deg 11' 0" W — Miami, far enough from 0,0 that a
	// parser returning zeroes cannot accidentally look correct.
	rational := func(at int, nums ...[2]uint32) {
		for i, n := range nums {
			be.PutUint32(b[at+i*8:], n[0])
			be.PutUint32(b[at+i*8+4:], n[1])
		}
	}
	rational(latOff, [2]uint32{25, 1}, [2]uint32{46, 1}, [2]uint32{30, 1})
	rational(lonOff, [2]uint32{80, 1}, [2]uint32{11, 1}, [2]uint32{0, 1})
	return b
}

// box assembles one ISO-BMFF box.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(typ string, version byte, payload []byte) []byte {
	body := append([]byte{version, 0, 0, 0}, payload...)
	return box(typ, body)
}

// heifFile assembles a HEIF holding one EXIF item, in the shape a real encoder
// writes: ftyp, then meta (iinf + iloc), then the payload in mdat.
//
// infeVersion and ilocVersion are parameters because the two boxes are
// versioned independently and the field widths shift with them — the bugs this
// parser can have are almost all "read the wrong number of bytes for this
// version".
// heifOpts covers the parts of the format that vary between encoders and that
// a parser can quietly get wrong on someone else's file while working on its
// own fixtures.
type heifOpts struct {
	infeVersion byte
	ilocVersion byte
	wide        bool // 8-byte iloc offsets/lengths, as large files use
	baseOffset  bool // split the address into a per-item base plus an extent
	tiffPad     int  // padding before the TIFF block, i.e. a nonzero skip
}

func heifFile(exifBlock []byte, o heifOpts) []byte {
	const itemID = 7
	infeVersion, ilocVersion := o.infeVersion, o.ilocVersion

	// iinf with a single infe entry declaring item `itemID` to be of type Exif.
	var infeBody []byte
	switch infeVersion {
	case 2:
		infeBody = make([]byte, 0, 10)
		infeBody = binary.BigEndian.AppendUint16(infeBody, itemID)
		infeBody = binary.BigEndian.AppendUint16(infeBody, 0) // protection index
		infeBody = append(infeBody, []byte("Exif")...)
		infeBody = append(infeBody, 0) // item_name
	case 3:
		infeBody = make([]byte, 0, 12)
		infeBody = binary.BigEndian.AppendUint32(infeBody, itemID)
		infeBody = binary.BigEndian.AppendUint16(infeBody, 0)
		infeBody = append(infeBody, []byte("Exif")...)
		infeBody = append(infeBody, 0)
	}
	infe := fullBox("infe", infeVersion, infeBody)

	iinfBody := binary.BigEndian.AppendUint16(nil, 1) // entry_count (version 0)
	iinfBody = append(iinfBody, infe...)
	iinf := fullBox("iinf", 0, iinfBody)

	// iloc field widths are packed into nibbles: offset_size/length_size in
	// the first byte, base_offset_size/index_size in the second.
	fieldSize := 4
	if o.wide {
		fieldSize = 8
	}
	baseSize := 0
	if o.baseOffset {
		baseSize = fieldSize
	}
	ilocBody := []byte{byte(fieldSize<<4 | fieldSize), byte(baseSize << 4)}
	if ilocVersion < 2 {
		ilocBody = binary.BigEndian.AppendUint16(ilocBody, 1) // item_count
		ilocBody = binary.BigEndian.AppendUint16(ilocBody, itemID)
	} else {
		ilocBody = binary.BigEndian.AppendUint32(ilocBody, 1)
		ilocBody = binary.BigEndian.AppendUint32(ilocBody, itemID)
	}
	if ilocVersion == 1 || ilocVersion == 2 {
		ilocBody = binary.BigEndian.AppendUint16(ilocBody, 0) // construction_method 0
	}
	ilocBody = binary.BigEndian.AppendUint16(ilocBody, 0) // data_reference_index
	putN := func(b []byte, v uint64) []byte {
		if fieldSize == 8 {
			return binary.BigEndian.AppendUint64(b, v)
		}
		return binary.BigEndian.AppendUint32(b, uint32(v))
	}
	baseAt := -1
	if baseSize > 0 {
		baseAt = len(ilocBody)
		ilocBody = putN(ilocBody, 0) // patched below
	}
	ilocBody = binary.BigEndian.AppendUint16(ilocBody, 1) // extent_count
	offsetAt := len(ilocBody)                             // patched once the layout is known
	ilocBody = putN(ilocBody, 0)
	ilocBody = putN(ilocBody, uint64(len(exifBlock)+4+o.tiffPad))
	iloc := fullBox("iloc", ilocVersion, ilocBody)

	metaBody := append(append([]byte{}, iinf...), iloc...)
	meta := fullBox("meta", 0, metaBody)
	ftyp := box("ftyp", []byte("heicheic"))

	// The item payload: a 4-byte offset to the TIFF header, that many bytes of
	// padding, then the block. The offset is usually zero, which is exactly
	// why a parser that ignores it passes every test until it meets a file
	// where it isn't.
	mdatPayload := binary.BigEndian.AppendUint32(nil, uint32(o.tiffPad))
	mdatPayload = append(mdatPayload, bytes.Repeat([]byte{0xAA}, o.tiffPad)...)
	mdatPayload = append(mdatPayload, exifBlock...)
	mdat := box("mdat", mdatPayload)

	out := append(append(append([]byte{}, ftyp...), meta...), mdat...)

	// The extent offset is an absolute file offset, which is only knowable
	// once everything before mdat has been assembled. Locate that field in the
	// finished file and write it: ftyp, then meta's header and version bytes,
	// then iinf, then iloc's own header and version bytes.
	payloadStart := uint64(len(ftyp) + len(meta) + 8)
	ilocBodyStart := len(ftyp) + 8 + 4 + len(iinf) + 8 + 4
	write := func(at int, v uint64) {
		if fieldSize == 8 {
			binary.BigEndian.PutUint64(out[at:at+8], v)
		} else {
			binary.BigEndian.PutUint32(out[at:at+4], uint32(v))
		}
	}
	// With a base offset the address is split between the two fields, which is
	// the arrangement a parser that ignores base_offset silently mishandles.
	if baseAt >= 0 {
		write(ilocBodyStart+baseAt, payloadStart-16)
		write(ilocBodyStart+offsetAt, 16)
	} else {
		write(ilocBodyStart+offsetAt, payloadStart)
	}
	return out
}

// ── Tests ────────────────────────────────────────────────────────────────────

// The whole point: a HEIC's date and GPS must come out, because without this
// every iPhone photo fell back to its filesystem timestamp and never appeared
// on the map.
func TestHeicExifReadsDateAndLocation(t *testing.T) {
	for _, c := range []struct {
		name string
		opts heifOpts
	}{
		{"infe v2 / iloc v0", heifOpts{infeVersion: 2, ilocVersion: 0}},
		{"infe v2 / iloc v1", heifOpts{infeVersion: 2, ilocVersion: 1}},
		{"infe v2 / iloc v2", heifOpts{infeVersion: 2, ilocVersion: 2}},
		{"infe v3 / iloc v1", heifOpts{infeVersion: 3, ilocVersion: 1}},
		{"64-bit offsets", heifOpts{infeVersion: 2, ilocVersion: 1, wide: true}},
		{"base offset split", heifOpts{infeVersion: 2, ilocVersion: 1, baseOffset: true}},
		{"64-bit base offset", heifOpts{infeVersion: 2, ilocVersion: 1, wide: true, baseOffset: true}},
		{"padded tiff header", heifOpts{infeVersion: 2, ilocVersion: 1, tiffPad: 12}},
	} {
		data := heifFile(tiffEXIF(), c.opts)
		dir := t.TempDir()
		p := filepath.Join(dir, "shot.heic")
		if err := os.WriteFile(p, data, 0644); err != nil {
			t.Fatal(err)
		}

		x, err := heicExif(p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		tm, err := x.DateTime()
		if err != nil {
			t.Fatalf("%s: DateTime: %v", c.name, err)
		}
		// DateTimeOriginal, not IFD0's DateTime: a photo copied last week was
		// still taken when it was taken, and sorting a library by the wrong
		// one of these is the whole bug being fixed.
		if got := tm.Format("2006-01-02 15:04:05"); got != "2016-03-04 09:30:15" {
			t.Errorf("%s: date = %s, want the capture time 2016-03-04 09:30:15", c.name, got)
		}
		lat, lng, err := x.LatLong()
		if err != nil {
			t.Fatalf("%s: LatLong: %v", c.name, err)
		}
		if d := lat - 25.775; d > 0.001 || d < -0.001 {
			t.Errorf("%s: lat = %v, want ~25.775", c.name, lat)
		}
		// West must come out negative. Dropping the hemisphere ref is the
		// classic GPS bug, and it puts Miami in Asia.
		if d := lng + 80.1833; d > 0.001 || d < -0.001 {
			t.Errorf("%s: lng = %v, want ~-80.1833", c.name, lng)
		}
	}
}

// fileDate and fileDateGeo are what the library walk actually calls, and the
// bug was that they skipped HEIC entirely and returned the file's mtime.
func TestFileDateUsesHeicExifNotModTime(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "iphone.heic")
	if err := os.WriteFile(p, heifFile(tiffEXIF(), heifOpts{infeVersion: 2, ilocVersion: 1}), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	got := fileDate(p, info)
	if got.Format("2006-01-02") != "2016-03-04" {
		t.Errorf("fileDate = %v, want the EXIF capture date 2016-03-04 (got the mtime, or the wrong date tag)", got)
	}

	when, lat, lng, hasGeo := fileDateGeo(p, info)
	if when.Format("2006-01-02") != "2016-03-04" {
		t.Errorf("fileDateGeo date = %v, want the EXIF capture date 2016-03-04", when)
	}
	if !hasGeo {
		t.Fatal("fileDateGeo found no GPS — the photo would never appear on the map")
	}
	if lat < 25.7 || lat > 25.8 || lng > -80.1 || lng < -80.3 {
		t.Errorf("fileDateGeo coords = %v,%v, want ~25.775,-80.1833", lat, lng)
	}
}

// A photo with no EXIF, a truncated file, or junk must fall back to the
// modification time rather than failing the walk. These run through the whole
// library, so a panic here takes out browsing, the timeline and the map.
func TestHeicExifDegradesGracefully(t *testing.T) {
	good := heifFile(tiffEXIF(), heifOpts{infeVersion: 2, ilocVersion: 1})
	cases := map[string][]byte{
		"empty":             {},
		"not a heif":        []byte("this is plain text, not a container at all"),
		"header only":       good[:8],
		"truncated in meta": good[:len(good)/2],
		"missing mdat":      good[:len(good)-20],
		"zeroed":            make([]byte, len(good)),
		"no exif item":      box("ftyp", []byte("heicheic")),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "broken.heic")
			if err := os.WriteFile(p, data, 0644); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			// Must not panic, and must fall back to mtime.
			got := fileDate(p, info)
			if !got.Equal(info.ModTime()) {
				t.Errorf("fileDate = %v, want the mtime %v", got, info.ModTime())
			}
			if _, _, _, hasGeo := fileDateGeo(p, info); hasGeo {
				t.Error("claimed GPS from a file that has none")
			}
		})
	}
}

// A box claiming to be larger than the file must be refused rather than used
// to read past the end or allocate wildly.
func TestHeicExifRejectsOversizedBoxes(t *testing.T) {
	data := heifFile(tiffEXIF(), heifOpts{infeVersion: 2, ilocVersion: 1})
	// Rewrite the ftyp box's size to something vastly larger than the file.
	binary.BigEndian.PutUint32(data[:4], 0xFFFFFF00)
	if _, err := heicExifBlock(bytes.NewReader(data)); err == nil {
		t.Error("accepted a box that overruns the file")
	}
}

// JPEG must keep working through the shared decoder — the refactor that added
// HEIC support routed every format through one function.
func TestDecodeExifIgnoresNonPhotos(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(p, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if x := decodeExif(p, "notes.txt"); x != nil {
		t.Error("decoded EXIF from a text file")
	}
}
