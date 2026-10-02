package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func orientationTIFF(order binary.ByteOrder, orientation int) []byte {
	data := make([]byte, 26)
	copy(data, "II")
	if order == binary.BigEndian {
		copy(data, "MM")
	}
	order.PutUint16(data[2:], 42)
	order.PutUint32(data[4:], 8)
	order.PutUint16(data[8:], 1)
	order.PutUint16(data[10:], 274)
	order.PutUint16(data[12:], 3)
	order.PutUint32(data[14:], 1)
	order.PutUint16(data[18:], uint16(orientation))
	return data
}

func pngMetadataChunk(data []byte) []byte {
	chunk := make([]byte, len(data)+12)
	binary.BigEndian.PutUint32(chunk, uint32(len(data)))
	copy(chunk[4:], "eXIf")
	copy(chunk[8:], data)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	return chunk
}

func TestOrientationMetadata(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for orientation := 0; orientation <= 9; orientation++ {
			tiff := orientationTIFF(order, orientation)
			jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(tiff) + 8)}, []byte("Exif\x00\x00")...)
			jpeg = append(jpeg, tiff...)
			png := append([]byte("\x89PNG\r\n\x1a\n"), pngMetadataChunk(tiff)...)
			for _, prefix := range []string{"", "Exif\x00\x00"} {
				webp := append([]byte("RIFF\x00\x00\x00\x00WEBPEXIF\x00\x00\x00\x00"), []byte(prefix)...)
				webp = append(webp, tiff...)
				binary.LittleEndian.PutUint32(webp[16:], uint32(len(prefix)+len(tiff)))
				want := orientation
				if want < 1 || want > 8 {
					want = 1
				}
				for _, data := range [][]byte{tiff, jpeg, png, webp} {
					got := exifOrientation(io.NewSectionReader(bytes.NewReader(data), 0, int64(len(data))))
					if got != want {
						t.Fatalf("%s, %q: orientation %d, want %d", order, data[:4], got, want)
					}
				}
			}
		}
	}
	valid := orientationTIFF(binary.LittleEndian, 6)
	outside := bytes.Clone(valid)
	binary.LittleEndian.PutUint32(outside[4:], 1<<30)
	for _, data := range [][]byte{nil, {0xff, 0xd8, 0xff}, valid[:19], outside} {
		if got := exifOrientation(io.NewSectionReader(bytes.NewReader(data), 0, int64(len(data)))); got != 1 {
			t.Fatalf("Malformed metadata produced orientation %d", got)
		}
	}

	short := append([]byte("\x89PNG\r\n\x1a\n"), pngMetadataChunk(valid)...)
	binary.BigEndian.PutUint32(short[8:], 8)
	if got := exifOrientation(io.NewSectionReader(bytes.NewReader(short), 0, int64(len(short)))); got != 1 {
		t.Fatal("TIFF reads escaped the metadata chunk")
	}
}

type countedOrientationReader struct {
	io.ReaderAt
	bytesRead int
}

func (r *countedOrientationReader) ReadAt(p []byte, offset int64) (int, error) {
	n, err := r.ReaderAt.ReadAt(p, offset)
	r.bytesRead += n
	return n, err
}

func TestOrientationSkipsLargePayload(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "large-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	header := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 8)...)
	binary.BigEndian.PutUint32(header[8:], 64<<20)
	copy(header[12:], "IDAT")
	if _, err := file.WriteAt(header, 0); err != nil {
		t.Fatal(err)
	}
	chunk := pngMetadataChunk(orientationTIFF(binary.LittleEndian, 8))
	offset := int64(8 + 12 + 64<<20)
	if _, err := file.WriteAt(chunk, offset); err != nil {
		t.Fatal(err)
	}
	reader := &countedOrientationReader{ReaderAt: file}
	if got := exifOrientation(io.NewSectionReader(reader, 0, offset+int64(len(chunk)))); got != 8 || reader.bytesRead > 128 {
		t.Fatalf("Orientation %d; metadata read %d bytes", got, reader.bytesRead)
	}
}

func TestLoadImageAndOrientationSampling(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i := 0; i < 6; i++ {
		img.SetNRGBA(i%3, i/3, color.NRGBA{R: byte(i + 1), A: 255})
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()

	withEXIF := append(bytes.Clone(data[:33]), pngMetadataChunk(orientationTIFF(binary.LittleEndian, 6))...)
	withEXIF = append(withEXIF, data[33:]...)
	path := filepath.Join(t.TempDir(), "rotated.png")
	if err := os.WriteFile(path, withEXIF, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, orientation, err := loadImage(path)
	if err != nil || orientation != 6 {
		t.Fatalf("Loading rotated PNG: orientation %d, %v", orientation, err)
	}
	want := [][]int{{1, 2, 3, 4, 5, 6}, {3, 2, 1, 6, 5, 4}, {6, 5, 4, 3, 2, 1}, {4, 5, 6, 1, 2, 3}, {1, 4, 2, 5, 3, 6}, {4, 1, 5, 2, 6, 3}, {6, 3, 5, 2, 4, 1}, {3, 6, 2, 5, 1, 4}}
	for i, pixels := range want {
		columns, rows := 3, 2
		if i >= 4 {
			columns, rows = rows, columns
		}
		cells := sample(loaded, i+1, columns, rows, [3]byte{})
		for j, pixel := range pixels {
			if math.Abs(float64(cells[j][2])*255-float64(pixel)) > .00001 {
				t.Fatalf("Orientation %d, pixel %d: got %g, want %d", i+1, j, cells[j][2]*255, pixel)
			}
		}
	}
}
