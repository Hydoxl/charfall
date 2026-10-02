package main

import (
	"encoding/binary"
	"errors"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
)

func tiffOrientation(data io.ReaderAt) int {
	var header [8]byte
	if _, err := data.ReadAt(header[:], 0); err != nil {
		return 1
	}
	var order binary.ByteOrder
	switch string(header[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(header[2:4]) != 42 {
		return 1
	}
	offset := int64(order.Uint32(header[4:8]))
	var count [2]byte
	if _, err := data.ReadAt(count[:], offset); err != nil {
		return 1
	}
	var entry [12]byte
	for i := 0; i < int(order.Uint16(count[:])); i++ {
		if _, err := data.ReadAt(entry[:], offset+2+int64(i)*12); err != nil {
			return 1
		}
		if order.Uint16(entry[:2]) == 274 && order.Uint16(entry[2:4]) == 3 && order.Uint32(entry[4:8]) == 1 {
			v := int(order.Uint16(entry[8:10]))
			if v >= 1 && v <= 8 {
				return v
			}
		}
	}
	return 1
}
func exifOrientation(data *io.SectionReader) int {
	var header [12]byte
	n, _ := data.ReadAt(header[:], 0)
	if n < 4 {
		return 1
	}
	size := data.Size()
	if string(header[:2]) == "II" || string(header[:2]) == "MM" {
		return tiffOrientation(data)
	}
	if header[0] == 0xff && header[1] == 0xd8 {
		var segment [4]byte
		var prefix [6]byte
		for at := int64(2); at+4 <= size; {
			if _, err := data.ReadAt(segment[:], at); err != nil || segment[0] != 0xff {
				break
			}
			marker := segment[1]
			at += 2
			if marker == 0xff {
				at--
				continue
			}
			if marker == 0xda || marker == 0xd9 {
				break
			}
			if marker == 1 || marker >= 0xd0 && marker <= 0xd7 {
				continue
			}
			length := int64(binary.BigEndian.Uint16(segment[2:4]))
			if length < 2 || length > size-at {
				break
			}
			if marker == 0xe1 && length >= 8 {
				if _, err := data.ReadAt(prefix[:], at+2); err != nil {
					break
				}
				if string(prefix[:]) == "Exif\x00\x00" {
					return tiffOrientation(io.NewSectionReader(data, at+8, length-8))
				}
			}
			at += length
		}
	}
	if n >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n" {
		var chunk [8]byte
		for at := int64(8); at+12 <= size; {
			if _, err := data.ReadAt(chunk[:], at); err != nil {
				break
			}
			length := int64(binary.BigEndian.Uint32(chunk[:4]))
			if length > size-at-12 {
				break
			}
			if string(chunk[4:]) == "eXIf" {
				return tiffOrientation(io.NewSectionReader(data, at+8, length))
			}
			at += 12 + length
		}
	}
	if n == 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		var chunk [8]byte
		var prefix [6]byte
		for at := int64(12); at+8 <= size; {
			if _, err := data.ReadAt(chunk[:], at); err != nil {
				break
			}
			length := int64(binary.LittleEndian.Uint32(chunk[4:]))
			if length > size-at-8 {
				break
			}
			if string(chunk[:4]) == "EXIF" {
				start := at + 8
				if length >= 6 {
					if _, err := data.ReadAt(prefix[:], start); err != nil {
						break
					}
					if string(prefix[:]) == "Exif\x00\x00" {
						start += 6
						length -= 6
					}
				}
				return tiffOrientation(io.NewSectionReader(data, start, length))
			}
			at += 8 + length + length%2
		}
	}
	return 1
}
func loadImage(path string) (image.Image, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 1, err
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, 1, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 89_478_485/config.Height {
		return nil, 1, errors.New("image exceeds 89 million pixels or has invalid dimensions")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, 1, err
	}
	decoded, _, err := image.Decode(file)
	if err != nil {
		return nil, 1, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, 1, err
	}
	return decoded, exifOrientation(io.NewSectionReader(file, 0, info.Size())), nil
}

func boxRanges(input, output int) [][2]int {
	ranges := make([][2]int, output)
	scale := float64(input) / float64(output)
	support := math.Max(1, scale) * .5
	for i := range ranges {
		// Explicit rounding prevents fused multiply-add from moving boundary pixels.
		center := float64((float64(i) + .5) * scale)
		lo := max(0, int(center-support+.5))
		hi := min(input, int(center+support+.5))
		for lo < hi && (float64(lo)+.5-center)*(1/math.Max(1, scale)) <= -.5 {
			lo++
		}
		for hi > lo && (float64(hi-1)+.5-center)*(1/math.Max(1, scale)) > .5 {
			hi--
		}
		ranges[i] = [2]int{lo, hi}
	}
	return ranges
}
func sample(img image.Image, orientation, columns, rows int, bg [3]byte) [][5]float32 {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	ow, oh := w, h
	if orientation >= 5 {
		ow, oh = h, w
	}
	xs, ys := boxRanges(ow, columns), boxRanges(oh, rows)
	cells := make([][5]float32, columns*rows)
	for y, yr := range ys {
		for x, xr := range xs {
			var vertical [5]float64
			for sy := yr[0]; sy < yr[1]; sy++ {
				var horizontal [5]float64
				for sx := xr[0]; sx < xr[1]; sx++ {
					ix, iy := sx, sy
					switch orientation {
					case 2:
						ix = w - 1 - sx
					case 3:
						ix, iy = w-1-sx, h-1-sy
					case 4:
						iy = h - 1 - sy
					case 5:
						ix, iy = sy, sx
					case 6:
						ix, iy = sy, h-1-sx
					case 7:
						ix, iy = w-1-sy, h-1-sx
					case 8:
						ix, iy = w-1-sy, sx
					}
					c := color.NRGBAModel.Convert(img.At(ix+bounds.Min.X, iy+bounds.Min.Y)).(color.NRGBA)
					var rgb [3]float32
					for channel, v := range [3]byte{c.R, c.G, c.B} {
						value := (int(v)*int(c.A) + int(bg[channel])*(255-int(c.A)) + 127) / 255
						rgb[channel] = float32(value) / 255
					}
					l := rgb[0]*float32(.2126) + rgb[1]*float32(.7152) + rgb[2]*float32(.0722)
					for channel, v := range [5]float32{l, l * l, rgb[0], rgb[1], rgb[2]} {
						horizontal[channel] += float64(v) / float64(xr[1]-xr[0])
					}
				}
				for channel, v := range horizontal {
					vertical[channel] += float64(float32(v)) / float64(yr[1]-yr[0])
				}
			}
			for channel, v := range vertical {
				cells[y*columns+x][channel] = float32(v)
			}
		}
	}
	return cells
}

// Three extended box passes reproduce Pillow's GaussianBlur(1.25) on 8-bit luma.
// Formula: Pillow's BoxBlur.c and Gwosdek et al. (2011).
func smoothLuma(cells [][5]float32, w, h int) []byte {
	data := make([]byte, len(cells))
	for i, c := range cells {
		data[i] = byte(c[0] * 255)
	}
	sigma2 := float32(1.25 * 1.25 / 3)
	length := float32(math.Sqrt(12*float64(sigma2) + 1))
	radius := float32(math.Floor(float64((length - 1) / 2)))
	fraction := (2*radius + 1) * (radius*(radius+1) - 3*sigma2) / (6 * (sigma2 - (radius+1)*(radius+1)))
	r := radius + fraction
	whole := int(r)
	weight := uint32(float32(1<<24) / (2*r + 1))
	fringe := (uint32(1<<24) - uint32(whole*2+1)*weight) / 2
	for axis := 0; axis < 2; axis++ {
		for pass := 0; pass < 3; pass++ {
			next := make([]byte, len(data))
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					at := func(delta int) byte {
						xx, yy := x, y
						if axis == 0 {
							xx = max(0, min(w-1, x+delta))
						} else {
							yy = max(0, min(h-1, y+delta))
						}
						return data[yy*w+xx]
					}
					var sum uint32
					for d := -whole; d <= whole; d++ {
						sum += uint32(at(d))
					}
					next[y*w+x] = byte((sum*weight + (uint32(at(-whole-1))+uint32(at(whole+1)))*fringe + (1 << 23)) >> 24)
				}
			}
			data = next
		}
	}
	return data
}
